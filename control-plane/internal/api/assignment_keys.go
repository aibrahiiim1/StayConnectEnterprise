package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// AssignmentKeysBase manages the lifecycle of the DEDICATED assignment-signing
// key. Key separation is a security property: an assignment must never be
// signable by the license / command / update / CA / auth-callout key. The
// appliance enforces it by trusting ONLY the keys in its local assignment trust
// registry; this API is the Platform-side view + rotation control.
type AssignmentKeysBase struct {
	*Base
}

// RegisterActiveKey records the key ctrlapi is currently signing with as 'active'
// and audits first registration. Called on boot.
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

func (b *AssignmentKeysBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequirePermission("platform.appliances.manage"))
	r.Get("/", b.list)
	// Retiring a key is security-sensitive: step-up + reason + audit.
	r.With(RequireReauth(b.Redis)).Post("/{keyID}/retire", b.retire)
	return r
}

func (b *AssignmentKeysBase) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT key_id, public_key, state, activated_at, retired_at, COALESCE(reason,''), COALESCE(note,'')
          FROM assignment_signing_keys ORDER BY activated_at DESC`)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var keyID, state, reason, note string
		var pub []byte
		var activated time.Time
		var retired *time.Time
		if rows.Scan(&keyID, &pub, &state, &activated, &retired, &reason, &note) != nil {
			continue
		}
		out = append(out, map[string]any{
			"key_id": keyID, "public_key": base64.StdEncoding.EncodeToString(pub),
			"state": state, "activated_at": activated, "retired_at": retired,
			"reason": reason, "note": note,
			"purpose": "assignment", // never license/command/update/CA
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// retire marks a key retired. Appliances reject assignments signed by a retired
// key once their trust registry is updated — so retire AFTER switching signing to
// the replacement key and distributing the new registry.
func (b *AssignmentKeysBase) retire(w http.ResponseWriter, r *http.Request) {
	keyID := chi.URLParam(r, "keyID")
	var in struct {
		Reason string `json:"reason"`
	}
	_ = DecodeJSON(r, &in)
	if in.Reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required to retire a signing key")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	sess := auth.FromContext(r.Context())
	var operatorID any
	if sess != nil && sess.OperatorID != "" {
		operatorID = sess.OperatorID
	}
	tag, err := b.DB.Exec(ctx, `
        UPDATE assignment_signing_keys
           SET state='retired', retired_at=now(), retired_by=$2, reason=$3
         WHERE key_id=$1 AND state='active'`, keyID, operatorID, in.Reason)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "retire failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "no active signing key with that id")
		return
	}
	audit.Op(ctx, b.DB, r, "assignment.signing_key_retired", "assignment_key", keyID,
		map[string]any{"key_id": keyID, "reason": in.Reason})
	WriteJSON(w, http.StatusOK, map[string]any{"status": "retired", "key_id": keyID})
}
