//go:build !windows

package main

import "syscall"

// getRSSPeakKB returns the peak RSS in KB using getrusage(2) (macOS/Linux).
func getRSSPeakKB() int64 {
	var rusage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &rusage); err != nil {
		return 0
	}
	// On macOS, Maxrss is in bytes; on Linux, it's in KB.
	maxrss := rusage.Maxrss
	if maxrss > 1024*1024 {
		maxrss /= 1024
	}
	return maxrss
}
