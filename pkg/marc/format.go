// Package marc defines the on-disk .marc archive format: header, manifest,
// object store, and footer/index. It is the only stable, externally-visible
// contract of the archiver.
package marc

import (
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"os"
)

// Version is the current archive format version.
const Version = "0.1.0"

// BlobsExt is the file extension for the legacy split-file blob sidecar.
const BlobsExt = ".blobs"

// Magic is the 8-byte header identifying a single-file .marc archive.
var Magic = [8]byte{'M', 'E', 'T', 'A', 'R', 'C', 0x01, 0x00}

// Chunk type bytes.
const (
	ChunkTypeBlob       = byte(0x01)
	ChunkTypeCatalog    = byte(0x02)
	ChunkTypeSolidBlock = byte(0x03)
)

// Blob compression modes stored in blobs.compressed.
const (
	CompressNone  = 0 // uncompressed
	CompressZstd  = 1 // standard zstd
	CompressDict  = 2 // zstd with shared dictionary
	CompressSolid = 3 // blob is inside a solid block
)

// FooterSize is the fixed size of the archive footer in bytes.
const FooterSize = 24

// Footer holds the parsed footer from a single-file .marc archive.
type Footer struct {
	CatalogOffset    uint64
	BlobRegionOffset uint64
	Checksum         [8]byte
}

// WriteFooter writes the footer to w in big-endian format.
func (f *Footer) WriteFooter(w io.Writer) error {
	if err := binary.Write(w, binary.BigEndian, f.CatalogOffset); err != nil {
		return err
	}
	if err := binary.Write(w, binary.BigEndian, f.BlobRegionOffset); err != nil {
		return err
	}
	_, err := w.Write(f.Checksum[:])
	return err
}

// ReadFooter reads a footer from the last 24 bytes of r.
func ReadFooter(r io.ReaderAt, size int64) (Footer, error) {
	if size < int64(FooterSize)+int64(len(Magic)) {
		return Footer{}, fmt.Errorf("marc: file too small for footer (%d bytes)", size)
	}
	var buf [FooterSize]byte
	if _, err := r.ReadAt(buf[:], size-FooterSize); err != nil {
		return Footer{}, fmt.Errorf("marc: read footer: %w", err)
	}
	var f Footer
	f.CatalogOffset = binary.BigEndian.Uint64(buf[0:8])
	f.BlobRegionOffset = binary.BigEndian.Uint64(buf[8:16])
	copy(f.Checksum[:], buf[16:24])
	return f, nil
}

// FormatKind describes the detected archive format.
const (
	FormatSingleFile = "single-file"
	FormatSplitFile  = "split-file"
)

// DetectFormat reads the first 8 bytes of path and returns the format kind.
func DetectFormat(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("marc.DetectFormat: %w", err)
	}
	defer func() { _ = f.Close() }()

	var buf [16]byte // read enough for both checks
	n, err := f.Read(buf[:])
	if err != nil && n == 0 {
		return "", fmt.Errorf("marc.DetectFormat: read header: %w", err)
	}

	// Check single-file magic.
	if n >= 8 {
		var magic [8]byte
		copy(magic[:], buf[:8])
		if magic == Magic {
			return FormatSingleFile, nil
		}
	}

	// Check for SQLite header ("SQLite format 3\000")
	sqliteHeader := "SQLite format 3\000"
	if n >= len(sqliteHeader) && string(buf[:len(sqliteHeader)]) == sqliteHeader {
		return FormatSplitFile, nil
	}

	return "", fmt.Errorf("marc.DetectFormat: unrecognized file header")
}

// Entry carries metadata for one filesystem node through the pipeline.
// File content is never attached; readers open files on demand.
type Entry struct {
	Path       string      // absolute path on disk
	RelPath    string      // relative to archive root
	Info       fs.FileInfo // from os.Lstat
	LinkTarget string      // non-empty for symlinks; set by scan.Walk
}

// SchemaDDL is the SQLite schema applied when creating a new .marc archive.
const SchemaDDL = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS names (id INTEGER PRIMARY KEY, name TEXT UNIQUE);
CREATE TABLE IF NOT EXISTS entries (
    id        INTEGER PRIMARY KEY,
    parent_id INTEGER REFERENCES entries(id),
    name_id   INTEGER NOT NULL REFERENCES names(id),
    mode      INTEGER NOT NULL,
    mtime_ns  INTEGER,
    size      INTEGER,
    uid       INTEGER NOT NULL DEFAULT 0,
    gid       INTEGER NOT NULL DEFAULT 0,
    transform TEXT,
    params    BLOB,
    symlink_target TEXT
);
CREATE INDEX IF NOT EXISTS idx_entries_parent ON entries(parent_id);
CREATE TABLE IF NOT EXISTS blobs (
    id           INTEGER PRIMARY KEY,
    sha          BLOB UNIQUE NOT NULL,
    offset       INTEGER NOT NULL,
    clen         INTEGER NOT NULL,
    ulen         INTEGER NOT NULL,
    compressed   INTEGER NOT NULL DEFAULT 1,
    block_id     INTEGER,
    block_offset INTEGER
);
CREATE TABLE IF NOT EXISTS entry_blobs (
    entry_id INTEGER REFERENCES entries(id),
    seq      INTEGER,
    blob_id  INTEGER REFERENCES blobs(id),
    PRIMARY KEY (entry_id, seq)
);
CREATE TABLE IF NOT EXISTS plan_log (
    entry_id        INTEGER REFERENCES entries(id),
    transform_id    TEXT,
    estimated_gain  INTEGER,
    estimated_cpu   INTEGER,
    applied         INTEGER,
    reason          TEXT
);
`
