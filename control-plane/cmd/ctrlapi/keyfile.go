package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"
)

// loadEd25519File reads a raw 64-byte Ed25519 private key and refuses one that other users can read, or whose
// public half does not belong to its seed (audit Phase 1 M17).
func loadEd25519File(path string) (ed25519.PrivateKey, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "linux" && fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %v); chmod 0600 it", path, fi.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%s: want %d bytes, got %d", path, ed25519.PrivateKeySize, len(raw))
	}
	if !ed25519.NewKeyFromSeed(raw[:ed25519.SeedSize]).Equal(ed25519.PrivateKey(raw)) {
		return nil, fmt.Errorf("%s is not a consistent Ed25519 key (its public half does not match its seed)", path)
	}
	return ed25519.PrivateKey(raw), nil
}

// distinctKeys refuses any two signing roles sharing a key: vendor licence, assignment signing and registry root.
func distinctKeys(keys map[string]ed25519.PrivateKey) error {
	seen := map[string]string{}
	for _, role := range []string{"vendor licence", "assignment signing", "registry root"} {
		k := keys[role]
		if k == nil {
			continue
		}
		if other, dup := seen[string(k)]; dup {
			return fmt.Errorf("the %s key and the %s key are the same key; each role needs its own", other, role)
		}
		seen[string(k)] = role
	}
	return nil
}

// writeNewKeyFile creates path exclusively. With force an existing file is first renamed to path.bak-<UTC
// timestamp>, never overwritten in place.
func writeNewKeyFile(path string, data []byte, force bool) error {
	if force {
		if _, err := os.Lstat(path); err == nil {
			bak := path + ".bak-" + time.Now().UTC().Format("20060102T150405Z")
			if err := os.Rename(path, bak); err != nil {
				return fmt.Errorf("keep the old key as %s: %w", bak, err)
			}
			fmt.Printf("previous key kept as %s\n", bak)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
