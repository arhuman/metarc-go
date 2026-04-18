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
	w        *Writer
	compress string
	zstdEnc  *zstd.Encoder
	dictEnc  *zstd.Encoder // encoder with dictionary (when dict-compress enabled)
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
func (s *blobSink) WriteWithSHA(_ context.Context, r io.Reader, sha [32]byte) (marc.BlobID, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return 0, fmt.Errorf("blobSink.WriteWithSHA: read: %w", err)
	}
	return s.writeData(data, sha)
}

// writeData deduplicates on sha, compresses, and writes the blob chunk.
func (s *blobSink) writeData(data []byte, sha [32]byte) (marc.BlobID, error) {
	// Check for existing blob with same hash.
	if id, ok := s.Reuse(sha); ok {
		return id, nil
	}

	// Route through solid accumulator when active.
	if s.w.solidAcc != nil {
		return s.w.solidAcc.addBlob(data, sha)
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
	res, err := s.w.tx.Exec(
		`INSERT INTO blobs (sha, offset, clen, ulen, compressed) VALUES (?, ?, ?, ?, ?)`,
		sha[:], chunkOffset, clen, ulen, compressed,
	)
	if err != nil {
		return 0, fmt.Errorf("blobSink.writeData: insert blob: %w", err)
	}
	id, _ := res.LastInsertId()
	return marc.BlobID(id), nil
}

// Reuse looks up an existing blob by its BLAKE3-256 hash.
func (s *blobSink) Reuse(sha [32]byte) (marc.BlobID, bool) {
	var id int64
	err := s.w.tx.QueryRow(`SELECT id FROM blobs WHERE sha = ?`, sha[:]).Scan(&id)
	if err != nil {
		return 0, false
	}
	return marc.BlobID(id), true
}

// compressZstd compresses data using zstd, reusing the encoder if available.
func (s *blobSink) compressZstd(data []byte) ([]byte, error) {
	if s.zstdEnc == nil {
		enc, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1),
		)
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
		enc, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			zstd.WithEncoderConcurrency(1),
			zstd.WithEncoderDict(s.w.dictData),
		)
		if err != nil {
			return nil, fmt.Errorf("blobSink: create dict zstd encoder: %w", err)
		}
		s.dictEnc = enc
	}
	return s.dictEnc.EncodeAll(data, nil), nil
}
