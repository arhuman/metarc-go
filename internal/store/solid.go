package store

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sync"

	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/klauspost/compress/zstd"
)

// DefaultMinSolidBlockSize is the size below which an extension change does
// not flush the block in flight. It bounds the number of undersized zstd
// frames a corpus with many rare extensions can produce.
const DefaultMinSolidBlockSize = 1 << 20

// solidCompressWorkers is the number of goroutines that compress solid blocks
// in parallel, and therefore also the number of blocks in flight: peak
// in-flight memory is this many block buffers plus their compressed results.
//
// It is a package var so tests can pin it. The cap is 2 because a level-11
// encoder with a 32 MiB window holds about 104 MB of match tables, and a third
// worker buys no wall time on source corpora: the blocks of a 263 MB tree
// compress in 2.5 s in total, which two encoders already cover.
var solidCompressWorkers = min(2, runtime.NumCPU())

// solidAccumulator groups multiple raw blobs into single zstd frames (solid blocks)
// to exploit cross-file redundancy before the compressor.
//
// Compression runs on a pool of worker goroutines; writing and cataloguing do
// not. The writer goroutine that calls addBlob owns w.blobOff, w.fileHasher and
// w.tx, all three strictly sequential, so completed blocks are written back on
// that goroutine in block-id order.
type solidAccumulator struct {
	w            *Writer
	maxBlockSize int64
	minBlockSize int64         // below this, an extension change does not flush
	buf          []byte        // concatenated raw blob data for the block in flight
	pending      []pendingBlob // blobs in the block in flight
	blockCounter int64         // next block ID to assign
	currentExt   string        // file extension shared by blobs in the current block
	extInit      bool          // currentExt has been set at least once
	streamEnc    *zstd.Encoder // encoder reserved for the streamed oversized path

	jobs    chan solidJob    // submitted blocks
	results chan solidResult // compressed blocks, arriving out of order
	wg      sync.WaitGroup
	workers int // pool size, also the in-flight cap

	inFlight  int                   // blocks submitted but not yet written
	nextBlock int64                 // block ID the completion stage writes next
	done      map[int64]solidResult // completed blocks waiting for their turn
	freeBufs  [][]byte              // block buffers returned after compression
	err       error                 // first worker or completion error, sticky
}

// solidJob is one block handed to a compression worker. The worker owns buf for
// the duration: the accumulator has given up that backing array and starts the
// next block on another one.
type solidJob struct {
	blockID int64
	buf     []byte
	pending []pendingBlob
}

// solidResult is a compressed block awaiting its turn to be written. raw is the
// job's input buffer, handed back so the accumulator can reuse it.
type solidResult struct {
	blockID    int64
	compressed []byte
	raw        []byte
	pending    []pendingBlob
	err        error
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
	if sa.err != nil {
		return 0, sa.err
	}
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
		blockOffset := int64(0)
		rowID, err := sa.insertBlobRow(sha, sourceSHA, -1, 0, dataLen, sa.blockCounter, blockOffset)
		if err != nil {
			return 0, fmt.Errorf("solidAccumulator.addBlob: insert oversized blob: %w", err)
		}
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

	// Insert the blob row with placeholder offset/clen; the completion stage
	// fills them in once the block it belongs to has been written.
	rowID, err := sa.insertBlobRow(sha, sourceSHA, -1, 0, dataLen, sa.blockCounter, blockOffset)
	if err != nil {
		return 0, fmt.Errorf("solidAccumulator.addBlob: insert blob: %w", err)
	}

	sa.pending = append(sa.pending, pendingBlob{
		rowID:       rowID,
		blockOffset: blockOffset,
		ulen:        dataLen,
	})

	return marc.BlobID(rowID), nil
}

// insertBlobRow inserts one solid-block blob row and returns its rowid. It runs
// on the writer goroutine, the only owner of w.tx.
func (sa *solidAccumulator) insertBlobRow(sha, sourceSHA [32]byte, offset, clen, ulen, blockID, blockOffset int64) (int64, error) {
	var sourceSHAParam any
	zeroSHA := [32]byte{}
	if sourceSHA != zeroSHA {
		sourceSHAParam = sourceSHA[:]
	}
	res, err := sa.w.tx.Exec(
		`INSERT INTO blobs (sha, source_sha, offset, clen, ulen, compressed, block_id, block_offset) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sha[:], sourceSHAParam, offset, clen, ulen, marc.CompressSolid, blockID, blockOffset,
	)
	if err != nil {
		return 0, err
	}
	rowID, _ := res.LastInsertId()
	return rowID, nil
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
	// This block writes itself, so every earlier block has to be in the file
	// already: it takes its offset and its block id from where they end.
	if err := sa.drainAll(); err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: drain: %w", err)
	}
	if err := sa.ensureStreamEncoder(); err != nil {
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

	sa.streamEnc.Reset(spool)
	if _, err := io.Copy(sa.streamEnc, r); err != nil {
		_ = sa.streamEnc.Close()
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: encode: %w", err)
	}
	if err := sa.streamEnc.Close(); err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: close encoder: %w", err)
	}
	// Close finished the frame and left the encoder bound to the spool, so it
	// has to be rebound before the next stream; Reset below does that.

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

	rowID, err := sa.insertBlobRow(sha, sourceSHA, chunkOffset, clen, ulen, sa.blockCounter, 0)
	if err != nil {
		return 0, fmt.Errorf("solidAccumulator.addOversizedStream: insert blob: %w", err)
	}
	sa.blockCounter++
	sa.nextBlock = sa.blockCounter
	return marc.BlobID(rowID), nil
}

// newSolidEncoder builds one solid-block encoder. Every worker and the streamed
// path share these settings, since a different level or window would change the
// bytes a block compresses to.
func (sa *solidAccumulator) newSolidEncoder() (*zstd.Encoder, error) {
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
		return nil, fmt.Errorf("solidAccumulator: create encoder: %w", err)
	}
	return enc, nil
}

// ensureStreamEncoder builds the encoder the streamed oversized path binds to
// its spool. It is separate from the workers' encoders because Reset onto a
// writer leaves an encoder unusable for EncodeAll.
func (sa *solidAccumulator) ensureStreamEncoder() error {
	if sa.streamEnc != nil {
		return nil
	}
	enc, err := sa.newSolidEncoder()
	if err != nil {
		return err
	}
	sa.streamEnc = enc
	return nil
}

// startWorkers launches the compression pool on first use. Each worker keeps
// its own encoder: at level 11 the match tables are sized from the window, so
// rebuilding one per block would dominate the cost of compressing it.
func (sa *solidAccumulator) startWorkers() error {
	if sa.jobs != nil {
		return nil
	}
	n := sa.workers
	if n <= 0 {
		n = solidCompressWorkers
	}
	if n < 1 {
		n = 1
	}
	sa.workers = n
	sa.done = make(map[int64]solidResult, n)
	sa.jobs = make(chan solidJob, n)
	sa.results = make(chan solidResult, n)

	for range n {
		sa.wg.Add(1)
		go sa.compressLoop()
	}
	return nil
}

// compressLoop is one compression worker. Its encoder is built on the first job
// and kept for every later one: at level 11 with a 32 MiB window the match
// tables cost about 100 MB to allocate, so a worker that never runs must not
// pay for one and a worker that runs must not pay twice.
func (sa *solidAccumulator) compressLoop() {
	defer sa.wg.Done()
	var enc *zstd.Encoder
	defer func() {
		if enc != nil {
			_ = enc.Close()
		}
	}()

	for job := range sa.jobs {
		res := solidResult{blockID: job.blockID, raw: job.buf, pending: job.pending}
		if enc == nil {
			var err error
			if enc, err = sa.newSolidEncoder(); err != nil {
				res.err = err
				sa.results <- res
				return
			}
		}
		res.compressed = enc.EncodeAll(job.buf, nil)
		sa.results <- res
	}
}

// flush closes the block in flight by handing it to a compression worker. It
// writes nothing: the block lands in the file later, in submission order, when
// a drain reaches it.
func (sa *solidAccumulator) flush() error {
	if sa.err != nil {
		return sa.err
	}
	if len(sa.buf) == 0 {
		return nil
	}
	if err := sa.startWorkers(); err != nil {
		return fmt.Errorf("solidAccumulator.flush: %w", err)
	}

	// The cap on blocks in flight is what bounds memory: draining below it
	// before submitting keeps at most workers block buffers resident.
	if err := sa.drainUntil(sa.workers); err != nil {
		return fmt.Errorf("solidAccumulator.flush: %w", err)
	}

	job := solidJob{blockID: sa.blockCounter, buf: sa.buf, pending: sa.pending}
	sa.blockCounter++

	for {
		select {
		case sa.jobs <- job:
			sa.inFlight++
			// The worker owns job.buf and job.pending now, so the next block
			// has to start on different backing arrays.
			sa.buf = sa.takeFreeBuf()
			sa.pending = make([]pendingBlob, 0, len(job.pending))
			return nil
		case res := <-sa.results:
			if err := sa.accept(res); err != nil {
				return fmt.Errorf("solidAccumulator.flush: %w", err)
			}
		}
	}
}

// takeFreeBuf returns a recycled block buffer, or nil to let append allocate.
// The pool is capped at the in-flight bound, so it holds at most workers buffers.
func (sa *solidAccumulator) takeFreeBuf() []byte {
	if n := len(sa.freeBufs); n > 0 {
		buf := sa.freeBufs[n-1]
		sa.freeBufs = sa.freeBufs[:n-1]
		return buf[:0]
	}
	return nil
}

// accept records a completed block and writes every block whose turn has come.
func (sa *solidAccumulator) accept(res solidResult) error {
	sa.inFlight--
	if res.err != nil {
		sa.fail(res.err)
		return res.err
	}
	sa.done[res.blockID] = res
	for {
		next, ok := sa.done[sa.nextBlock]
		if !ok {
			return nil
		}
		delete(sa.done, sa.nextBlock)
		sa.nextBlock++
		if err := sa.writeBlock(next); err != nil {
			sa.fail(err)
			return err
		}
	}
}

// writeBlock appends one compressed block to the archive and points its blob
// rows at the chunk. It must run on the writer goroutine: w.blobOff and
// w.fileHasher advance together in file order, and w.tx has no concurrency.
func (sa *solidAccumulator) writeBlock(res solidResult) error {
	if len(res.compressed) > math.MaxUint32 {
		return fmt.Errorf("solidAccumulator: solid block exceeds max chunk size (4 GB)")
	}

	chunkOffset := sa.w.blobOff
	var chunkHeader [5]byte
	chunkHeader[0] = marc.ChunkTypeSolidBlock
	binary.BigEndian.PutUint32(chunkHeader[1:5], uint32(len(res.compressed)))

	if err := sa.w.writeAndHash(chunkHeader[:]); err != nil {
		return fmt.Errorf("solidAccumulator: write chunk header: %w", err)
	}
	if err := sa.w.writeAndHash(res.compressed); err != nil {
		return fmt.Errorf("solidAccumulator: write chunk payload: %w", err)
	}

	clen := int64(len(res.compressed))
	sa.w.blobOff += int64(len(chunkHeader)) + clen

	for _, p := range res.pending {
		if _, err := sa.w.tx.Exec(
			`UPDATE blobs SET offset = ?, clen = ? WHERE id = ?`,
			chunkOffset, clen, p.rowID,
		); err != nil {
			return fmt.Errorf("solidAccumulator: update blob %d: %w", p.rowID, err)
		}
	}

	if res.raw != nil && len(sa.freeBufs) < sa.workers {
		sa.freeBufs = append(sa.freeBufs, res.raw)
	}
	return nil
}

// drainUntil waits until fewer than n blocks are in flight, writing each block
// as its turn comes. It runs on the writer goroutine.
func (sa *solidAccumulator) drainUntil(n int) error {
	if sa.err != nil {
		return sa.err
	}
	for sa.inFlight >= n {
		if err := sa.accept(<-sa.results); err != nil {
			return err
		}
	}
	return nil
}

// drainAll waits for every submitted block to be written and catalogued.
func (sa *solidAccumulator) drainAll() error {
	return sa.drainUntil(1)
}

// fail records the first error. Every later operation reports it rather than
// writing a block behind the hole the failed one left.
func (sa *solidAccumulator) fail(err error) {
	if sa.err == nil {
		sa.err = err
	}
}

// finish submits the block in flight and drains every submitted block, so all
// solid data is in the archive and catalogued before the transaction commits.
func (sa *solidAccumulator) finish() error {
	if err := sa.flush(); err != nil {
		return err
	}
	return sa.drainAll()
}

// close releases the compression pool and its encoders. Safe to call on an
// accumulator that never flushed.
func (sa *solidAccumulator) close() {
	if sa.jobs != nil {
		close(sa.jobs)
		sa.wg.Wait()
		sa.jobs = nil
	}
	if sa.streamEnc != nil {
		_ = sa.streamEnc.Close()
		sa.streamEnc = nil
	}
}
