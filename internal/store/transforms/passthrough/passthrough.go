// Package passthrough implements the passthrough/v1 transform, which stores
// already-compressed file types (.png, .jpg, .zip, …) as raw bytes without
// running them through zstd. zstd produces no meaningful gain on already-
// compressed data and often slightly inflates output, so this transform
// saves CPU on the hot path and shaves a few bytes off the archive.
package passthrough

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
)

const passthroughID marc.TransformID = "passthrough/v1"

// Allowlist of extensions known to be already-compressed or otherwise
// incompressible. Lowercase, including the leading dot. Matched against
// the file's final extension via filepath.Ext.
var passthroughExtensions = map[string]bool{
	// Image formats with built-in compression.
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".heic": true,
	".heif": true,
	// Audio / video.
	".mp3":  true,
	".mp4":  true,
	".m4a":  true,
	".m4v":  true,
	".mov":  true,
	".avi":  true,
	".mkv":  true,
	".webm": true,
	".ogg":  true,
	".oga":  true,
	".ogv":  true,
	".opus": true,
	".flac": true,
	// Archives / already-compressed bundles.
	".zip": true,
	".gz":  true,
	".bz2": true,
	".xz":  true,
	".zst": true,
	".lz4": true,
	".7z":  true,
	".rar": true,
	".tgz": true,
	".tbz": true,
	".txz": true,
	// Web fonts (compressed).
	".woff":  true,
	".woff2": true,
	// Other.
	".pdf": true,
	".ico": true,
}

// rawWriter is the optional interface a BlobSink may implement to bypass
// the zstd path. The concrete *store.blobSink implements it; mocks and
// future BlobSink implementations may not, in which case Apply falls
// back to sink.Write — correctness is preserved, only the
// CPU/incompressible-bytes savings are lost.
type rawWriter interface {
	WriteRaw(ctx context.Context, r io.Reader) (marc.BlobID, error)
}

// Passthrough is the transform.
type Passthrough struct{}

// New returns a new Passthrough transform.
func New() *Passthrough { return &Passthrough{} }

// ID returns the stable transform identifier.
func (p *Passthrough) ID() marc.TransformID { return passthroughID }

// Applicable matches files whose lowercased final extension is in the
// allowlist. Empty files are skipped (no point).
func (p *Passthrough) Applicable(_ context.Context, e marc.Entry, facts marc.Facts) bool {
	if facts.Size <= 0 {
		return false
	}
	base := filepath.Base(e.RelPath)
	ext := strings.ToLower(filepath.Ext(base))
	return passthroughExtensions[ext]
}

// CostEstimate returns a small positive gain (~2% of file size — the
// typical inflation when running zstd on already-compressed data) and
// zero CPU cost. The planner rule is `gain > cpu`, so this guarantees
// the transform fires when applicable.
func (p *Passthrough) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size / 50, 0
}

// Apply streams the source bytes raw via the sink's WriteRaw method when
// available; falls back to sink.Write (which compresses) for sinks that
// don't implement WriteRaw. Always returns handled=true so the transform
// chain stops here.
func (p *Passthrough) Apply(ctx context.Context, _ marc.Entry, _ marc.Facts, src io.Reader, sink marc.BlobSink) (marc.Result, bool, error) {
	var (
		id  marc.BlobID
		err error
	)
	if rw, ok := sink.(rawWriter); ok {
		id, err = rw.WriteRaw(ctx, src)
	} else {
		id, err = sink.Write(ctx, src)
	}
	if err != nil {
		return marc.Result{}, false, fmt.Errorf("passthrough: write: %w", err)
	}
	return marc.Result{BlobIDs: []marc.BlobID{id}}, true, nil
}

// Reverse opens the single blob and copies it to dst. The blob was
// written raw, so no decompression is needed at this layer (the blob
// reader handles CompressNone transparently).
func (p *Passthrough) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}
	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("passthrough: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(dst, rc); err != nil {
		return fmt.Errorf("passthrough: copy blob: %w", err)
	}
	return nil
}
