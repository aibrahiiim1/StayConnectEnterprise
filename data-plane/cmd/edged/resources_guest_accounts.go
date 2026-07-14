package main

// Guest Username/Password Accounts — a first-class guest auth method, separate
// from vouchers. An account is a username + argon2id password hash bound to a
// Guest Access Plan (ticket_templates). Passwords are write-only: no handler,
// list, export, log or audit payload ever returns a hash.

import (
	"crypto/rand"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

type edgeGuestAccount struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName *string    `json:"display_name,omitempty"`
	Notes       *string    `json:"notes,omitempty"`
	TemplateID  string     `json:"template_id"`
	Enabled     bool       `json:"enabled"`
	ValidFrom   *time.Time `json:"valid_from,omitempty"`
	ValidUntil  *time.Time `json:"valid_until,omitempty"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	LoginCount  int64      `json:"login_count"`
	LockedUntil *time.Time `json:"locked_until,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	// Derived (list/get only): plan device cap + how many distinct devices are
	// currently active on this account. Password/hash is NEVER included.
	MaxDevices    *int `json:"max_devices,omitempty"`
	ActiveDevices int  `json:"active_devices"`
}

const gaCols = `id, username, display_name, notes, template_id, enabled,
                valid_from, valid_until, last_login_at, login_count, locked_until, created_at, updated_at`

func scanGuestAccount(row interface{ Scan(...any) error }, a *edgeGuestAccount) error {
	return row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.TemplateID, &a.Enabled,
		&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.LockedUntil, &a.CreatedAt, &a.UpdatedAt)
}

// gaEnrichedCols / scanGuestAccountEnriched add the plan device cap and the live
// distinct-active-device count for list/get views.
const gaEnrichedCols = `g.id, g.username, g.display_name, g.notes, g.template_id, g.enabled,
                g.valid_from, g.valid_until, g.last_login_at, g.login_count, g.locked_until,
                g.created_at, g.updated_at,
                t.max_concurrent_devices,
                (SELECT count(DISTINCT s.mac) FROM sessions s
                  WHERE s.guest_account_id = g.id AND s.state='active')`

func scanGuestAccountEnriched(row interface{ Scan(...any) error }, a *edgeGuestAccount) error {
	var md *int
	var active int64
	if err := row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.TemplateID, &a.Enabled,
		&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.LockedUntil, &a.CreatedAt, &a.UpdatedAt,
		&md, &active); err != nil {
		return err
	}
	a.MaxDevices = md
	a.ActiveDevices = int(active)
	return nil
}

func (s *server) guestAccountsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listGuestAccounts)
	r.Post("/", s.createGuestAccount)
	// Portal visibility toggle (static segment; chi matches it before "/{id}").
	r.Get("/portal", s.getGuestAccountPortal)
	r.Post("/portal", s.setGuestAccountPortal)
	r.Get("/{id}", s.getGuestAccount)
	r.Patch("/{id}", s.patchGuestAccount)
	r.Post("/{id}/set-password", s.setGuestAccountPassword)
	r.Post("/{id}/disconnect", s.disconnectGuestAccountSessions)
	r.Delete("/{id}", s.deleteGuestAccount)
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

func (s *server) listGuestAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT `+gaEnrichedCols+`
	    FROM guest_accounts g LEFT JOIN ticket_templates t ON t.id = g.template_id
	   WHERE g.tenant_id=$1 ORDER BY g.created_at DESC, g.id DESC`, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeGuestAccount
	for rows.Next() {
		var a edgeGuestAccount
		if err := scanGuestAccountEnriched(rows, &a); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, a)
	}
	writeList(w, out)
}

func (s *server) getGuestAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var a edgeGuestAccount
	err := scanGuestAccountEnriched(s.db.QueryRow(ctx, `SELECT `+gaEnrichedCols+`
	    FROM guest_accounts g LEFT JOIN ticket_templates t ON t.id = g.template_id
	   WHERE g.id=$1 AND g.tenant_id=$2`, id, s.tenantID), &a)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	writeJSON(w, http.StatusOK, a)
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
	if in.TemplateID == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "template_id (guest access plan) required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	if !s.requireProvisioning(w, r) {
		return
	}
	var tplExists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_templates WHERE id=$1 AND tenant_id=$2 AND is_active)`, in.TemplateID, s.tenantID).Scan(&tplExists); err != nil || !tplExists {
		jsonErr(w, http.StatusBadRequest, "bad_request", "guest access plan not found or inactive")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	var createdBy any
	if sess := sessFrom(r.Context()); sess != nil {
		createdBy = sess.OperatorID
	}
	var a edgeGuestAccount
	err = scanGuestAccount(s.db.QueryRow(ctx, `
        INSERT INTO guest_accounts (tenant_id, site_id, template_id, username, password_hash,
                                    display_name, notes, enabled, valid_from, valid_until, created_by)
        VALUES ($1, NULLIF($2,'')::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11)
        RETURNING `+gaCols,
		s.tenantID, s.siteID, in.TemplateID, in.Username, hash,
		in.DisplayName, in.Notes, enabled, in.ValidFrom, in.ValidUntil, createdBy), &a)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "username already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	// Audit records the username/plan only — NEVER the password or hash.
	s.audit(r, "guest_account.created", "guest_account", a.ID, map[string]any{"username": a.Username, "template_id": a.TemplateID})
	// One-time password reveal: the server-generated password is returned exactly
	// once here so the operator can copy it. It is NOT stored in plaintext and is
	// never returned by any read/list API afterwards.
	out := map[string]any{"account": a}
	if generated {
		out["generated_password"] = in.Password
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *server) patchGuestAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Username    *string    `json:"username,omitempty"`
		DisplayName *string    `json:"display_name,omitempty"`
		Notes       *string    `json:"notes,omitempty"`
		TemplateID  *string    `json:"template_id,omitempty"`
		Enabled     *bool      `json:"enabled,omitempty"`
		ValidFrom   *time.Time `json:"valid_from,omitempty"`
		ValidUntil  *time.Time `json:"valid_until,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if in.Username != nil {
		*in.Username = strings.TrimSpace(*in.Username)
		if !validUsername(*in.Username) {
			jsonErr(w, http.StatusBadRequest, "bad_request", "username must be 1-64 chars: letters, digits, . _ - @ (no spaces)")
			return
		}
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Plan reassignment must point at an ACTIVE plan owned by THIS tenant.
	if in.TemplateID != nil && *in.TemplateID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_templates WHERE id=$1 AND tenant_id=$2 AND is_active)`, *in.TemplateID, s.tenantID).Scan(&ok); err != nil || !ok {
			jsonErr(w, http.StatusBadRequest, "bad_request", "guest access plan not found or inactive")
			return
		}
	}
	var a edgeGuestAccount
	err := scanGuestAccount(s.db.QueryRow(ctx, `
        UPDATE guest_accounts SET
            username     = COALESCE($3, username),
            display_name = COALESCE($4, display_name),
            notes        = COALESCE($5, notes),
            template_id  = COALESCE(NULLIF($6,'')::uuid, template_id),
            enabled      = COALESCE($7, enabled),
            valid_from   = COALESCE($8, valid_from),
            valid_until  = COALESCE($9, valid_until),
            updated_at   = now()
         WHERE id=$1 AND tenant_id=$2
         RETURNING `+gaCols,
		id, s.tenantID, in.Username, in.DisplayName, in.Notes, in.TemplateID, in.Enabled, in.ValidFrom, in.ValidUntil), &a)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "username already exists")
			return
		}
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	action := "guest_account.updated"
	if in.Enabled != nil {
		if *in.Enabled {
			action = "guest_account.enabled"
		} else {
			action = "guest_account.disabled"
		}
	}
	meta := map[string]any{"username": a.Username}
	if in.TemplateID != nil && *in.TemplateID != "" {
		// Account plan changes apply to FUTURE logins only; a running session
		// keeps its policy snapshot (expiry + shaping set at authorization,
		// and acctd does not re-derive an account session's plan).
		meta["new_template_id"] = *in.TemplateID
		meta["applies_to"] = "future_sessions"
	}
	s.audit(r, action, "guest_account", a.ID, meta)
	writeJSON(w, http.StatusOK, a)
}

func (s *server) setGuestAccountPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password           string `json:"password"`
		Generate           bool   `json:"generate,omitempty"`
		DisconnectSessions bool   `json:"disconnect_sessions,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
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
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Resetting the password invalidates the old one immediately and clears any
	// lockout. It does NOT by itself drop already-authorized devices — the
	// operator opts into that with disconnect_sessions.
	var username string
	err = s.db.QueryRow(ctx, `
        UPDATE guest_accounts SET password_hash=$3, failed_attempts=0, locked_until=NULL, updated_at=now()
         WHERE id=$1 AND tenant_id=$2 RETURNING username`, id, s.tenantID, hash).Scan(&username)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	disconnected := 0
	if in.DisconnectSessions {
		disconnected = s.disconnectAccountSessions(r, id)
	}
	// Audit records username + whether sessions were dropped — NEVER the password.
	s.audit(r, "guest_account.password_reset", "guest_account", id, map[string]any{
		"username": username, "disconnected_sessions": disconnected,
	})
	out := map[string]any{"status": "password_updated", "disconnected_sessions": disconnected}
	if generated {
		out["generated_password"] = in.Password
	}
	writeJSON(w, http.StatusOK, out)
}

// disconnectAccountSessions ends every active session for an account by asking
// scd (which owns nft/tc) to revoke each session IP. Returns how many were
// dropped. Errors per-IP are logged by scd; we count successful revokes.
func (s *server) disconnectAccountSessions(r *http.Request, accountID string) int {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx,
		`SELECT host(ip) FROM sessions WHERE guest_account_id=$1 AND tenant_id=$2 AND state='active'`,
		accountID, s.tenantID)
	if err != nil {
		return 0
	}
	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err == nil {
			ips = append(ips, ip)
		}
	}
	rows.Close()
	n := 0
	for _, ip := range ips {
		st, _, err := s.scd.call(r.Context(), http.MethodPost, "/v1/sessions/revoke",
			map[string]string{"ip": ip, "reason": "admin"})
		if err == nil && st == http.StatusOK {
			n++
		}
	}
	return n
}

// disconnectGuestAccountSessions is the standalone "Disconnect active sessions"
// action for an account (independent of a password reset).
func (s *server) disconnectGuestAccountSessions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var username string
	if err := s.db.QueryRow(ctx, `SELECT username FROM guest_accounts WHERE id=$1 AND tenant_id=$2`, id, s.tenantID).Scan(&username); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	n := s.disconnectAccountSessions(r, id)
	s.audit(r, "guest_account.sessions_disconnected", "guest_account", id, map[string]any{"username": username, "disconnected_sessions": n})
	writeJSON(w, http.StatusOK, map[string]any{"status": "disconnected", "disconnected_sessions": n})
}

func (s *server) deleteGuestAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var username string
	err := s.db.QueryRow(ctx, `DELETE FROM guest_accounts WHERE id=$1 AND tenant_id=$2 RETURNING username`, id, s.tenantID).Scan(&username)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	s.audit(r, "guest_account.deleted", "guest_account", id, map[string]any{"username": username})
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}
