package main

// Custom Admin Console roles (migration 0206). A site administrator names a role and chooses, per feature, No access, View or Manage.
// The seven built-in roles stay compiled in rolePerms and are never changed here.
//
// No escalation: a custom role grants a feature at most at the highest level a built-in role other than Site admin grants it, so what
// only Site admin can do today stays Site admin's (switching modules, financial onboarding, PMS routing, managing staff and roles).
// The cap is applied when a role is saved and again when the roles are loaded, so an edited row cannot exceed it either. Only Site
// admin reaches the roles resource (no role holds it), and assigning a role is staff management (operators WRITE, Site admin only).
// A role's permissions take effect on the next request of everyone holding it; assigning or removing one ends that person's sessions.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

const customRolePrefix = "custom:"

// customRoleArraySQL is a staff account's custom roles as "custom:<id>" entries, appended to its built-in roles in the operator queries (alias o).
const customRoleArraySQL = `COALESCE((SELECT array_agg('custom:' || c.role_id::text ORDER BY c.role_id) FROM operator_custom_roles c WHERE c.operator_id = o.id), '{}'::text[])`

// roleID is the custom role's id, or "" for a built-in role.
func roleID(role string) string {
	id, ok := strings.CutPrefix(role, customRolePrefix)
	if !ok {
		return ""
	}
	return id
}

type roleFeature struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	Label string `json:"label"`
	Help  string `json:"help"`
	// Sensitive names why holding the feature needs care (personal data, money, secrets, the whole database); "" when it does not.
	Sensitive string `json:"sensitive,omitempty"`
}

// roleFeatures is every resource edged enforces, grouped as the Admin Console shows them.
var roleFeatures = []roleFeature{
	{"guest-accounts", "Clients and access", "Client accounts", "Create, change and disable username and email accounts.", ""},
	{"sessions", "Clients and access", "Active sessions and devices", "See who is online; disconnect, release or replace a device.", ""},
	{"package-controls", "Clients and access", "End or reset a package", "End a client's whole package or give it a new cycle. Never a payment.", ""},
	{"usage", "Clients and access", "Usage explorer", "How much data a room, account or device used.", ""},
	{"vouchers", "Clients and access", "Vouchers", "Issue, export and revoke voucher cards.", ""},
	{"voucher-codes", "Clients and access", "Read voucher codes", "Show an issued voucher code in the clear.", "Reveals sign-in codes"},
	{"voucher-code-settings", "Clients and access", "Voucher code format", "Length and characters of new voucher codes.", ""},
	{"account-password-settings", "Clients and access", "Account password rules", "Rules for passwords of staff-issued client accounts.", ""},
	{"guest-device-self-service", "Clients and access", "Device self-service", "Whether clients may manage their own devices on the portal.", ""},
	{"post-stay-profiles", "Clients and access", "Post-stay access", "Reset or revoke a guest's post-stay sign-in.", ""},
	{"stay-transfers", "Clients and access", "Cross-PMS transfer", "Move a stay's access between PMS systems.", ""},

	{"auth-methods", "Client sign-in", "Sign-in methods", "Which ways clients sign in, and room sign-in rules.", ""},
	{"client-groups", "Client sign-in", "Client groups", "Groups by email domain or identity, and what they are offered.", ""},
	{"social-providers", "Client sign-in", "Identity providers", "Google, Microsoft and other social sign-in.", "Holds application secrets"},
	{"notification-providers", "Client sign-in", "Delivery", "Email, SMS and WhatsApp senders for codes and account emails.", "Holds sender credentials"},
	{"walled-garden", "Client sign-in", "Allowed sites", "Sites reachable before signing in.", ""},
	{"guest-signin-attempts", "Client sign-in", "Sign-in attempts", "Failed and successful sign-ins and their reasons.", ""},
	{"guest-signin-credentials", "Client sign-in", "What clients typed", "The submitted and accepted sign-in values, unmasked.", "Personal data"},
	{"guest-signin-protection", "Client sign-in", "Sign-in protection policy", "Thresholds that pause a device after repeated failures.", ""},
	{"guest-signin-restrictions", "Client sign-in", "Paused devices", "End one device's sign-in pause early.", ""},
	{"code-requests", "Client sign-in", "Code requests", "Sign-in codes requested; let one device try again.", ""},
	{"code-reveal", "Client sign-in", "Read a live code", "Show a live one-time code to the person at the desk.", "Reveals sign-in codes"},

	{"pms-stays", "Hotel", "Stays", "Rooms and reservations in house.", ""},
	{"pms-stay-identity", "Hotel", "Guest names on stays", "Guest names and reservation numbers.", "Personal data"},
	{"pms-events", "Hotel", "PMS activity", "Messages received from the PMS.", ""},
	{"pms-event-decisions", "Hotel", "PMS event decisions", "Decide a PMS message that needs a person.", ""},
	{"pms-resolutions", "Hotel", "Guest sign-in checks", "How room sign-ins were verified.", ""},
	{"pms-interfaces", "Hotel", "PMS connection", "Connect, resync and configure the PMS.", ""},
	{"pms-routing", "Hotel", "PMS routing", "Which PMS serves which network.", ""},
	{"pms-source-conflicts", "Hotel", "Duplicate sources", "Rooms claimed by two PMS interfaces.", ""},
	{"pms-reconciliation", "Hotel", "Checkout reconciliation", "Checkout boundaries from the PMS.", ""},
	{"pms-roster-reconciliation", "Hotel", "Roster reconciliation", "Close the stays a complete roster omits.", ""},
	{"checkout-grace", "Hotel", "Checkout grace", "Access kept for a while after checkout.", ""},
	{"after-checkout-access", "Hotel", "After Check-out Access", "Access offered after departure.", ""},
	{"operational-alerts", "Hotel", "Alerts", "Operational warnings; acknowledge them.", ""},

	{"commercial-packages", "Packages and payments", "Internet packages and service plans", "What clients are offered, at what speed and price.", ""},
	{"payment-providers", "Packages and payments", "Payment providers", "Card payment accounts.", "Money"},
	{"financial-review", "Packages and payments", "Charge review", "Room charges that need a decision; package payments.", "Money"},
	{"financial-ops", "Packages and payments", "Charge operations", "Retry, release and resolve room charges.", "Money"},
	{"pms-financial-onboarding", "Packages and payments", "Room charge onboarding", "Approve a PMS interface for room charges.", "Money"},
	{"modules", "Packages and payments", "Modules", "Which licensed features are switched on.", ""},

	{"portal-branding", "Client Portal", "Portal settings", "Branding, terms and languages of the Client Portal.", ""},
	{"portal-assets", "Client Portal", "Portal images", "Logos and backgrounds.", ""},
	{"portal-https", "Client Portal", "Portal HTTPS", "The portal's address and certificate.", ""},

	{"network", "Appliance", "Networks", "Client networks, DHCP, WAN/LAN and access protection.", ""},
	{"diagnostics", "Appliance", "Diagnostics", "Service health and checks.", ""},
	{"license", "Appliance", "Appliance and licence", "Activation and licence status.", ""},
	{"backups", "Appliance", "Backups", "Backups, their schedule and the erasure ledger's storage.", ""},
	{"backup-download", "Appliance", "Download a backup", "Download a copy of the whole database.", "The whole database"},
	{"reports", "Appliance", "Overview and reports", "The overview page and reports.", ""},
	{"audit", "Appliance", "Activity", "Who did what, when.", ""},
	{"operators", "Appliance", "Staff accounts", "The list of staff accounts. Managing them stays with Site admin.", ""},
	{"guest-data-rights", "Appliance", "Guest data requests", "Export or erase one guest's data (GDPR).", "Personal data"},
}

// maxGrantable is the highest level any built-in role other than Site admin grants for the resource.
func maxGrantable(resource string) perm {
	best := permNone
	for role, m := range rolePerms {
		if role == "tenant_admin" || role == "tenant_operator" {
			continue
		}
		if p := m[resource]; p > best {
			best = p
		}
	}
	return best
}

type customRole struct {
	ID          string            `json:"id"`
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Permissions map[string]string `json:"permissions"`
	Members     int               `json:"members"`
	UpdatedAt   time.Time         `json:"updated_at"`
	perms       map[string]perm
}

type cachedRole struct {
	name  string
	perms map[string]perm
}

// customRoleCache maps "custom:<id>" to the role's capped permissions; permFor reads it on every request.
var customRoleCache atomic.Pointer[map[string]cachedRole]

func customRolePerm(role, resource string) perm {
	m := customRoleCache.Load()
	if m == nil {
		return permNone
	}
	return (*m)[role].perms[resource]
}

// sessionCustomRoles is what whoami tells the console about the custom roles a session holds, so it shows what they allow.
func sessionCustomRoles(roles []string) map[string]any {
	out := map[string]any{}
	m := customRoleCache.Load()
	for _, r := range roles {
		if !strings.HasPrefix(r, customRolePrefix) {
			continue
		}
		c := cachedRole{}
		if m != nil {
			c = (*m)[r]
		}
		p := map[string]string{}
		for k, v := range c.perms {
			p[k] = permName(v)
		}
		out[r] = map[string]any{"name": c.name, "permissions": p}
	}
	return out
}

func permName(p perm) string {
	switch p {
	case permWrite:
		return "write"
	case permRead:
		return "read"
	}
	return "none"
}

// cappedPerms turns stored levels into perms no higher than maxGrantable; unknown features and levels are dropped.
func cappedPerms(raw map[string]string) map[string]perm {
	out := map[string]perm{}
	for k, v := range raw {
		want := permNone
		switch v {
		case "read":
			want = permRead
		case "write":
			want = permWrite
		}
		if max := maxGrantable(k); want > max {
			want = max
		}
		if want > permNone {
			out[k] = want
		}
	}
	return out
}

func (s *server) loadCustomRoles(ctx context.Context) ([]customRole, error) {
	rows, err := s.db.Query(ctx, `
	    SELECT c.id::text, c.name, c.description, c.permissions::text, c.updated_at,
	           (SELECT count(*) FROM operator_custom_roles a WHERE a.role_id = c.id)::int
	      FROM custom_roles c
	     WHERE c.tenant_id = $1
	     ORDER BY lower(c.name)`, s.tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []customRole
	for rows.Next() {
		var r customRole
		var raw string
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &raw, &r.UpdatedAt, &r.Members); err != nil {
			return nil, err
		}
		var stored map[string]string
		_ = json.Unmarshal([]byte(raw), &stored)
		r.Key = customRolePrefix + r.ID
		r.perms = cappedPerms(stored)
		r.Permissions = map[string]string{}
		for k, p := range r.perms {
			r.Permissions[k] = permName(p)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// reloadCustomRoles refreshes the cache; on an unreadable table the previous cache stays (a role is never widened by an error).
func (s *server) reloadCustomRoles(ctx context.Context) error {
	if s.db == nil || s.tenantID == "" {
		return nil
	}
	roles, err := s.loadCustomRoles(ctx)
	if err != nil {
		if isUndefinedObject(err) {
			empty := map[string]cachedRole{}
			customRoleCache.Store(&empty)
			return nil
		}
		return err
	}
	m := make(map[string]cachedRole, len(roles))
	for _, r := range roles {
		m[r.Key] = cachedRole{name: r.Name, perms: r.perms}
	}
	customRoleCache.Store(&m)
	return nil
}

// keepCustomRolesFresh loads the roles at start and every minute, so a database edit cannot leave the cache behind for long.
func (s *server) keepCustomRolesFresh(ctx context.Context) {
	for {
		lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := s.reloadCustomRoles(lctx); err != nil {
			slog.Warn("custom roles could not be loaded; the last loaded set stays in force", "err", err)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Minute):
		}
	}
}

func (s *server) customRoleExists(role string) bool {
	m := customRoleCache.Load()
	if m == nil {
		return false
	}
	_, ok := (*m)[role]
	return ok
}

func (s *server) rolesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listRoles)
	r.Post("/", s.createCustomRole)
	r.Patch("/{id}", s.patchCustomRole)
	r.Delete("/{id}", s.deleteCustomRole)
	return r
}

type builtInRole struct {
	Key         string            `json:"key"`
	Permissions map[string]string `json:"permissions"`
	All         bool              `json:"all,omitempty"`
}

func (s *server) listRoles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	custom, err := s.loadCustomRoles(ctx)
	if err != nil && !isUndefinedObject(err) {
		jsonErr(w, http.StatusInternalServerError, "internal", "the roles could not be read")
		return
	}
	builtIn := []builtInRole{{Key: "site_admin", All: true, Permissions: map[string]string{}}}
	keys := make([]string, 0, len(rolePerms))
	for k := range rolePerms {
		if k != "tenant_admin" && k != "tenant_operator" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := builtInRole{Key: k, Permissions: map[string]string{}}
		for res, p := range rolePerms[k] {
			b.Permissions[res] = permName(p)
		}
		builtIn = append(builtIn, b)
	}
	maxima := map[string]string{}
	for _, f := range roleFeatures {
		maxima[f.Key] = permName(maxGrantable(f.Key))
	}
	if custom == nil {
		custom = []customRole{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"features": roleFeatures, "max": maxima, "built_in": builtIn, "custom": custom})
}

type customRoleReq struct {
	Name        *string           `json:"name"`
	Description *string           `json:"description"`
	Permissions map[string]string `json:"permissions"`
	Password    string            `json:"password"`
}

var builtInRoleNames = []string{"site admin", "site it manager", "client services operator", "client relations operator",
	"voucher operator", "payments operator", "site viewer"}

// validateRole returns the stored permissions or the first problem, in words the console shows as they are.
func validateRole(name, description string, raw map[string]string) (map[string]string, string) {
	n := strings.TrimSpace(name)
	if l := len([]rune(n)); l < 2 || l > 60 {
		return nil, "A role name of 2 to 60 characters is required."
	}
	low := strings.ToLower(n)
	for _, b := range builtInRoleNames {
		if low == b || strings.ReplaceAll(low, " ", "_") == strings.ReplaceAll(b, " ", "_") {
			return nil, "That name belongs to a built-in role. Choose another."
		}
	}
	if len([]rune(description)) > 300 {
		return nil, "The description can be at most 300 characters."
	}
	known := map[string]bool{}
	for _, f := range roleFeatures {
		known[f.Key] = true
	}
	out := map[string]string{}
	for k, v := range raw {
		if !known[k] {
			return nil, "Unknown feature: " + k
		}
		var want perm
		switch v {
		case "none", "":
			continue
		case "read":
			want = permRead
		case "write":
			want = permWrite
		default:
			return nil, "A feature's level is none, read or write."
		}
		if want > maxGrantable(k) {
			return nil, "A custom role cannot manage " + featureLabel(k) + ": only Site admin can."
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil, "Give the role at least one feature."
	}
	return out, ""
}

func featureLabel(key string) string {
	for _, f := range roleFeatures {
		if f.Key == key {
			return f.Label
		}
	}
	return key
}

func (s *server) roleWriteActor(w http.ResponseWriter, r *http.Request, password string) (string, bool) {
	if s.tenantID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet")
		return "", false
	}
	if !s.reauthStrict(r, password) {
		s.reauthRefused(w, r, "your password is required to change a role")
		return "", false
	}
	sess := sessFrom(r.Context())
	if sess == nil || !uuidRe.MatchString(sess.OperatorID) {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return "", false
	}
	return sess.OperatorID, true
}

func (s *server) createCustomRole(w http.ResponseWriter, r *http.Request) {
	var in customRoleReq
	if err := decodeJSON(r, &in); err != nil || in.Name == nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a name and permissions are required")
		return
	}
	desc := ""
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	perms, problem := validateRole(*in.Name, desc, in.Permissions)
	if problem != "" {
		jsonErr(w, http.StatusBadRequest, "invalid_role", problem)
		return
	}
	actor, ok := s.roleWriteActor(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	raw, _ := json.Marshal(perms)
	var id string
	err := s.db.QueryRow(ctx, `INSERT INTO custom_roles (tenant_id, name, description, permissions, created_by, updated_by)
	     VALUES ($1, $2, $3, $4::jsonb, $5::uuid, $5::uuid) RETURNING id::text`,
		s.tenantID, strings.TrimSpace(*in.Name), desc, string(raw), actor).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "A role with that name already exists.")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the role could not be saved")
		return
	}
	_ = s.reloadCustomRoles(ctx)
	s.audit(r, "role.created", "custom_role", id, map[string]any{"name": strings.TrimSpace(*in.Name), "permissions": perms})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "key": customRolePrefix + id})
}

func (s *server) patchCustomRole(w http.ResponseWriter, r *http.Request) {
	id, ok := canonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "role not found")
		return
	}
	var in customRoleReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var name, desc, rawCur string
	if err := s.db.QueryRow(ctx, `SELECT name, description, permissions::text FROM custom_roles WHERE id = $1::uuid AND tenant_id = $2`,
		id, s.tenantID).Scan(&name, &desc, &rawCur); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "role not found")
		return
	}
	var before map[string]string
	_ = json.Unmarshal([]byte(rawCur), &before)
	if in.Name != nil {
		name = *in.Name
	}
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	want := before
	if in.Permissions != nil {
		want = in.Permissions
	}
	perms, problem := validateRole(name, desc, want)
	if problem != "" {
		jsonErr(w, http.StatusBadRequest, "invalid_role", problem)
		return
	}
	actor, ok := s.roleWriteActor(w, r, in.Password)
	if !ok {
		return
	}
	raw, _ := json.Marshal(perms)
	if _, err := s.db.Exec(ctx, `UPDATE custom_roles SET name=$3, description=$4, permissions=$5::jsonb, updated_by=$6::uuid, updated_at=now()
	     WHERE id = $1::uuid AND tenant_id = $2`, id, s.tenantID, strings.TrimSpace(name), desc, string(raw), actor); err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "A role with that name already exists.")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the role could not be saved")
		return
	}
	_ = s.reloadCustomRoles(ctx)
	s.audit(r, "role.updated", "custom_role", id, map[string]any{"name": strings.TrimSpace(name), "before": before, "after": perms})
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *server) deleteCustomRole(w http.ResponseWriter, r *http.Request) {
	id, ok := canonicalUUID(chi.URLParam(r, "id"))
	if !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "role not found")
		return
	}
	var in struct {
		Password string `json:"password"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil && !errors.Is(err, io.EOF) {
			jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
			return
		}
	}
	if _, ok := s.roleWriteActor(w, r, in.Password); !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the role could not be removed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var name string
	var members int
	if err := tx.QueryRow(ctx, `SELECT c.name, (SELECT count(*) FROM operator_custom_roles a WHERE a.role_id = c.id)::int
	     FROM custom_roles c WHERE c.id = $1::uuid AND c.tenant_id = $2 FOR UPDATE`, id, s.tenantID).Scan(&name, &members); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonErr(w, http.StatusNotFound, "not_found", "role not found")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the role could not be removed")
		return
	}
	if members > 0 {
		jsonErr(w, http.StatusConflict, "role_in_use", "This role is held by "+itoa(members)+" staff account(s). Remove it from them first.")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM custom_roles WHERE id = $1::uuid AND tenant_id = $2`, id, s.tenantID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the role could not be removed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the role could not be removed")
		return
	}
	_ = s.reloadCustomRoles(ctx)
	s.audit(r, "role.deleted", "custom_role", id, map[string]any{"name": name})
	w.WriteHeader(http.StatusNoContent)
}

// customRoleNames maps the custom roles in the list to their names, for showing a staff account's roles.
func customRoleNames(ctx context.Context, s *server) map[string]string {
	roles, err := s.loadCustomRoles(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(roles))
	for _, r := range roles {
		out[r.Key] = r.Name
	}
	return out
}

func roleNamesFor(roles []string, names map[string]string) map[string]string {
	var out map[string]string
	for _, r := range roles {
		if n, ok := names[r]; ok {
			if out == nil {
				out = map[string]string{}
			}
			out[r] = n
		}
	}
	return out
}
