package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/klauspost/compress/zstd"
)

const (
	// defaultMaxSamples is the max number of files to sample for dictionary training.
	defaultMaxSamples = 500
	// defaultMaxSampleBytes is the max total bytes to collect for training.
	defaultMaxSampleBytes = 8 * 1024 * 1024
	// maxSingleFileSize skips files larger than this (dict helps small/medium files).
	maxSingleFileSize = 64 * 1024
	// minSamplesForTraining is the minimum number of samples needed to train.
	minSamplesForTraining = 8
	// dictMaxSize is the max size of the trained dictionary.
	dictMaxSize = 32 * 1024
)

// TrainDictionary samples files from root and trains a zstd dictionary.
// Returns the dictionary bytes, or nil if training fails or not enough data.
func TrainDictionary(root string, maxSamples int, maxSampleBytes int64) ([]byte, error) {
	if maxSamples <= 0 {
		maxSamples = defaultMaxSamples
	}
	if maxSampleBytes <= 0 {
		maxSampleBytes = defaultMaxSampleBytes
	}

	var samples [][]byte
	var totalBytes int64

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if len(samples) >= maxSamples || totalBytes >= maxSampleBytes {
			return fs.SkipAll
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		sz := info.Size()
		if sz == 0 || sz > maxSingleFileSize {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		samples = append(samples, data)
		totalBytes += int64(len(data))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store.TrainDictionary: walk: %w", err)
	}

	if len(samples) < minSamplesForTraining {
		return nil, nil // not enough data to train
	}

	// Build history by concatenating all samples (capped at dictMaxSize).
	// History provides back-reference content; Contents builds entropy tables.
	var history []byte
	for _, s := range samples {
		history = append(history, s...)
		if len(history) >= dictMaxSize {
			history = history[:dictMaxSize]
			break
		}
	}

	dict, err := zstd.BuildDict(zstd.BuildDictOptions{
		ID:       1,
		Contents: samples,
		History:  history,
		Level:    zstd.SpeedDefault,
	})
	if err != nil {
		return nil, fmt.Errorf("store.TrainDictionary: build: %w", err)
	}

	return dict, nil
}
