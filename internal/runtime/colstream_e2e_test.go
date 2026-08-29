package runtime_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/arhuman/metarc-go/internal/plan"
	"github.com/arhuman/metarc-go/internal/runtime"
)

const colStreamID = "col-stream/v1"

// structuredLogCSV mirrors the shape of a loghub *_structured.csv: a dense
// counter column, a quoted time field, a low-cardinality level, and varied
// content. This is the shape col-stream/v1 was measured on, and the only one
// its measured predicate accepts.
func structuredLogCSV(rows int) string {
	components := []string{
		"org.apache.hadoop.mapreduce.v2.app.MRAppMaster",
		"org.apache.hadoop.yarn.client.RMProxy",
		"org.apache.hadoop.ipc.Client",
		"org.apache.hadoop.mapreduce.v2.app.job.impl.TaskAttemptImpl",
	}
	levels := []string{"INFO", "WARN", "ERROR", "DEBUG"}
	var b strings.Builder
	b.WriteString("LineId,Time,Level,Component,Content\n")
	for i := 0; i < rows; i++ {
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(`,"18:0`)
		b.WriteString(strconv.Itoa(i % 10))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(10 + i%50))
		b.WriteByte(',')
		b.WriteString(strconv.Itoa(100 + i%900))
		b.WriteString(`",`)
		b.WriteString(levels[i%len(levels)])
		b.WriteByte(',')
		b.WriteString(components[i%len(components)])
		b.WriteString(",Created MRAppMaster for application appattempt_144514442372")
		b.WriteString(strconv.Itoa(i % 1000))
		b.WriteString("_000001\n")
	}
	return b.String()
}

// colStreamFixtures are the byte-exactness cases that must survive a real
// archive and extract: the plain shape, CRLF endings, a missing trailing
// newline, and a ragged row that goes through the exceptions stream.
func colStreamFixtures() map[string]string {
	base := structuredLogCSV(4000)
	return map[string]string{
		"structured.csv": base,
		"crlf.csv":       strings.ReplaceAll(base, "\n", "\r\n"),
		"notrail.csv":    strings.TrimSuffix(base, "\n"),
		"ragged.csv":     base + "only,two\n",
	}
}

// writeFixtures writes the fixture set into a fresh source directory.
func writeFixtures(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

// TestArchive_colStreamNeverSelected pins the registration: col-stream/v1
// missed its kill criterion (-2.14% on loghub against a >= 5% bar), so it is
// registered decode-only and no entry may carry its ID, even for the exact
// content its predicate accepts.
func TestArchive_colStreamNeverSelected(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	writeFixtures(t, srcDir, colStreamFixtures())

	marcPath := filepath.Join(tmp, "out.marc")
	if err := runtime.ArchiveWithOpts(context.Background(), marcPath, srcDir, "zstd", false,
		runtime.ArchiveOpts{SolidBlockSize: runtime.DefaultSolidBlockSize}); err != nil {
		t.Fatalf("archive: %v", err)
	}

	for rel, id := range entryTransforms(t, marcPath) {
		if id == colStreamID {
			t.Fatalf("%s: entry carries decode-only transform %s", rel, colStreamID)
		}
	}
}

// TestExtract_colStreamArchiveStillDecodes is the guarantee that makes a
// decode-only registration safe: an archive written while col-stream/v1 was
// write-selectable extracts byte-exactly now that the planner never picks it.
// It is also where the transform's byte-exactness cases (CRLF, no trailing
// newline, exception rows) are proven through the real pipeline rather than
// against an in-memory sink.
func TestExtract_colStreamArchiveStillDecodes(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	files := colStreamFixtures()
	writeFixtures(t, srcDir, files)

	marcPath := filepath.Join(tmp, "legacy.marc")
	func() {
		restore := withWriteEnabled(t, colStreamID)
		defer restore()
		if err := runtime.ArchiveWithOpts(context.Background(), marcPath, srcDir, "zstd", false,
			runtime.ArchiveOpts{SolidBlockSize: runtime.DefaultSolidBlockSize}); err != nil {
			t.Fatalf("archive: %v", err)
		}
	}()

	// The read path must run with the retirement back in force, or this would
	// not distinguish a decode that honours Write from one that ignores it.
	for _, e := range plan.Registry {
		if string(e.Transform.ID()) == colStreamID && e.Write {
			t.Fatal("registry still write-selectable for " + colStreamID)
		}
	}

	// Without this the test could pass on an archive that never used the
	// transform, proving nothing about the decode path.
	got := entryTransforms(t, marcPath)
	if got["structured.csv"] != colStreamID {
		t.Fatalf("fixture did not exercise %s, entry transforms = %v", colStreamID, got)
	}

	restoreDir := filepath.Join(tmp, "restored")
	if err := runtime.Extract(context.Background(), marcPath, restoreDir); err != nil {
		t.Fatalf("extract: %v", err)
	}
	for name, want := range files {
		restored, err := os.ReadFile(filepath.Join(restoreDir, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if !bytes.Equal(restored, []byte(want)) {
			t.Errorf("%s: content mismatch after round-trip: restored %d bytes, archived %d",
				name, len(restored), len(want))
		}
	}
}

// TestArchive_colStreamDeclinesRegressingContent is the "compete and commit"
// guard. Even with writing forced on, content the column form does not
// compress better must be left raw. The loghub Mac and Zookeeper CSVs are the
// real instances of this; the fixture reproduces the property that makes them
// regress, namely columns with no exploitable per-column structure.
func TestArchive_colStreamDeclinesRegressingContent(t *testing.T) {
	var b strings.Builder
	b.WriteString("a,b\n")
	for i := 0; i < 8000; i++ {
		b.WriteString(strconv.FormatInt(int64(i*2654435761&0x7fffffff), 36))
		b.WriteByte(',')
		b.WriteString(strconv.FormatInt(int64(i*40503&0x7fffffff), 36))
		b.WriteByte('\n')
	}

	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	writeFixtures(t, srcDir, map[string]string{"noisy.csv": b.String()})

	marcPath := filepath.Join(tmp, "out.marc")
	restore := withWriteEnabled(t, colStreamID)
	if err := runtime.ArchiveWithOpts(context.Background(), marcPath, srcDir, "zstd", false,
		runtime.ArchiveOpts{SolidBlockSize: runtime.DefaultSolidBlockSize}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	restore()

	if got := entryTransforms(t, marcPath)["noisy.csv"]; got == colStreamID {
		t.Error("col-stream/v1 claimed content it does not compress better; the measured predicate did not fire")
	}
}
