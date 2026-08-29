package runtime_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/arhuman/metarc-go/internal/plan"
	"github.com/arhuman/metarc-go/internal/runtime"
	"github.com/arhuman/metarc-go/internal/store"
)

const logdateID = "log-date-subst/v2"

// windowsLogFixture resembles the one loghub file log-date-subst/v2 used to fire
// on: a dense run of near-identical timestamped lines, which is exactly the text
// zstd models well and the transform replaced with incompressible binary.
func windowsLogFixture(lines int) []byte {
	var buf bytes.Buffer
	base := time.Date(2016, 9, 29, 2, 4, 0, 0, time.UTC)
	for i := range lines {
		ts := base.Add(time.Duration(i) * time.Second)
		fmt.Fprintf(&buf, "%s, Info                  CBS    Loaded Servicing Stack v6.1.7601.23505 entry %d\n",
			ts.Format("2006-01-02 15:04:05"), i)
	}
	return buf.Bytes()
}

// entryTransforms maps each file entry's relative path to the transform ID the
// catalog records for it.
func entryTransforms(t *testing.T, marcPath string) map[string]string {
	t.Helper()
	r, err := store.OpenReader(marcPath)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer func() { _ = r.Close() }()

	got := make(map[string]string)
	if err := r.WalkEntries(func(relPath string, e store.EntryRow) error {
		if e.Mode.IsRegular() {
			got[relPath] = e.Transform
		}
		return nil
	}); err != nil {
		t.Fatalf("walk entries: %v", err)
	}
	return got
}

// withWriteEnabled makes a decode-only registry entry write-selectable and
// returns the function that puts the registry back. It is the only seam that can
// produce an archive carrying a retired transform, which the decode regression
// guard needs as its input.
//
// The caller must restore before exercising the read path: an extraction that
// runs while Write is still true would pass even if decode filtered on Write,
// which is the very thing the guard exists to catch.
func withWriteEnabled(t *testing.T, id string) func() {
	t.Helper()
	orig := plan.Registry
	patched := make([]plan.Entry, len(orig))
	copy(patched, orig)
	found := false
	for i := range patched {
		if string(patched[i].Transform.ID()) == id {
			patched[i].Write = true
			found = true
		}
	}
	if !found {
		t.Fatalf("transform %q is not in the registry", id)
	}
	plan.Registry = patched
	restored := false
	restore := func() {
		if !restored {
			plan.Registry = orig
			restored = true
		}
	}
	t.Cleanup(restore)
	return restore
}

// TestArchive_logdateNeverSelected pins the retirement: archiving a file
// log-date-subst/v2 used to claim leaves no entry carrying its ID.
func TestArchive_logdateNeverSelected(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Windows_2k.log"), windowsLogFixture(4000), 0o644); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "out.marc")
	if err := runtime.ArchiveWithOpts(context.Background(), marcPath, srcDir, "zstd", false,
		runtime.ArchiveOpts{SolidBlockSize: runtime.DefaultSolidBlockSize}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	for rel, id := range entryTransforms(t, marcPath) {
		if id == logdateID {
			t.Fatalf("%s: entry carries retired transform %s", rel, logdateID)
		}
	}
}

// TestExtract_logdateArchiveStillDecodes is the regression guard for the whole
// change: an archive written while log-date-subst/v2 was write-selectable must
// still extract byte-exactly now that the planner never selects it.
func TestExtract_logdateArchiveStillDecodes(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := windowsLogFixture(4000)
	if err := os.WriteFile(filepath.Join(srcDir, "Windows_2k.log"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	marcPath := filepath.Join(tmp, "legacy.marc")
	func() {
		restore := withWriteEnabled(t, logdateID)
		defer restore()
		if err := runtime.ArchiveWithOpts(context.Background(), marcPath, srcDir, "zstd", false,
			runtime.ArchiveOpts{SolidBlockSize: runtime.DefaultSolidBlockSize}); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}()

	// The retirement must be back in force before the read path runs, or the
	// guard below would not distinguish a filtered decode from an unfiltered one.
	for _, e := range plan.Registry {
		if string(e.Transform.ID()) == logdateID && e.Write {
			t.Fatal("registry still write-selectable for " + logdateID)
		}
	}

	// Without this the test would pass on an archive that never used the
	// transform, proving nothing about the decode path.
	got := entryTransforms(t, marcPath)
	if got["Windows_2k.log"] != logdateID {
		t.Fatalf("fixture did not exercise %s, entry transforms = %v", logdateID, got)
	}

	restoreDir := filepath.Join(tmp, "restored")
	if err := runtime.Extract(context.Background(), marcPath, restoreDir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(restoreDir, "Windows_2k.log"))
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if !bytes.Equal(restored, want) {
		t.Fatalf("content mismatch after round-trip: restored %d bytes, archived %d",
			len(restored), len(want))
	}
}

// TestArchive_disableWriteSelectableTransform pins that --disable-transform still
// reaches the write-selectable set now that the loop filters on Write first.
func TestArchive_disableWriteSelectableTransform(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goSrc := []byte("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n")
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), goSrc, 0o644); err != nil {
		t.Fatal(err)
	}

	const golineID = "go-line-subst/v1"
	ctx := context.Background()

	enabled := filepath.Join(tmp, "enabled.marc")
	if err := runtime.ArchiveWithOpts(ctx, enabled, srcDir, "zstd", false,
		runtime.ArchiveOpts{SolidBlockSize: runtime.DefaultSolidBlockSize}); err != nil {
		t.Fatalf("archive enabled: %v", err)
	}
	if got := entryTransforms(t, enabled)["main.go"]; got != golineID {
		t.Fatalf("fixture does not exercise %s, got %q", golineID, got)
	}

	disabled := filepath.Join(tmp, "disabled.marc")
	if err := runtime.ArchiveWithOpts(ctx, disabled, srcDir, "zstd", false,
		runtime.ArchiveOpts{
			SolidBlockSize:     runtime.DefaultSolidBlockSize,
			DisabledTransforms: []string{golineID},
		}); err != nil {
		t.Fatalf("archive disabled: %v", err)
	}
	if got := entryTransforms(t, disabled)["main.go"]; got == golineID {
		t.Fatalf("--disable-transform %s had no effect", golineID)
	}

	restoreDir := filepath.Join(tmp, "restored")
	if err := runtime.Extract(ctx, disabled, restoreDir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	restored, err := os.ReadFile(filepath.Join(restoreDir, "main.go"))
	if err != nil {
		t.Fatalf("read restored: %v", err)
	}
	if !bytes.Equal(restored, goSrc) {
		t.Fatal("content mismatch after round-trip with a disabled transform")
	}
}
