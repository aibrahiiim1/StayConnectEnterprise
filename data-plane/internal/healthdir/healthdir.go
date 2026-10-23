// Package healthdir writes the shared world-writable health directory safely: the directory is sticky, and
// writes go to an O_EXCL temp file renamed over the target, so a planted symlink is replaced, never followed
// by a root writer.
package healthdir

import (
	"os"
	"path/filepath"
)

const Mode = os.ModeSticky | 0o777

// Ensure never fails the caller: health files are diagnostics, and a service must not stop over one.
func Ensure(dir string) {
	_ = os.MkdirAll(dir, 0o755)
	if fi, err := os.Lstat(dir); err == nil && fi.IsDir() {
		_ = os.Chmod(dir, Mode)
	}
}

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
