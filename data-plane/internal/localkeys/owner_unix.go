//go:build unix

package localkeys

import (
	"os"
	"syscall"
)

// ownerOK accepts a key owned by root or by the user this process runs as.
func ownerOK(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return st.Uid == 0 || int(st.Uid) == os.Geteuid()
}

func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
}
