// Package store executes transforms, writes deduplicated blobs, and produces
// the final .marc archive file.
package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/arhuman/metarc-go/internal/plan"
	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
	"github.com/zeebo/blake3"

	_ "modernc.org/sqlite"
)

// Writer creates a single-file .marc archive.
//
// Layout: [Magic 8B][Blob chunks...][Catalog chunk][Footer 24B]
// The catalog is an in-memory SQLite DB serialized at Close time.
type Writer struct {
	outFile    *os.File
	db         *sql.DB
	dbPath     string // temp file path for in-memory catalog
	tx         *sql.Tx
	blobOff    int64 // current write position in the output file
	entryN     int64 // entries written in current tx batch
	nameCache  map[string]int64
	parentMap  map[string]int64
	compressor string // "zstd" or "none"
	zstdEnc    *zstd.Encoder
	dictEnc    *zstd.Encoder  // encoder with dictionary (reused across blobs)
	dictData   []byte         // trained zstd dictionary (nil if not using dict compression)
	dictSimple bool           // online dict training mode
	dictSamples [][]byte      // buffered samples for online training
	dictSampleBytes int64     // total bytes buffered so far
	dictTrained bool          // true once online training completed (or failed)
	fileHasher *blake3.Hasher // running hash of all bytes written so far
	solidAcc   *solidAccumulator // non-nil when solid block compression is enabled
	solidSize  int64              // solid block size threshold (0 = disabled)
}

const batchSize = 1000

// Option configures an OpenWriter call.
type Option func(*Writer)

// WithCompressor sets the blob compressor. Valid values: "zstd" (default), "none".
func WithCompressor(c string) Option {
	return func(w *Writer) {
		w.compressor = c
	}
}

// WithDictCompress enables zstd dictionary compression using the provided
// pre-trained dictionary bytes. Requires compressor to be "zstd".
func WithDictCompress(dict []byte) Option {
	return func(w *Writer) {
		w.dictData = dict
	}
}

// WithDictSimple enables online dictionary training: the Writer collects
// raw content from the first small blobs, trains a dictionary mid-stream,
// and switches to dict-compressed zstd for all subsequent blobs.
func WithDictSimple() Option {
	return func(w *Writer) {
		w.dictSimple = true
	}
}

// WithSolidBlockSize enables solid block compression. Multiple blobs are
// concatenated and compressed as a single zstd frame, exploiting cross-file
// redundancy. size is the maximum uncompressed block size in bytes.
func WithSolidBlockSize(size int64) Option {
	return func(w *Writer) {
		w.solidSize = size
	}
}

// OpenWriter creates a new single-file .marc archive at marcPath.
func OpenWriter(marcPath string, opts ...Option) (*Writer, error) {
	// Remove any existing files (including legacy sidecar).
	_ = os.Remove(marcPath)
	_ = os.Remove(marcPath + marc.BlobsExt)

	outFile, err := os.Create(marcPath)
	if err != nil {
		return nil, fmt.Errorf("store.OpenWriter: create output: %w", err)
	}

	// Create a temporary SQLite file for the catalog (will be serialized at Close).
	tmpDB, err := os.CreateTemp("", "marc-catalog-*.db")
	if err != nil {
		_ = outFile.Close()
		return nil, fmt.Errorf("store.OpenWriter: create temp db: %w", err)
	}
	dbPath := tmpDB.Name()
	_ = tmpDB.Close()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		_ = outFile.Close()
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("store.OpenWriter: open db: %w", err)
	}

	// Performance pragmas.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA cache_size=-64000", // 64 MB
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			_ = outFile.Close()
			_ = os.Remove(dbPath)
			return nil, fmt.Errorf("store.OpenWriter: pragma: %w", err)
		}
	}

	if _, err := db.Exec(marc.SchemaDDL); err != nil {
		_ = db.Close()
		_ = outFile.Close()
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("store.OpenWriter: schema: %w", err)
	}

	// Insert version metadata.
	if _, err := db.Exec(`INSERT INTO meta (key, value) VALUES ('version', ?)`, marc.Version); err != nil {
		_ = db.Close()
		_ = outFile.Close()
		_ = os.Remove(dbPath)
		return nil, fmt.Errorf("store.OpenWriter: meta: %w", err)
	}

	w := &Writer{
		outFile:    outFile,
		db:         db,
		dbPath:     dbPath,
		nameCache:  make(map[string]int64),
		parentMap:  make(map[string]int64),
		compressor: "zstd",
		fileHasher: blake3.New(),
	}

	for _, opt := range opts {
		opt(w)
	}

	// Create solid accumulator if solid block mode was requested.
	if w.solidSize > 0 {
		w.solidAcc = &solidAccumulator{
			w:            w,
			maxBlockSize: w.solidSize,
		}
	}

	// Write magic header.
	if err := w.writeAndHash(marc.Magic[:]); err != nil {
		w.cleanup()
		return nil, fmt.Errorf("store.OpenWriter: write magic: %w", err)
	}
	w.blobOff = int64(len(marc.Magic))

	tx, err := db.Begin()
	if err != nil {
		w.cleanup()
		return nil, fmt.Errorf("store.OpenWriter: begin tx: %w", err)
	}
	w.tx = tx

	return w, nil
}

// writeAndHash writes data to outFile and updates the running hash.
func (w *Writer) writeAndHash(data []byte) error {
	n, err := w.outFile.Write(data)
	if err != nil {
		return err
	}
	_, _ = w.fileHasher.Write(data[:n])
	return nil
}

// internName returns the name_id for a filename, inserting if needed.
func (w *Writer) internName(name string) (int64, error) {
	if id, ok := w.nameCache[name]; ok {
		return id, nil
	}

	res, err := w.tx.Exec(`INSERT OR IGNORE INTO names (name) VALUES (?)`, name)
	if err != nil {
		return 0, fmt.Errorf("store: intern name: %w", err)
	}

	id, _ := res.LastInsertId()
	if id == 0 {
		// Already existed.
		err = w.tx.QueryRow(`SELECT id FROM names WHERE name = ?`, name).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("store: lookup name: %w", err)
		}
	}

	w.nameCache[name] = id
	return id, nil
}

// parentID resolves the entry id for the parent directory of relPath.
// Returns 0 (NULL) for top-level entries.
func (w *Writer) parentID(relPath string) int64 {
	parent := filepath.Dir(relPath)
	if parent == "." {
		return 0
	}
	return w.parentMap[parent]
}

// WriteEntryWithSHA writes a single entry using a pre-computed BLAKE3-256 hash.
// The SHA is used for dedup lookup, skipping redundant hashing when analyze
// workers have already computed it. Must be called from a single goroutine.
func (w *Writer) WriteEntryWithSHA(ctx context.Context, e marc.Entry, sha [32]byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	name := filepath.Base(e.RelPath)
	if e.RelPath == "." {
		name = "."
	}

	nameID, err := w.internName(name)
	if err != nil {
		return err
	}

	parentID := w.parentID(e.RelPath)
	mode := uint32(e.Info.Mode())
	mtimeNs := e.Info.ModTime().UnixNano()
	uid, gid := ownerIDs(e.Info)

	if e.Info.Mode()&fs.ModeSymlink != 0 {
		return w.writeSymlink(e, nameID, parentID, mode, mtimeNs, uid, gid)
	}
	if e.Info.IsDir() {
		return w.writeDir(e, nameID, parentID, mode, mtimeNs, uid, gid)
	}
	return w.writeFileWithSHA(ctx, e, nameID, parentID, mode, mtimeNs, uid, gid, sha)
}

// WriteEntry writes a single entry (file or directory) to the archive.
// For files, it reads content from disk and writes blob chunks to the archive.
// Must be called from a single goroutine.
func (w *Writer) WriteEntry(ctx context.Context, e marc.Entry) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	name := filepath.Base(e.RelPath)
	if e.RelPath == "." {
		name = "."
	}

	nameID, err := w.internName(name)
	if err != nil {
		return err
	}

	parentID := w.parentID(e.RelPath)
	mode := uint32(e.Info.Mode())
	mtimeNs := e.Info.ModTime().UnixNano()
	uid, gid := ownerIDs(e.Info)

	if e.Info.Mode()&fs.ModeSymlink != 0 {
		return w.writeSymlink(e, nameID, parentID, mode, mtimeNs, uid, gid)
	}
	if e.Info.IsDir() {
		return w.writeDir(e, nameID, parentID, mode, mtimeNs, uid, gid)
	}
	return w.writeFile(ctx, e, nameID, parentID, mode, mtimeNs, uid, gid)
}

func (w *Writer) writeDir(e marc.Entry, nameID, parentID int64, mode uint32, mtimeNs int64, uid, gid uint32) error {
	res, err := w.tx.Exec(
		`INSERT INTO entries (parent_id, name_id, mode, mtime_ns, size, uid, gid) VALUES (?, ?, ?, ?, 0, ?, ?)`,
		nullableInt64(parentID), nameID, mode, mtimeNs, uid, gid,
	)
	if err != nil {
		return fmt.Errorf("store: insert dir entry: %w", err)
	}

	entryID, _ := res.LastInsertId()
	w.parentMap[e.RelPath] = entryID
	w.entryN++

	return w.maybeBatchCommit()
}

func (w *Writer) writeSymlink(e marc.Entry, nameID, parentID int64, mode uint32, mtimeNs int64, uid, gid uint32) error {
	res, err := w.tx.Exec(
		`INSERT INTO entries (parent_id, name_id, mode, mtime_ns, size, uid, gid, symlink_target) VALUES (?, ?, ?, ?, 0, ?, ?, ?)`,
		nullableInt64(parentID), nameID, mode, mtimeNs, uid, gid, e.LinkTarget,
	)
	if err != nil {
		return fmt.Errorf("store: insert symlink entry: %w", err)
	}

	entryID, _ := res.LastInsertId()
	w.parentMap[e.RelPath] = entryID
	w.entryN++

	return w.maybeBatchCommit()
}

func (w *Writer) writeFile(ctx context.Context, e marc.Entry, nameID, parentID int64, mode uint32, mtimeNs int64, uid, gid uint32) error {
	return w.writeFileWithSHA(ctx, e, nameID, parentID, mode, mtimeNs, uid, gid, [32]byte{})
}

// writeFileWithSHA writes a file entry, optionally using a pre-computed BLAKE3 hash.
// If sha is zero, the hash is computed during blob writing (original behavior).
func (w *Writer) writeFileWithSHA(ctx context.Context, e marc.Entry, nameID, parentID int64, mode uint32, mtimeNs int64, uid, gid uint32, sha [32]byte) error {
	sink := &blobSink{
		w:        w,
		compress: w.compressor,
		zstdEnc:  w.zstdEnc,
		dictEnc:  w.dictEnc,
	}

	facts := marc.Facts{Size: e.Info.Size()}
	t, decision := plan.Decide(ctx, e, facts)

	// Open the source file.
	f, err := os.Open(e.Path)
	if err != nil {
		return fmt.Errorf("store: open file %s: %w", e.Path, err)
	}
	defer func() { _ = f.Close() }()

	var transformID string
	var blobIDs []marc.BlobID
	var params []byte

	zeroSHA := [32]byte{}
	hasSHA := sha != zeroSHA

	if t != nil {
		result, applyErr := t.Apply(ctx, e, f, sink)
		if errors.Is(applyErr, marc.ErrNotApplicable) {
			// Fall back: seek file to start, write via raw sink.
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return fmt.Errorf("store: seek %s: %w", e.Path, err)
			}
			var id marc.BlobID
			if hasSHA {
				id, err = sink.WriteWithSHA(ctx, f, sha)
			} else {
				id, err = sink.Write(ctx, f)
			}
			if err != nil {
				return fmt.Errorf("store: fallback write %s: %w", e.Path, err)
			}
			blobIDs = []marc.BlobID{id}
			decision.TransformID = ""
			decision.Applied = false
			decision.Reason = fmt.Sprintf("%s: content not recognized, fell back to raw", t.ID())
		} else if applyErr != nil {
			return fmt.Errorf("store: apply transform %s to %s: %w", t.ID(), e.Path, applyErr)
		} else {
			transformID = string(t.ID())
			blobIDs = result.BlobIDs
			params = result.Params
		}
	} else {
		// No transform -- write raw blob via sink (use pre-computed SHA if available).
		var id marc.BlobID
		if hasSHA {
			id, err = sink.WriteWithSHA(ctx, f, sha)
		} else {
			id, err = sink.Write(ctx, f)
		}
		if err != nil {
			return fmt.Errorf("store: write raw blob %s: %w", e.Path, err)
		}
		blobIDs = []marc.BlobID{id}
	}

	// Reclaim the zstd encoders if sink created/used them.
	if sink.zstdEnc != nil {
		w.zstdEnc = sink.zstdEnc
	}
	if sink.dictEnc != nil {
		w.dictEnc = sink.dictEnc
	}

	entryID, err := w.insertEntryWithBlobs(e, nameID, parentID, mode, mtimeNs, uid, gid, transformID, params, blobIDs)
	if err != nil {
		return err
	}

	return w.writePlanLog(entryID, decision)
}

// insertEntryWithBlobs inserts an entry row with optional transform metadata
// and links it to one or more blobs. Returns the inserted entry ID.
func (w *Writer) insertEntryWithBlobs(e marc.Entry, nameID, parentID int64, mode uint32, mtimeNs int64, uid, gid uint32, transformID string, params []byte, blobIDs []marc.BlobID) (int64, error) {
	var txfm any
	if transformID != "" {
		txfm = transformID
	}
	var p any
	if len(params) > 0 {
		p = params
	}

	entryRes, err := w.tx.Exec(
		`INSERT INTO entries (parent_id, name_id, mode, mtime_ns, size, uid, gid, transform, params) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableInt64(parentID), nameID, mode, mtimeNs, e.Info.Size(), uid, gid, txfm, p,
	)
	if err != nil {
		return 0, fmt.Errorf("store: insert file entry: %w", err)
	}
	entryID, _ := entryRes.LastInsertId()

	for seq, blobID := range blobIDs {
		if _, err := w.tx.Exec(
			`INSERT INTO entry_blobs (entry_id, seq, blob_id) VALUES (?, ?, ?)`,
			entryID, seq, int64(blobID),
		); err != nil {
			return 0, fmt.Errorf("store: insert entry_blob: %w", err)
		}
	}

	w.entryN++
	return entryID, w.maybeBatchCommit()
}

// writePlanLog inserts one plan_log row for the given entry.
func (w *Writer) writePlanLog(entryID int64, d plan.Decision) error {
	applied := 0
	if d.Applied {
		applied = 1
	}
	var txID any
	if d.TransformID != "" {
		txID = d.TransformID
	}
	_, err := w.tx.Exec(
		`INSERT INTO plan_log (entry_id, transform_id, estimated_gain, estimated_cpu, applied, reason) VALUES (?, ?, ?, ?, ?, ?)`,
		entryID, txID, d.EstimatedGain, d.EstimatedCPU, applied, d.Reason,
	)
	if err != nil {
		return fmt.Errorf("store: write plan_log: %w", err)
	}
	return nil
}

// DeletePlanLog removes all rows from plan_log.
func (w *Writer) DeletePlanLog() error {
	if err := w.flushTx(); err != nil {
		return fmt.Errorf("store: delete plan_log: flush: %w", err)
	}
	_, err := w.tx.Exec(`DELETE FROM plan_log`)
	if err != nil {
		return fmt.Errorf("store: delete plan_log: %w", err)
	}
	return nil
}

func (w *Writer) maybeBatchCommit() error {
	if w.entryN < batchSize {
		return nil
	}
	return w.flushTx()
}

func (w *Writer) flushTx() error {
	if w.tx == nil {
		return nil
	}
	if err := w.tx.Commit(); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	w.entryN = 0

	tx, err := w.db.Begin()
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	w.tx = tx
	return nil
}

// Close finalizes the archive: commits the pending transaction, serializes the
// catalog SQLite DB, writes the catalog chunk and footer, then closes all resources.
func (w *Writer) Close() error {
	var errs []error

	// Flush remaining solid block data before committing the transaction,
	// because flush needs the tx to update blob rows.
	if w.solidAcc != nil && w.tx != nil {
		if err := w.solidAcc.flush(); err != nil {
			errs = append(errs, fmt.Errorf("solid flush: %w", err))
		}
	}

	if w.tx != nil {
		if err := w.tx.Commit(); err != nil {
			errs = append(errs, fmt.Errorf("commit: %w", err))
		}
		w.tx = nil
	}

	// Serialize the catalog and write catalog chunk + footer.
	if w.db != nil && w.outFile != nil && len(errs) == 0 {
		if err := w.finalize(); err != nil {
			errs = append(errs, fmt.Errorf("finalize: %w", err))
		}
	}

	if w.db != nil {
		if err := w.db.Close(); err != nil {
			errs = append(errs, fmt.Errorf("db close: %w", err))
		}
	}

	// Clean up temp catalog DB.
	if w.dbPath != "" {
		_ = os.Remove(w.dbPath)
		_ = os.Remove(w.dbPath + "-wal")
		_ = os.Remove(w.dbPath + "-shm")
	}

	if w.outFile != nil {
		if err := w.outFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("output close: %w", err))
		}
	}

	return errors.Join(errs...)
}

// finalize serializes the catalog and writes the catalog chunk + footer.
func (w *Writer) finalize() error {
	// If simple dict mode had samples but never trained (too few blobs), train now.
	// This won't help blobs already written, but stores the dict for future reference.
	if w.dictSimple && !w.dictTrained && len(w.dictSamples) >= minSamplesForTraining {
		w.trainDictFromSamples()
	}

	// Store dictionary in meta if dict compression was used.
	if w.dictData != nil {
		encoded := base64.StdEncoding.EncodeToString(w.dictData)
		if _, err := w.db.Exec(`INSERT OR REPLACE INTO meta (key, value) VALUES ('dict', ?)`, encoded); err != nil {
			return fmt.Errorf("store dict in meta: %w", err)
		}
	}

	// VACUUM INTO a clean copy to serialize the catalog.
	tmpF, err := os.CreateTemp("", "marc-catalog-serial-*.db")
	if err != nil {
		return fmt.Errorf("create serial temp: %w", err)
	}
	serialPath := tmpF.Name()
	_ = tmpF.Close()
	defer func() { _ = os.Remove(serialPath) }()

	if _, err := w.db.Exec(`VACUUM INTO ?`, serialPath); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}

	// Read the serialized DB bytes.
	dbBytes, err := os.ReadFile(serialPath)
	if err != nil {
		return fmt.Errorf("read serialized db: %w", err)
	}

	// Compress with zstd.
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))
	if err != nil {
		return fmt.Errorf("create zstd encoder for catalog: %w", err)
	}
	compressed := enc.EncodeAll(dbBytes, nil)
	_ = enc.Close()

	// Record catalog offset.
	catalogOffset := w.blobOff

	if len(compressed) > math.MaxUint32 {
		return fmt.Errorf("catalog chunk exceeds max chunk size (4 GB)")
	}

	// Write catalog chunk: [Type=0x02][Len uint32 BE][compressed payload]
	var chunkHeader [5]byte
	chunkHeader[0] = marc.ChunkTypeCatalog
	binary.BigEndian.PutUint32(chunkHeader[1:5], uint32(len(compressed)))
	if err := w.writeAndHash(chunkHeader[:]); err != nil {
		return fmt.Errorf("write catalog chunk header: %w", err)
	}
	if err := w.writeAndHash(compressed); err != nil {
		return fmt.Errorf("write catalog chunk payload: %w", err)
	}

	// Compute checksum of everything written so far.
	var checksum [8]byte
	sum := w.fileHasher.Sum(nil)
	copy(checksum[:], sum[:8])

	// Write footer (not hashed).
	footer := marc.Footer{
		CatalogOffset:    uint64(catalogOffset),
		BlobRegionOffset: uint64(len(marc.Magic)),
		Checksum:         checksum,
	}
	if err := footer.WriteFooter(w.outFile); err != nil {
		return fmt.Errorf("write footer: %w", err)
	}

	return nil
}

// collectSample buffers a copy of data for online dictionary training.
// Once enough samples are collected, it trains the dictionary and switches mode.
func (w *Writer) collectSample(data []byte) {
	if w.dictTrained || w.dictData != nil {
		return
	}

	sz := int64(len(data))
	if sz == 0 || sz > maxSingleFileSize {
		return
	}

	sample := make([]byte, len(data))
	copy(sample, data)
	w.dictSamples = append(w.dictSamples, sample)
	w.dictSampleBytes += sz

	// Train once we have enough samples.
	if len(w.dictSamples) >= minSamplesForTraining && w.dictSampleBytes >= 32*1024 {
		w.trainDictFromSamples()
	}

	// Also train if we've collected the maximum number of samples.
	if !w.dictTrained && len(w.dictSamples) >= defaultMaxSamples {
		w.trainDictFromSamples()
	}
}

// trainDictFromSamples trains a zstd dictionary from the buffered samples.
func (w *Writer) trainDictFromSamples() {
	w.dictTrained = true // mark done regardless of outcome

	var history []byte
	for _, s := range w.dictSamples {
		history = append(history, s...)
		if len(history) >= dictMaxSize {
			history = history[:dictMaxSize]
			break
		}
	}

	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       1,
		Contents: w.dictSamples,
		History:  history,
		Level:    zstd.SpeedDefault,
	})
	if err != nil {
		// Training failed; continue with standard zstd.
		w.dictSamples = nil
		return
	}

	w.dictData = dict
	w.dictSamples = nil // free memory
}

// cleanup releases all resources without writing footer (used on init errors).
func (w *Writer) cleanup() {
	if w.db != nil {
		_ = w.db.Close()
	}
	if w.dbPath != "" {
		_ = os.Remove(w.dbPath)
	}
	if w.outFile != nil {
		_ = w.outFile.Close()
	}
}

// nullableInt64 returns nil for 0 (representing NULL parent_id for root entries).
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// EntryRow represents a single entry read back from the archive.
type EntryRow struct {
	ID         int64
	ParentID   int64 // 0 = root
	Name       string
	Mode       fs.FileMode
	MtimeNs    int64
	Size       int64
	UID        uint32
	GID        uint32
	BlobID     int64  // 0 for directories; first blob for files
	Transform  string // transform ID or "" for raw
	Params     []byte // per-entry transform params
	LinkTarget string // non-empty for symlinks
}
