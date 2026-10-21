//go:build linux

package main

import (
	"io/fs"
	"os"
	"syscall"
)

// openLogNoFollow opens a log by name without ever following a symlink: a symlink planted in a log directory
// (writable by the service that logs there) makes the open fail with ELOOP instead of reaching its target.
func openLogNoFollow(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
}

// logFileInPlace reports whether an opened log is a file of its own in dir: on dir's filesystem and with no
// other name. A hard link to a file elsewhere has two names, and root must not empty or copy that file.
func logFileInPlace(dir, f fs.FileInfo) bool {
	ds, ok1 := dir.Sys().(*syscall.Stat_t)
	fst, ok2 := f.Sys().(*syscall.Stat_t)
	return ok1 && ok2 && ds.Dev == fst.Dev && fst.Nlink == 1
}
