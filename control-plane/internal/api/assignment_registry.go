package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// RegistryBase builds, signs and serves the SIGNED assignment trust registry.
// The registry lists every assignment-signing key + its state; it is signed by
// the registry ROOT key (a manufacture-time trust anchor on the appliance) and
// versioned so the edge can reject rollbacks and forgeries.
type RegistryBase struct {
	*Base
	RootKey ed25519.PrivateKey
}

// Rebuild reads the current assignment-signing keys, mints the NEXT registry
// version, signs it with the root key and stores it as the current registry.
// Call it after any key-state change (new key, verify_only, revoke) and on boot.
func (b *RegistryBase) Rebuild(ctx context.Context, reason string) (*assignment.SignedRegistry, error) {
	if b.RootKey == nil {
		return nil, nil // registry signing disabled
	}
	rows, err := b.DB.Query(ctx,
		`SELECT key_id, public_key, state, activated_at FROM assignment_signing_keys ORDER BY activated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []assignment.TrustedKey
	for rows.Next() {
		var keyID, state string
		var pub []byte
		var added time.Time
		if rows.Scan(&keyID, &pub, &state, &added) != nil {
			continue
		}
		keys = append(keys, assignment.TrustedKey{
			KeyID: keyID, PublicKey: base64.StdEncoding.EncodeToString(pub),
			State: state, AddedAt: added.UTC().Format(time.RFC3339),
		})
	}

	var ver int64
	_ = b.DB.QueryRow(ctx, `SELECT COALESCE(MAX(registry_version),0)+1 FROM assignment_registry`).Scan(&ver)
	now := time.Now().UTC()
	sr := &assignment.SignedRegistry{
		RegistryVersion: ver,
		IssuedAt:        now.Unix(),
		NotBefore:       now.Add(-5 * time.Minute).Unix(), // clock-skew tolerance
		NotAfter:        0,                                // durable — no upper bound
		Keys:            keys,
	}
	assignment.SignRegistry(b.RootKey, sr)

	env, _ := json.Marshal(sr)
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE assignment_registry SET is_current=false WHERE is_current`); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO assignment_registry (registry_version, signed_envelope, signer_key_id, issued_at, not_before, is_current)
        VALUES ($1,$2,$3,now(), to_timestamp($4), true)`,
		ver, env, sr.SignerKeyID, sr.NotBefore); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	audit.System(ctx, b.DB, "assignment.registry_signed", "assignment_registry", itoa(ver),
		map[string]any{"registry_version": ver, "keys": len(keys), "reason": reason})
	return sr, nil
}

// Serve returns the current signed registry to an appliance (mTLS).
func (b *RegistryBase) Serve(w http.ResponseWriter, r *http.Request) {
	if ident := auth.ApplianceFromContext(r.Context()); ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "appliance identity required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	var env []byte
	if err := b.DB.QueryRow(ctx, `SELECT signed_envelope FROM assignment_registry WHERE is_current`).Scan(&env); err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(env)
}
