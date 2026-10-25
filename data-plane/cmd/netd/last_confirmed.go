package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// lastConfirmed is the on-disk copy of the configuration the database last recorded as confirmed. It is written only after the database commit, so
// it can lag the database but never lead it; when the database answers, the database decides and this file is rewritten from it. It carries
// network intent only: no sessions, devices or credentials, so restoring from it can never reinstate access that was revoked.
type lastConfirmed struct {
	Revision  string                `json:"revision"`
	Bundle    string                `json:"bundle"`
	PortalVIP string                `json:"portal_vip"`
	Intent    []netcfg.GuestNetwork `json:"intent"`
	At        string                `json:"at"`
	Digest    string                `json:"digest"`
}

func (a *applier) lastConfirmedPath() string {
	return filepath.Join(a.generatedDir, "last-confirmed.json")
}

func lastConfirmedDigest(id, vip string, intent []netcfg.GuestNetwork) string {
	raw, _ := json.Marshal(struct {
		ID     string
		VIP    string
		Intent []netcfg.GuestNetwork
	}{id, vip, intent})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// saveLastConfirmed writes the snapshot atomically; a crash leaves the previous snapshot or the new one, never a torn file.
func (a *applier) saveLastConfirmed(id, bundle, vip string, intent []netcfg.GuestNetwork) error {
	if a.generatedDir == "" || a.dryRun || id == "" {
		return nil
	}
	m := lastConfirmed{Revision: id, Bundle: bundle, PortalVIP: vip, Intent: intent, At: time.Now().UTC().Format(time.RFC3339),
		Digest: lastConfirmedDigest(id, vip, intent)}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(a.lastConfirmedPath(), raw, 0o640)
}

// readLastConfirmed returns the snapshot only when it is whole and its digest matches its content.
func (a *applier) readLastConfirmed() (lastConfirmed, error) {
	raw, err := os.ReadFile(a.lastConfirmedPath())
	if err != nil {
		return lastConfirmed{}, err
	}
	var m lastConfirmed
	if err := json.Unmarshal(raw, &m); err != nil {
		return lastConfirmed{}, fmt.Errorf("the last-confirmed snapshot is unreadable: %w", err)
	}
	if m.Revision == "" || len(m.Intent) == 0 || m.Digest != lastConfirmedDigest(m.Revision, m.PortalVIP, m.Intent) {
		return lastConfirmed{}, errors.New("the last-confirmed snapshot is incomplete or altered")
	}
	return m, nil
}

// keepLastConfirmedCurrent rewrites the snapshot from the database's confirmed revision when they differ.
func (a *applier) keepLastConfirmedCurrent(id, bundle, vip string, intent []netcfg.GuestNetwork) {
	if cur, err := a.readLastConfirmed(); err == nil && cur.Digest == lastConfirmedDigest(id, vip, intent) {
		return
	}
	if err := a.saveLastConfirmed(id, bundle, vip, intent); err != nil {
		slog.Error("netd could not save the last-confirmed snapshot; a restart with the database down could not restore this configuration", "revision", id, "err", err)
	}
}

// databaseUnreachable separates "the database did not answer" from "the database answered and said something": only the first allows the snapshot to stand in.
func databaseUnreachable(err error) bool {
	if err == nil || errors.Is(err, ErrNoConfirmedActiveRevision) {
		return false
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return false
	}
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.As(err, &ne) || pgconn.SafeToRetry(err) || pgconn.Timeout(err)
}
