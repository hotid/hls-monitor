//go:build unix

package store

import (
	"path/filepath"
	"syscall"
)

// diskSpace returns the free and total bytes of the volume holding path.
func diskSpace(path string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &st); err != nil {
		return 0, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
}
