package runtime

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/arhuman/metarc-go/internal/plan"
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
	Data  []byte   // file content when the hasher read it inline, nil otherwise
	Seq   int      // original scan order, for re-ordering
	Err   error
	cost  int64 // in-flight budget charge, released by the writer
}

// inlineFileLimit is the largest file the analyze workers read into memory so
// the writer does not reopen it. A var, not a const, so tests can shrink it.
var inlineFileLimit int64 = 1 << 20

// inFlightBudget bounds the bytes held between the sequencer and the writer.
// It caps both the inlined content and, through the per-entry overhead in
// entryCost, the number of entries buffered by the reorder stage.
var inFlightBudget int64 = 64 << 20

// entryCostOverhead is charged per in-flight entry on top of its inlined bytes.
const entryCostOverhead = 4096

// DictMode selects the dictionary compression strategy.
const (
	DictNone    = ""        // no dictionary
	DictPrescan = "prescan" // walk source tree upfront, train dict before archiving
	DictSimple  = "simple"  // collect samples from early blobs, train mid-stream
)

// ArchiveOpts holds optional settings for Archive.
type ArchiveOpts struct {
	DictCompress       string   // "", "prescan", or "simple"
	Workers            int      // 0 = runtime.NumCPU()
	SolidBlockSize     int64    // 0 = disabled; >0 = solid mode with this threshold in bytes
	DisabledTransforms []string // transform IDs to skip (e.g. "go-line-subst/v1")
	// ZstdLevels carries per-chunk-type zstd encoder settings. The zero value
	// means "use store.DefaultZstdConfig()". Callers may set ZstdLevels to
	// override individual levels; any zero field within ZstdLevels is replaced
	// with the corresponding default at archive time.
	ZstdLevels store.ZstdConfig
}

// DefaultSolidBlockSize is the default solid block threshold (32 MiB).
//
// Larger blocks keep more cross-file context inside a single zstd frame,
// which is what makes solid mode pay off on source-code corpora. The block
// size also sets the encoder's match window (store.solidWindowFor), so it
// bounds how far back a match can reach.
//
// 32 MiB is the measured ratio-per-byte-of-RAM optimum (2026-08-14, corpora
// without .git): moving from 16 MiB buys -1.24% on kubernetes and -0.55% on
// react for +103 MiB of archive peak RSS, while 64 MiB buys a further -1.34%
// on kubernetes alone for +148 MiB more. Corpora smaller than one block are
// unaffected. Peak RSS on the kubernetes corpus is ~361 MiB archiving and
// ~349 MiB extracting, both above the 256 MiB figure quoted in the Phase-6
// notes, which the previous 16 MiB default already exceeded (258/270 MiB).
const DefaultSolidBlockSize = 32 * 1024 * 1024

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
// The archive contains a single source tree rooted at root; the root directory
// itself is recorded as the synthetic "." entry.
func ArchiveWithOpts(ctx context.Context, marcPath, root, compressor string, keepPlanLog bool, aopts ArchiveOpts) error {
	return archiveWithSources(ctx, marcPath, []string{root}, compressor, keepPlanLog, aopts, false)
}

// ArchiveMulti creates a .marc archive of multiple source directory trees.
// Each source becomes a top-level directory inside the archive, named after
// its basename. If two sources share the same basename, an error is returned.
//
// This is intended for bundling several independent directories — for
// example, a set of sibling project folders — into one .marc file. Extracting
// such an archive restores each source as a sibling directory under the
// destination.
//
// For a single-source archive, prefer Archive / ArchiveWithOpts: those
// preserve the existing single-root archive layout (root recorded as ".").
func ArchiveMulti(ctx context.Context, marcPath string, roots []string, compressor string, keepPlanLog bool, workers ...int) error {
	opts := ArchiveOpts{SolidBlockSize: DefaultSolidBlockSize}
	if len(workers) > 0 && workers[0] > 0 {
		opts.Workers = workers[0]
	}
	return ArchiveMultiWithOpts(ctx, marcPath, roots, compressor, keepPlanLog, opts)
}

// ArchiveMultiWithOpts creates a multi-source .marc archive with full control
// over options. See ArchiveMulti for a description of the layout.
func ArchiveMultiWithOpts(ctx context.Context, marcPath string, roots []string, compressor string, keepPlanLog bool, aopts ArchiveOpts) error {
	if len(roots) == 0 {
		return fmt.Errorf("runtime.ArchiveMulti: no source roots provided")
	}
	return archiveWithSources(ctx, marcPath, roots, compressor, keepPlanLog, aopts, true)
}

// archiveWithSources is the shared implementation for single- and multi-root
// archiving. When multi is false, roots must contain exactly one entry and the
// legacy single-root walk is used (root recorded as "."). When multi is true,
// each source is prefixed with its basename and surfaces as a top-level
// directory in the archive.
func archiveWithSources(ctx context.Context, marcPath string, roots []string, compressor string, keepPlanLog bool, aopts ArchiveOpts, multi bool) error {
	// Apply disabled transforms before archiving.
	plan.Disabled = make(map[string]bool, len(aopts.DisabledTransforms))
	for _, id := range aopts.DisabledTransforms {
		plan.Disabled[id] = true
	}
	// Dedup is structural: blobs.sha is UNIQUE, so duplicate content cannot be
	// stored twice. Fail loudly instead of silently ignoring the flag.
	if plan.Disabled["dedup/v1"] {
		return fmt.Errorf("runtime.Archive: transform dedup/v1 cannot be disabled: the blob store is content-addressed")
	}

	numWorkers := runtime.NumCPU()
	if aopts.Workers > 0 {
		numWorkers = aopts.Workers
	}

	// Resolve multi roots upfront so we can surface basename collisions before
	// opening the output file.
	var resolved []scan.MultiRoot
	if multi {
		var err error
		resolved, err = scan.ResolveMultiRoots(roots)
		if err != nil {
			return fmt.Errorf("runtime.Archive: %w", err)
		}
	} else if len(roots) != 1 {
		return fmt.Errorf("runtime.Archive: single-root archive requires exactly 1 source, got %d", len(roots))
	}

	slog.Debug("archiving", "roots", roots, "output", marcPath, "compressor", compressor, "dict", aopts.DictCompress, "workers", numWorkers, "solid", aopts.SolidBlockSize, "multi", multi)

	var opts []store.Option
	if compressor != "" {
		opts = append(opts, store.WithCompressor(compressor))
	}

	dictMode := aopts.DictCompress
	useZstd := compressor == "" || compressor == "zstd"

	// prescan: walk source tree upfront, train dict before archiving.
	if dictMode == DictPrescan && useZstd {
		// Dictionary training samples a single root. For multi-root archives we
		// train against the first source; this is a pragmatic default that
		// keeps the code simple while still benefiting cross-file dedup.
		trainRoot := roots[0]
		slog.Debug("training zstd dictionary (prescan)", "root", trainRoot)
		dictLevel := store.ResolveZstdConfig(aopts.ZstdLevels).Dict
		dict, err := store.TrainDictionaryWithLevel(trainRoot, 0, 0, dictLevel)
		if err != nil {
			slog.Warn("dict training failed, continuing without dictionary", "err", err)
		} else if dict != nil {
			slog.Debug("dictionary trained", "size", len(dict))
			opts = append(opts, store.WithDictCompress(dict))
		} else {
			slog.Debug("not enough samples for dictionary training")
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

	opts = append(opts, store.WithZstdConfig(store.ResolveZstdConfig(aopts.ZstdLevels)))

	// The rows would be deleted at finalize anyway, so skip the inserts rather
	// than writing one per file entry and then DELETEing them all.
	if !keepPlanLog {
		opts = append(opts, store.WithoutPlanLog())
	}

	w, err := store.OpenWriter(marcPath, opts...)
	if err != nil {
		return fmt.Errorf("runtime.Archive: %w", err)
	}
	defer func() { _ = w.Close() }()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var entries <-chan marc.Entry
	if multi {
		entries = scan.WalkMulti(ctx, resolved)
	} else {
		entries = scan.Walk(ctx, roots[0])
	}

	return runArchivePipeline(ctx, cancel, w, entries, numWorkers, keepPlanLog)
}

// inlineSize reports the byte count the analyze workers will read into memory
// for this entry, or 0 when it takes the streaming path.
func inlineSize(e marc.Entry) int64 {
	if e.Info.IsDir() || e.Info.Mode()&fs.ModeSymlink != 0 {
		return 0
	}
	size := e.Info.Size()
	if size <= 0 || size > inlineFileLimit {
		return 0
	}
	return size
}

// entryCost is the in-flight budget charge for one entry: its inlined bytes
// plus a fixed overhead that bounds how many entries can be buffered at once.
func entryCost(e marc.Entry) int64 {
	return entryCostOverhead + inlineSize(e)
}

// budget bounds the bytes in flight between the sequencer and the writer.
//
// A single acquirer (the sequencer) and a single releaser (the writer) mean a
// missed notify cannot lose a wakeup: the only waiter re-tests used under the
// lock after every notify, and the writer always notifies after releasing.
type budget struct {
	mu     sync.Mutex
	used   int64
	limit  int64
	notify chan struct{}
}

func newBudget(limit int64) *budget {
	return &budget{limit: limit, notify: make(chan struct{}, 1)}
}

func (b *budget) acquire(ctx context.Context, n int64) error {
	for {
		b.mu.Lock()
		if b.used+n <= b.limit {
			b.used += n
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		select {
		case <-b.notify:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (b *budget) release(n int64) {
	b.mu.Lock()
	b.used -= n
	b.mu.Unlock()
	select {
	case b.notify <- struct{}{}:
	default:
	}
}

// runArchivePipeline consumes entries from the scan channel, hashes files in
// parallel using numWorkers goroutines, re-orders results to preserve the write
// order, and writes them through the store.Writer. Hashing and compression
// overlap: the writer consumes results as they arrive instead of waiting for
// the whole tree to be analyzed. On success, it optionally clears the plan_log
// table.
func runArchivePipeline(ctx context.Context, cancel context.CancelFunc, w *store.Writer, entries <-chan marc.Entry, numWorkers int, keepPlanLog bool) error {
	// A single entry must always fit, or the sequencer would wait on a budget
	// only its own entry could free.
	limit := inFlightBudget
	if minLimit := inlineFileLimit + entryCostOverhead; limit < minLimit {
		limit = minLimit
	}

	// Directories must be written first (in scan order) so that parent
	// references exist when child entries are inserted. Only metadata is held
	// here; content is read later, inside the budget.
	var dirs, files []marc.Entry
	for e := range entries {
		if e.Info.IsDir() {
			dirs = append(dirs, e)
		} else {
			files = append(files, e)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("runtime.Archive: scan: %w", err)
	}

	// Sort files by extension so that solid blocks group similar content,
	// improving zstd compression. Stability is load-bearing: the scan order it
	// keeps holds same-directory files together, and re-sorting those on
	// (basename, size) measured worse on every corpus (see .claude/CHANGELOG.md).
	sort.SliceStable(files, func(i, j int) bool {
		return filepath.Ext(files[i].RelPath) < filepath.Ext(files[j].RelPath)
	})

	// sequenced wraps each entry with a sequence number for re-ordering.
	type sequenced struct {
		entry marc.Entry
		seq   int
		cost  int64
	}

	bud := newBudget(limit)
	seqCh := make(chan sequenced, 256)
	resultCh := make(chan AnalyzedEntry, 256)

	// Fan-out: number entries in final write order and dispatch to workers.
	go func() {
		defer close(seqCh)
		seq := 0
		for _, group := range [2][]marc.Entry{dirs, files} {
			for _, e := range group {
				cost := entryCost(e)
				if err := bud.acquire(ctx, cost); err != nil {
					return
				}
				select {
				case seqCh <- sequenced{entry: e, seq: seq, cost: cost}:
					seq++
				case <-ctx.Done():
					return
				}
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

				ae := AnalyzedEntry{Entry: s.entry, Seq: s.seq, cost: s.cost}
				isSymlink := s.entry.Info.Mode()&fs.ModeSymlink != 0
				if !s.entry.Info.IsDir() && !isSymlink && s.entry.Info.Size() > 0 {
					if inlineSize(s.entry) > 0 {
						data, sha, err := readAndHashFile(s.entry.Path)
						if err != nil {
							ae.Err = err
						} else {
							ae.SHA = sha
							ae.Data = data
						}
					} else if sha, err := hashFile(s.entry.Path); err != nil {
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
	}()

	var count int64
	for ae := range orderedCh {
		if ae.Err != nil {
			cancel()
			return fmt.Errorf("runtime.Archive: analyze %s: %w", ae.Entry.RelPath, ae.Err)
		}
		err := w.WriteEntryContent(ctx, ae.Entry, ae.SHA, ae.Data)
		bud.release(ae.cost)
		if err != nil {
			cancel()
			return fmt.Errorf("runtime.Archive: write entry %s: %w", ae.Entry.RelPath, err)
		}
		count++
		if count%5000 == 0 {
			slog.Debug("archive progress", "entries", count)
		}
	}

	if !keepPlanLog {
		if err := w.DeletePlanLog(); err != nil {
			return fmt.Errorf("runtime.Archive: delete plan_log: %w", err)
		}
	}

	slog.Debug("archive complete", "entries", count)
	return nil
}

// readAndHashFile reads a whole file and hashes the buffer, so the writer can
// consume the content without reopening the path.
func readAndHashFile(path string) ([]byte, [32]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("readAndHashFile: read %s: %w", path, err)
	}
	h := blake3.New()
	_, _ = h.Write(data)
	var sha [32]byte
	copy(sha[:], h.Sum(nil))
	return data, sha, nil
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
