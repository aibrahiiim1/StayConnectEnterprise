package main

// The hash is the shared hashPassword's: the IAM-v2 adapter verifies the same argon2id PHC format the legacy
// path produces, so there is no second hashing scheme.

import (
	"context"
	"net/http"
	"time"
)

// createGuestAccountIAMv2 writes only iam_v2 (not a dual write) and answers in the legacy response shape.
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
	// Audit records the username only, never the password or hash.
	s.audit(r, "guest_account.created", "guest_account", id,
		map[string]any{"username": username, "authority": "iam_v2"})

	out := map[string]any{"account": map[string]any{
		"id":           id,
		"username":     username,
		"display_name": displayName,
		"enabled":      enabled,
		"authority":    "iam_v2",
	}}
	// The generated password is revealed once here and never stored or returned again.
	if generated {
		out["generated_password"] = plaintext
	}
	writeJSON(w, http.StatusCreated, out)
}
