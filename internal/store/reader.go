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
	"github.com/zeebo/blake3"

	_ "modernc.org/sqlite"
)

// chunkHeaderSize is the [Type 1B][Len 4B] prefix in front of every chunk
// payload in a single-file archive.
const chunkHeaderSize = 5

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
	blobBytesSize  int64            // size of the file holding blob bytes: the archive, or the legacy sidecar
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
	payloadLen := int64(binary.BigEndian.Uint32(chunkHdr[1:5]))

	// Both the offset and the length come out of the archive. Reject a chunk the
	// file cannot hold before sizing a buffer from its length, otherwise an
	// edited footer or chunk header buys a 4 GB allocation attempt.
	if catalogOff < 0 || catalogOff > info.Size()-chunkHeaderSize || payloadLen > info.Size()-catalogOff-chunkHeaderSize {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: catalog chunk at %d claims %d bytes, archive holds %d", catalogOff, payloadLen, info.Size())
	}

	// Read compressed catalog payload.
	compressed := make([]byte, payloadLen)
	if _, err := f.ReadAt(compressed, catalogOff+chunkHeaderSize); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store.OpenReader: read catalog payload: %w", err)
	}

	// Decompress with size limit to prevent zip-bomb style attacks.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(maxDecompressedSize))
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
	if len(dbBytes) > maxDecompressedSize {
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
		db:            db,
		arcFile:       f,
		dbTmp:         tmpPath,
		format:        marc.FormatSingleFile,
		solidCache:    make(map[int64][]byte),
		blobBytesSize: info.Size(),
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

	blobInfo, err := blobFile.Stat()
	if err != nil {
		_ = blobFile.Close()
		_ = db.Close()
		return nil, fmt.Errorf("store.OpenReader: stat blobs: %w", err)
	}

	r := &Reader{
		db:            db,
		blobFile:      blobFile,
		format:        marc.FormatSplitFile,
		solidCache:    make(map[int64][]byte),
		blobBytesSize: blobInfo.Size(),
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

	if err := r.checkBlobExtent(blobID, offset, clen, ulen); err != nil {
		return nil, err
	}

	// Handle solid block blobs: decompress the whole block, slice out the blob.
	if compressed == marc.CompressSolid {
		return r.openSolidBlob(offset, clen, blockOffset, ulen)
	}

	var blobReader io.ReaderAt
	if r.format == marc.FormatSingleFile {
		blobReader = r.arcFile
		// offset points to chunk header start: skip [Type 1B][Len 4B] = 5 bytes.
		offset += chunkHeaderSize
	} else {
		blobReader = r.blobFile
	}

	section := io.NewSectionReader(blobReader, offset, clen)

	if compressed == marc.CompressNone {
		return newBoundedBlob(blobID, ulen, io.NopCloser(section)), nil
	}

	// Build decoder options; attach dictionary for dict-compressed blobs.
	// The memory bound comes from the blob's own ulen: every frame in a .marc
	// archive is produced by zstd.EncodeAll, whose declared window never exceeds
	// max(content size, zstd.MinWindowSize), so this cannot reject a genuine
	// blob while a forged ulen no longer inherits klauspost's 64 GiB default.
	opts := []zstd.DOption{
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(blobDecoderMemory(ulen)),
	}
	if compressed == marc.CompressDict && r.dictData != nil {
		opts = append(opts, zstd.WithDecoderDicts(r.dictData))
	}

	dec, err := zstd.NewReader(section, opts...)
	if err != nil {
		return nil, fmt.Errorf("store.OpenBlob: zstd reader for blob %d: %w", blobID, err)
	}

	return newBoundedBlob(blobID, ulen, &zstdReadCloser{dec: dec}), nil
}

// checkBlobExtent rejects a blobs row describing bytes the archive cannot hold.
// offset, clen and ulen are attacker-controlled in a crafted archive and each
// one reaches an allocation, a section reader or a zstd decoder downstream, so
// they are bounded here once instead of at every use site.
//
// offset is the chunk offset as stored in the catalog; the chunk header a
// single-file archive puts in front of the payload is accounted for here.
//
// ulen is bounded against maxDecompressedSize rather than against the archive
// size: a compressed blob decodes to more bytes than it occupies, so the file
// length says nothing about it. The write path caps the same quantity, so a
// ulen over the limit can only come from a forged catalog.
func (r *Reader) checkBlobExtent(blobID, offset, clen, ulen int64) error {
	if offset < 0 || clen < 0 || ulen < 0 {
		return fmt.Errorf("store.OpenBlob: blob %d: negative extent (offset %d, clen %d, ulen %d)", blobID, offset, clen, ulen)
	}
	if ulen > maxDecompressedSize {
		return fmt.Errorf("store.OpenBlob: blob %d: ulen %d is over the %d byte payload limit", blobID, ulen, maxDecompressedSize)
	}

	payloadOff := offset
	if r.format == marc.FormatSingleFile {
		if offset > r.blobBytesSize-chunkHeaderSize {
			return fmt.Errorf("store.OpenBlob: blob %d: chunk offset %d exceeds the %d bytes stored", blobID, offset, r.blobBytesSize)
		}
		payloadOff += chunkHeaderSize
	}
	if clen > r.blobBytesSize || payloadOff > r.blobBytesSize-clen {
		return fmt.Errorf("store.OpenBlob: blob %d: %d compressed bytes at offset %d exceed the %d bytes stored", blobID, clen, payloadOff, r.blobBytesSize)
	}
	return nil
}

// blobDecoderMemory turns a catalog ulen into a zstd decoder memory bound.
// klauspost reads this as the maximum match window when decoding a stream and
// as the maximum decoded size when decoding in one shot, so the floor keeps a
// small blob above the smallest legal window and the ceiling keeps a forged
// ulen from re-enabling the library's 64 GiB default.
func blobDecoderMemory(ulen int64) uint64 {
	switch {
	case ulen < zstd.MinWindowSize:
		return uint64(zstd.MinWindowSize)
	case ulen > maxDecompressedSize:
		return maxDecompressedSize
	default:
		return uint64(ulen)
	}
}

// newBoundedBlob caps a blob at the ulen its catalog row records. Every consumer
// of OpenBlob inherits the bound, including the transform Reverse paths that
// buffer a blob whole with io.ReadAll, so a crafted archive cannot grow one of
// those buffers past the length it advertised.
func newBoundedBlob(blobID, ulen int64, rc io.ReadCloser) io.ReadCloser {
	return &boundedBlobReader{
		lim:    io.LimitedReader{R: rc, N: ulen + 1},
		closer: rc,
		blobID: blobID,
		ulen:   ulen,
	}
}

// boundedBlobReader fails instead of truncating when a blob's payload runs past
// the ulen recorded for it: the limit is ulen+1, so an exhausted limit means the
// payload and the catalog disagree.
type boundedBlobReader struct {
	lim    io.LimitedReader
	closer io.Closer
	blobID int64
	ulen   int64
}

func (b *boundedBlobReader) Read(p []byte) (int, error) {
	n, err := b.lim.Read(p)
	if b.lim.N == 0 {
		return n, fmt.Errorf("store.OpenBlob: blob %d: payload exceeds the %d bytes recorded in the catalog", b.blobID, b.ulen)
	}
	return n, err
}

func (b *boundedBlobReader) Close() error {
	return b.closer.Close()
}

// solidCacheMaxEntries is the maximum number of decompressed solid blocks cached.
const solidCacheMaxEntries = 4

// openSolidBlob reads a solid block (decompress if not cached) and returns a
// reader for the slice [blockOffset : blockOffset+ulen].
func (r *Reader) openSolidBlob(chunkOffset, clen, blockOffset, ulen int64) (io.ReadCloser, error) {
	block, ok := r.solidCache[chunkOffset]
	if !ok {
		// Skip past the 5-byte chunk header to get the compressed payload.
		// clen has already been bounded against the archive size by
		// checkBlobExtent, which is what makes this allocation safe.
		payloadOff := chunkOffset + chunkHeaderSize
		compData := make([]byte, clen)
		if _, err := r.arcFile.ReadAt(compData, payloadOff); err != nil {
			return nil, fmt.Errorf("store.openSolidBlob: read compressed block at %d: %w", chunkOffset, err)
		}

		// A solid block decodes to the whole block, not to this blob, so ulen is
		// not its bound: the shared cap applies instead.
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxDecompressedSize))
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

	// Compared without adding blockOffset to ulen: both come from the catalog,
	// and their sum overflows int64 for crafted values, which would turn this
	// bounds check into the slice panic it exists to prevent.
	if blockOffset < 0 || ulen < 0 || blockOffset > int64(len(block)) || ulen > int64(len(block))-blockOffset {
		return nil, fmt.Errorf("store.openSolidBlob: slice at %d for %d bytes out of bounds (block len %d)", blockOffset, ulen, len(block))
	}

	return io.NopCloser(bytes.NewReader(block[blockOffset : blockOffset+ulen])), nil
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

// QueryBlobSHAs returns the content hash stored for every blob, in blob id
// order. Archives written since marc.StoredSHALen was introduced hold a
// truncated BLAKE3 prefix rather than the full 32-byte hash, so the values are
// returned as raw bytes and callers must not assume a fixed width.
func (r *Reader) QueryBlobSHAs() ([][]byte, error) {
	rows, err := r.db.Query(`SELECT sha FROM blobs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store.QueryBlobSHAs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var shas [][]byte
	for rows.Next() {
		var shaBytes []byte
		if err := rows.Scan(&shaBytes); err != nil {
			return nil, fmt.Errorf("store.QueryBlobSHAs: scan: %w", err)
		}
		shas = append(shas, shaBytes)
	}
	return shas, rows.Err()
}

// QueryBlobIDs returns every blob id in the catalog, in ascending order.
func (r *Reader) QueryBlobIDs() ([]int64, error) {
	rows, err := r.db.Query(`SELECT id FROM blobs ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store.QueryBlobIDs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("store.QueryBlobIDs: scan: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// VerifyBlob recomputes the BLAKE3 hash of blob blobID and compares it with the
// hash recorded in blobs.sha, which the write path computed over the blob's
// stored content: post-transform, pre-compression bytes. OpenBlob hands back
// exactly those bytes (zstd already reversed, solid block already sliced at the
// blob's block_offset/ulen), so this is the one place a comparison is
// meaningful. Any transform Reverse must run after it, never before.
//
// The stored hash is truncated to marc.StoredSHALen bytes once an archive is
// finalized, so only that prefix is compared, and a catalog hash narrower than
// StoredSHALen is rejected rather than compared on a shorter prefix. It detects
// corruption, not tampering: an attacker who rewrites a blob can rewrite its
// catalog hash too.
func (r *Reader) VerifyBlob(blobID int64) error {
	var stored []byte
	if err := r.db.QueryRow(`SELECT sha FROM blobs WHERE id = ?`, blobID).Scan(&stored); err != nil {
		return fmt.Errorf("store.VerifyBlob: lookup blob %d: %w", blobID, err)
	}
	// Enforce a width floor: the comparison below is prefix-based, so a stored
	// hash of one or two bytes would "verify" on a coin flip. Reject it instead.
	if len(stored) < marc.StoredSHALen {
		return fmt.Errorf("store.VerifyBlob: blob %d: catalog hash is %d bytes, want at least %d", blobID, len(stored), marc.StoredSHALen)
	}

	rc, err := r.OpenBlob(blobID)
	if err != nil {
		return fmt.Errorf("store.VerifyBlob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	h := blake3.New()
	if _, err := io.Copy(h, rc); err != nil {
		return fmt.Errorf("store.VerifyBlob: read blob %d: %w", blobID, err)
	}
	sum := h.Sum(nil)

	n := min(len(stored), len(sum))
	if !bytes.Equal(stored[:n], sum[:n]) {
		return fmt.Errorf("store.VerifyBlob: blob %d: hash mismatch (catalog %x, computed %x)", blobID, stored[:n], sum[:n])
	}
	return nil
}

// VerifyFooter recomputes the BLAKE3 hash over every archive byte that precedes
// the footer and compares it with the 8-byte prefix the footer stores. checked
// is false for legacy split-file archives, which carry no footer to check.
//
// This reads the whole file, so it belongs to `marc verify` and never to
// OpenReader, which must stay proportional to the catalog.
func (r *Reader) VerifyFooter() (checked, ok bool, err error) {
	if r.format != marc.FormatSingleFile || r.arcFile == nil {
		return false, false, nil
	}

	info, err := r.arcFile.Stat()
	if err != nil {
		return true, false, fmt.Errorf("store.VerifyFooter: stat: %w", err)
	}
	size := info.Size()

	footer, err := marc.ReadFooter(r.arcFile, size)
	if err != nil {
		return true, false, fmt.Errorf("store.VerifyFooter: %w", err)
	}

	h := blake3.New()
	if _, err := io.Copy(h, io.NewSectionReader(r.arcFile, 0, size-int64(marc.FooterSize))); err != nil {
		return true, false, fmt.Errorf("store.VerifyFooter: read archive: %w", err)
	}
	sum := h.Sum(nil)

	return true, bytes.Equal(sum[:len(footer.Checksum)], footer.Checksum[:]), nil
}

// PlanLogStat holds per-transform aggregated plan_log stats.
type PlanLogStat struct {
	TransformID   string
	Applied       int64
	Skipped       int64
	EstimatedGain int64 // sum of estimated_gain for applied entries
}

// QueryPlanLog returns aggregated plan_log statistics grouped by transform.
func (r *Reader) QueryPlanLog() ([]PlanLogStat, error) {
	rows, err := r.db.Query(`
		SELECT COALESCE(transform_id, 'raw'),
		       SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN applied = 0 THEN 1 ELSE 0 END),
		       SUM(CASE WHEN applied = 1 THEN estimated_gain ELSE 0 END)
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
		if err := rows.Scan(&s.TransformID, &s.Applied, &s.Skipped, &s.EstimatedGain); err != nil {
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

// QueryMeta returns the value of a meta-table key. ok is false when the key
// is absent (older archives may not record every key).
func (r *Reader) QueryMeta(key string) (value string, ok bool, err error) {
	row := r.db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key)
	if scanErr := row.Scan(&value); scanErr != nil {
		if scanErr == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("store.QueryMeta %s: %w", key, scanErr)
	}
	return value, true, nil
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
