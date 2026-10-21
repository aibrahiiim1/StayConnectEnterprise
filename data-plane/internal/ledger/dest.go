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

// ErrNotFound is returned by Get for an object that does not exist (as opposed to a storage that cannot be reached).
var ErrNotFound = errors.New("ledger: not found")

// Destination is somewhere a copy of the ledger can be kept. It is deliberately tiny: put a whole object atomically, get it
// back, say whether it can be used. The ledger is small and written rarely (an erasure is a handful of lines), so every
// write replaces the whole object: there is no append protocol to get wrong over a network, and a reader sees the old
// ledger or the new one, never half of one.
type Destination interface {
	// Kind is "dir", "sftp" or "s3".
	Kind() string
	// Describe names the place without any secret ("/mnt/ledger", "sftp://user@host/path", "s3://bucket/prefix").
	Describe() string
	// Check proves the place can be used without changing anything that matters: it is reachable, it can be written and read
	// back, and (for a directory) it is genuinely not the appliance's own disk.
	Check(ctx context.Context) error
	// Put replaces the named object atomically and durably.
	Put(ctx context.Context, name string, data []byte) error
	// Get returns the named object, or ErrNotFound.
	Get(ctx context.Context, name string) ([]byte, error)
	// Remove deletes the named object; a missing one is not an error.
	Remove(ctx context.Context, name string) error
}

// DirDest keeps the ledger in a directory: a mounted external disk, or a NAS share mounted by the operating system.
type DirDest struct {
	Root string
	// Independent, when set, is asked to prove the directory is not on the appliance's own storage. Production sets it
	// (independence_linux.go); tests that only exercise the file protocol leave it nil.
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

// checkRoundTrip writes a probe object, reads it back byte for byte and removes it, so "Test" answers the only question that
// matters: would a ledger put here come back?
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

// Remove deletes an object (used to clean the probe up).
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
