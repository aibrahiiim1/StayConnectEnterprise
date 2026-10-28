package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
)

// Assignment-signing key states ("stop signing with it" and "stop trusting it" are different decisions):
//
//	active      — may sign new assignments, and verifies existing ones
//	verify_only — must NOT sign; still verifies documents already issued under it
//	revoked     — rejected for ALL verification (compromise, or post-migration)
//
// Rotation is a ctrlapi subcommand (`ctrlapi assignment-key verify-only|revoke`), not a console button.

// RegisterActiveKey records the key ctrlapi is signing with and audits first use.
func RegisterActiveKey(ctx context.Context, b *Base, pub ed25519.PublicKey, note string) error {
	keyID := assignment.KeyID(pub)
	tag, err := b.DB.Exec(ctx, `
        INSERT INTO assignment_signing_keys (key_id, public_key, state, note)
        VALUES ($1,$2,'active',$3)
        ON CONFLICT (key_id) DO NOTHING`, keyID, []byte(pub), note)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		audit.System(ctx, b.DB, "assignment.signing_key_registered", "assignment_key", keyID,
			map[string]any{"key_id": keyID, "state": "active", "note": note})
	}
	return nil
}

// SigningKeyState returns the recorded state of the key ctrlapi signs with.
func SigningKeyState(ctx context.Context, b *Base, keyID string) (string, error) {
	var state string
	err := b.DB.QueryRow(ctx, `SELECT state FROM assignment_signing_keys WHERE key_id=$1`, keyID).Scan(&state)
	return state, err
}

// errNoRegistryRoot refuses a key-state change that could not be published to appliances (audit Phase 1 M3).
var errNoRegistryRoot = errors.New("the assignment registry root key is not loaded (CTRLAPI_REGISTRY_ROOT_KEY): " +
	"a key-state change could not be re-signed into the trust registry, so nothing was changed")

// resignRegistry publishes the key states; its failure is returned, never swallowed (audit Phase 1 M3).
func resignRegistry(ctx context.Context, b *Base, regRoot ed25519.PrivateKey, reason string) error {
	if regRoot == nil {
		return errNoRegistryRoot
	}
	rb := &RegistryBase{Base: b, RootKey: regRoot}
	if _, err := rb.Rebuild(ctx, reason); err != nil {
		return fmt.Errorf("the key state is changed but the trust registry was NOT re-signed (appliances keep the "+
			"previous registry until ctrlapi re-signs it; restart ctrlapi or repeat the command): %w", err)
	}
	return nil
}

// KeyToVerifyOnly stops a key signing while keeping it trusted for documents already issued. Always safe.
func KeyToVerifyOnly(ctx context.Context, b *Base, regRoot ed25519.PrivateKey, keyID, reason string) error {
	if reason == "" {
		return errors.New("a reason is required")
	}
	if regRoot == nil {
		return errNoRegistryRoot
	}
	tag, err := b.DB.Exec(ctx, `
        UPDATE assignment_signing_keys SET state='verify_only', verify_only_at=now(), reason=$2
         WHERE key_id=$1 AND state='active'`, keyID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("no active signing key with that id")
	}
	rerr := resignRegistry(ctx, b, regRoot, "key "+keyID+" -> verify_only")
	audit.System(ctx, b.DB, "assignment.signing_key_verify_only", "assignment_key", keyID,
		map[string]any{"key_id": keyID, "reason": reason, "registry_resigned": rerr == nil})
	return rerr
}

// KeyRevoke removes all trust in a key. Refused while a current assignment is still signed by it (those appliances
// could no longer verify what they hold), unless emergency is set for a confirmed compromise.
func KeyRevoke(ctx context.Context, b *Base, regRoot ed25519.PrivateKey, keyID, reason string, emergency bool) (int64, error) {
	if reason == "" {
		return 0, errors.New("a reason is required")
	}
	if regRoot == nil {
		return 0, errNoRegistryRoot
	}
	var state string
	var deps int64
	if err := b.DB.QueryRow(ctx,
		`SELECT k.state, COALESCE(u.current_assignments,0)
           FROM assignment_signing_keys k
           LEFT JOIN assignment_signer_usage u ON u.key_id=k.key_id
          WHERE k.key_id=$1`, keyID).Scan(&state, &deps); err != nil {
		return 0, errors.New("unknown signing key")
	}
	if state == "revoked" {
		return deps, errors.New("key is already revoked")
	}
	if deps > 0 && !emergency {
		return deps, fmt.Errorf("refusing to revoke: this key still signs the CURRENT assignment of %d appliance(s), "+
			"which would strand them. Re-sign them onto the new active key first, or — only for a confirmed key "+
			"compromise — pass --emergency", deps)
	}
	if _, err := b.DB.Exec(ctx, `
        UPDATE assignment_signing_keys SET state='revoked', revoked_at=now(), reason=$2, emergency=$3
         WHERE key_id=$1`, keyID, reason, emergency); err != nil {
		return deps, err
	}
	action := "assignment.signing_key_revoked"
	if emergency {
		action = "assignment.signing_key_revoked_emergency"
	}
	rerr := resignRegistry(ctx, b, regRoot, "key "+keyID+" -> revoked")
	audit.System(ctx, b.DB, action, "assignment_key", keyID, map[string]any{
		"key_id": keyID, "reason": reason, "emergency_compromise": emergency, "stranded_assignments": deps,
		"registry_resigned": rerr == nil})
	return deps, rerr
}
