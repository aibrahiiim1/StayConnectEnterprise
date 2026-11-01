//go:build linux

package ledger

import "syscall"

// dirCapacity is the free and total space of the filesystem holding dir, as an unprivileged writer sees it (0, 0 when unknown).
func dirCapacity(dir string) (free, total int64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, 0
	}
	return int64(st.Bavail) * int64(st.Bsize), int64(st.Blocks) * int64(st.Bsize)
}
