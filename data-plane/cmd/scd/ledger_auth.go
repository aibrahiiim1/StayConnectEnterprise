package main

// Authentication of the erasure ledger (audit AD-3, migration 0220; internal/ledger/mac.go). The key lives in the appliance
// database; scd reads it (never edged) to issue durability receipts, to MAC the secondary storage's head note and to check
// a ledger before a restore or a recovery applies it. A copy of the key is kept sealed to the backup key holders so a
// replacement appliance can still tell its own history from a forgery.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// ledgerKeyEscrowPath is the key sealed to the backup key holders. It is not a database dump, so the backup download list does
// not offer it (audit AD-9).
var ledgerKeyEscrowPath = "/opt/stayconnect/backups/ledger-mac-key.age"

// ledgerCachedKey is what the ledger store uses: no I/O, never blocks.
func (s *server) ledgerCachedKey() ledger.MACKey {
	rt := &s.ledgerRT
	rt.keyMu.Lock()
	defer rt.keyMu.Unlock()
	return rt.key2
}

// ledgerKey returns the database's ledger key, loading it once. nil means the database has no key (it predates migration
// 0220), in which case the ledger works as it did before.
func (s *server) ledgerKey(ctx context.Context) ledger.MACKey {
	if k := s.ledgerCachedKey(); len(k) > 0 {
		return k
	}
	if s.db == nil {
		return nil
	}
	var hexKey string
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.ledger_mac_key_get()`).Scan(&hexKey); err != nil {
		if !ledgerMissing(err) {
			slog.Warn("erasure ledger: the authentication key could not be read", "err", err)
		}
		return nil
	}
	k, err := ledger.ParseMACKey(hexKey)
	if err != nil {
		slog.Error("erasure ledger: the authentication key in the database is malformed", "err", err)
		return nil
	}
	rt := &s.ledgerRT
	rt.keyMu.Lock()
	rt.key2 = k
	rt.keyMu.Unlock()
	return k
}

// ledgerForgetKey is called when the database is swapped (a restore) or the previous customer's ledger is removed.
func (s *server) ledgerForgetKey() {
	rt := &s.ledgerRT
	rt.keyMu.Lock()
	rt.key2 = nil
	rt.keyMu.Unlock()
}

// ledgerSeal appends the seal that makes entries written before the key existed usable. It runs only after reconciliation
// found the database and both copies in step (the caller checks State == "ok"), so the sealed chain is the one all three hold.
// When they disagree nothing is sealed, the unsealed entries stay unusable, and no restore will apply them.
func (s *server) ledgerSeal(ctx context.Context) error {
	rt := &s.ledgerRT
	if rt.store == nil || rt.store.Destination() == nil || s.db == nil || len(s.ledgerKey(ctx)) == 0 {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, ledgerLockSQL); err != nil {
		return err
	}
	tenant, site := s.tenID, s.siteID
	if tenant == "" || site == "" {
		tenant, site = "00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000"
	}
	var raw *string
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_seal_legacy($1::uuid,$2::uuid)::text`, tenant, site).Scan(&raw); err != nil {
		return err
	}
	if raw == nil {
		return nil // nothing to seal, or already sealed
	}
	e, err := parseEntries([]byte("[" + *raw + "]"))
	if err != nil || len(e) != 1 {
		return fmt.Errorf("the seal entry could not be read: %v", err)
	}
	if err := rt.store.Commit(ctx, e); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.ledger_confirm($1)`, []string{e[0].Hash}); err != nil {
		_ = rt.store.Abort(ctx, e)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = rt.store.Abort(ctx, e)
		return err
	}
	slog.Info("erasure ledger: the entries written before authentication existed were sealed", "seq", e[0].Seq)
	return nil
}

// ledgerWriteEscrow keeps the key sealed to the backup key holders. Without a key holder it says so and keeps nothing: the key
// then exists only in the database (and so only in its encrypted backups).
func (s *server) ledgerWriteEscrow(ctx context.Context) {
	key := s.ledgerKey(ctx)
	if len(key) == 0 {
		return
	}
	rcpt, err := s.backupRecipients(ctx)
	if err != nil || len(rcpt) == 0 {
		slog.Warn("erasure ledger: there is no backup key holder, so the ledger key has no sealed copy outside the database")
		return
	}
	if err := writeLedgerEscrow(ledgerKeyEscrowPath, key, rcpt); err != nil {
		slog.Warn("erasure ledger: the sealed copy of the ledger key could not be written", "err", err)
	}
}

func writeLedgerEscrow(path string, key ledger.MACKey, rcpt []age.Recipient) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w, err := backupcrypt.Encrypt(f, rcpt)
	if err == nil {
		_, err = w.Write([]byte(key.Hex() + "\n"))
		if cerr := w.Close(); err == nil {
			err = cerr
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// readLedgerEscrow opens the sealed copy with a key holder's key.
func readLedgerEscrow(path string, ident age.Identity) (ledger.MACKey, error) {
	if ident == nil {
		return nil, errors.New("a backup key holder's key is needed to open the sealed ledger key")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := backupcrypt.Decrypt(f, ident)
	if err != nil {
		return nil, errors.New("that key does not open the sealed ledger key")
	}
	b, err := io.ReadAll(io.LimitReader(r, 4096))
	if err != nil {
		return nil, err
	}
	return ledger.ParseMACKey(strings.TrimSpace(string(b)))
}

// restoreLedgerKey decides which key authenticates the ledger copies about to be applied to a restored database. The live
// database's key is preferred; a replacement appliance (whose key is new) falls back to the sealed copy. It returns a plain
// refusal when the entries cannot be authenticated by either: a restore never erases records on an entry it cannot vouch for.
func (s *server) restoreLedgerKey(ctx context.Context, entries []ledger.Entry, after int64, ident age.Identity) (ledger.MACKey, string) {
	live := s.ledgerKey(ctx)
	anyMAC := false
	for _, e := range entries {
		if e.Seq > after && e.Mac != "" {
			anyMAC = true
			break
		}
	}
	if len(live) > 0 {
		if err := live.AuthenticateAfter(entries, after); err == nil {
			return live, ""
		}
	} else if !anyMAC {
		return nil, "" // an appliance and a ledger that both predate authentication: nothing to check against
	}
	esc, eerr := readLedgerEscrow(ledgerKeyEscrowPath, ident)
	if eerr == nil {
		if err := esc.AuthenticateAfter(entries, after); err == nil {
			return esc, ""
		}
	}
	reason := "the erasure ledger holds an entry that this appliance did not write (or that no seal covers), so the restore was not attempted: nothing is erased on an entry that cannot be vouched for"
	if eerr != nil && ident == nil {
		reason += ". If this is a replacement appliance, give a backup key holder's key so the sealed ledger key can be opened"
	}
	return nil, reason
}

// installRestoreKey puts the key the ledger copies were written with into the staging database, so the database's own check
// (ledger_apply) agrees with ours. A backup older than migration 0220 has no such function and needs nothing.
func (s *server) installRestoreKey(ctx context.Context, staging string, key ledger.MACKey) (string, bool) {
	if len(key) == 0 {
		return "", true
	}
	out, err := execPsql(ctx, staging, fmt.Sprintf("SELECT iam_v2.ledger_mac_key_install('%s')", key.Hex()))
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return "", true
		}
		return "the ledger key could not be put into the restored data: " + err.Error(), false
	}
	if strings.TrimSpace(out) != "t" {
		return "the backup already holds ledger entries written with a different key, so the restore was not attempted", false
	}
	return "", true
}
