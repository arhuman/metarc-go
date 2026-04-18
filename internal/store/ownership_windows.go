//go:build windows

package store

import "io/fs"

func ownerIDs(info fs.FileInfo) (uid, gid uint32) {
	return 0, 0
}
