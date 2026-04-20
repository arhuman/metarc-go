// Command analyze walks directories and counts stripped line occurrences
// across files matching a given pattern.
//
// Usage:
//
//	analyze --pattern='*.go' dir1 dir2 ...
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	pattern := flag.String("pattern", "*", "glob pattern for file names (e.g. '*.go')")
	flag.Parse()

	dirs := flag.Args()
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: analyze --pattern=PATTERN dir [dir ...]")
		os.Exit(1)
	}

	freq := make(map[string]int)

	for _, root := range dirs {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if d.IsDir() {
				return nil
			}
			matched, err := filepath.Match(*pattern, d.Name())
			if err != nil || !matched {
				return nil
			}
			countFile(path, freq)
			return nil
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "walk %s: %v\n", root, err)
		}
	}

	type entry struct {
		line  string
		count int
	}
	entries := make([]entry, 0, len(freq))
	for line, count := range freq {
		entries = append(entries, entry{line, count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count != entries[j].count {
			return entries[i].count > entries[j].count
		}
		return entries[i].line < entries[j].line
	})

	w := bufio.NewWriter(os.Stdout)
	for _, e := range entries {
		if e.count <= 2 {
			break
		}
		_, _ = fmt.Fprintf(w, "%d %s\n", e.count, e.line)
	}
	_ = w.Flush()
}

func countFile(path string, freq map[string]int) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimLeft(scanner.Text(), " \t")
		if len(line) > 5 {
			freq[line]++
		}
	}
}
