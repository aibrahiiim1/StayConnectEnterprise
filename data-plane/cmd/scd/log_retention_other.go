//go:build !linux

package main

import (
	"errors"
	"io/fs"
	"os"
)

// THE NO-FOLLOW OPEN IS LINUX-ONLY. scd's log retention runs on the appliance, which is Linux; this build exists
// so the package compiles and its unit tests run on a workstation. It refuses a symlink by name before opening,
// which is weaker (the name can change in between) and acceptable only because this build is not the appliance.

func openLogNoFollow(path string, flag int) (*os.File, error) {
	if fi, err := os.Lstat(path); err != nil {
		return nil, err
	} else if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, errors.New("refusing to follow a symlink")
	}
	return os.OpenFile(path, flag, 0)
}

func logFileInPlace(_, _ fs.FileInfo) bool { return true }
