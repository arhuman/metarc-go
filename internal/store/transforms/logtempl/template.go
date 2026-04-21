// Package logtempl implements the log-template/v1 transform, which extracts
// a common prefix from log files and stores only the suffixes, reducing
// redundancy in files with repeated timestamp/hostname patterns.
package logtempl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/arhuman/metarc-go/pkg/marc"
)

const templateID marc.TransformID = "log-template/v1"

// Size limits for applicability.
const (
	minSize = 1024             // 1 KB
	maxSize = 50 * 1024 * 1024 // 50 MB
)

// logBasenames are exact filenames that match regardless of extension.
var logBasenames = map[string]bool{
	"syslog":   true,
	"messages": true,
	"kern.log": true,
}

// Template implements the log-template/v1 transform.
type Template struct{}

// NewTemplate returns a new log-template transform.
func NewTemplate() *Template { return &Template{} }

// ID returns the stable transform identifier.
func (t *Template) ID() marc.TransformID { return templateID }

// Applicable checks whether the entry looks like a log file based on filename heuristics.
func (t *Template) Applicable(_ context.Context, e marc.Entry, facts marc.Facts) bool {
	if facts.Size < minSize || facts.Size > maxSize {
		return false
	}
	base := filepath.Base(e.RelPath)
	if logBasenames[base] {
		return true
	}
	// Match *.log and *.log.* (e.g. access.log, error.log.1, app.log.gz)
	if filepath.Ext(base) == ".log" {
		return true
	}
	// Strip one extension and check again (handles .log.1, .log.gz)
	stripped := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Ext(stripped) == ".log"
}

// CostEstimate returns gain and CPU estimates for log template extraction.
func (t *Template) CostEstimate(_ marc.Entry, facts marc.Facts) (gainBytes, cpuUnits int64) {
	return facts.Size / 3, facts.Size / 256
}

// templateParams is the JSON structure stored in Result.Params.
type templateParams struct {
	Tmpl  string `json:"tmpl"`
	Count int    `json:"count"`
}

// Apply reads the log content, finds a common prefix, and stores suffixes.
func (t *Template) Apply(ctx context.Context, _ marc.Entry, src io.Reader, sink marc.BlobSink) (marc.Result, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return marc.Result{}, fmt.Errorf("log-template: read: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	// Remove trailing empty line from final newline.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	if len(lines) < 10 {
		return marc.Result{}, marc.ErrNotApplicable
	}

	// Find the longest common prefix shared by >50% of lines.
	prefix := findCommonPrefix(lines)
	if prefix == "" {
		return marc.Result{}, marc.ErrNotApplicable
	}

	// Count how many lines share this prefix.
	matchCount := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			matchCount++
		}
	}

	if float64(matchCount)/float64(len(lines)) < 0.5 {
		return marc.Result{}, marc.ErrNotApplicable
	}

	// Build suffixes: strip the prefix from matching lines, keep non-matching lines with a marker.
	suffixes := make([]string, len(lines))
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			suffixes[i] = strings.TrimPrefix(line, prefix)
		} else {
			// Non-matching lines: store with prefix marker so Reverse can distinguish.
			suffixes[i] = "\x00" + line
		}
	}

	suffixData := strings.Join(suffixes, "\n")
	id, err := sink.Write(ctx, bytes.NewReader([]byte(suffixData)))
	if err != nil {
		return marc.Result{}, fmt.Errorf("log-template: write blob: %w", err)
	}

	params, err := json.Marshal(templateParams{Tmpl: prefix, Count: len(lines)})
	if err != nil {
		return marc.Result{}, fmt.Errorf("log-template: marshal params: %w", err)
	}

	return marc.Result{
		BlobIDs: []marc.BlobID{id},
		Params:  params,
	}, nil
}

// Reverse reconstructs the original log content by prepending the template to each suffix.
func (t *Template) Reverse(_ context.Context, r marc.Result, blobs marc.BlobReader, dst io.Writer) error {
	if len(r.BlobIDs) == 0 {
		return nil
	}

	var p templateParams
	if err := json.Unmarshal(r.Params, &p); err != nil {
		return fmt.Errorf("log-template: unmarshal params: %w", err)
	}

	rc, err := blobs.Open(r.BlobIDs[0])
	if err != nil {
		return fmt.Errorf("log-template: open blob: %w", err)
	}
	defer func() { _ = rc.Close() }()

	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("log-template: read blob: %w", err)
	}

	suffixes := strings.Split(string(data), "\n")

	var buf bytes.Buffer
	for i, suffix := range suffixes {
		if i > 0 {
			buf.WriteByte('\n')
		}
		if strings.HasPrefix(suffix, "\x00") {
			// Non-matching line, stored verbatim after marker.
			buf.WriteString(strings.TrimPrefix(suffix, "\x00"))
		} else {
			buf.WriteString(p.Tmpl)
			buf.WriteString(suffix)
		}
	}
	// Original data ended with \n (we stripped the trailing empty string).
	buf.WriteByte('\n')

	_, err = dst.Write(buf.Bytes())
	return err
}

// findCommonPrefix finds the longest prefix shared by more than 50% of lines.
// It starts with the first line as a candidate and progressively shortens it.
func findCommonPrefix(lines []string) string {
	if len(lines) == 0 {
		return ""
	}

	// Use the first non-empty line as the initial candidate.
	candidate := ""
	for _, line := range lines {
		if len(line) > 0 {
			candidate = line
			break
		}
	}
	if candidate == "" {
		return ""
	}

	// Try progressively shorter prefixes.
	threshold := float64(len(lines)) * 0.5
	for length := len(candidate); length > 0; length-- {
		prefix := candidate[:length]
		count := 0
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) {
				count++
			}
		}
		if float64(count) > threshold {
			return prefix
		}
	}

	return ""
}
