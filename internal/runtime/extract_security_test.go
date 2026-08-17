package runtime_test

// TODO: uncomment and validate — these test cases were added by an agent on
// 2026-08-17 to lock the three High findings of the security review. The
// assertions encode a contract (extract never writes through a pre-existing
// symlink, extract works with the default destination, corrupt blobs abort the
// entry) that the repository owner should confirm.

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/internal/runtime"
)

// writeTree creates a source directory holding the given name -> content files.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// TestExtract_doesNotFollowExistingSymlink plants a symlink in the destination
// pointing at a file outside it, then extracts an archive holding a regular
// file at that path. The outside file must be untouched and the destination
// path must end up as a regular file.
func TestExtract_doesNotFollowExistingSymlink(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeTree(t, src, map[string]string{"a.txt": "archived-content"})

	ctx := context.Background()
	marcPath := filepath.Join(tmp, "out.marc")
	if err := runtime.Archive(ctx, marcPath, src, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	outside := filepath.Join(tmp, "outside.txt")
	if err := os.WriteFile(outside, []byte("SECRET-ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(tmp, "dest")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dest, "a.txt")
	if err := os.Symlink(outside, planted); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	if err := runtime.Extract(ctx, marcPath, dest); err != nil {
		t.Fatalf("extract: %v", err)
	}

	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "SECRET-ORIGINAL" {
		t.Fatalf("file outside the destination was written through: got %q", got)
	}

	fi, err := os.Lstat(planted)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("planted symlink survived extraction: mode %v", fi.Mode())
	}
	if !fi.Mode().IsRegular() {
		t.Fatalf("expected a regular file at %s, got mode %v", planted, fi.Mode())
	}

	restored, err := os.ReadFile(planted)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != "archived-content" {
		t.Fatalf("restored content = %q, want %q", restored, "archived-content")
	}
}

// TestExtract_defaultDestination covers the invocation `marc extract archive.marc`
// with no -C, i.e. destDir ".". The traversal guard used to reject every entry
// in this configuration.
func TestExtract_defaultDestination(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	writeTree(t, src, map[string]string{
		"top.txt":       "top",
		"sub/inner.txt": "inner",
	})

	ctx := context.Background()
	marcPath := filepath.Join(tmp, "out.marc")
	if err := runtime.Archive(ctx, marcPath, src, "zstd", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	dest := t.TempDir()
	t.Chdir(dest)

	if err := runtime.Extract(ctx, marcPath, "."); err != nil {
		t.Fatalf("extract with default destination: %v", err)
	}

	for name, want := range map[string]string{
		"top.txt":       "top",
		"sub/inner.txt": "inner",
	} {
		got, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestExtract_detectsCorruptBlob flips one byte inside the blob region of an
// uncompressed archive; the stored BLAKE3 prefix must no longer match and the
// entry must fail by name.
func TestExtract_detectsCorruptBlob(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	const marker = "CORRUPT-ME-0123456789-CORRUPT-ME"
	writeTree(t, src, map[string]string{"payload.txt": marker})

	ctx := context.Background()
	marcPath := filepath.Join(tmp, "out.marc")
	// "none" keeps blob payloads verbatim so a byte can be flipped without
	// tripping zstd's own frame checksum first.
	if err := runtime.Archive(ctx, marcPath, src, "none", false); err != nil {
		t.Fatalf("archive: %v", err)
	}

	flipByteInArchive(t, marcPath, marker)

	err := runtime.Extract(ctx, marcPath, filepath.Join(tmp, "dest"))
	if err == nil {
		t.Fatal("extract of a corrupted archive succeeded, want a hash mismatch error")
	}
	if !strings.Contains(err.Error(), "payload.txt") {
		t.Fatalf("error does not name the failing entry: %v", err)
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("error does not report a hash mismatch: %v", err)
	}
}

// flipByteInArchive locates marker in the archive at path and flips its first
// byte in place.
func flipByteInArchive(t *testing.T, path, marker string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(string(data), marker)
	if i < 0 {
		t.Fatalf("marker not found in %s (archive not stored verbatim?)", path)
	}
	data[i] ^= 0xFF
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
