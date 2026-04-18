package store

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
)

// solidAccumulator groups multiple raw blobs into single zstd frames (solid blocks)
// to exploit cross-file redundancy before the compressor.
type solidAccumulator struct {
	w            *Writer
	maxBlockSize int64
	buf          []byte        // concatenated raw blob data
	pending      []pendingBlob // blobs in current block
	blockCounter int64         // incrementing block ID
}

type pendingBlob struct {
	rowID       int64
	blockOffset int64
	ulen        int64
}

// addBlob appends raw blob data to the current solid block. If the block would
// exceed maxBlockSize, it is flushed first. Returns the new blob's BlobID.
func (sa *solidAccumulator) addBlob(data []byte, sha [32]byte) (marc.BlobID, error) {
	dataLen := int64(len(data))

	// Flush current block if adding this blob would exceed the limit.
	if len(sa.buf) > 0 && int64(len(sa.buf))+dataLen > sa.maxBlockSize {
		if err := sa.flush(); err != nil {
			return 0, fmt.Errorf("solidAccumulator.addBlob: flush: %w", err)
		}
	}

	// Oversized blobs that exceed maxBlockSize on their own are written as
	// standalone solid blocks to avoid unbounded buffer growth.
	if dataLen > sa.maxBlockSize && len(sa.buf) == 0 {
		sa.buf = append(sa.buf, data...)
		// Insert blob row, then flush immediately as a single-blob block.
		blockOffset := int64(0)
		res, err := sa.w.tx.Exec(
			`INSERT INTO blobs (sha, offset, clen, ulen, compressed, block_id, block_offset) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			sha[:], int64(-1), int64(0), dataLen, marc.CompressSolid, sa.blockCounter, blockOffset,
		)
		if err != nil {
			return 0, fmt.Errorf("solidAccumulator.addBlob: insert oversized blob: %w", err)
		}
		rowID, _ := res.LastInsertId()
		sa.pending = append(sa.pending, pendingBlob{
			rowID:       rowID,
			blockOffset: blockOffset,
			ulen:        dataLen,
		})
		if err := sa.flush(); err != nil {
			return 0, fmt.Errorf("solidAccumulator.addBlob: flush oversized: %w", err)
		}
		return marc.BlobID(rowID), nil
	}

	blockOffset := int64(len(sa.buf))
	sa.buf = append(sa.buf, data...)

	// Insert blob row with placeholder offset/clen; will be updated on flush.
	res, err := sa.w.tx.Exec(
		`INSERT INTO blobs (sha, offset, clen, ulen, compressed, block_id, block_offset) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sha[:], int64(-1), int64(0), dataLen, marc.CompressSolid, sa.blockCounter, blockOffset,
	)
	if err != nil {
		return 0, fmt.Errorf("solidAccumulator.addBlob: insert blob: %w", err)
	}
	rowID, _ := res.LastInsertId()

	sa.pending = append(sa.pending, pendingBlob{
		rowID:       rowID,
		blockOffset: blockOffset,
		ulen:        dataLen,
	})

	return marc.BlobID(rowID), nil
}

// flush compresses the accumulated buffer as one zstd frame, writes it as a
// solid block chunk, and updates all pending blob rows with the final offset.
func (sa *solidAccumulator) flush() error {
	if len(sa.buf) == 0 {
		return nil
	}

	// Compress the entire concatenated buffer as one zstd frame.
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		return fmt.Errorf("solidAccumulator.flush: create encoder: %w", err)
	}
	compressed := enc.EncodeAll(sa.buf, nil)
	_ = enc.Close()

	// Record the chunk header offset.
	chunkOffset := sa.w.blobOff

	if len(compressed) > math.MaxUint32 {
		return fmt.Errorf("solidAccumulator.flush: solid block exceeds max chunk size (4 GB)")
	}

	// Write chunk: [0x03][Len uint32 BE][compressed payload]
	var chunkHeader [5]byte
	chunkHeader[0] = marc.ChunkTypeSolidBlock
	binary.BigEndian.PutUint32(chunkHeader[1:5], uint32(len(compressed)))

	if err := sa.w.writeAndHash(chunkHeader[:]); err != nil {
		return fmt.Errorf("solidAccumulator.flush: write chunk header: %w", err)
	}
	if err := sa.w.writeAndHash(compressed); err != nil {
		return fmt.Errorf("solidAccumulator.flush: write chunk payload: %w", err)
	}

	clen := int64(len(compressed))
	sa.w.blobOff += int64(len(chunkHeader)) + clen

	// Update all pending blob rows with the actual chunk offset and compressed length.
	for _, p := range sa.pending {
		if _, err := sa.w.tx.Exec(
			`UPDATE blobs SET offset = ?, clen = ? WHERE id = ?`,
			chunkOffset, clen, p.rowID,
		); err != nil {
			return fmt.Errorf("solidAccumulator.flush: update blob %d: %w", p.rowID, err)
		}
	}

	sa.blockCounter++
	sa.buf = sa.buf[:0]
	sa.pending = sa.pending[:0]

	return nil
}
