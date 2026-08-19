package main

// IAM-v2 GUEST-ACCOUNT ISSUANCE.
//
// The runtime had an IAM-v2 authentication domain and no IAM-v2 issuance path at all. Every admin route
// wrote legacy credentials, so an appliance with STAYCONNECT_IAMV2_ACCOUNT=true had ACCOUNT authentication
// pointed at iam_v2.guest_access_accounts while that table sat at 0 rows -- authentication that could not
// possibly succeed, for want of anything to authenticate against. The only INSERTs into that table in the
// whole repository were in test fixtures.
//
// The password hash is deliberately the one computed by the shared hashPassword: the accepted IAM-v2
// adapter verifies the SAME argon2id PHC format the legacy path produces, so issuance needs no second
// hashing scheme and a credential means the same thing on both sides of the transition.

import (
	"context"
	"net/http"
	"time"
)

// createGuestAccountIAMv2 writes the credential into iam_v2.guest_access_accounts and answers with the
// same response shape as the legacy path, so the Hotel Admin UI is unaffected by which domain issued it.
//
// It writes ONLY iam_v2. See the switch in createGuestAccount for why this is not a dual write.
func (s *server) createGuestAccountIAMv2(w http.ResponseWriter, r *http.Request, ctx context.Context,
	username, hash string, displayName, notes *string, enabled bool,
	validFrom, validUntil *time.Time, plaintext string, generated bool) {

	var id string
	err := s.db.QueryRow(ctx, `
        INSERT INTO iam_v2.guest_access_accounts
               (tenant_id, site_id, username, password_hash, display_name, notes,
                enabled, valid_from, valid_until)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        RETURNING id::text`,
		s.tenantID, s.siteID, username, hash, displayName, notes,
		enabled, validFrom, validUntil).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "username already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	// Audit records the username only -- NEVER the password or the hash, exactly as the legacy path does.
	s.audit(r, "guest_account.created", "guest_account", id,
		map[string]any{"username": username, "authority": "iam_v2"})

	out := map[string]any{"account": map[string]any{
		"id":           id,
		"username":     username,
		"display_name": displayName,
		"enabled":      enabled,
		// The authority is stated in the response so an operator (or a test) can tell which domain owns
		// this credential without inferring it from a flag -- the inference that hid the original defect.
		"authority": "iam_v2",
	}}
	// One-time password reveal, same contract as the legacy path: returned exactly once, never stored in
	// plaintext, never returned by any read/list API afterwards.
	if generated {
		out["generated_password"] = plaintext
	}
	writeJSON(w, http.StatusCreated, out)
}
