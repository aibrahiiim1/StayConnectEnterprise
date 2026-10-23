package main

// Guest account operator surface: credential validation and the portal visibility toggle; IAM-v2 owns storage.

import (
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

func (s *server) guestAccountsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listGuestAccountsIAMv2)
	r.Post("/", s.createGuestAccount)
	r.Get("/portal", s.getGuestAccountPortal)
	r.Post("/portal", s.setGuestAccountPortal)
	r.Get("/{id}", s.getGuestAccountIAMv2)
	r.Patch("/{id}", s.patchGuestAccountIAMv2)
	r.Post("/{id}/set-password", s.setGuestAccountPasswordIAMv2)
	r.Post("/{id}/disconnect", s.disconnectGuestAccountSessionsIAMv2)
	r.Post("/{id}/resend-verification", s.resendGuestAccountVerification)
	r.Get("/{id}/manage", s.getGuestAccountManage)
	r.Post("/{id}/verify-email", s.markGuestAccountEmailVerified)
	r.Post("/{id}/restart-verification", s.restartGuestAccountVerification)
	r.Delete("/{id}", s.deleteGuestAccountIAMv2)
	return r
}

func (s *server) getGuestAccountPortal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var enabled *bool
	// A failed read is reported, not answered as "hidden", so the switch never shows a state the appliance did not report.
	if err := s.db.QueryRow(ctx,
		`SELECT (auth_methods #>> '{guest_account,enabled}')::boolean FROM tenants WHERE id=$1`, s.tenantID).Scan(&enabled); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the sign-in configuration could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled != nil && *enabled,
		"can_change": permFor(sessRoles(r), "auth-methods", permWrite)})
}

// A sign-in method switch, so it is governed like one: the route sits under guest-accounts (desk and voucher
// station hold WRITE there), but which sign-in ways exist is the auth-methods decision, so auth-methods WRITE is
// required too and the change is audited as auth_methods.updated.
func (s *server) setGuestAccountPortal(w http.ResponseWriter, r *http.Request) {
	if !permFor(sessRoles(r), "auth-methods", permWrite) {
		jsonErr(w, http.StatusForbidden, "forbidden",
			"role lacks access to auth-methods: whether the portal offers client accounts is set on Sign-in methods")
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	if _, err := s.db.Exec(ctx, `
        UPDATE tenants SET auth_methods = jsonb_set(COALESCE(auth_methods,'{}'::jsonb),
            '{guest_account}', jsonb_build_object('enabled', $2::boolean), true)
         WHERE id=$1`, s.tenantID, in.Enabled); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	s.audit(r, "auth_methods.updated", "tenant", s.tenantID,
		map[string]any{"methods_changed": []string{"guest_account"}, "enabled": in.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"enabled": in.Enabled})
}

func sessRoles(r *http.Request) []string {
	if sess := sessFrom(r.Context()); sess != nil {
		return sess.Roles
	}
	return nil
}

// Usernames are case-insensitive (lower(username) unique index; login folds case); passwords stay case-sensitive.
// Callers trim whitespace first.
func validUsername(u string) bool {
	if len(u) < 1 || len(u) > 64 {
		return false
	}
	for _, r := range u {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') &&
			r != '.' && r != '_' && r != '-' && r != '@' {
			return false
		}
	}
	// The removal marker (guest_accounts_manage.go) is reserved: a live account may not look removed.
	return !isRemovedUsername(u)
}

// Short passwords are allowed (hotel-managed temporary credentials); only ASCII control characters are rejected
// because the captive portal cannot enter them reliably.
func validPassword(p string) (bool, string) {
	if len(p) < 1 {
		return false, "password must be at least 1 character"
	}
	if len(p) > 128 {
		return false, "password must be at most 128 characters"
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return false, "password contains an unsupported control character"
		}
	}
	return true, ""
}

// The generated password is returned to the operator once and never stored in plaintext.

func (s *server) createGuestAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username    string     `json:"username"`
		Password    string     `json:"password"`
		Generate    bool       `json:"generate,omitempty"`
		DisplayName *string    `json:"display_name,omitempty"`
		Notes       *string    `json:"notes,omitempty"`
		TemplateID  string     `json:"template_id"`
		Enabled     *bool      `json:"enabled,omitempty"`
		ValidFrom   *time.Time `json:"valid_from,omitempty"`
		ValidUntil  *time.Time `json:"valid_until,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if !validUsername(in.Username) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "username must be 1-64 chars: letters, digits, . _ - @ (no spaces)")
		return
	}
	if msg := accountValidityError(in.ValidFrom, in.ValidUntil); msg != "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	if in.DisplayName != nil && utf8.RuneCountInString(strings.TrimSpace(*in.DisplayName)) > accountDisplayNameMax {
		jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("the name is limited to %d characters", accountDisplayNameMax))
		return
	}
	if in.Notes != nil && utf8.RuneCountInString(strings.TrimSpace(*in.Notes)) > accountNotesMax {
		jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("notes are limited to %d characters", accountNotesMax))
		return
	}
	// Operator-typed or server-generated; the plaintext is returned once and never stored.
	ctx, cancel := dbCtx(r)
	defer cancel()
	generated := false
	if in.Generate && in.Password == "" {
		pw, ok := s.generateAccountPassword(w, ctx)
		if !ok {
			return
		}
		in.Password = pw
		generated = true
	}
	if ok, msg := validPassword(in.Password); !ok {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	// No access-plan binding: eligibility is decided by package rules, not by the credential. template_id is still
	// accepted on the wire and ignored so older clients get an account rather than a 400.
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	s.createGuestAccountIAMv2(w, r, ctx, in.Username, hash, in.DisplayName, in.Notes, enabled,
		in.ValidFrom, in.ValidUntil, in.Password, generated)
}
