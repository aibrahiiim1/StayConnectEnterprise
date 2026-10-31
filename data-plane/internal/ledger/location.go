package ledger

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Location is what a destination can say about where its objects actually land, so an administrator can compare it with what
// they see on the other side (a chrooted SFTP account and the server's own disk name the same folder differently). Every field is
// best effort; empty means the destination could not tell.
type Location struct {
	// Where is the folder, server path or bucket and prefix the objects are written to, as the destination resolved it.
	Where string `json:"where"`
	// Medium: external_disk | nas_smb | nas_nfs | network_share | sftp | s3.
	Medium     string `json:"medium,omitempty"`
	FSType     string `json:"filesystem,omitempty"`
	Source     string `json:"source,omitempty"`
	FreeBytes  int64  `json:"free_bytes,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
}

// Locator is implemented by destinations that can report their Location.
type Locator interface {
	Locate(ctx context.Context) (Location, error)
}

// FileDest is implemented by destinations that can keep files too large to hold in memory (backup sets). PutFile replaces the
// object atomically where the protocol allows; Digest reads the stored object back IN FULL and returns its SHA-256 and size, which
// is the only proof a caller accepts that the copy exists and is the one that was sent.
type FileDest interface {
	PutFile(ctx context.Context, name, localPath string) error
	Digest(ctx context.Context, name string) (sha string, size int64, err error)
}

func digestReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// FileDigest is the SHA-256 and size of a local file.
func FileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	return digestReader(f)
}

// validObjectName is the one rule for every destination: a single path element, never a traversal.
func validObjectName(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\\x00") || name == "." || name == ".." {
		return fmt.Errorf("ledger: bad object name %q", name)
	}
	return nil
}

func (d *DirDest) Locate(_ context.Context) (Location, error) {
	loc := Location{Where: d.Root, Medium: "external_disk"}
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		if m := mountOf(parseMountinfo(string(b)), d.Root); m != nil {
			loc.FSType, loc.Source = m.fstype, m.source
			switch {
			case m.fstype == "cifs" || m.fstype == "smb3" || m.fstype == "smbfs":
				loc.Medium = "nas_smb"
			case m.fstype == "nfs" || m.fstype == "nfs4":
				loc.Medium = "nas_nfs"
			case m.network:
				loc.Medium = "network_share"
			}
		}
	}
	loc.FreeBytes, loc.TotalBytes = dirCapacity(d.Root)
	return loc, nil
}

func (d *DirDest) PutFile(ctx context.Context, name, localPath string) error {
	if err := validObjectName(name); err != nil {
		return err
	}
	in, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(d.Root, name+".*.partial")
	if err != nil {
		return err
	}
	tn := tmp.Name()
	fail := func(e error) error { tmp.Close(); _ = os.Remove(tn); return e }
	if _, err := io.Copy(tmp, contextReader{ctx, in}); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tn)
		return err
	}
	_ = os.Chmod(tn, 0o600)
	if err := os.Rename(tn, filepath.Join(d.Root, name)); err != nil {
		_ = os.Remove(tn)
		return err
	}
	syncDir(d.Root)
	return nil
}

func (d *DirDest) Digest(_ context.Context, name string) (string, int64, error) {
	if err := validObjectName(name); err != nil {
		return "", 0, err
	}
	sum, n, err := FileDigest(filepath.Join(d.Root, name))
	if errors.Is(err, os.ErrNotExist) {
		return "", 0, ErrNotFound
	}
	return sum, n, err
}

// contextReader stops a long copy when its context ends.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (c contextReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
