package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
	"github.com/zeebo/blake3"
)

// blobSink implements marc.BlobSink, writing blob chunks to the single-file
// archive and recording them in the SQLite blobs table. It deduplicates on BLAKE3-256.
//
// Each blob is written as a chunk: [Type=0x01 (1B)][Len uint32 BE][payload].
// The offset stored in the blobs table points to the start of the chunk header.
type blobSink struct {
	w           *Writer
	compress    string
	zstdEnc     *zstd.Encoder
	dictEnc     *zstd.Encoder     // encoder with dictionary (when dict-compress enabled)
	sourceSHA   [32]byte          // original content SHA for dedup (set by writeFileWithSHA)
	relPath     string            // entry the sink is writing for, used to name it in errors
	zstdLevel   zstd.EncoderLevel // level for per-blob zstd encoder
	dictLevel   zstd.EncoderLevel // level for dict-encoded zstd encoder
	window      int               // optional zstd window size (0 = library default)
	payloadSize int64             // entry size from scan, used to pick the streaming path
}

// Write computes BLAKE3-256 while streaming, deduplicates, and writes the blob chunk.
func (s *blobSink) Write(_ context.Context, r io.Reader) (marc.BlobID, error) {
	// Stream through a BLAKE3 hasher while buffering for potential write.
	h := blake3.New()
	data, err := io.ReadAll(io.TeeReader(r, h))
	if err != nil {
		return 0, fmt.Errorf("blobSink.Write: read: %w", err)
	}

	var sha [32]byte
	copy(sha[:], h.Sum(nil))

	return s.writeData(data, sha)
}

// WriteWithSHA writes blob data with a pre-computed BLAKE3-256 hash,
// skipping the hash computation. Used when analyze workers have already hashed.
//
// A blob large enough to become a solid block of its own is compressed as a
// stream rather than buffered whole. Buffering one costs several times the
// payload (the payload, the encoder's working buffer and the compressed result
// are resident together), which is what a single large file does to peak RSS.
func (s *blobSink) WriteWithSHA(_ context.Context, r io.Reader, sha [32]byte) (marc.BlobID, error) {
	if s.streamApplies(s.payloadSize) {
		return s.writeStreaming(r, sha, s.payloadSize)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("blobSink.WriteWithSHA: read: %w", err)
	}
	return s.writeData(data, sha)
}

// WriteRaw writes blob data without zstd compression and without routing
// through the solid accumulator. Used by transforms (e.g. passthrough)
// that handle already-compressed file types where running through zstd
// is wasted CPU and slightly inflates the archive. The blob is recorded
// with compressed=CompressNone so the reader streams its bytes as-is.
//
// Dedup still applies: if a blob with the same content hash already
// exists, the existing BlobID is reused.
func (s *blobSink) WriteRaw(_ context.Context, r io.Reader) (marc.BlobID, error) {
	h := blake3.New()
	data, err := io.ReadAll(io.TeeReader(r, h))
	if err != nil {
		return 0, fmt.Errorf("blobSink.WriteRaw: read: %w", err)
	}
	var sha [32]byte
	copy(sha[:], h.Sum(nil))
	return s.writeDataRaw(data, sha)
}

// streamApplies reports whether a payload of this size takes the streaming path.
//
// The candidate is a blob that would become a solid block of its own: large
// enough that buffering it sets peak RSS, and already destined for a frame with
// no other member, so streaming changes how it is written and not what it is
// grouped with. Everything that shares a block stays on the buffered path,
// because a shared frame only pays off concatenated. The dict modes are
// excluded: their encoder is selected per blob.
//
// size comes from the caller's already-stat'ed entry rather than from r, so
// deciding costs no syscall and no open on the blobs that skip this path.
func (s *blobSink) streamApplies(size int64) bool {
	if s.compress != "zstd" || s.w.dictData != nil || s.w.dictSimple {
		return false
	}
	if s.w.solidAcc == nil {
		return false
	}
	// Mirrors addBlob's oversized test: at or below the block size the blob
	// joins the shared frame and must stay buffered.
	return size > s.w.solidAcc.maxBlockSize
}

// writeStreaming routes an oversized blob into its own streamed solid block.
// It flushes any block in flight first, because the streamed block has to stand
// alone, and resolves dedup up front: the hash is already known, so a duplicate
// costs neither a read nor an encode.
func (s *blobSink) writeStreaming(r io.Reader, sha [32]byte, ulen int64) (marc.BlobID, error) {
	if err := checkBlobPayloadSize(s.relPath, ulen); err != nil {
		return 0, fmt.Errorf("blobSink.writeStreaming: %w", err)
	}
	if id, ok := s.Reuse(sha); ok {
		return id, nil
	}
	if err := s.w.solidAcc.flush(); err != nil {
		return 0, fmt.Errorf("blobSink.writeStreaming: flush open block: %w", err)
	}
	id, err := s.w.solidAcc.addOversizedStream(r, ulen, sha, s.sourceSHA)
	if err != nil {
		return 0, fmt.Errorf("blobSink.writeStreaming: %w", err)
	}
	return id, nil
}

// writeDataRaw is like writeData but never compresses and never goes
// through the solid accumulator. The blob row is inserted with
// compressed=CompressNone.
func (s *blobSink) writeDataRaw(data []byte, sha [32]byte) (marc.BlobID, error) {
	if err := checkBlobPayloadSize(s.relPath, int64(len(data))); err != nil {
		return 0, fmt.Errorf("blobSink.writeDataRaw: %w", err)
	}

	if id, ok := s.Reuse(sha); ok {
		return id, nil
	}

	if len(data) > math.MaxUint32 {
		return 0, fmt.Errorf("blobSink.writeDataRaw: blob exceeds max chunk size (4 GB)")
	}

	chunkOffset := s.w.blobOff
	var chunkHeader [5]byte
	chunkHeader[0] = marc.ChunkTypeBlob
	binary.BigEndian.PutUint32(chunkHeader[1:5], uint32(len(data)))

	if err := s.w.writeAndHash(chunkHeader[:]); err != nil {
		return 0, fmt.Errorf("blobSink.writeDataRaw: write chunk header: %w", err)
	}
	if err := s.w.writeAndHash(data); err != nil {
		return 0, fmt.Errorf("blobSink.writeDataRaw: write chunk payload: %w", err)
	}
	s.w.blobOff += int64(len(chunkHeader)) + int64(len(data))

	ulen := int64(len(data))
	clen := ulen
	var sourceSHAParam any
	zeroSHA := [32]byte{}
	if s.sourceSHA != zeroSHA {
		sourceSHAParam = s.sourceSHA[:]
	}
	res, err := s.w.tx.Exec(
		`INSERT INTO blobs (sha, source_sha, offset, clen, ulen, compressed) VALUES (?, ?, ?, ?, ?, ?)`,
		sha[:], sourceSHAParam, chunkOffset, clen, ulen, marc.CompressNone,
	)
	if err != nil {
		return 0, fmt.Errorf("blobSink.writeDataRaw: insert blob: %w", err)
	}
	id, _ := res.LastInsertId()
	return marc.BlobID(id), nil
}

// writeData deduplicates on sha, compresses, and writes the blob chunk.
func (s *blobSink) writeData(data []byte, sha [32]byte) (marc.BlobID, error) {
	// Ahead of the dedup lookup: the cap is a property of the payload, not of
	// whether this archive happens to have stored it already.
	if err := checkBlobPayloadSize(s.relPath, int64(len(data))); err != nil {
		return 0, fmt.Errorf("blobSink.writeData: %w", err)
	}

	// Check for existing blob with same hash.
	if id, ok := s.Reuse(sha); ok {
		return id, nil
	}

	// Route through solid accumulator when active. A solid block exists only
	// to share one zstd frame, so --final-compressor none must bypass it.
	if s.w.solidAcc != nil && s.compress == "zstd" {
		return s.w.solidAcc.addBlob(data, sha, s.sourceSHA)
	}

	// Online dict training: collect samples from small blobs.
	if s.w.dictSimple && !s.w.dictTrained && s.compress == "zstd" {
		s.w.collectSample(data)
	}

	// Prepare the payload (raw or zstd-compressed).
	var payload []byte
	var err error
	compressed := marc.CompressNone
	ulen := int64(len(data))

	if s.compress == "zstd" && s.w.dictData != nil {
		compressed = marc.CompressDict
		payload, err = s.compressDictZstd(data)
		if err != nil {
			return 0, fmt.Errorf("blobSink.writeData: dict compress: %w", err)
		}
	} else if s.compress == "zstd" {
		compressed = marc.CompressZstd
		payload, err = s.compressZstd(data)
		if err != nil {
			return 0, fmt.Errorf("blobSink.writeData: compress: %w", err)
		}
	} else {
		payload = data
	}

	if len(payload) > math.MaxUint32 {
		return 0, fmt.Errorf("blobSink.writeData: blob exceeds max chunk size (4 GB)")
	}

	// Record the chunk header offset (where the blob chunk starts).
	chunkOffset := s.w.blobOff

	// Write chunk: [0x01][len uint32 BE][payload]
	var chunkHeader [5]byte
	chunkHeader[0] = marc.ChunkTypeBlob
	binary.BigEndian.PutUint32(chunkHeader[1:5], uint32(len(payload)))

	if err := s.w.writeAndHash(chunkHeader[:]); err != nil {
		return 0, fmt.Errorf("blobSink.writeData: write chunk header: %w", err)
	}
	if err := s.w.writeAndHash(payload); err != nil {
		return 0, fmt.Errorf("blobSink.writeData: write chunk payload: %w", err)
	}
	s.w.blobOff += int64(len(chunkHeader)) + int64(len(payload))

	clen := int64(len(payload))

	// Insert blob row. Offset points to start of chunk header.
	// source_sha records the original content hash (before transforms) for dedup.
	var sourceSHAParam any
	zeroSHA := [32]byte{}
	if s.sourceSHA != zeroSHA {
		sourceSHAParam = s.sourceSHA[:]
	}
	res, err := s.w.tx.Exec(
		`INSERT INTO blobs (sha, source_sha, offset, clen, ulen, compressed) VALUES (?, ?, ?, ?, ?, ?)`,
		sha[:], sourceSHAParam, chunkOffset, clen, ulen, compressed,
	)
	if err != nil {
		return 0, fmt.Errorf("blobSink.writeData: insert blob: %w", err)
	}
	id, _ := res.LastInsertId()
	return marc.BlobID(id), nil
}

// Reuse looks up an existing blob by its BLAKE3-256 hash.
// It checks source_sha first (original content hash, for pre-transform dedup),
// then falls back to sha (blob content hash).
func (s *blobSink) Reuse(sha [32]byte) (marc.BlobID, bool) {
	var id int64
	// Check source_sha first: catches duplicates before any transform has run.
	err := s.w.tx.QueryRow(`SELECT id FROM blobs WHERE source_sha = ?`, sha[:]).Scan(&id)
	if err == nil {
		return marc.BlobID(id), true
	}
	// Fall back to blob content hash.
	err = s.w.tx.QueryRow(`SELECT id FROM blobs WHERE sha = ?`, sha[:]).Scan(&id)
	if err != nil {
		return 0, false
	}
	return marc.BlobID(id), true
}

// compressZstd compresses data using zstd, reusing the encoder if available.
func (s *blobSink) compressZstd(data []byte) ([]byte, error) {
	if s.zstdEnc == nil {
		opts := []zstd.EOption{
			zstd.WithEncoderLevel(s.zstdLevel),
			zstd.WithEncoderConcurrency(1),
		}
		if s.window > 0 {
			opts = append(opts, zstd.WithWindowSize(s.window))
		}
		enc, err := zstd.NewWriter(nil, opts...)
		if err != nil {
			return nil, fmt.Errorf("blobSink: create zstd encoder: %w", err)
		}
		s.zstdEnc = enc
	}
	return s.zstdEnc.EncodeAll(data, nil), nil
}

// compressDictZstd compresses data using zstd with a shared dictionary.
func (s *blobSink) compressDictZstd(data []byte) ([]byte, error) {
	if s.dictEnc == nil {
		opts := []zstd.EOption{
			zstd.WithEncoderLevel(s.dictLevel),
			zstd.WithEncoderConcurrency(1),
			zstd.WithEncoderDict(s.w.dictData),
		}
		if s.window > 0 {
			opts = append(opts, zstd.WithWindowSize(s.window))
		}
		enc, err := zstd.NewWriter(nil, opts...)
		if err != nil {
			return nil, fmt.Errorf("blobSink: create dict zstd encoder: %w", err)
		}
		s.dictEnc = enc
	}
	return s.dictEnc.EncodeAll(data, nil), nil
}
