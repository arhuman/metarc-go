//go:build windows

package main

// getRSSPeakKB returns 0 on Windows (getrusage not available).
func getRSSPeakKB() int64 { return 0 }
