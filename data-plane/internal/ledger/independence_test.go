package ledger

import (
	"strings"
	"testing"
)

type fakeTree map[string][]string

func (f fakeTree) Disks(devno string) ([]string, error) {
	if d, ok := f[devno]; ok {
		return d, nil
	}
	return nil, errUnknownDev
}

var errUnknownDev = &struct{ error }{}

// A mount table of an appliance: root and /opt on sda, the database on an LVM volume over sda and sdb, a USB disk on sdc,
// an NFS share and a CIFS share, and a second partition of the root disk.
const sampleMountinfo = `22 1 8:2 / / rw,relatime shared:1 - ext4 /dev/sda2 rw
30 22 8:3 / /opt rw,relatime shared:2 - ext4 /dev/sda3 rw
31 22 253:0 / /var/lib/docker rw,relatime - xfs /dev/mapper/vg-docker rw
32 22 8:33 / /mnt/usb\040disk rw,relatime - ext4 /dev/sdc1 rw
33 22 0:45 / /mnt/nas rw,relatime - nfs4 192.168.50.10:/export/ledger rw
34 22 0:46 / /mnt/smb rw,relatime - cifs //nas.hotel.example/ledger rw
35 22 8:4 / /mnt/second rw,relatime - ext4 /dev/sda4 rw
36 22 0:47 / /mnt/self rw,relatime - nfs4 127.0.0.1:/export rw
37 22 253:0 / /mnt/lvm rw,relatime - xfs /dev/mapper/vg-docker rw
`

var sampleTree = fakeTree{
	"8:2": {"sda"}, "8:3": {"sda"}, "8:4": {"sda"}, "8:33": {"sdc"}, "253:0": {"sda", "sdb"},
}

func TestIndependence(t *testing.T) {
	differ := []string{"/opt/stayconnect", "/var/lib/stayconnect", "/var/lib/docker"}
	cases := []struct {
		dir     string
		wantErr string
	}{
		{"/mnt/usb disk/ledger", ""},
		{"/mnt/nas/site1", ""},
		{"/mnt/smb", ""},
		{"/home/admin/ledger", "own disk"},                  // just a folder on the root filesystem
		{"/opt/stayconnect/ledger-copy", "same filesystem"}, // on /opt
		{"/mnt/second", "same physical disk"},               // another partition of the disk the appliance runs from
		{"/mnt/lvm", "same physical disk"},                  // the database volume itself
		{"/mnt/self", "served by this appliance"},           // an NFS share the appliance exports to itself
	}
	for _, c := range cases {
		err := checkIndependent(sampleMountinfo, sampleTree, c.dir, differ)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: refused: %v", c.dir, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: err = %v, want %q", c.dir, err, c.wantErr)
		}
	}
}

func TestParseMountinfoUnescapesSpaces(t *testing.T) {
	ms := parseMountinfo(sampleMountinfo)
	if m := mountOf(ms, "/mnt/usb disk/x"); m == nil || m.fstype != "ext4" || !strings.Contains(m.point, "usb disk") {
		t.Fatalf("mount = %+v", m)
	}
	if networkHost("//nas.hotel.example/ledger") != "nas.hotel.example" || networkHost("192.168.50.10:/export") != "192.168.50.10" || networkHost("me@box:/p") != "box" {
		t.Fatal("network host parsing")
	}
}
