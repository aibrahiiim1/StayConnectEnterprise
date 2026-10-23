package ledger

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// A second copy on the same disk is not a second copy: a folder destination must be an external disk or network
// share, decided from the kernel's mount table and block-device tree, not the folder name (so an unplugged USB mount
// point on the root disk is refused).

type mountEntry struct {
	id      string
	devno   string // "major:minor"
	point   string
	fstype  string
	source  string
	network bool
}

var networkFS = map[string]bool{"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "fuse.sshfs": true, "9p": true, "ceph": true, "glusterfs": true, "fuse.glusterfs": true}

func parseMountinfo(text string) []mountEntry {
	var out []mountEntry
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		dash := -1
		for i, x := range f {
			if x == "-" {
				dash = i
				break
			}
		}
		if dash < 0 || len(f) < dash+3 || dash < 6 {
			continue
		}
		e := mountEntry{id: f[0], devno: f[2], point: unescapeMount(f[4]), fstype: f[dash+1], source: f[dash+2]}
		e.network = networkFS[e.fstype]
		out = append(out, e)
	}
	return out
}

// unescapeMount undoes the octal escapes (\040 for a space) the kernel writes in mount points.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+4 <= len(s) {
			var v int
			if _, err := fmt.Sscanf(s[i+1:i+4], "%03o", &v); err == nil {
				b.WriteByte(byte(v))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// mountOf is the mount a path lives on: the entry with the longest mount point that is a prefix of the path.
func mountOf(ms []mountEntry, p string) *mountEntry {
	p = path.Clean(filepath.ToSlash(p))
	var best *mountEntry
	for i := range ms {
		mp := path.Clean(ms[i].point)
		if mp == "/" || p == mp || strings.HasPrefix(p, mp+"/") {
			if best == nil || len(mp) >= len(path.Clean(best.point)) {
				best = &ms[i]
			}
		}
	}
	return best
}

type BlockTree interface {
	Disks(devno string) ([]string, error)
}

type sysBlock struct{ root string }

func (s sysBlock) Disks(devno string) ([]string, error) {
	set := map[string]bool{}
	if err := s.walk(filepath.Join(s.root, "dev", "block", devno), set, 0); err != nil {
		return nil, err
	}
	var out []string
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out, nil
}

func (s sysBlock) walk(link string, set map[string]bool, depth int) error {
	if depth > 8 {
		return errors.New("the block device tree is nested too deeply")
	}
	dir, err := filepath.EvalSymlinks(link)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "partition")); err == nil {
		dir = filepath.Dir(dir) // a partition: the disk is its parent
	}
	slaves, _ := os.ReadDir(filepath.Join(dir, "slaves"))
	if len(slaves) > 0 { // device-mapper / md / lvm: the disks are underneath
		for _, sl := range slaves {
			if err := s.walk(filepath.Join(dir, "slaves", sl.Name()), set, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	set[filepath.Base(dir)] = true
	return nil
}

// ProductionIndependence returns the check production uses: root is "" for the real system. mustDiffer are the places the
// appliance keeps its own data (database, application, backups, the primary ledger).
func ProductionIndependence(mustDiffer []string) func(string) error {
	return func(target string) error {
		b, err := os.ReadFile("/proc/self/mountinfo")
		if err != nil {
			return errors.New("the appliance cannot tell what disk that folder is on, so it cannot be used")
		}
		return checkIndependent(string(b), sysBlock{root: "/sys"}, target, mustDiffer)
	}
}

func checkIndependent(mountinfo string, tree BlockTree, target string, mustDiffer []string) error {
	ms := parseMountinfo(mountinfo)
	tm := mountOf(ms, target)
	if tm == nil {
		return errors.New("the appliance cannot tell what disk that folder is on, so it cannot be used")
	}
	if path.Clean(tm.point) == "/" {
		return errors.New("that folder is on the appliance's own disk: use an external disk or a network share that is mounted there")
	}
	if tm.network {
		host := networkHost(tm.source)
		if host != "" && isLocalHost(host) {
			return errors.New("that network share is served by this appliance: a copy kept here is not independent of it")
		}
		return nil
	}
	tdisks, err := tree.Disks(tm.devno)
	if err != nil {
		return errors.New("the appliance cannot tell which physical disk that folder is on, so it cannot be used")
	}
	for _, ref := range mustDiffer {
		rm := mountOf(ms, ref)
		if rm == nil {
			continue
		}
		if rm.id == tm.id {
			return fmt.Errorf("that folder is on the same filesystem as %s", ref)
		}
		if rm.network {
			continue
		}
		rdisks, err := tree.Disks(rm.devno)
		if err != nil {
			return errors.New("the appliance cannot tell which physical disk its own data is on, so it cannot check that folder")
		}
		for _, a := range tdisks {
			for _, b := range rdisks {
				if a == b {
					return fmt.Errorf("that folder is on the same physical disk (%s) as %s", a, ref)
				}
			}
		}
	}
	return nil
}

// networkHost extracts the server from an NFS ("server:/export"), CIFS ("//server/share") or sshfs ("user@server:/path") source.
func networkHost(src string) string {
	switch {
	case strings.HasPrefix(src, "//"):
		h := strings.TrimPrefix(src, "//")
		if i := strings.IndexByte(h, '/'); i >= 0 {
			h = h[:i]
		}
		return strings.Trim(stripUser(h), "[]")
	case strings.Contains(src, ":"):
		h := src[:strings.LastIndex(src, ":")]
		return strings.Trim(stripUser(h), "[]")
	}
	return ""
}

func stripUser(h string) string {
	if i := strings.LastIndexByte(h, '@'); i >= 0 {
		h = h[i+1:]
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}
