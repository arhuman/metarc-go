package runtime

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"runtime"
	"sync"

	"github.com/arhuman/metarc-go/internal/scan"
	"github.com/arhuman/metarc-go/internal/store"
	"github.com/arhuman/metarc-go/pkg/marc"
	"github.com/zeebo/blake3"
)

// AnalyzedEntry carries an entry with its pre-computed BLAKE3-256 hash.
// Dirs and empty files have a zero SHA.
type AnalyzedEntry struct {
	Entry marc.Entry
	SHA   [32]byte // BLAKE3-256, zero for dirs/empty files
	Seq   int      // original scan order, for re-ordering
	Err   error
}

// DictMode selects the dictionary compression strategy.
const (
	DictNone    = ""        // no dictionary
	DictPrescan = "prescan" // walk source tree upfront, train dict before archiving
	DictSimple  = "simple"  // collect samples from early blobs, train mid-stream
)

// ArchiveOpts holds optional settings for Archive.
type ArchiveOpts struct {
	DictCompress   string // "", "prescan", or "simple"
	Workers        int    // 0 = runtime.NumCPU()
	SolidBlockSize int64  // 0 = disabled; >0 = solid mode with this threshold in bytes
}

// DefaultSolidBlockSize is the default solid block threshold (4 MB).
const DefaultSolidBlockSize = 4 * 1024 * 1024

// Archive creates a .marc archive of the directory tree rooted at root.
// compressor is the blob compression method: "zstd" (default) or "none".
// If keepPlanLog is false, plan_log rows are deleted after a successful archive.
// workers controls the number of parallel analysis goroutines (0 = runtime.NumCPU()).
// Solid block compression is enabled by default.
func Archive(ctx context.Context, marcPath, root, compressor string, keepPlanLog bool, workers ...int) error {
	opts := ArchiveOpts{SolidBlockSize: DefaultSolidBlockSize}
	if len(workers) > 0 && workers[0] > 0 {
		opts.Workers = workers[0]
	}
	return ArchiveWithOpts(ctx, marcPath, root, compressor, keepPlanLog, opts)
}

// ArchiveWithOpts creates a .marc archive with full control over options.
func ArchiveWithOpts(ctx context.Context, marcPath, root, compressor string, keepPlanLog bool, aopts ArchiveOpts) error {
	numWorkers := runtime.NumCPU()
	if aopts.Workers > 0 {
		numWorkers = aopts.Workers
	}

	slog.Info("archiving", "root", root, "output", marcPath, "compressor", compressor, "dict", aopts.DictCompress, "workers", numWorkers, "solid", aopts.SolidBlockSize)

	var opts []store.Option
	if compressor != "" {
		opts = append(opts, store.WithCompressor(compressor))
	}

	dictMode := aopts.DictCompress
	useZstd := compressor == "" || compressor == "zstd"

	// prescan: walk source tree upfront, train dict before archiving.
	if dictMode == DictPrescan && useZstd {
		slog.Info("training zstd dictionary (prescan)", "root", root)
		dict, err := store.TrainDictionary(root, 0, 0)
		if err != nil {
			slog.Warn("dict training failed, continuing without dictionary", "err", err)
		} else if dict != nil {
			slog.Info("dictionary trained", "size", len(dict))
			opts = append(opts, store.WithDictCompress(dict))
		} else {
			slog.Info("not enough samples for dictionary training")
		}
	}

	// simple: enable online dict training inside the Writer.
	if dictMode == DictSimple && useZstd {
		opts = append(opts, store.WithDictSimple())
	}

	// Solid block compression groups blobs into shared zstd frames.
	if aopts.SolidBlockSize > 0 {
		opts = append(opts, store.WithSolidBlockSize(aopts.SolidBlockSize))
	}

	w, err := store.OpenWriter(marcPath, opts...)
	if err != nil {
		return fmt.Errorf("runtime.Archive: %w", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	entries := scan.Walk(ctx, root)

	// sequenced wraps each entry with a sequence number for re-ordering.
	type sequenced struct {
		entry marc.Entry
		seq   int
	}

	seqCh := make(chan sequenced, 1024)
	resultCh := make(chan AnalyzedEntry, 256)

	// Fan-out: number entries and dispatch to workers.
	go func() {
		defer close(seqCh)
		seq := 0
		for e := range entries {
			select {
			case seqCh <- sequenced{entry: e, seq: seq}:
				seq++
			case <-ctx.Done():
				return
			}
		}
	}()

	// Worker pool: compute BLAKE3 hashes in parallel.
	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for s := range seqCh {
				select {
				case <-ctx.Done():
					return
				default:
				}

				ae := AnalyzedEntry{Entry: s.entry, Seq: s.seq}
				isSymlink := s.entry.Info.Mode()&fs.ModeSymlink != 0
				if !s.entry.Info.IsDir() && !isSymlink && s.entry.Info.Size() > 0 {
					sha, err := hashFile(s.entry.Path)
					if err != nil {
						ae.Err = err
					} else {
						ae.SHA = sha
					}
				}

				select {
				case resultCh <- ae:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Close resultCh when all workers finish.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Re-order: buffer out-of-order results, emit in sequence order.
	orderedCh := make(chan AnalyzedEntry, 256)
	go func() {
		defer close(orderedCh)
		pending := make(map[int]AnalyzedEntry)
		nextSeq := 0

		for ae := range resultCh {
			pending[ae.Seq] = ae

			// Flush all consecutive entries starting from nextSeq.
			for {
				next, ok := pending[nextSeq]
				if !ok {
					break
				}
				delete(pending, nextSeq)
				nextSeq++

				select {
				case orderedCh <- next:
				case <-ctx.Done():
					return
				}
			}
		}

		// Flush remaining (should be empty if no errors).
		for {
			next, ok := pending[nextSeq]
			if !ok {
				break
			}
			delete(pending, nextSeq)
			nextSeq++
			orderedCh <- next
		}
	}()

	// Single store writer goroutine (current goroutine).
	var count int64
	for ae := range orderedCh {
		if ae.Err != nil {
			cancel()
			return fmt.Errorf("runtime.Archive: analyze %s: %w", ae.Entry.RelPath, ae.Err)
		}

		if err := w.WriteEntryWithSHA(ctx, ae.Entry, ae.SHA); err != nil {
			cancel()
			return fmt.Errorf("runtime.Archive: write entry %s: %w", ae.Entry.RelPath, err)
		}
		count++
		if count%5000 == 0 {
			slog.Info("archive progress", "entries", count)
		}
	}

	if !keepPlanLog {
		if err := w.DeletePlanLog(); err != nil {
			return fmt.Errorf("runtime.Archive: delete plan_log: %w", err)
		}
	}

	slog.Info("archive complete", "entries", count)
	return nil
}

// hashFile computes the BLAKE3-256 hash of a file.
func hashFile(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, fmt.Errorf("hashFile: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	h := blake3.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, fmt.Errorf("hashFile: read %s: %w", path, err)
	}

	var sha [32]byte
	copy(sha[:], h.Sum(nil))
	return sha, nil
}
