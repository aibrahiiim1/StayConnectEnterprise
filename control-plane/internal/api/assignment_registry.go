package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// RegistryBase builds, signs and serves the assignment trust registry: every signing key and its state, signed by
// the root key and versioned so the edge can reject rollbacks and forgeries.
type RegistryBase struct {
	*Base
	RootKey ed25519.PrivateKey
}

// Rebuild mints the next registry version. Call it after any key-state change and on boot.
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
		// A key that cannot be read is an error, never silently left out of the registry.
		if err := rows.Scan(&keyID, &pub, &state, &added); err != nil {
			return nil, err
		}
		keys = append(keys, assignment.TrustedKey{
			KeyID: keyID, PublicKey: base64.StdEncoding.EncodeToString(pub),
			State: state, AddedAt: added.UTC().Format(time.RFC3339),
		})
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	var ver int64
	if err := b.DB.QueryRow(ctx, `SELECT COALESCE(MAX(registry_version),0)+1 FROM assignment_registry`).Scan(&ver); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sr := &assignment.SignedRegistry{
		RegistryVersion: ver,
		IssuedAt:        now.Unix(),
		NotBefore:       now.Add(-5 * time.Minute).Unix(), // clock-skew tolerance
		NotAfter:        0,
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
	audit.System(ctx, b.DB, "assignment.registry_signed", "assignment_registry", strconv.FormatInt(ver, 10),
		map[string]any{"registry_version": ver, "keys": len(keys), "reason": reason})
	return sr, nil
}

// currentRegistryEnvelope is the signed registry document Central serves to appliances (GET /v1/appliance/assignment-registry) and also embeds in an
// offline activation package: one query, so the two can never disagree. pgx.ErrNoRows when no registry has been signed.
func currentRegistryEnvelope(ctx context.Context, db *pgxpool.Pool) ([]byte, error) {
	var env []byte
	if err := db.QueryRow(ctx, `SELECT signed_envelope FROM assignment_registry WHERE is_current`).Scan(&env); err != nil {
		return nil, err
	}
	return env, nil
}

func (b *RegistryBase) Serve(w http.ResponseWriter, r *http.Request) {
	if ident := auth.ApplianceFromContext(r.Context()); ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "appliance identity required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	env, err := currentRegistryEnvelope(ctx, b.DB)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(env)
}
