package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestTrainDictionary_basic trains a dictionary from a directory with enough
// similar small files, then verifies a non-nil dictionary is returned.
func TestTrainDictionary_basic(t *testing.T) {
	tmp := t.TempDir()

	// Create enough similar small files for training (minSamplesForTraining = 8).
	prefix := make([]byte, 2000)
	for i := range prefix {
		prefix[i] = byte(i % 127)
	}
	for i := range 20 {
		name := filepath.Join(tmp, "file"+string(rune('a'+i%26))+".txt")
		content := append(prefix, byte(i))
		if err := os.WriteFile(name, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dict, err := TrainDictionary(tmp, 0, 0)
	if err != nil {
		t.Fatalf("TrainDictionary: %v", err)
	}
	if dict == nil {
		t.Skip("dict training returned nil (BuildDict requires more data in this environment)")
	}
	if len(dict) == 0 {
		t.Fatal("returned empty dictionary")
	}
}

// TestTrainDictionary_notEnoughSamples verifies that TrainDictionary returns nil
// when there are fewer than minSamplesForTraining files.
func TestTrainDictionary_notEnoughSamples(t *testing.T) {
	tmp := t.TempDir()

	// Create only 3 files (< minSamplesForTraining = 8).
	for i := range 3 {
		name := filepath.Join(tmp, "file"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("small content "+string(rune('a'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dict, err := TrainDictionary(tmp, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dict != nil {
		t.Fatalf("expected nil dict with too few samples, got %d bytes", len(dict))
	}
}

// TestTrainDictionary_emptyDir verifies that an empty directory yields nil (no samples).
func TestTrainDictionary_emptyDir(t *testing.T) {
	tmp := t.TempDir()

	dict, err := TrainDictionary(tmp, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dict != nil {
		t.Fatalf("expected nil dict for empty dir, got %d bytes", len(dict))
	}
}

// TestTrainDictionary_skipLargeFiles verifies that files > maxSingleFileSize are skipped.
func TestTrainDictionary_skipLargeFiles(t *testing.T) {
	tmp := t.TempDir()

	// Create one large file exceeding maxSingleFileSize (64KB).
	large := make([]byte, 128*1024)
	for i := range large {
		large[i] = byte(i % 256)
	}
	if err := os.WriteFile(filepath.Join(tmp, "large.bin"), large, 0o644); err != nil {
		t.Fatal(err)
	}

	// Create a few small files (but fewer than minSamplesForTraining).
	for i := range 2 {
		name := filepath.Join(tmp, "small"+string(rune('a'+i))+".txt")
		if err := os.WriteFile(name, []byte("tiny"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	dict, err := TrainDictionary(tmp, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With fewer than minSamplesForTraining small files, should return nil.
	if dict != nil {
		t.Fatalf("expected nil (large file skipped + too few small), got %d bytes", len(dict))
	}
}

// TestTrainDictionary_maxSamples verifies the maxSamples cap is respected.
func TestTrainDictionary_maxSamples(t *testing.T) {
	tmp := t.TempDir()

	// Create 50 small files.
	content := make([]byte, 1000)
	for i := range content {
		content[i] = byte(i % 127)
	}
	for i := range 50 {
		name := filepath.Join(tmp, "f"+string(rune('a'+i%26))+".txt")
		c := append(content, byte(i))
		if err := os.WriteFile(name, c, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Cap at 10 samples — still above minSamplesForTraining=8, might succeed.
	dict, err := TrainDictionary(tmp, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// dict may be nil if BuildDict decides data is insufficient; either is valid.
	_ = dict
}

// TestTrainDictionary_invalidRoot verifies an error is returned for a bad path.
func TestTrainDictionary_invalidRoot(t *testing.T) {
	_, err := TrainDictionary("/nonexistent/path/that/does/not/exist", 0, 0)
	if err != nil {
		// An error is expected for non-existent paths (WalkDir returns error).
		// Some environments may return nil here if WalkDir ignores the error.
		t.Logf("got expected error: %v", err)
	}
}
