package main

// Guest account operator surface. IAM-v2 is the authority and the only implementation.
//
// The superseded half of this file is REMOVED: the public.guest_accounts CRUD, its argon2 password writes,
// its bind-to-an-access-plan validation against public.ticket_templates, and its live-device count taken
// from public.sessions. With one implementation left there is nothing to dispatch between, so the authority
// switch that used to wrap every route is gone too -- a switch with one destination is a claim that a second
// one might come back.
//
// What survives is credential VALIDATION (username and password rules, and the generated password), which is
// policy rather than storage and is shared by the IAM-v2 create path, plus the portal visibility toggle,
// which is tenant configuration rather than account storage.

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *server) guestAccountsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listGuestAccountsIAMv2)
	r.Post("/", s.createGuestAccount)
	// Portal visibility toggle (static segment; chi matches it before "/{id}").
	r.Get("/portal", s.getGuestAccountPortal)
	r.Post("/portal", s.setGuestAccountPortal)
	r.Get("/{id}", s.getGuestAccountIAMv2)
	r.Patch("/{id}", s.patchGuestAccountIAMv2)
	r.Post("/{id}/set-password", s.setGuestAccountPasswordIAMv2)
	r.Post("/{id}/disconnect", s.disconnectGuestAccountSessionsIAMv2)
	r.Delete("/{id}", s.deleteGuestAccountIAMv2)
	return r
}

// getGuestAccountPortal reports whether the Username & Password tab is shown on
// the captive portal (tenants.auth_methods -> guest_account.enabled).
func (s *server) getGuestAccountPortal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var enabled *bool
	_ = s.db.QueryRow(ctx,
		`SELECT (auth_methods #>> '{guest_account,enabled}')::boolean FROM tenants WHERE id=$1`, s.tenantID).Scan(&enabled)
	writeJSON(w, http.StatusOK, map[string]any{"enabled": enabled != nil && *enabled})
}

// setGuestAccountPortal enables/disables the portal tab.
func (s *server) setGuestAccountPortal(w http.ResponseWriter, r *http.Request) {
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
	s.audit(r, "guest_account.portal_toggled", "auth_methods", "guest_account", map[string]any{"enabled": in.Enabled})
	writeJSON(w, http.StatusOK, map[string]any{"enabled": in.Enabled})
}

// validUsername: 1..64 chars, letters/digits/._-@ and no spaces or control
// chars. One letter ("A") or one digit ("1") is allowed. Usernames are
// case-INSENSITIVE — uniqueness is enforced by a lower(username) DB index and
// login folds case — while passwords stay case-sensitive. Callers trim
// leading/trailing whitespace before validating.
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
	return true
}

// validPassword: 1..128 chars. Very short passwords are allowed (these are
// hotel-managed temporary guest credentials); the UI shows a non-blocking
// weak-password warning. We reject only ASCII control characters, which cannot
// be entered reliably through the captive portal — nothing else is stripped.
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

// generatePassword returns a readable, reasonably strong random password for the
// optional server-side "Generate" action. It is returned to the operator ONCE
// in the create/reset response and never stored in plaintext.
const genPasswordAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

func generatePassword() (string, error) {
	const n = 14
	b := make([]byte, n)
	max := big.NewInt(int64(len(genPasswordAlphabet)))
	for i := range b {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = genPasswordAlphabet[idx.Int64()]
	}
	return string(b), nil
}

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
	// Password: operator-typed, or server-generated when generate=true. The
	// plaintext is returned ONCE below and never stored.
	generated := false
	if in.Generate && in.Password == "" {
		pw, err := generatePassword()
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "generate failed")
			return
		}
		in.Password = pw
		generated = true
	}
	if ok, msg := validPassword(in.Password); !ok {
		jsonErr(w, http.StatusBadRequest, "bad_request", msg)
		return
	}
	// NO ACCESS-PLAN BINDING.
	//
	// A superseded guest account carried a ticket_template ("guest access plan") that decided what it may
	// do. IAM-v2 does not work that way: what a subject may acquire is decided by PACKAGE ELIGIBILITY RULES
	// (AUTH_METHOD, SUBJECT_KIND, DATE_WINDOW, SITE_NETWORK, ...) evaluated when packages are listed, so the
	// package declares who is eligible rather than the credential declaring what it gets. iam_v2 has no
	// template column at all -- it has an OPTIONAL assigned_package_id for direct assignment.
	//
	// The template_id field is still accepted on the wire and ignored, so an older client posting it gets a
	// created account rather than a 400. It is not stored, because there is nowhere left to store it.
	ctx, cancel := dbCtx(r)
	defer cancel()
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
