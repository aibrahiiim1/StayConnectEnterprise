// Package healthdir writes the shared health scratch directory (/run/stayconnect/health) safely (audit B-3).
//
// Every service writes its own backoff tracker and heartbeat there, several of them as root, and the directory
// was 0777 without the sticky bit: any local account could delete another service's file and put a symlink in
// its place, and the next root write -- os.WriteFile, os.Chtimes, OpenFile(O_CREATE) all follow symlinks --
// would write through it to wherever it pointed.
//
// Two rules now hold:
//
//   - The directory is 1777 (sticky): an account may create files there, and may remove or rename only its own.
//   - Every write goes to a fresh, exclusively created temporary file in the same directory and is renamed over
//     the target. os.CreateTemp opens with O_EXCL, so a planted name is never opened, and rename(2) replaces
//     the directory entry itself -- a symlink at the target is replaced, never followed.
package healthdir

import (
	"os"
	"path/filepath"
)

// Mode is the directory mode: world-writable WITH the sticky bit.
const Mode = os.ModeSticky | 0o777

// Ensure creates the directory if needed and asserts its mode. A failure is not fatal to the caller: health
// files are diagnostics, and a service must never stop because it could not write one.
func Ensure(dir string) {
	_ = os.MkdirAll(dir, 0o755)
	if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
		_ = os.Chmod(dir, Mode)
	}
}

// WriteFile replaces dir/name with data, never following a symlink at the target.
func WriteFile(dir, name string, data []byte) error {
	Ensure(dir)
	f, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
