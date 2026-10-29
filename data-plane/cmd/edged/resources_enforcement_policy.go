package main

// Enforcement policy (migration 0166): the operator surface over public.enforcement_policy, the singleton the data
// plane reads (netd: private-network denial, allow-list, upstream exception, MAC binding; acctd: cut-off period).
//
// Permission: the existing "network" key (read to see, write to change). These are network-security settings of the
// same kind the network area already owns, hotel_it_manager holds that key, and a second key would only add a role
// to maintain. Every change goes through enforcement_policy_set(), which validates, bumps config_version and appends
// to the append-only enforcement_policy_changes; the operator audit log records it as well.
// The bounds are served with the values so UI and server validation cannot drift.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	enforcementMaxCIDRs  = 64
	enforcementMinPrefix = 8
	enforcementMaxPrefix = 32
	enforcementCutoffMin = 1
	enforcementCutoffMax = 60
	enforcementHistory   = 50
)

type enforcementPolicy struct {
	PrivateDenyEnabled          bool      `json:"private_deny_enabled"`
	PrivateAllowCIDRs           []string  `json:"private_allow_cidrs"`
	AllowUpstreamInfrastructure bool      `json:"allow_upstream_infrastructure"`
	MACBindingEnabled           bool      `json:"mac_binding_enabled"`
	CutoffSeconds               int       `json:"cutoff_seconds"`
	ConfigVersion               int64     `json:"config_version"`
	UpdatedAt                   time.Time `json:"updated_at"`
	UpdatedBy                   string    `json:"updated_by,omitempty"`
}

type enforcementChange struct {
	ChangedAt     time.Time `json:"changed_at"`
	ChangedBy     string    `json:"changed_by"`
	Reason        string    `json:"reason"`
	Field         string    `json:"field"`
	OldValue      *string   `json:"old_value"`
	NewValue      *string   `json:"new_value"`
	ConfigVersion int64     `json:"config_version"`
}

// enforcementFieldChange is one validated field write: Value is the text enforcement_policy_set takes.
type enforcementFieldChange struct{ Field, Value string }

// enforcementStore is the database seam; the pgx implementation below is the only production one.
type enforcementStore interface {
	read(ctx context.Context) (enforcementPolicy, error)
	history(ctx context.Context, limit int) ([]enforcementChange, error)
	// apply writes every change in ONE transaction: all of them or none.
	apply(ctx context.Context, changes []enforcementFieldChange, operator, reason string) error
}

type pgEnforcementStore struct{ s *server }

func (p pgEnforcementStore) read(ctx context.Context) (enforcementPolicy, error) {
	var o enforcementPolicy
	var by *string
	err := p.s.db.QueryRow(ctx, `
		SELECT private_deny_enabled, private_allow_cidrs, allow_upstream_infrastructure, mac_binding_enabled,
		       cutoff_seconds, config_version, updated_at, updated_by
		  FROM public.enforcement_policy`).Scan(&o.PrivateDenyEnabled, &o.PrivateAllowCIDRs,
		&o.AllowUpstreamInfrastructure, &o.MACBindingEnabled, &o.CutoffSeconds, &o.ConfigVersion, &o.UpdatedAt, &by)
	if err != nil {
		return o, err
	}
	if by != nil {
		o.UpdatedBy = *by
	}
	if o.PrivateAllowCIDRs == nil {
		o.PrivateAllowCIDRs = []string{}
	}
	return o, nil
}

func (p pgEnforcementStore) history(ctx context.Context, limit int) ([]enforcementChange, error) {
	rows, err := p.s.db.Query(ctx, `
		SELECT changed_at, changed_by, reason, field, old_value, new_value, config_version
		  FROM public.enforcement_policy_changes ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []enforcementChange{}
	for rows.Next() {
		var c enforcementChange
		if err := rows.Scan(&c.ChangedAt, &c.ChangedBy, &c.Reason, &c.Field, &c.OldValue, &c.NewValue, &c.ConfigVersion); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (p pgEnforcementStore) apply(ctx context.Context, changes []enforcementFieldChange, operator, reason string) error {
	tx, err := p.s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, c := range changes {
		if _, err := tx.Exec(ctx, `SELECT public.enforcement_policy_set($1,$2,$3,$4)`, c.Field, c.Value, operator, reason); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *server) enforcementStore() enforcementStore {
	if s.enforcementStoreOverride != nil {
		return s.enforcementStoreOverride
	}
	return pgEnforcementStore{s}
}

// enforcementSettingOut documents one setting: default, unit, explanation and bounds (CLAUDE.md 0C).
type enforcementSettingOut struct {
	Field       string `json:"field"`
	Label       string `json:"label"`
	Default     any    `json:"default"`
	Unit        string `json:"unit,omitempty"`
	Explanation string `json:"explanation"`
	Min         *int   `json:"min,omitempty"`
	Max         *int   `json:"max,omitempty"`
}

func enforcementSettings() []enforcementSettingOut {
	lo, hi := enforcementCutoffMin, enforcementCutoffMax
	zero, maxc := 0, enforcementMaxCIDRs
	return []enforcementSettingOut{
		{Field: "private_deny_enabled", Label: "Deny guests the hotel's private networks", Default: true,
			Explanation: "A signed-in guest cannot reach private addresses (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 100.64.0.0/10, 169.254.0.0/16). Off restores the earlier behaviour in which only the management networks were protected."},
		{Field: "private_allow_cidrs", Label: "Private networks a guest may still reach", Default: []string{}, Unit: "IPv4 networks (CIDR, /8 to /32)",
			Explanation: "Exceptions to the denial above, such as a printer or casting network. At most 64.", Min: &zero, Max: &maxc},
		{Field: "allow_upstream_infrastructure", Label: "Keep the upstream gateway and DNS reachable", Default: true,
			Explanation: "Adds the appliance's own WAN gateway and DNS resolvers as exceptions, so the hotel's configured upstream is never cut by the denial."},
		{Field: "mac_binding_enabled", Label: "Bind a guest's access to its device address (MAC)", Default: false,
			Explanation: "The AD-23 control: access is keyed to the bridge, IP address and the MAC address from the DHCP lease, so another device taking over an address does not inherit the access. Off keeps the earlier IP-only key. Ships off until the bound ruleset is proven on the test appliance."},
		{Field: "cutoff_seconds", Label: "Cut-off delay when an allowance is used up", Default: 5, Unit: "seconds",
			Explanation: "How soon a guest whose allowance ran out is cut off: the period of the accounting enforcement pass. Shorter is stricter and costs a little more work.", Min: &lo, Max: &hi},
	}
}

func (s *server) getEnforcementPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	st := s.enforcementStore()
	pol, err := st.read(ctx)
	if err != nil {
		internalErr(w, r, http.StatusInternalServerError, "policy_unreadable", err)
		return
	}
	hist, err := st.history(ctx, enforcementHistory)
	if err != nil {
		internalErr(w, r, http.StatusInternalServerError, "policy_history_unreadable", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"policy":   pol,
		"settings": enforcementSettings(),
		"limits": map[string]any{
			"max_cidrs": enforcementMaxCIDRs, "min_prefix": enforcementMinPrefix, "max_prefix": enforcementMaxPrefix,
			"cutoff_min": enforcementCutoffMin, "cutoff_max": enforcementCutoffMax,
		},
		"history": hist,
	})
}

// normaliseEnforcementCIDRs applies the same rules as enforcement_policy_set, with readable messages: IPv4 only,
// /8../32, no host bits (the database's cidr type rejects them), duplicates dropped, at most 64.
func normaliseEnforcementCIDRs(in []string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, raw := range in {
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}
		ip, n, err := net.ParseCIDR(c)
		if err != nil || ip.To4() == nil || strings.Contains(c, ":") {
			return nil, fmt.Errorf("%q is not an IPv4 network in CIDR form (for example 192.168.50.0/24)", c)
		}
		ones, _ := n.Mask.Size()
		if ones < enforcementMinPrefix || ones > enforcementMaxPrefix {
			return nil, fmt.Errorf("%q must be between /%d and /%d", c, enforcementMinPrefix, enforcementMaxPrefix)
		}
		if !ip.Equal(n.IP) {
			return nil, fmt.Errorf("%q has host bits set; write the network address, %s", c, n.String())
		}
		if !seen[n.String()] {
			seen[n.String()] = true
			out = append(out, n.String())
		}
	}
	if len(out) > enforcementMaxCIDRs {
		return nil, fmt.Errorf("at most %d networks may be allowed", enforcementMaxCIDRs)
	}
	sort.Strings(out)
	return out, nil
}

type enforcementPatch struct {
	PrivateDenyEnabled          *bool     `json:"private_deny_enabled"`
	PrivateAllowCIDRs           *[]string `json:"private_allow_cidrs"`
	AllowUpstreamInfrastructure *bool     `json:"allow_upstream_infrastructure"`
	MACBindingEnabled           *bool     `json:"mac_binding_enabled"`
	CutoffSeconds               *int      `json:"cutoff_seconds"`
	Reason                      string    `json:"reason"`
}

// diffEnforcementPatch validates the patch and returns only the fields that differ from the current policy.
func diffEnforcementPatch(cur enforcementPolicy, in enforcementPatch) ([]enforcementFieldChange, error) {
	var ch []enforcementFieldChange
	b := strconv.FormatBool
	if in.PrivateDenyEnabled != nil && *in.PrivateDenyEnabled != cur.PrivateDenyEnabled {
		ch = append(ch, enforcementFieldChange{"private_deny_enabled", b(*in.PrivateDenyEnabled)})
	}
	if in.AllowUpstreamInfrastructure != nil && *in.AllowUpstreamInfrastructure != cur.AllowUpstreamInfrastructure {
		ch = append(ch, enforcementFieldChange{"allow_upstream_infrastructure", b(*in.AllowUpstreamInfrastructure)})
	}
	if in.MACBindingEnabled != nil && *in.MACBindingEnabled != cur.MACBindingEnabled {
		ch = append(ch, enforcementFieldChange{"mac_binding_enabled", b(*in.MACBindingEnabled)})
	}
	if in.CutoffSeconds != nil {
		if *in.CutoffSeconds < enforcementCutoffMin || *in.CutoffSeconds > enforcementCutoffMax {
			return nil, fmt.Errorf("cut-off delay must be between %d and %d seconds", enforcementCutoffMin, enforcementCutoffMax)
		}
		if *in.CutoffSeconds != cur.CutoffSeconds {
			ch = append(ch, enforcementFieldChange{"cutoff_seconds", strconv.Itoa(*in.CutoffSeconds)})
		}
	}
	if in.PrivateAllowCIDRs != nil {
		n, err := normaliseEnforcementCIDRs(*in.PrivateAllowCIDRs)
		if err != nil {
			return nil, err
		}
		have, _ := normaliseEnforcementCIDRs(cur.PrivateAllowCIDRs)
		if strings.Join(n, ",") != strings.Join(have, ",") {
			ch = append(ch, enforcementFieldChange{"private_allow_cidrs", strings.Join(n, ",")})
		}
	}
	return ch, nil
}

func (s *server) patchEnforcementPolicy(w http.ResponseWriter, r *http.Request) {
	var in enforcementPatch
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "The request could not be read: "+err.Error())
		return
	}
	reason, ok := reasonText(in.Reason)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "reason_required", "A reason is required: "+reasonBoundsMessage)
		return
	}
	operator := protectionActor(sessFrom(r.Context()))
	if operator == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	st := s.enforcementStore()
	cur, err := st.read(ctx)
	if err != nil {
		internalErr(w, r, http.StatusInternalServerError, "policy_unreadable", err)
		return
	}
	changes, err := diffEnforcementPatch(cur, in)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}
	if len(changes) == 0 {
		jsonErr(w, http.StatusBadRequest, "no_change", "Nothing was changed: the values sent are already in effect.")
		return
	}
	if err := st.apply(ctx, changes, operator, reason); err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "22023" { // invalid_parameter_value: the function's own validation
			jsonErr(w, http.StatusBadRequest, "invalid_policy", pe.Message)
			return
		}
		s.audit(r, "enforcement_policy.update_failed", "enforcement_policy", "", map[string]any{"reason": reason, "error": err.Error()})
		internalErr(w, r, http.StatusInternalServerError, "policy_not_saved", err)
		return
	}
	audited := make([]map[string]any, 0, len(changes))
	for _, c := range changes {
		audited = append(audited, map[string]any{"field": c.Field, "new": c.Value})
	}
	s.audit(r, "enforcement_policy.update", "enforcement_policy", "", map[string]any{"reason": reason, "changes": audited})
	s.getEnforcementPolicy(w, r)
}
