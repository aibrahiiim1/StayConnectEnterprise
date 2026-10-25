package main

// What Kea loads when it STARTS (a crash, a package upgrade, a reboot) is the file /etc/kea/kea-dhcp4.conf. It must hold the CONFIRMED
// configuration and nothing else: a change that is still awaiting confirmation, or that was rolled back, must never be what a cold Kea
// comes up with. So the file is written by netd itself, from the confirmed revision's intent, at the three moments the confirmed
// configuration is known to be on the wire: a confirm, a boot reconcile and a rollback to the previous revision. It is written
// atomically (never a partial file) and every failure is reported, instead of being swallowed as the old `config-write` was.
//
// The one exception is the factory-clean bootstrap (kea_bootstrap.go): Kea is installed stopped and cannot be configured through its
// control socket until it runs, so the first apply has to give it a file. Nothing was ever confirmed before that, and the rollback
// restores the file it replaced.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// persistConfirmedKea writes the confirmed active revision's DHCP configuration to the file Kea starts from. eventRev names the revision
// the event is recorded against. It is a no-op on a dry run and when the file already holds exactly this configuration.
func (a *applier) persistConfirmedKea(ctx context.Context, eventRev string) error {
	if a.dryRun {
		return nil
	}
	err := a.persistConfirmedKeaOnce(ctx, eventRev)
	a.setKeaDisk(err)
	return err
}

func (a *applier) persistConfirmedKeaOnce(ctx context.Context, eventRev string) error {
	id, _, intent, err := a.currentActiveIntent(ctx)
	if err != nil {
		// Nothing confirmed (or unreadable): there is no confirmed configuration to persist, and guessing one is what this avoids.
		return fmt.Errorf("the confirmed configuration could not be read: %w", err)
	}
	if id == "" {
		return nil
	}
	full, err := json.MarshalIndent(map[string]any{"Dhcp4": a.renderKea(intent)}, "", "  ")
	if err != nil {
		return fmt.Errorf("render the DHCP configuration: %w", err)
	}
	path := a.keaConfPath()
	if cur, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(cur, full) {
		return nil
	}
	if werr := writeFileAtomic(path, full, 0o644); werr != nil {
		a.event(ctx, eventRev, "kea_disk", false, map[string]any{"path": path, "error": werr.Error()})
		return fmt.Errorf("write %s: %w", path, werr)
	}
	a.event(ctx, eventRev, "kea_disk", true, map[string]any{"path": path, "confirmed_revision": id})
	return nil
}

// setKeaDisk remembers whether the last attempt to persist the confirmed configuration failed, for /v1/health and the retry.
func (a *applier) setKeaDisk(err error) {
	a.keaDiskMu.Lock()
	defer a.keaDiskMu.Unlock()
	if err == nil {
		a.keaDiskErr = ""
		return
	}
	a.keaDiskErr = err.Error()
}

// keaDiskState reports whether the file Kea starts from is known to hold the confirmed configuration.
func (a *applier) keaDiskState() (ok bool, detail string) {
	a.keaDiskMu.Lock()
	defer a.keaDiskMu.Unlock()
	return a.keaDiskErr == "", a.keaDiskErr
}
