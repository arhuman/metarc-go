package store

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
)

// DefaultMinSolidBlockSize is the size below which an extension change does
// not flush the block in flight. It bounds the number of undersized zstd
// frames a corpus with many rare extensions can produce.
const DefaultMinSolidBlockSize = 1 << 20

// solidAccumulator groups multiple raw blobs into single zstd frames (solid blocks)
// to exploit cross-file redundancy before the compressor.
type solidAccumulator struct {
	w            *Writer
	maxBlockSize int64
	minBlockSize int64         // below this, an extension change does not flush
	buf          []byte        // concatenated raw blob data
	pending      []pendingBlob // blobs in current block
	blockCounter int64         // incrementing block ID
	currentExt   string        // file extension shared by blobs in the current block
	extInit      bool          // currentExt has been set at least once
	enc          *zstd.Encoder // built on first use, reused for every block
	encDirty     bool          // enc was Reset onto a stream and needs rebinding before EncodeAll
}

// setExtension declares the extension of blobs that are about to be added.
// When the extension changes, the block in flight is flushed so that blocks
// stay extension-pure and a boundary file does not straddle two languages.
//
// The flush is skipped while the block holds less than minBlockSize: a frame
// that small carries its own zstd header and entropy tables and sees no
// cross-file context, which costs more than the purity buys. Since the
// pipeline feeds files in extension order, skipping the flush makes
// consecutive rare extensions pool into one block on their own, with no
// separate routing.
func (sa *solidAccumulator) setExtension(ext string) error {
	if sa.extInit && ext != sa.currentExt && int64(len(sa.buf)) >= sa.minBlockSize {
		if err := sa.flush(); err != nil {
			return fmt.Errorf("solidAccumulator.setExtension: flush: %w", err)
		}
	}
	sa.currentExt = ext
	sa.extInit = true
	return nil
}

type pendingBlob struct {
	rowID       int64
	blockOffset int64
	ulen        int64
}

// solidWindowFor returns the zstd match window for a solid block of blockSize
// bytes: the smallest power of two that covers the whole block, so the last
// byte of a block can still reference the first.
//
// Without this, zstd's level defaults apply an 8 MiB window (levels 3, 7 and
// 11 alike), which leaves the tail of a 16 MiB block unable to match its head.
// Widening the window is a write-side choice only: the frame header carries
// the window size and klauspost's decoder accepts up to MaxWindowSize by
// default, so archives stay readable by existing binaries.
func solidWindowFor(blockSize int64) int {
	if blockSize <= zstd.MinWindowSize {
		return zstd.MinWindowSize
	}
	if blockSize >= zstd.MaxWindowSize {
		return zstd.MaxWindowSize
	}
	w := int64(zstd.MinWindowSize)
	for w < blockSize {
		w <<= 1
	}
	return int(w)
}

// addBlob appends raw blob data to the current solid block. If the block would
// exceed maxBlockSize, it is flushed first. Returns the new blob's BlobID.
func (sa *solidAccumulator) addBlob(data []byte, sha [32]byte, sourceSHA [32]byte) (marc.BlobID, error) {
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
		var sourceSHAParam any
		zeroSHA := [32]byte{}
		if sourceSHA != zeroSHA {
			sourceSHAParam = sourceSHA[:]
		}
		res, err := sa.w.tx.Exec(
			`INSERT INTO blobs (sha, source_sha, offset, clen, ulen, compressed, block_id, block_offset) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			sha[:], sourceSHAParam, int64(-1), int64(0), dataLen, marc.CompressSolid, sa.blockCounter, blockOffset,
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
	var sourceSHAParam2 any
	zeroSHA2 := [32]byte{}
	if sourceSHA != zeroSHA2 {
		sourceSHAParam2 = sourceSHA[:]
	}
	res, err := sa.w.tx.Exec(
		`INSERT INTO blobs (sha, source_sha, offset, clen, ulen, compressed, block_id, block_offset) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sha[:], sourceSHAParam2, int64(-1), int64(0), dataLen, marc.CompressSolid, sa.blockCounter, blockOffset,
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

// addOversizedStream writes a blob larger than maxBlockSize as its own solid
// block, compressing r as a stream rather than buffering it. The block is
// byte-compatible with the one addBlob produces: same chunk type, same encoder
// settings, one blob at offset 0.
//
// It exists because the buffered path holds the payload, the encoder's working
// buffer and the compressed result at once, so a single large file sets peak
// RSS for the whole archive. The caller must have flushed any open block first,
// since this block has to stand alone.
//
// The spool is the price of the chunk header: [type][len][payload] needs the
// compressed length before the payload, and streaming only learns it at the end.
func (sa *solidAccumulator) addOversizedStream(r io.Reader, ulen int64, sha [32]byte, sourceSHA [32]byte) (marc.BlobID, error) {
	if err := sa.ensureEncoder(); err != nil {
		return 0, err
	}

	spool, err := os.CreateTemp("", "marc-solid-*.zst")
	if err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: create spool: %w", err)
	}
	spoolPath := spool.Name()
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spoolPath)
	}()

	sa.enc.Reset(spool)
	if _, err := io.Copy(sa.enc, r); err != nil {
		_ = sa.enc.Close()
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: encode: %w", err)
	}
	if err := sa.enc.Close(); err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: close encoder: %w", err)
	}
	// Close finishes the frame but leaves the encoder unusable until Reset;
	// flush() resets it to nil-writer mode before its next EncodeAll.
	sa.encDirty = true

	clen, err := spool.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: spool size: %w", err)
	}
	if clen > math.MaxUint32 {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: solid block exceeds max chunk size (4 GB)")
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: rewind spool: %w", err)
	}

	chunkOffset := sa.w.blobOff
	var chunkHeader [5]byte
	chunkHeader[0] = marc.ChunkTypeSolidBlock
	binary.BigEndian.PutUint32(chunkHeader[1:5], uint32(clen))
	if err := sa.w.writeAndHash(chunkHeader[:]); err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: write chunk header: %w", err)
	}
	if err := sa.w.copyAndHash(spool); err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: write chunk payload: %w", err)
	}
	sa.w.blobOff += int64(len(chunkHeader)) + clen

	var sourceSHAParam any
	zeroSHA := [32]byte{}
	if sourceSHA != zeroSHA {
		sourceSHAParam = sourceSHA[:]
	}
	res, err := sa.w.tx.Exec(
		`INSERT INTO blobs (sha, source_sha, offset, clen, ulen, compressed, block_id, block_offset) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sha[:], sourceSHAParam, chunkOffset, clen, ulen, marc.CompressSolid, sa.blockCounter, int64(0),
	)
	if err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: insert blob: %w", err)
	}
	rowID, _ := res.LastInsertId()
	sa.blockCounter++
	return marc.BlobID(rowID), nil
}

// ensureEncoder builds the shared solid encoder if it does not exist yet.
func (sa *solidAccumulator) ensureEncoder() error {
	if sa.enc != nil {
		return nil
	}
	window := sa.w.zstdCfg.WindowSize
	if window == 0 {
		window = solidWindowFor(sa.maxBlockSize)
	}
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(sa.w.zstdCfg.Solid),
		zstd.WithEncoderConcurrency(1),
		zstd.WithWindowSize(window),
	)
	if err != nil {
		return fmt.Errorf("solidAccumulator: create encoder: %w", err)
	}
	sa.enc = enc
	return nil
}

// flush compresses the accumulated buffer as one zstd frame, writes it as a
// solid block chunk, and updates all pending blob rows with the final offset.
func (sa *solidAccumulator) flush() error {
	if len(sa.buf) == 0 {
		return nil
	}

	// Compress the entire concatenated buffer as one zstd frame at the
	// writer-configured solid-block level. The encoder is built once and
	// reused: at level 11 its match tables are sized from the window, so
	// rebuilding one per block would dominate the cost of flushing.
	if err := sa.ensureEncoder(); err != nil {
		return fmt.Errorf("solidAccumulator.flush: %w", err)
	}
	// A streamed block left the encoder bound to its spool and closed;
	// EncodeAll needs it back in the unbound state NewWriter(nil) gives.
	if sa.encDirty {
		sa.enc.Reset(nil)
		sa.encDirty = false
	}
	compressed := sa.enc.EncodeAll(sa.buf, nil)

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

// close releases the cached encoder. Safe to call on an accumulator that
// never flushed.
func (sa *solidAccumulator) close() {
	if sa.enc != nil {
		_ = sa.enc.Close()
		sa.enc = nil
	}
}
