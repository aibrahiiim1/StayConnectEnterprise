//go:build linux

package ledger

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The real block-device walk, on a fake /sys built with real symlinks (what the kernel exposes), and the real mount table of
// whatever Linux this runs on. (Windows has neither; scripts/ledger-linux-checks.sh runs these in a Linux container.)

func fakeSys(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	mk := func(p string) { _ = os.MkdirAll(filepath.Join(root, p), 0o755) }
	// sda with partitions sda1, sda2; sdb with sdb1; a device-mapper volume over sda2 and sdb1
	for _, d := range []string{"devices/pci0/block/sda/sda1", "devices/pci0/block/sda/sda2", "devices/pci1/block/sdb/sdb1", "devices/virtual/block/dm-0", "dev/block"} {
		mk(d)
	}
	for _, p := range []string{"devices/pci0/block/sda/sda1/partition", "devices/pci0/block/sda/sda2/partition", "devices/pci1/block/sdb/sdb1/partition"} {
		if err := os.WriteFile(filepath.Join(root, p), []byte("1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("devices/virtual/block/dm-0/slaves")
	link := func(from, to string) {
		if err := os.Symlink(to, filepath.Join(root, from)); err != nil {
			t.Fatal(err)
		}
	}
	link("dev/block/8:1", "../../devices/pci0/block/sda/sda1")
	link("dev/block/8:2", "../../devices/pci0/block/sda/sda2")
	link("dev/block/8:17", "../../devices/pci1/block/sdb/sdb1")
	link("dev/block/253:0", "../../devices/virtual/block/dm-0")
	link("dev/block/8:0", "../../devices/pci0/block/sda")
	link("devices/virtual/block/dm-0/slaves/sda2", "../../../pci0/block/sda/sda2")
	link("devices/virtual/block/dm-0/slaves/sdb1", "../../../pci1/block/sdb/sdb1")
	return root
}

func TestSysBlockResolvesPartitionsToDisksAndDeviceMapperToItsSlaves(t *testing.T) {
	tree := sysBlock{root: fakeSys(t)}
	for dev, want := range map[string][]string{
		"8:1":   {"sda"},
		"8:2":   {"sda"},
		"8:17":  {"sdb"},
		"253:0": {"sda", "sdb"},
	} {
		got, err := tree.Disks(dev)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("Disks(%s) = %v %v, want %v", dev, got, err, want)
		}
	}
	if _, err := tree.Disks("9:9"); err == nil {
		t.Error("an unknown device was resolved")
	}
}

func TestIndependenceThroughTheRealSysBlockWalk(t *testing.T) {
	tree := sysBlock{root: fakeSys(t)}
	mi := `22 1 8:1 / / rw - ext4 /dev/sda1 rw
30 22 8:2 / /opt rw - ext4 /dev/sda2 rw
31 22 8:17 / /mnt/usb rw - ext4 /dev/sdb1 rw
32 22 253:0 / /var/lib/docker rw - xfs /dev/mapper/vg-d rw
`
	// the USB disk is its own disk, except that the database volume spans it too
	if err := checkIndependent(mi, tree, "/mnt/usb/ledger", []string{"/opt/stayconnect"}); err != nil {
		t.Errorf("an independent disk was refused: %v", err)
	}
	if err := checkIndependent(mi, tree, "/mnt/usb/ledger", []string{"/var/lib/docker"}); err == nil || !strings.Contains(err.Error(), "same physical disk") {
		t.Errorf("a disk under the database volume was accepted: %v", err)
	}
}

// On any Linux, a folder on the root filesystem is refused, from the machine's real mount table.
func TestProductionIndependenceRefusesTheRootFilesystem(t *testing.T) {
	if _, err := os.Stat("/proc/self/mountinfo"); err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	err := ProductionIndependence([]string{"/opt/stayconnect"})(dir)
	if err == nil {
		// t.TempDir might live on its own tmpfs mount on some systems; then it is refused for another reason or accepted legitimately
		t.Skipf("%s is on a mount of its own here", dir)
	}
	if !strings.Contains(err.Error(), "own disk") && !strings.Contains(err.Error(), "cannot tell") && !strings.Contains(err.Error(), "same") {
		t.Errorf("unexpected refusal: %v", err)
	}
}
