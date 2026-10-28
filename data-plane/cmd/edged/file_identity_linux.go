//go:build linux

package main

import (
	"os"
	"syscall"
)

// fileIdentity is what must stay equal for a cached digest to still describe a file. ctime is included because no
// user-space write can set it back, unlike mtime.
type fileIdentity struct {
	dev, ino         uint64
	size             int64
	mtimeNs, ctimeNs int64
}

func identityOf(fi os.FileInfo) (fileIdentity, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, false
	}
	return fileIdentity{
		dev: uint64(st.Dev), ino: st.Ino, size: st.Size,
		mtimeNs: st.Mtim.Nano(), ctimeNs: st.Ctim.Nano(),
	}, true
}
