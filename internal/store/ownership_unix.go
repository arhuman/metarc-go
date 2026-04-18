//go:build !windows

package store

import (
	"io/fs"
	"syscall"
)

func ownerIDs(info fs.FileInfo) (uid, gid uint32) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Uid, stat.Gid
	}
	return 0, 0
}
