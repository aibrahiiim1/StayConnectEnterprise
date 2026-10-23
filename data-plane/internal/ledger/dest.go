package ledger

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned by Get for a missing object (as opposed to unreachable storage).
var ErrNotFound = errors.New("ledger: not found")

// Destination keeps a copy of the ledger. Every write replaces the whole object atomically, so there is no append
// protocol over the network and a reader never sees half a ledger.
type Destination interface {
	Kind() string
	// Describe names the place without any secret ("/mnt/ledger", "sftp://user@host/path", "s3://bucket/prefix").
	Describe() string
	// Check proves the place is usable without changing anything that matters: reachable, writable and readable, and
	// for a directory not the appliance's own disk.
	Check(ctx context.Context) error
	Put(ctx context.Context, name string, data []byte) error
	Get(ctx context.Context, name string) ([]byte, error)
	Remove(ctx context.Context, name string) error
}

// DirDest keeps the ledger in a directory: a mounted external disk, or a NAS share mounted by the operating system.
type DirDest struct {
	Root string
	// Independent, when set, proves the directory is not on the appliance's own storage; production sets it, tests leave it nil.
	Independent func(root string) error
}

func (d *DirDest) Kind() string     { return "dir" }
func (d *DirDest) Describe() string { return d.Root }

func (d *DirDest) path(name string) (string, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("ledger: bad object name %q", name)
	}
	return filepath.Join(d.Root, name), nil
}

func (d *DirDest) Check(ctx context.Context) error {
	if !filepath.IsAbs(d.Root) {
		return errors.New("the folder must be an absolute path")
	}
	fi, err := os.Stat(d.Root)
	if err != nil {
		return fmt.Errorf("the folder cannot be used: %w", err)
	}
	if !fi.IsDir() {
		return errors.New("that path is not a folder")
	}
	if d.Independent != nil {
		if err := d.Independent(d.Root); err != nil {
			return err
		}
	}
	return checkRoundTrip(ctx, d)
}

func (d *DirDest) Put(_ context.Context, name string, data []byte) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.Root, name+".*.partial")
	if err != nil {
		return err
	}
	tn := tmp.Name()
	fail := func(e error) error { tmp.Close(); _ = os.Remove(tn); return e }
	if _, err := tmp.Write(data); err != nil {
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
	if err := os.Rename(tn, p); err != nil {
		_ = os.Remove(tn)
		return err
	}
	if dh, err := os.Open(d.Root); err == nil {
		_ = dh.Sync()
		dh.Close()
	}
	return nil
}

func (d *DirDest) Get(_ context.Context, name string) ([]byte, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	return b, err
}

// checkRoundTrip writes, reads back and removes a probe object: would a ledger put here come back?
func checkRoundTrip(ctx context.Context, d Destination) error {
	probe := []byte("stayconnect ledger probe " + hex.EncodeToString(probeNonce()))
	name := "ledger-probe.tmp"
	if err := d.Put(ctx, name, probe); err != nil {
		return fmt.Errorf("it cannot be written: %w", err)
	}
	got, err := d.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("what was written cannot be read back: %w", err)
	}
	if string(got) != string(probe) {
		return errors.New("what was read back is not what was written")
	}
	_ = d.Remove(ctx, name)
	return nil
}

func probeNonce() []byte {
	b := make([]byte, 8)
	_, _ = io.ReadFull(randReader{}, b)
	return b
}

func (d *DirDest) Remove(_ context.Context, name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
