package main

// Guest Username/Password Accounts — a first-class guest auth method, separate
// from vouchers. An account is a username + argon2id password hash bound to a
// Guest Access Plan (ticket_templates). Passwords are write-only: no handler,
// list, export, log or audit payload ever returns a hash.

import (
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
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

const gaCols = `id, username, display_name, notes, template_id, enabled,
                valid_from, valid_until, last_login_at, login_count, created_at, updated_at`

func scanGuestAccount(row interface{ Scan(...any) error }, a *edgeGuestAccount) error {
	return row.Scan(&a.ID, &a.Username, &a.DisplayName, &a.Notes, &a.TemplateID, &a.Enabled,
		&a.ValidFrom, &a.ValidUntil, &a.LastLoginAt, &a.LoginCount, &a.CreatedAt, &a.UpdatedAt)
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

// validUsername: 3..64 chars, letters/digits/._- and no spaces. Case-insensitive
// uniqueness is enforced by the DB index.
func validUsername(u string) bool {
	if len(u) < 3 || len(u) > 64 {
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

func (s *server) listGuestAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT `+gaCols+` FROM guest_accounts WHERE tenant_id=$1 ORDER BY created_at DESC, id DESC`, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []edgeGuestAccount
	for rows.Next() {
		var a edgeGuestAccount
		if err := scanGuestAccount(rows, &a); err != nil {
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
	err := scanGuestAccount(s.db.QueryRow(ctx, `SELECT `+gaCols+` FROM guest_accounts WHERE id=$1 AND tenant_id=$2`, id, s.tenantID), &a)
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
		jsonErr(w, http.StatusBadRequest, "bad_request", "username must be 3-64 chars: letters, digits, . _ - @")
		return
	}
	if len(in.Password) < 6 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "password must be at least 6 characters")
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
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_templates WHERE id=$1 AND tenant_id=$2)`, in.TemplateID, s.tenantID).Scan(&tplExists); err != nil || !tplExists {
		jsonErr(w, http.StatusBadRequest, "bad_request", "guest access plan not found")
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
	s.audit(r, "guest_account.created", "guest_account", a.ID, map[string]any{"username": a.Username, "template_id": a.TemplateID})
	writeJSON(w, http.StatusCreated, a)
}

func (s *server) patchGuestAccount(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
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
	ctx, cancel := dbCtx(r)
	defer cancel()
	if in.TemplateID != nil && *in.TemplateID != "" {
		var ok bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ticket_templates WHERE id=$1 AND tenant_id=$2)`, *in.TemplateID, s.tenantID).Scan(&ok); err != nil || !ok {
			jsonErr(w, http.StatusBadRequest, "bad_request", "guest access plan not found")
			return
		}
	}
	var a edgeGuestAccount
	err := scanGuestAccount(s.db.QueryRow(ctx, `
        UPDATE guest_accounts SET
            display_name = COALESCE($3, display_name),
            notes        = COALESCE($4, notes),
            template_id  = COALESCE(NULLIF($5,'')::uuid, template_id),
            enabled      = COALESCE($6, enabled),
            valid_from   = COALESCE($7, valid_from),
            valid_until  = COALESCE($8, valid_until),
            updated_at   = now()
         WHERE id=$1 AND tenant_id=$2
         RETURNING `+gaCols,
		id, s.tenantID, in.DisplayName, in.Notes, in.TemplateID, in.Enabled, in.ValidFrom, in.ValidUntil), &a)
	if err != nil {
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
	s.audit(r, action, "guest_account", a.ID, map[string]any{"username": a.Username})
	writeJSON(w, http.StatusOK, a)
}

func (s *server) setGuestAccountPassword(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if len(in.Password) < 6 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "password must be at least 6 characters")
		return
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "hash failed")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Resetting the password also clears any lockout and invalidates the old one.
	var username string
	err = s.db.QueryRow(ctx, `
        UPDATE guest_accounts SET password_hash=$3, failed_attempts=0, locked_until=NULL, updated_at=now()
         WHERE id=$1 AND tenant_id=$2 RETURNING username`, id, s.tenantID, hash).Scan(&username)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "account not found")
		return
	}
	s.audit(r, "guest_account.password_reset", "guest_account", id, map[string]any{"username": username})
	writeJSON(w, http.StatusOK, map[string]any{"status": "password_updated"})
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
