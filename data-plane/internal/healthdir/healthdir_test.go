package healthdir

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A symlink planted at the target is replaced, never written through.
func TestWriteFileDoesNotFollowASymlinkAtTheTarget(t *testing.T) {
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "netd.backoff.json")
	if err := os.Symlink(victim, target); err != nil {
		t.Skipf("symlinks unavailable here: %v", err)
	}
	if err := WriteFile(dir, "netd.backoff.json", []byte("tracker")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Fatalf("the write followed the planted symlink: victim now %q", b)
	}
	fi, err := os.Lstat(target)
	if err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("the target is still a symlink: %v", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "tracker" {
		t.Fatalf("target = %q", b)
	}
	left, _ := filepath.Glob(filepath.Join(dir, ".netd.backoff.json.*"))
	if len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestEnsureSetsTheStickyBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sticky bit on Windows")
	}
	dir := filepath.Join(t.TempDir(), "health")
	Ensure(dir)
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode()&os.ModeSticky == 0 {
		t.Fatalf("mode %v err %v", fi.Mode(), err)
	}
}
