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

// AssignmentKeysBase manages the lifecycle of the DEDICATED assignment-signing key.
//
// Three states, because "stop signing with it" and "stop trusting it" are not the
// same decision:
//
//	active      — may sign new assignments, and verifies existing ones
//	verify_only — must NOT sign; still verifies documents already issued under it
//	revoked     — rejected for ALL verification (compromise, or post-migration)
//
// Revoking a key that still signs CURRENT assignments would strand exactly those
// appliances (they could no longer verify the document they hold, and would fall
// back to awaiting-assignment on next boot). The API therefore refuses it unless an
// explicit emergency compromise override is given.
type AssignmentKeysBase struct {
	*Base
}

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

func (b *AssignmentKeysBase) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequirePermission("platform.appliances.manage"))
	r.Get("/", b.list)
	reauth := RequireReauth(b.Redis)
	r.With(reauth).Post("/{keyID}/verify-only", b.toVerifyOnly)
	r.With(reauth).Post("/{keyID}/revoke", b.toRevoked)
	return r
}

func (b *AssignmentKeysBase) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT k.key_id, k.public_key, k.state, k.activated_at, k.verify_only_at, k.revoked_at,
               COALESCE(k.reason,''), COALESCE(k.note,''), k.emergency,
               COALESCE(u.current_assignments, 0)
          FROM assignment_signing_keys k
          LEFT JOIN assignment_signer_usage u ON u.key_id = k.key_id
         ORDER BY k.activated_at DESC`)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var keyID, state, reason, note string
		var pub []byte
		var emergency bool
		var deps int64
		var activated time.Time
		var verifyOnlyAt, revokedAt *time.Time
		if rows.Scan(&keyID, &pub, &state, &activated, &verifyOnlyAt, &revokedAt,
			&reason, &note, &emergency, &deps) != nil {
			continue
		}
		out = append(out, map[string]any{
			"key_id": keyID, "public_key": base64.StdEncoding.EncodeToString(pub),
			"state": state, "purpose": "assignment",
			"can_sign": assignment.CanSign(state), "can_verify": assignment.CanVerify(state),
			"current_assignments": deps, // appliances that would be stranded by revocation
			"activated_at":        activated, "verify_only_at": verifyOnlyAt, "revoked_at": revokedAt,
			"reason": reason, "note": note, "emergency": emergency,
		})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

// toVerifyOnly stops a key signing while KEEPING it trusted for documents already
// issued under it. This is always safe — it cannot strand anyone — so it needs no
// override. It is step 8 of the rotation sequence.
func (b *AssignmentKeysBase) toVerifyOnly(w http.ResponseWriter, r *http.Request) {
	keyID := chi.URLParam(r, "keyID")
	var in struct {
		Reason string `json:"reason"`
	}
	_ = DecodeJSON(r, &in)
	if in.Reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `
        UPDATE assignment_signing_keys
           SET state='verify_only', verify_only_at=now(), reason=$2
         WHERE key_id=$1 AND state='active'`, keyID, in.Reason)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "no active signing key with that id")
		return
	}
	audit.Op(ctx, b.DB, r, "assignment.signing_key_verify_only", "assignment_key", keyID,
		map[string]any{"key_id": keyID, "reason": in.Reason})
	WriteJSON(w, http.StatusOK, map[string]any{
		"status": "verify_only", "key_id": keyID,
		"note": "key can no longer sign; appliances still holding documents signed by it can still verify and boot",
	})
}

// toRevoked removes ALL trust in a key. GUARDED: refused while any CURRENT
// assignment is still signed by it, because those appliances would be unable to
// verify the document they hold and would strand in awaiting-assignment on reboot.
//
// The guard is bypassable ONLY for a confirmed key compromise, which requires an
// explicit emergency flag + typed confirmation + reason, and is audited as such.
func (b *AssignmentKeysBase) toRevoked(w http.ResponseWriter, r *http.Request) {
	keyID := chi.URLParam(r, "keyID")
	var in struct {
		Reason       string `json:"reason"`
		Emergency    bool   `json:"emergency_compromise"`
		Confirmation string `json:"confirmation"` // must equal the key id
	}
	_ = DecodeJSON(r, &in)
	if in.Reason == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	var state string
	var deps int64
	if err := b.DB.QueryRow(ctx,
		`SELECT k.state, COALESCE(u.current_assignments,0)
           FROM assignment_signing_keys k
           LEFT JOIN assignment_signer_usage u ON u.key_id=k.key_id
          WHERE k.key_id=$1`, keyID).Scan(&state, &deps); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "unknown signing key")
		return
	}
	if state == "revoked" {
		Fail(w, r, http.StatusConflict, "already_revoked", "key is already revoked")
		return
	}

	// THE GUARD. Revoking a signer that still backs live assignments strands them.
	if deps > 0 && !in.Emergency {
		Fail(w, r, http.StatusConflict, "signer_in_use",
			"refusing to revoke: this key still signs the CURRENT assignment of "+
				itoa(deps)+" appliance(s), which would strand them in awaiting-assignment. "+
				"Re-sign them onto the new active key first (rotation step 5), or — only for a "+
				"confirmed key compromise — retry with emergency_compromise=true and "+
				"confirmation=<key_id>.")
		return
	}
	if in.Emergency && in.Confirmation != keyID {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest,
			"emergency revocation requires confirmation=<key_id>")
		return
	}

	if _, err := b.DB.Exec(ctx, `
        UPDATE assignment_signing_keys
           SET state='revoked', revoked_at=now(), reason=$2, emergency=$3
         WHERE key_id=$1`, keyID, in.Reason, in.Emergency); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "update failed")
		return
	}
	action := "assignment.signing_key_revoked"
	if in.Emergency {
		action = "assignment.signing_key_revoked_emergency"
	}
	audit.Op(ctx, b.DB, r, action, "assignment_key", keyID, map[string]any{
		"key_id": keyID, "reason": in.Reason, "emergency_compromise": in.Emergency,
		"stranded_assignments": deps,
	})
	WriteJSON(w, http.StatusOK, map[string]any{
		"status": "revoked", "key_id": keyID, "emergency_compromise": in.Emergency,
		"stranded_assignments": deps,
	})
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
