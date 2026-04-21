package store

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"

	_ "modernc.org/sqlite"
)

// Reader reads entries and blobs from an existing .marc archive.
type Reader struct {
	db             *sql.DB
	arcFile        *os.File         // the single .marc file (nil for split-file)
	blobFile       *os.File         // legacy split-file blob sidecar (nil for single-file)
	dbTmp          string           // temp file for extracted catalog (single-file only)
	format         string           // "single-file" or "split-file"
	dictData       []byte           // shared zstd dictionary (nil if not present)
	hasSolidBlocks bool             // true if blobs table has block_id column
	solidCache     map[int64][]byte // offset -> decompressed solid block data
	solidCacheKeys []int64          // insertion order for LRU eviction
}

// OpenReader opens an existing .marc archive for reading. It auto-detects
// whether the archive is the new single-file format or the legacy split-file format.
func OpenReader(marcPath string) (*Reader, error) {
	format, err := marc.DetectFormat(marcPath)
	if err != nil {
		return nil, fmt.Errorf("store.OpenReader: %w", err)
	}

	switch format {
	case marc.FormatSingleFile:
		return openReaderSingle(marcPath)
	case marc.FormatSplitFile:
		return openReaderSplit(marcPath)
	default:
		return nil, fmt.Errorf("store.OpenReader: unknown format %q", format)
	}
}

// openReaderSingle opens a single-file .marc archive.
func openReaderSingle(marcPath string) (*Reader, error) {
	f, err := os.Open(marcPath)
	if err != nil {
		return nil, fmt.Errorf("store.OpenReader: open: %w", err)
	}

	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: stat: %w", err)
	}

	// Verify magic.
	var magic [8]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: read magic: %w", err)
	}
	if magic != marc.Magic {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: invalid magic")
	}

	// Read footer.
	footer, err := marc.ReadFooter(f, info.Size())
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: %w", err)
	}

	// Read catalog chunk.
	catalogOff := int64(footer.CatalogOffset)

	// Read chunk header: [Type 1B][Len 4B]
	var chunkHdr [5]byte
	if _, err := f.ReadAt(chunkHdr[:], catalogOff); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: read catalog chunk header: %w", err)
	}
	if chunkHdr[0] != marc.ChunkTypeCatalog {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: expected catalog chunk type 0x%02x, got 0x%02x", marc.ChunkTypeCatalog, chunkHdr[0])
	}
	payloadLen := binary.BigEndian.Uint32(chunkHdr[1:5])

	// Read compressed catalog payload.
	compressed := make([]byte, payloadLen)
	if _, err := f.ReadAt(compressed, catalogOff+5); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: read catalog payload: %w", err)
	}

	// Decompress with size limit to prevent zip-bomb style attacks.
	const maxCatalogSize = 512 * 1024 * 1024 // 512 MB
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(uint64(maxCatalogSize)))
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: create zstd decoder: %w", err)
	}
	dbBytes, err := dec.DecodeAll(compressed, nil)
	dec.Close()
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: decompress catalog: %w", err)
	}
	if len(dbBytes) > maxCatalogSize {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: decompressed catalog exceeds size limit (%d bytes)", len(dbBytes))
	}

	// Write to temp file and open as SQLite.
	tmpF, err := os.CreateTemp("", "marc-catalog-read-*.db")
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: create temp catalog: %w", err)
	}
	tmpPath := tmpF.Name()
	if _, err := tmpF.Write(dbBytes); err != nil {
		_ = tmpF.Close()
		_ = os.Remove(tmpPath)
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: write temp catalog: %w", err)
	}
	_ = tmpF.Close()

	db, err := sql.Open("sqlite", tmpPath+"?mode=ro")
	if err != nil {
		_ = os.Remove(tmpPath)
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: open catalog db: %w", err)
	}

	r := &Reader{
		db:         db,
		arcFile:    f,
		dbTmp:      tmpPath,
		format:     marc.FormatSingleFile,
		solidCache: make(map[int64][]byte),
	}

	// Load shared dictionary if present.
	var dictB64 string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'dict'`).Scan(&dictB64); err == nil {
		if dictBytes, decErr := base64.StdEncoding.DecodeString(dictB64); decErr == nil {
			r.dictData = dictBytes
		}
	}

	r.hasSolidBlocks = detectSolidColumns(db)

	return r, nil
}

// openReaderSplit opens a legacy split-file .marc archive (SQLite + .blobs sidecar).
func openReaderSplit(marcPath string) (*Reader, error) {
	db, err := sql.Open("sqlite", marcPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("store.OpenReader: open db: %w", err)
	}

	blobFile, err := os.Open(marcPath + marc.BlobsExt)
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store.OpenReader: open blobs: %w", err)
	}

	r := &Reader{
		db:         db,
		blobFile:   blobFile,
		format:     marc.FormatSplitFile,
		solidCache: make(map[int64][]byte),
	}
	r.hasSolidBlocks = detectSolidColumns(db)
	return r, nil
}

// WalkEntries calls fn for each entry in tree order (parents before children).
// It reconstructs full relative paths by joining parent names.
func (r *Reader) WalkEntries(fn func(relPath string, e EntryRow) error) error {
	rows, err := r.db.Query(`
		SELECT e.id, e.parent_id, n.name, e.mode, e.mtime_ns, e.size, e.uid, e.gid,
		       COALESCE(eb.blob_id, 0), e.transform, e.params, COALESCE(e.symlink_target, '')
		FROM entries e
		JOIN names n ON e.name_id = n.id
		LEFT JOIN entry_blobs eb ON eb.entry_id = e.id AND eb.seq = 0
		ORDER BY e.id
	`)
	if err != nil {
		return fmt.Errorf("store.WalkEntries: query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	paths := make(map[int64]string) // entry id -> relative path

	for rows.Next() {
		var e EntryRow
		var parentID sql.NullInt64
		var mtimeNs sql.NullInt64
		var size sql.NullInt64
		var transform sql.NullString
		var params []byte

		if err := rows.Scan(&e.ID, &parentID, &e.Name, &e.Mode, &mtimeNs, &size, &e.UID, &e.GID, &e.BlobID, &transform, &params, &e.LinkTarget); err != nil {
			return fmt.Errorf("store.WalkEntries: scan: %w", err)
		}
		if transform.Valid {
			e.Transform = transform.String
		}
		e.Params = params

		if parentID.Valid {
			e.ParentID = parentID.Int64
		}
		if mtimeNs.Valid {
			e.MtimeNs = mtimeNs.Int64
		}
		if size.Valid {
			e.Size = size.Int64
		}

		// Reconstruct relative path.
		var relPath string
		if e.Name == "." {
			relPath = "."
		} else if e.ParentID == 0 {
			relPath = e.Name
		} else if parentPath, ok := paths[e.ParentID]; ok {
			if parentPath == "." {
				relPath = e.Name
			} else {
				relPath = parentPath + "/" + e.Name
			}
		} else {
			relPath = e.Name
		}
		paths[e.ID] = relPath

		if err := fn(relPath, e); err != nil {
			return err
		}
	}

	return rows.Err()
}

// OpenBlob returns a reader for the bytes of a blob identified by blobID.
// If the blob is zstd-compressed, the returned reader transparently decompresses.
// For solid blocks, the entire block is decompressed and the relevant slice returned.
// The caller must close the returned ReadCloser.
func (r *Reader) OpenBlob(blobID int64) (io.ReadCloser, error) {
	var offset, clen, ulen int64
	var compressed int
	var blockID, blockOffset int64

	if r.hasSolidBlocks {
		err := r.db.QueryRow(
			`SELECT offset, clen, ulen, compressed, COALESCE(block_id, 0), COALESCE(block_offset, 0) FROM blobs WHERE id = ?`, blobID,
		).Scan(&offset, &clen, &ulen, &compressed, &blockID, &blockOffset)
		if err != nil {
			return nil, fmt.Errorf("store.OpenBlob: lookup blob %d: %w", blobID, err)
		}
	} else {
		err := r.db.QueryRow(
			`SELECT offset, clen, ulen, compressed FROM blobs WHERE id = ?`, blobID,
		).Scan(&offset, &clen, &ulen, &compressed)
		if err != nil {
			return nil, fmt.Errorf("store.OpenBlob: lookup blob %d: %w", blobID, err)
		}
	}

	// Handle solid block blobs: decompress the whole block, slice out the blob.
	if compressed == marc.CompressSolid {
		return r.openSolidBlob(offset, clen, blockOffset, ulen)
	}

	var blobReader io.ReaderAt
	if r.format == marc.FormatSingleFile {
		blobReader = r.arcFile
		// offset points to chunk header start: skip [Type 1B][Len 4B] = 5 bytes.
		offset += 5
	} else {
		blobReader = r.blobFile
	}

	section := io.NewSectionReader(blobReader, offset, clen)

	if compressed == marc.CompressNone {
		return io.NopCloser(section), nil
	}

	// Build decoder options; attach dictionary for dict-compressed blobs.
	opts := []zstd.DOption{zstd.WithDecoderConcurrency(1)}
	if compressed == marc.CompressDict && r.dictData != nil {
		opts = append(opts, zstd.WithDecoderDicts(r.dictData))
	}

	dec, err := zstd.NewReader(section, opts...)
	if err != nil {
		return nil, fmt.Errorf("store.OpenBlob: zstd reader for blob %d: %w", blobID, err)
	}

	return &zstdReadCloser{dec: dec}, nil
}

// solidCacheMaxEntries is the maximum number of decompressed solid blocks cached.
const solidCacheMaxEntries = 4

// openSolidBlob reads a solid block (decompress if not cached) and returns a
// reader for the slice [blockOffset : blockOffset+ulen].
func (r *Reader) openSolidBlob(chunkOffset, clen, blockOffset, ulen int64) (io.ReadCloser, error) {
	block, ok := r.solidCache[chunkOffset]
	if !ok {
		// Skip past the 5-byte chunk header to get the compressed payload.
		payloadOff := chunkOffset + 5
		compData := make([]byte, clen)
		if _, err := r.arcFile.ReadAt(compData, payloadOff); err != nil {
			return nil, fmt.Errorf("store.openSolidBlob: read compressed block at %d: %w", chunkOffset, err)
		}

		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			return nil, fmt.Errorf("store.openSolidBlob: create decoder: %w", err)
		}
		block, err = dec.DecodeAll(compData, nil)
		dec.Close()
		if err != nil {
			return nil, fmt.Errorf("store.openSolidBlob: decompress block at %d: %w", chunkOffset, err)
		}

		// Evict oldest entry if cache is full.
		if len(r.solidCache) >= solidCacheMaxEntries {
			evict := r.solidCacheKeys[0]
			r.solidCacheKeys = r.solidCacheKeys[1:]
			delete(r.solidCache, evict)
		}
		r.solidCache[chunkOffset] = block
		r.solidCacheKeys = append(r.solidCacheKeys, chunkOffset)
	}

	end := blockOffset + ulen
	if end > int64(len(block)) {
		return nil, fmt.Errorf("store.openSolidBlob: slice [%d:%d] out of bounds (block len %d)", blockOffset, end, len(block))
	}

	return io.NopCloser(bytes.NewReader(block[blockOffset:end])), nil
}

// detectSolidColumns checks whether the blobs table has the block_id column
// (present in archives created with solid block compression).
func detectSolidColumns(db *sql.DB) bool {
	rows, err := db.Query("PRAGMA table_info(blobs)")
	if err != nil {
		return false
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == "block_id" {
			return true
		}
	}
	return false
}

// QuerySolidBlockCount returns the number of distinct solid blocks in the archive.
// Returns 0 for archives without solid blocks.
func (r *Reader) QuerySolidBlockCount() int64 {
	if !r.hasSolidBlocks {
		return 0
	}
	var count int64
	_ = r.db.QueryRow(`SELECT COUNT(DISTINCT block_id) FROM blobs WHERE block_id IS NOT NULL`).Scan(&count)
	return count
}

// zstdReadCloser wraps a zstd.Decoder to implement io.ReadCloser.
type zstdReadCloser struct {
	dec *zstd.Decoder
}

func (z *zstdReadCloser) Read(p []byte) (int, error) {
	return z.dec.Read(p)
}

func (z *zstdReadCloser) Close() error {
	z.dec.Close()
	return nil
}

// BlobReaderAdapter returns a marc.BlobReader backed by this Reader.
func (r *Reader) BlobReaderAdapter() marc.BlobReader {
	return &blobReaderAdapter{r: r}
}

type blobReaderAdapter struct {
	r *Reader
}

func (a *blobReaderAdapter) Open(id marc.BlobID) (io.ReadCloser, error) {
	return a.r.OpenBlob(int64(id))
}

// QueryBlobSHAs returns all blob SHA hashes in the archive.
func (r *Reader) QueryBlobSHAs() ([][32]byte, error) {
	rows, err := r.db.Query(`SELECT sha FROM blobs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store.QueryBlobSHAs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var shas [][32]byte
	for rows.Next() {
		var shaBytes []byte
		if err := rows.Scan(&shaBytes); err != nil {
			return nil, fmt.Errorf("store.QueryBlobSHAs: scan: %w", err)
		}
		var sha [32]byte
		copy(sha[:], shaBytes)
		shas = append(shas, sha)
	}
	return shas, rows.Err()
}

// PlanLogStat holds per-transform aggregated plan_log stats.
type PlanLogStat struct {
	TransformID string
	Applied     int64
	Skipped     int64
}

// QueryPlanLog returns aggregated plan_log statistics grouped by transform.
func (r *Reader) QueryPlanLog() ([]PlanLogStat, error) {
	rows, err := r.db.Query(`
		SELECT COALESCE(transform_id, 'raw'),
		       SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN applied = 0 THEN 1 ELSE 0 END)
		FROM plan_log
		GROUP BY COALESCE(transform_id, 'raw')
		ORDER BY COALESCE(transform_id, 'raw')
	`)
	if err != nil {
		return nil, fmt.Errorf("store.QueryPlanLog: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var stats []PlanLogStat
	for rows.Next() {
		var s PlanLogStat
		if err := rows.Scan(&s.TransformID, &s.Applied, &s.Skipped); err != nil {
			return nil, fmt.Errorf("store.QueryPlanLog: scan: %w", err)
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// Overview holds high-level statistics about an archive's content.
type Overview struct {
	EntryCount   int64
	BlobCount    int64
	OriginalSize int64 // sum of entries.size (total source size before archiving)
	TotalUlen    int64 // sum of blobs.ulen (unique blob bytes, after dedup)
	TotalClen    int64 // sum of blobs.clen (compressed blob bytes)
}

// QueryOverview returns aggregate entry/blob counts and sizes from the catalog.
func (r *Reader) QueryOverview() (Overview, error) {
	var ov Overview
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&ov.EntryCount); err != nil {
		return ov, fmt.Errorf("store.QueryOverview: count entries: %w", err)
	}
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM blobs`).Scan(&ov.BlobCount); err != nil {
		return ov, fmt.Errorf("store.QueryOverview: count blobs: %w", err)
	}
	if err := r.db.QueryRow(`SELECT COALESCE(SUM(size), 0) FROM entries`).Scan(&ov.OriginalSize); err != nil {
		return ov, fmt.Errorf("store.QueryOverview: sum entry sizes: %w", err)
	}
	if err := r.db.QueryRow(`SELECT COALESCE(SUM(ulen), 0) FROM blobs`).Scan(&ov.TotalUlen); err != nil {
		return ov, fmt.Errorf("store.QueryOverview: sum ulen: %w", err)
	}
	// For TotalClen, solid blobs share clen (each row stores the full block's clen).
	// Count each solid block's clen once, plus non-solid blobs' clen individually.
	err := r.db.QueryRow(`
		SELECT COALESCE(SUM(clen), 0) FROM (
			SELECT clen FROM blobs WHERE block_id IS NULL
			UNION ALL
			SELECT clen FROM blobs WHERE block_id IS NOT NULL GROUP BY block_id
		)
	`).Scan(&ov.TotalClen)
	if err != nil {
		// Fallback for old archives without block_id column.
		err = r.db.QueryRow(`SELECT COALESCE(SUM(clen), 0) FROM blobs`).Scan(&ov.TotalClen)
		if err != nil {
			return ov, fmt.Errorf("store.QueryOverview: sum clen: %w", err)
		}
	}
	return ov, nil
}

// Close releases all resources held by the reader.
func (r *Reader) Close() error {
	var firstErr error
	if err := r.db.Close(); err != nil {
		firstErr = err
	}
	if r.arcFile != nil {
		if err := r.arcFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.blobFile != nil {
		if err := r.blobFile.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if r.dbTmp != "" {
		_ = os.Remove(r.dbTmp)
	}
	return firstErr
}
