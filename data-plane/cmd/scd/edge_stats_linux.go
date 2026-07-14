//go:build linux

package main

import "syscall"

// diskStats returns free/total bytes for the filesystem holding path,
// or (-1, -1) when unavailable.
func diskStats(path string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return -1, -1
	}
	return int64(st.Bavail) * st.Bsize, int64(st.Blocks) * st.Bsize
}
