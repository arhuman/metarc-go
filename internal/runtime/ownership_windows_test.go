//go:build windows

package runtime_test

import (
	"os"
	"testing"
)

func checkOwnership(_ *testing.T, _ string, _, _ os.FileInfo) {}
