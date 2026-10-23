package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

// pmsSignInModes are the verification values a site may accept with the room number, mapped to portal/resolver wire values.
// "either" is deliberately absent: it still works from stored config but made the portal guess the field from the input's shape.
// "room_any" is the supported alternative: the server compares the value against all three PMS fields, failing closed when it matches more than one Stay.
var pmsSignInModes = map[string]bool{
	"room_lastname":    true,
	"room_firstname":   true,
	"room_reservation": true,
	"room_any":         true,
}

// authMethodsRoutes reads and writes the site's guest sign-in configuration. The write is a per-key merge:
// the screen owns only the methods it renders, so other keys (providers, future methods) must survive.
func (s *server) authMethodsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		var raw []byte
		if err := s.db.QueryRow(ctx,
			`SELECT auth_methods FROM tenants WHERE id = $1`, s.tenantID).Scan(&raw); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods load failed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	})
	r.Put("/", func(w http.ResponseWriter, req *http.Request) {
		var patch map[string]json.RawMessage
		if err := json.NewDecoder(req.Body).Decode(&patch); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "body must be a JSON object")
			return
		}
		if err := validateAuthMethodsPatch(patch); err != nil {
			jsonErr(w, http.StatusBadRequest, "validation", err.Error())
			return
		}
		// A method of an unlicensed module cannot be switched on, in the API as well as the screen; switching off is always allowed.
		if code, msg := s.authMethodModuleRefusal(req.Context(), patch); code != 0 {
			jsonErr(w, code, "module_not_licensed", msg)
			return
		}
		ctx, cancel := dbCtx(req)
		defer cancel()

		// Read-modify-write in one transaction so concurrent saves of different methods do not lose one.
		tx, err := s.db.Begin(ctx)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods update failed")
			return
		}
		defer func() { _ = tx.Rollback(ctx) }()

		var currentRaw []byte
		if err := tx.QueryRow(ctx,
			`SELECT auth_methods FROM tenants WHERE id = $1 FOR UPDATE`, s.tenantID).Scan(&currentRaw); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods load failed")
			return
		}
		merged := map[string]json.RawMessage{}
		if len(currentRaw) > 0 {
			if err := json.Unmarshal(currentRaw, &merged); err != nil {
				// Refuse rather than overwrite: an unparseable column may hold configuration this build does not understand.
				jsonErr(w, http.StatusConflict, "conflict",
					"the stored sign-in configuration could not be read; refusing to overwrite it")
				return
			}
		}
		for k, v := range patch {
			merged[k] = v
		}
		out, _ := json.Marshal(merged)
		if _, err := tx.Exec(ctx,
			`UPDATE tenants SET auth_methods = $1::jsonb, updated_at = now() WHERE id = $2`,
			string(out), s.tenantID); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods update failed")
			return
		}
		if err := tx.Commit(ctx); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "auth_methods update failed")
			return
		}
		// The audit records which methods were touched, never the blob, which may carry provider identifiers.
		changed := make([]string, 0, len(patch))
		for k := range patch {
			changed = append(changed, k)
		}
		sort.Strings(changed)
		s.audit(req, "auth_methods.updated", "tenant", s.tenantID, map[string]any{"methods_changed": changed})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	return r
}

// validateAuthMethodsPatch refuses a patch the portal could not act on. Only methods tenantcfg serves are accepted,
// each as an object of its known fields, so a typo or stale client cannot store configuration that breaks readers.
func validateAuthMethodsPatch(patch map[string]json.RawMessage) error {
	if len(patch) == 0 {
		return errors.New("nothing to change: send at least one sign-in method")
	}
	keys := make([]string, 0, len(patch))
	for k := range patch {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		raw := patch[k]
		switch k {
		case "voucher", "email_account", "sms", "whatsapp", "guest_account", "open":
			if err := validateMethodObject(k, raw, simpleMethodFields); err != nil {
				return err
			}
		case "social":
			var providers map[string]json.RawMessage
			if err := json.Unmarshal(raw, &providers); err != nil || providers == nil {
				return errors.New("social must be an object keyed by identity provider")
			}
			for p, v := range providers {
				if !socialAllowedProviders[p] {
					return fmt.Errorf("social.%s is not an identity provider (google, apple, facebook or microsoft)", p)
				}
				if err := validateMethodObject("social."+p, v, simpleMethodFields); err != nil {
					return err
				}
			}
		case "pms":
			if err := validateMethodObject(k, raw, pmsMethodFields); err != nil {
				return err
			}
		case "portal", "email":
			// checked below
		default:
			return fmt.Errorf("%q is not a sign-in method this appliance serves", k)
		}
	}
	if rawPortal, ok := patch["portal"]; ok {
		if err := validateMethodObject("portal", rawPortal, portalFields); err != nil {
			return err
		}
		var p struct {
			PrimaryMethod      *string `json:"primary_method"`
			RememberDeviceDays *int    `json:"remember_device_days"`
		}
		if err := json.Unmarshal(rawPortal, &p); err != nil {
			return errors.New("portal must be an object")
		}
		if p.PrimaryMethod != nil && *p.PrimaryMethod != "" && !portalPrimaryMethods[*p.PrimaryMethod] {
			return errors.New("portal.primary_method must be pms, email_account, sms, whatsapp, guest_account or voucher")
		}
		if p.RememberDeviceDays != nil && (*p.RememberDeviceDays < 0 || *p.RememberDeviceDays > 365) {
			return errors.New("portal.remember_device_days must be between 0 (never) and 365")
		}
	}
	// Email accounts replace the email one-time code; the old key is refused so a stale screen cannot store a method nothing serves.
	if _, ok := patch["email"]; ok {
		return errors.New("email (Email code) has been replaced by email_account (Email accounts)")
	}
	if raw, ok := patch["email_account"]; ok {
		var m struct {
			Enabled *bool `json:"enabled"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return errors.New("email_account must be an object with enabled true or false")
		}
	}
	rawPMS, ok := patch["pms"]
	if !ok {
		return nil
	}
	var cfg struct {
		Enabled bool   `json:"enabled"`
		Mode    string `json:"mode"`
	}
	if err := json.Unmarshal(rawPMS, &cfg); err != nil {
		return errors.New("pms must be an object")
	}
	if !cfg.Enabled {
		return nil // a disabled method needs no mode
	}
	// "either" stays accepted so an existing stored configuration can be saved back unchanged.
	if cfg.Mode == "either" {
		return nil
	}
	if !pmsSignInModes[cfg.Mode] {
		return errors.New("pms.mode must be room_any, room_lastname, room_firstname or room_reservation")
	}
	return nil
}

type amKind int

const (
	amBool amKind = iota
	amString
	amInt
)

// The fields tenantcfg reads per kind of method. template_id is a legacy plan link some stored objects carry; accepted so they save back unchanged.
var (
	simpleMethodFields = map[string]amKind{"enabled": amBool, "template_id": amString}
	pmsMethodFields    = map[string]amKind{"enabled": amBool, "template_id": amString, "provider": amString,
		"mode": amString, "max_failures_per_room": amInt, "lockout_window_minutes": amInt}
	portalFields = map[string]amKind{"primary_method": amString, "remember_device_days": amInt}
)

func validateMethodObject(name string, raw json.RawMessage, fields map[string]amKind) error {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return fmt.Errorf("%s must be an object", name)
	}
	for f, v := range obj {
		kind, ok := fields[f]
		if !ok {
			return fmt.Errorf("%s.%s is not a setting of this sign-in method", name, f)
		}
		var bad bool
		switch kind {
		case amBool:
			var b bool
			bad = json.Unmarshal(v, &b) != nil
		case amString:
			var s string
			bad = json.Unmarshal(v, &s) != nil
		case amInt:
			var n int
			bad = json.Unmarshal(v, &n) != nil
		}
		if bad {
			want := map[amKind]string{amBool: "true or false", amString: "text", amInt: "a whole number"}[kind]
			return fmt.Errorf("%s.%s must be %s", name, f, want)
		}
	}
	return nil
}

// portalPrimaryMethods are the methods that may lead the landing page (contract §6.2).
var portalPrimaryMethods = map[string]bool{"pms": true, "email_account": true, "sms": true, "whatsapp": true, "guest_account": true, "voucher": true}

type wgRule struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Value       string    `json:"value"`
	Ports       []int     `json:"ports,omitempty"`
	Description *string   `json:"description,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// WiderThanPolicy marks a rule stored before (or wider than) the site's widest-network policy; it is listed so the operator can correct it.
	WiderThanPolicy bool `json:"wider_than_policy,omitempty"`
}

func validWGRule(kind, value string) bool {
	switch kind {
	case "ip":
		ip := net.ParseIP(value)
		return ip != nil && ip.To4() != nil
	case "cidr":
		_, n, err := net.ParseCIDR(value)
		return err == nil && n.IP.To4() != nil
	case "domain":
		v := strings.TrimSpace(value)
		return v != "" && !strings.ContainsAny(v, " /:") && strings.Contains(v, ".")
	}
	return false
}

// wgPolicy is the site's walled-garden policy: the widest network a rule may name.
type wgPolicy struct {
	WidestPrefixLen int        `json:"widest_prefix_len"`
	Min             int        `json:"min"`
	Max             int        `json:"max"`
	Default         int        `json:"default"`
	ConfigVersion   int64      `json:"config_version"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
	UpdatedBy       *string    `json:"updated_by,omitempty"`
}

const (
	wgWidestDefault = 24
	wgWidestMin     = 16
	wgWidestMax     = 32
)

func (s *server) walledGardenPolicy(ctx context.Context) (wgPolicy, error) {
	p := wgPolicy{Min: wgWidestMin, Max: wgWidestMax, Default: wgWidestDefault}
	err := s.db.QueryRow(ctx, `SELECT widest_prefix_len, config_version, updated_at, updated_by
	    FROM iam_v2.walled_garden_settings_get($1::uuid, $2::uuid)`, s.tenantID, s.siteID).
		Scan(&p.WidestPrefixLen, &p.ConfigVersion, &p.UpdatedAt, &p.UpdatedBy)
	return p, err
}

type wgInput struct {
	Kind        string `json:"kind"`
	Value       string `json:"value"`
	Ports       []int  `json:"ports"`
	Description string `json:"description"`
}

// check validates a rule's shape and, for addresses/networks, the site policy: 0.0.0.0/0, networks wider than the policy and meaningless targets are refused.
// It returns the refusal to show the operator, or "".
func (in *wgInput) check(widest int) string {
	in.Kind, in.Value = strings.TrimSpace(in.Kind), strings.TrimSpace(in.Value)
	if !validWGRule(in.Kind, in.Value) {
		return "kind must be domain|ip|cidr with a valid value"
	}
	for _, p := range in.Ports {
		if p < 1 || p > 65535 {
			return "ports must be 1..65535"
		}
	}
	if in.Kind == "ip" || in.Kind == "cidr" {
		if why := nft.GardenTargetRefusal(in.Value, widest); why != "" {
			return why
		}
	}
	return ""
}

func (s *server) walledGardenRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		pol, perr := s.walledGardenPolicy(ctx)
		rows, err := s.db.Query(ctx, `
            SELECT id, kind, value, COALESCE(ports, '{}'), description, created_at
              FROM walled_garden_rules WHERE tenant_id = $1 ORDER BY created_at DESC
        `, s.tenantID)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		defer rows.Close()
		var out []wgRule
		for rows.Next() {
			var rr wgRule
			if err := rows.Scan(&rr.ID, &rr.Kind, &rr.Value, &rr.Ports, &rr.Description, &rr.CreatedAt); err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
				return
			}
			if perr == nil && (rr.Kind == "ip" || rr.Kind == "cidr") {
				rr.WiderThanPolicy = nft.GardenTargetRefusal(rr.Value, pol.WidestPrefixLen) != ""
			}
			out = append(out, rr)
		}
		writeList(w, out)
	})
	// The policy: readable by all who can see the garden; changed with a password and reason, audited.
	r.Get("/policy", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		pol, err := s.walledGardenPolicy(ctx)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the walled-garden policy could not be read")
			return
		}
		writeJSON(w, http.StatusOK, pol)
	})
	r.Put("/policy", func(w http.ResponseWriter, req *http.Request) {
		var in struct {
			WidestPrefixLen int    `json:"widest_prefix_len"`
			Reason          string `json:"reason"`
			Password        string `json:"password"`
		}
		if err := decodeJSON(req, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
			return
		}
		if in.WidestPrefixLen < wgWidestMin || in.WidestPrefixLen > wgWidestMax {
			jsonErr(w, http.StatusBadRequest, "validation",
				fmt.Sprintf("the widest network must be between /%d and /%d", wgWidestMin, wgWidestMax))
			return
		}
		if r := strings.TrimSpace(in.Reason); len(r) < 4 || len(r) > 500 {
			jsonErr(w, http.StatusBadRequest, "validation", "a reason of 4-500 characters is required")
			return
		}
		// A wider garden is a weaker captive portal: the same step-up as every other security setting.
		actor, ok := s.stepUpActor(w, req, in.Password)
		if !ok {
			return
		}
		ctx, cancel := dbCtx(req)
		defer cancel()
		before, _ := s.walledGardenPolicy(ctx)
		var version int64
		if err := s.db.QueryRow(ctx, `SELECT iam_v2.walled_garden_settings_set($1::uuid,$2::uuid,$3,$4,$5)`,
			s.tenantID, s.siteID, in.WidestPrefixLen, actor, in.Reason).Scan(&version); err != nil {
			jsonErr(w, http.StatusBadRequest, "validation", pgErr(err))
			return
		}
		s.audit(req, "walled_garden.policy_changed", "walled_garden_policy", s.siteID, map[string]any{
			"from_widest_prefix_len": before.WidestPrefixLen, "to_widest_prefix_len": in.WidestPrefixLen,
			"reason": strings.TrimSpace(in.Reason), "config_version": version})
		pol, _ := s.walledGardenPolicy(ctx)
		writeJSON(w, http.StatusOK, pol)
	})
	r.Post("/", func(w http.ResponseWriter, req *http.Request) {
		var in wgInput
		if err := decodeJSON(req, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "kind must be domain|ip|cidr with a valid value")
			return
		}
		ctx, cancel := dbCtx(req)
		defer cancel()
		pol, err := s.walledGardenPolicy(ctx)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the walled-garden policy could not be read")
			return
		}
		if why := in.check(pol.WidestPrefixLen); why != "" {
			jsonErr(w, http.StatusBadRequest, "bad_request", why)
			return
		}
		var id string
		err = s.db.QueryRow(ctx, `
            INSERT INTO walled_garden_rules (tenant_id, site_id, kind, value, ports, description)
            VALUES ($1, NULL, $2, $3, NULLIF($4::int[], '{}'), NULLIF($5,''))
            RETURNING id
        `, s.tenantID, in.Kind, in.Value, in.Ports, in.Description).Scan(&id)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
			return
		}
		s.audit(req, "walled_garden.created", "walled_garden_rule", id,
			map[string]any{"kind": in.Kind, "value": in.Value, "ports": in.Ports, "description": in.Description})
		s.pokeSCD(req, "/v1/admin/walled-garden/reload")
		writeJSON(w, http.StatusCreated, map[string]string{"id": id})
	})
	// Edit applies the same checks as create; the audit row carries the old and new rule.
	r.Put("/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "id")
		var in wgInput
		if err := decodeJSON(req, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "kind must be domain|ip|cidr with a valid value")
			return
		}
		ctx, cancel := dbCtx(req)
		defer cancel()
		pol, err := s.walledGardenPolicy(ctx)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the walled-garden policy could not be read")
			return
		}
		if why := in.check(pol.WidestPrefixLen); why != "" {
			jsonErr(w, http.StatusBadRequest, "bad_request", why)
			return
		}
		var oldKind, oldValue string
		var oldPorts []int
		if err := s.db.QueryRow(ctx, `SELECT kind, value, COALESCE(ports,'{}') FROM walled_garden_rules
		    WHERE id = $1 AND tenant_id = $2`, id, s.tenantID).Scan(&oldKind, &oldValue, &oldPorts); err != nil {
			jsonErr(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		if _, err := s.db.Exec(ctx, `UPDATE walled_garden_rules
		    SET kind = $3, value = $4, ports = NULLIF($5::int[], '{}'), description = NULLIF($6,'')
		  WHERE id = $1 AND tenant_id = $2`, id, s.tenantID, in.Kind, in.Value, in.Ports, in.Description); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
			return
		}
		s.audit(req, "walled_garden.updated", "walled_garden_rule", id, map[string]any{
			"from": map[string]any{"kind": oldKind, "value": oldValue, "ports": oldPorts},
			"to":   map[string]any{"kind": in.Kind, "value": in.Value, "ports": in.Ports, "description": in.Description}})
		s.pokeSCD(req, "/v1/admin/walled-garden/reload")
		writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
	})
	r.Delete("/{id}", func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "id")
		ctx, cancel := dbCtx(req)
		defer cancel()
		var kind, value string
		var ports []int
		err := s.db.QueryRow(ctx, `DELETE FROM walled_garden_rules WHERE id = $1 AND tenant_id = $2
		    RETURNING kind, value, COALESCE(ports,'{}')`, id, s.tenantID).Scan(&kind, &value, &ports)
		if errors.Is(err, pgx.ErrNoRows) {
			jsonErr(w, http.StatusNotFound, "not_found", "rule not found")
			return
		}
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
			return
		}
		s.audit(req, "walled_garden.deleted", "walled_garden_rule", id,
			map[string]any{"kind": kind, "value": value, "ports": ports})
		s.pokeSCD(req, "/v1/admin/walled-garden/reload")
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
	return r
}

// guestAuditResources: audit entries that name a guest, by action prefix, and the screen whose read permission shows them.
// Searches are client data too, so guest_data entries follow guest-data-rights.
var guestAuditResources = []struct{ prefix, resource string }{
	{"guest_data.", "guest-data-rights"},
	{"guest_account.", "guest-accounts"},
	{"guest_signin_", "guest-signin-attempts"},
	{"network.dhcp.reservation.", "network"},
	{"client_device.", "sessions"},
	{"session.", "sessions"},
	{"poststay.", "pms-stay-identity"},
	{"stay.", "pms-stay-identity"},
}

// hiddenGuestAuditPrefixes lists the prefixes whose guest details these roles may not see.
func hiddenGuestAuditPrefixes(roles []string) []string {
	out := []string{}
	for _, g := range guestAuditResources {
		if !permFor(roles, g.resource, permRead) {
			out = append(out, g.prefix)
		}
	}
	return out
}

func guestAuditHidden(action string, hidden []string) bool {
	for _, p := range hidden {
		if strings.HasPrefix(action, p) {
			return true
		}
	}
	return false
}

type auditRow struct {
	TS         time.Time       `json:"ts"`
	ActorType  string          `json:"actor_type"`
	ActorID    *string         `json:"actor_id,omitempty"`
	Action     string          `json:"action"`
	TargetType *string         `json:"target_type,omitempty"`
	TargetID   *string         `json:"target_id,omitempty"`
	IP         *string         `json:"ip,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// auditSearchHeader carries the trail's free-text search and auditSearchActionsHeader the action codes whose title matched it
// (titles live in the Admin Console). Headers, not the URL: edged logs request lines.
const (
	auditSearchHeader        = "X-Audit-Search"
	auditSearchActionsHeader = "X-Audit-Search-Actions"
)

// auditActionCount counts one action code over the period (and actor) only, never narrowed by category, action or search.
type auditActionCount struct {
	Action string `json:"action"`
	Count  int    `json:"count"`
}

type auditPage struct {
	pagedList[auditRow]
	Actions []auditActionCount `json:"actions"`
}

func (s *server) auditRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		// Paged in SQL; ?limit= still sizes the page for older clients.
		pg, ok := readPage(w, req, 500)
		if !ok {
			return
		}
		search, ok := readSearch(w, req, auditSearchHeader)
		if !ok {
			return
		}
		qv := req.URL.Query()

		// Filters are applied in SQL. `period` is period and actor alone (the per-action counts use it); `where` adds action, category and search and selects the page.
		args := []any{s.tenantID}
		bind := func(v any) int { args = append(args, v); return len(args) }
		period := ` WHERE tenant_id = $1`

		// Guest details in the trail follow the screen they came from: a role that cannot read it sees that an action happened, when and by whom, not whom about.
		// Searches and the actor filter skip those entries, otherwise they would confirm a guest's presence.
		var roles []string
		if sess := sessFrom(req.Context()); sess != nil {
			roles = sess.Roles
		}
		hidden := hiddenGuestAuditPrefixes(roles)
		hiddenLike := make([]string, len(hidden))
		for i, h := range hidden {
			hiddenLike[i] = likeEscape(h) + "%"
		}

		// One placeholder, referenced twice: on a sign-in the operator is the target, not the actor, so an actor_id-only search would miss every login.
		if v := qv.Get("actor"); v != "" {
			n, h := bind(v), bind(hiddenLike)
			period += fmt.Sprintf(` AND (actor_id = $%[1]d OR (target_id = $%[1]d AND NOT action LIKE ANY($%[2]d)))`, n, h)
		}
		if v := qv.Get("from"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				period += fmt.Sprintf(` AND ts >= $%d`, bind(t))
			}
		}
		if v := qv.Get("to"); v != "" {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				period += fmt.Sprintf(` AND ts <= $%d`, bind(t))
			}
		}
		periodArgs := len(args)

		where := period
		// action= is an exact list. Present but empty means none: a category with no entries must show nothing, not everything.
		if qv.Has("action") {
			codes := []string{}
			for _, c := range strings.Split(qv.Get("action"), ",") {
				if c = strings.TrimSpace(c); c != "" {
					codes = append(codes, c)
				}
			}
			where += fmt.Sprintf(` AND action = ANY($%d)`, bind(codes))
		}
		// A prefix, not an exact match, so one category maps to one clause ("network" selects network.apply, network.guest.created, ...).
		if p := qv.Get("action_prefix"); p != "" {
			where += fmt.Sprintf(` AND action LIKE $%d`, bind(likeEscape(p)+"%"))
		}
		if search != "" {
			titled := []string{}
			for _, c := range strings.Split(req.Header.Get(auditSearchActionsHeader), ",") {
				if c = strings.TrimSpace(c); c != "" && len(titled) < 200 {
					titled = append(titled, c)
				}
			}
			n, m, h := bind(likePattern(search)), bind(titled), bind(hiddenLike)
			where += fmt.Sprintf(` AND (action ILIKE $%[1]d OR actor_id ILIKE $%[1]d
			    OR (target_id ILIKE $%[1]d AND NOT action LIKE ANY($%[3]d))
			    OR target_type ILIKE $%[1]d OR host(ip) ILIKE $%[1]d OR action = ANY($%[2]d))`, n, m, h)
		}

		ctx, cancel := dbCtx(req)
		defer cancel()

		// ts alone is not unique and offset paging could repeat or skip rows; rows sharing a ts share a hypertable chunk, so ctid settles the order.
		q := `SELECT ts, actor_type, actor_id, action, target_type, target_id, ip::text, payload
                FROM audit_log` + where + fmt.Sprintf(` ORDER BY ts DESC, ctid DESC LIMIT %d OFFSET %d`, pg.Fetch(), pg.Offset())
		rows, err := s.db.Query(ctx, q, args...)
		if err != nil {
			slog.Error("audit read failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the audit trail could not be read")
			return
		}
		defer rows.Close()
		out := []auditRow{}
		for rows.Next() {
			var a auditRow
			if err := rows.Scan(&a.TS, &a.ActorType, &a.ActorID, &a.Action,
				&a.TargetType, &a.TargetID, &a.IP, &a.Payload); err != nil {
				slog.Error("audit scan failed", "err", err)
				jsonErr(w, http.StatusInternalServerError, "internal", "the audit trail could not be read")
				return
			}
			if guestAuditHidden(a.Action, hidden) {
				a.TargetID = nil
				if len(a.Payload) > 0 {
					a.Payload = json.RawMessage(`{"details_hidden":true}`)
				}
			}
			out = append(out, a)
		}
		// A failed read is not an empty one: pgx reports a permission error on the first Next(), not at Query().
		if err := rows.Err(); err != nil {
			slog.Error("audit read failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the audit trail could not be read")
			return
		}
		rows.Close()
		out, more := trimPage(out, pg)

		var total int
		if err := s.db.QueryRow(ctx, `SELECT count(*)::int FROM audit_log`+where, args...).Scan(&total); err != nil {
			slog.Error("audit read failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the audit trail could not be read")
			return
		}
		actions := []auditActionCount{}
		arows, err := s.db.Query(ctx, `SELECT action, count(*)::int FROM audit_log`+period+`
			GROUP BY action ORDER BY action`, args[:periodArgs]...)
		if err != nil {
			slog.Error("audit read failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the audit trail could not be read")
			return
		}
		defer arows.Close()
		for arows.Next() {
			var a auditActionCount
			if err := arows.Scan(&a.Action, &a.Count); err != nil {
				slog.Error("audit scan failed", "err", err)
				jsonErr(w, http.StatusInternalServerError, "internal", "the audit trail could not be read")
				return
			}
			actions = append(actions, a)
		}
		if err := arows.Err(); err != nil {
			slog.Error("audit read failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "internal", "the audit trail could not be read")
			return
		}
		writeJSON(w, http.StatusOK, auditPage{pagedList: newPagedList(out, more, pg, &total), Actions: actions})
	})
	return r
}

func (s *server) reportsRoutes() http.Handler {
	r := chi.NewRouter()
	// The richer operational snapshot for the dashboard; /summary stays unchanged because other screens read it as a stable contract.
	r.Get("/dashboard", s.reportsDashboard)
	// The overview and the appliance's own resources are additive; /dashboard is unchanged.
	r.Get("/overview", s.reportsOverview)
	r.Get("/appliance-resources", s.reportsApplianceResources)
	r.Get("/summary", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := dbCtx(req)
		defer cancel()
		out := map[string]any{}

		// Counted from iam_v2.sessions, the single session authority.
		var active, today, sess7d, vUnused, vRedeemed int64
		var upToday, downToday int64
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions
		     WHERE tenant_id=$1 AND state = 'active'`, s.tenantID).Scan(&active)
		_ = s.db.QueryRow(ctx, `
            SELECT count(*), COALESCE(sum(bytes_up),0), COALESCE(sum(bytes_down),0)
              FROM iam_v2.sessions WHERE tenant_id=$1 AND started >= date_trunc('day', now())
        `, s.tenantID).Scan(&today, &upToday, &downToday)
		_ = s.db.QueryRow(ctx,
			`SELECT count(*) FROM iam_v2.sessions
			  WHERE tenant_id=$1 AND started >= now() - interval '7 days'`, s.tenantID).Scan(&sess7d)
		// Voucher tiles come from scd: svc_edged has no privilege on iam_v2.vouchers (scd owns the key material), and asking scd counts what the voucher screen counts.
		if st, raw, err := s.scd.call(ctx, http.MethodGet, "/v1/vouchers/summary", nil); err == nil && st == http.StatusOK {
			var sum struct {
				Unused   int64 `json:"unused"`
				Redeemed int64 `json:"redeemed"`
			}
			if json.Unmarshal(raw, &sum) == nil {
				vUnused, vRedeemed = sum.Unused, sum.Redeemed
			}
		}

		out["active_sessions"] = active
		out["sessions_today"] = today
		out["bytes_up_today"] = upToday
		out["bytes_down_today"] = downToday
		out["sessions_7d"] = sess7d
		out["vouchers_unused"] = vUnused
		// Named for what it counts: under iam_v2 "active" is a property of the entitlement, not of a voucher.
		out["vouchers_redeemed"] = vRedeemed

		// Top packages: the internet package an entitlement was granted from. The link runs through the package revision
		// (entitlements pin package_revision_id; the display name lives in the revision's `display` JSON).
		type topPlan struct {
			TemplateID string `json:"template_id"`
			Name       string `json:"name"`
			Sessions   int64  `json:"sessions"`
		}
		var top []topPlan
		rows, err := s.db.Query(ctx, `
            SELECT p.id::text,
                   COALESCE(NULLIF(cur.display->>'name',''), p.code) AS name,
                   count(se.id) AS n
              FROM iam_v2.sessions se
              JOIN iam_v2.entitlements e
                ON e.tenant_id = se.tenant_id AND e.site_id = se.site_id AND e.id = se.entitlement_id
              JOIN iam_v2.internet_package_revisions r
                ON r.tenant_id = e.tenant_id AND r.site_id = e.site_id AND r.id = e.package_revision_id
              JOIN iam_v2.internet_packages p
                ON p.tenant_id = r.tenant_id AND p.site_id = r.site_id AND p.id = r.package_id
              LEFT JOIN iam_v2.internet_package_revisions cur ON cur.id = p.current_revision_id
             WHERE se.tenant_id = $1 AND se.started >= now() - interval '7 days'
             GROUP BY p.id, p.code, cur.display->>'name' ORDER BY n DESC LIMIT 5
        `, s.tenantID)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var tp topPlan
				if rows.Scan(&tp.TemplateID, &tp.Name, &tp.Sessions) == nil {
					top = append(top, tp)
				}
			}
		}
		out["top_plans_7d"] = top
		writeJSON(w, http.StatusOK, out)
	})
	return r
}

type backupRow struct {
	ID         string     `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Status     string     `json:"status"`
	Kind       string     `json:"kind"`
	Path       *string    `json:"path,omitempty"`
	SizeBytes  *int64     `json:"size_bytes,omitempty"`
	Error      *string    `json:"error,omitempty"`
}

// listBackupRecords is the ledger of backups this appliance took, distinct from artefacts on disk.
func (s *server) listBackupRecords(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := dbCtx(req)
	defer cancel()
	rows, err := s.db.Query(ctx, `
            SELECT id, started_at, finished_at, status, kind, path, size_bytes, error
              FROM backup_records ORDER BY started_at DESC LIMIT 50
        `)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []backupRow
	for rows.Next() {
		var b backupRow
		if err := rows.Scan(&b.ID, &b.StartedAt, &b.FinishedAt, &b.Status, &b.Kind,
			&b.Path, &b.SizeBytes, &b.Error); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, b)
	}
	writeList(w, out)
}

// pokeSCD fires a best-effort admin action on scd; failures go to the audit payload but never fail the write (reconcilers converge anyway).
func (s *server) pokeSCD(req *http.Request, path string) {
	if s.scd == nil { // a test server with no scd: the reconcilers converge on their own
		return
	}
	if _, _, err := s.scd.call(req.Context(), http.MethodPost, path, nil); err != nil {
		s.audit(req, "scd.poke_failed", "scd", path, map[string]any{"error": err.Error()})
	}
}

var authMethodModules = map[string]struct{ module, label string }{
	"pms": {"hospitality", "Room sign-in"},
	// email_otp now licenses "Email accounts", which replaced the email code.
	"email_account": {"email_otp", "Email accounts"},
	"sms":           {"sms_otp", "SMS code"},
	"whatsapp":      {"whatsapp_otp", "WhatsApp code"},
	"social":        {"social_login", "Social login"},
}

// authMethodModuleRefusal returns (status, message) when the patch switches ON a method whose module is not
// licensed here, else (0, ""). Unreadable module state refuses switching an optional method on (fail closed).
func (s *server) authMethodModuleRefusal(ctx context.Context, patch map[string]json.RawMessage) (int, string) {
	for key, m := range authMethodModules {
		raw, ok := patch[key]
		if !ok || !turnsOn(key, raw) {
			continue
		}
		if s.modCache == nil {
			continue // a server assembled without module wiring (focused unit tests)
		}
		rep, ok := s.moduleReport(ctx)
		if !ok {
			return http.StatusServiceUnavailable, "The appliance could not determine which modules are available. Try again shortly."
		}
		st := rep.Modules[m.module]
		if !st.Licensed || !st.Deployed {
			return http.StatusConflict, m.label + " is not part of this site's licence, so it cannot be switched on."
		}
	}
	return 0, ""
}

// turnsOn reports whether a method patch enables it ({"enabled":true}; for social, any provider enabled).
func turnsOn(key string, raw json.RawMessage) bool {
	if key == "social" {
		var providers map[string]struct {
			Enabled bool `json:"enabled"`
		}
		if json.Unmarshal(raw, &providers) != nil {
			return false
		}
		for _, p := range providers {
			if p.Enabled {
				return true
			}
		}
		return false
	}
	var m struct {
		Enabled bool `json:"enabled"`
	}
	return json.Unmarshal(raw, &m) == nil && m.Enabled
}
