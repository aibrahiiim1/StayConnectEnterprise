package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
)

// The lifecycle of the DEDICATED assignment-signing key.
//
// Three states, because "stop signing with it" and "stop trusting it" are not the same decision:
//
//	active      — may sign new assignments, and verifies existing ones
//	verify_only — must NOT sign; still verifies documents already issued under it
//	revoked     — rejected for ALL verification (compromise, or post-migration)
//
// Key rotation is a rare, deliberate host operation, so it is a ctrlapi subcommand
// (`ctrlapi assignment-key verify-only|revoke`), not a console button. The keys are shown read-only under
// GET /cloud/v1/trust.

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

func resignRegistry(ctx context.Context, b *Base, regRoot ed25519.PrivateKey, reason string) {
	if regRoot == nil {
		return
	}
	rb := &RegistryBase{Base: b, RootKey: regRoot}
	_, _ = rb.Rebuild(ctx, reason)
}

// KeyToVerifyOnly stops a key signing while KEEPING it trusted for documents already issued under it. Always
// safe — it cannot strand anyone.
func KeyToVerifyOnly(ctx context.Context, b *Base, regRoot ed25519.PrivateKey, keyID, reason string) error {
	if reason == "" {
		return errors.New("a reason is required")
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
	resignRegistry(ctx, b, regRoot, "key "+keyID+" -> verify_only")
	audit.System(ctx, b.DB, "assignment.signing_key_verify_only", "assignment_key", keyID,
		map[string]any{"key_id": keyID, "reason": reason})
	return nil
}

// KeyRevoke removes ALL trust in a key. GUARDED: refused while any CURRENT assignment is still signed by it
// (those appliances could no longer verify the document they hold), unless emergency is set for a
// confirmed key compromise.
func KeyRevoke(ctx context.Context, b *Base, regRoot ed25519.PrivateKey, keyID, reason string, emergency bool) (int64, error) {
	if reason == "" {
		return 0, errors.New("a reason is required")
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
	resignRegistry(ctx, b, regRoot, "key "+keyID+" -> revoked")
	audit.System(ctx, b.DB, action, "assignment_key", keyID, map[string]any{
		"key_id": keyID, "reason": reason, "emergency_compromise": emergency, "stranded_assignments": deps})
	return deps, nil
}
