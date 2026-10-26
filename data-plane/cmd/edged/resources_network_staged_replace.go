package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// A topology change is a replacement in three moments. STAGE (POST /guest-networks/{id}/replace) records the
// request and changes nothing else. APPLY (POST /network/apply) materialises every staged request in one
// transaction (successor created with the original's addressing, pools, reservations, settings and PMS route; the
// original disabled), then asks netd to apply; on any failure the materialisation is reverted in the same request.
// SETTLE (confirm) marks it CONFIRMED and republishes dependent packages onto the successor; rollback leaves it PENDING.
// netd rolls back an unconfirmed apply by itself, so reconcileStaleReplacements reverts any APPLIED request whose
// revision is no longer in flight (idempotent, run on the operator's own reads).

const (
	replStatePending   = "PENDING"
	replStateApplied   = "APPLIED"
	replStateConfirmed = "CONFIRMED"
	replStateCancelled = "CANCELLED"
)

type stagedReplacement struct {
	ID           string  `json:"id"`
	OriginalID   string  `json:"original_network_id"`
	Successor    *string `json:"successor_network_id,omitempty"`
	State        string  `json:"state"`
	Type         string  `json:"network_type"`
	Parent       string  `json:"parent_interface"`
	VLANID       *int    `json:"vlan_id,omitempty"`
	Subnet       *string `json:"subnet_cidr,omitempty"`
	Gateway      *string `json:"gateway_ip,omitempty"`
	NewName      *string `json:"new_name,omitempty"`
	Reason       string  `json:"reason"`
	CreatedBy    string  `json:"created_by"`
	CreatedAt    string  `json:"created_at"`
	OriginalName string  `json:"original_network_name,omitempty"`
}

type replaceNetworkInput struct {
	NetworkType     string        `json:"network_type"`
	ParentInterface string        `json:"parent_interface"`
	VLANID          *int          `json:"vlan_id"`
	Name            string        `json:"name"`
	GatewayIP       string        `json:"gateway_ip"`
	SubnetCIDR      string        `json:"subnet_cidr"`
	Pools           []netcfg.Pool `json:"pools"`
	Reason          string        `json:"reason"`
}

func (s *server) stageGuestNetworkReplacement(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in replaceNetworkInput
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "say why the network is being replaced (at least 4 characters)")
		return
	}
	if in.NetworkType != "untagged" && in.NetworkType != "vlan" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "network_type must be 'untagged' or 'vlan'")
		return
	}
	if strings.TrimSpace(in.ParentInterface) == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "choose the port the new network runs on")
		return
	}
	if in.NetworkType == "vlan" && (in.VLANID == nil || *in.VLANID < 1 || *in.VLANID > 4094) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a VLAN network needs a VLAN id between 1 and 4094")
		return
	}
	if in.NetworkType == "untagged" {
		in.VLANID = nil
	}
	if (in.SubnetCIDR == "") != (in.GatewayIP == "") {
		jsonErr(w, http.StatusBadRequest, "bad_request", "give both a new subnet and its gateway, or neither to keep the current addressing")
		return
	}
	if in.SubnetCIDR != "" && len(in.Pools) == 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a new subnet needs its DHCP pool(s)")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	var cur struct {
		name, netType, parent, subnet string
		vlan                          *int
		enabled                       bool
	}
	err := s.db.QueryRow(ctx, `SELECT name, network_type, parent_interface, vlan_id, subnet_cidr::text, enabled
		  FROM guest_networks WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID).
		Scan(&cur.name, &cur.netType, &cur.parent, &cur.vlan, &cur.subnet, &cur.enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	sameVLAN := (cur.vlan == nil && in.VLANID == nil) || (cur.vlan != nil && in.VLANID != nil && *cur.vlan == *in.VLANID)
	sameAddressing := in.SubnetCIDR == "" || in.SubnetCIDR == cur.subnet
	if in.NetworkType == cur.netType && in.ParentInterface == cur.parent && sameVLAN && sameAddressing {
		jsonErr(w, http.StatusBadRequest, "no_topology_change",
			"the new topology is the same as the current one; edit the network instead of replacing it")
		return
	}

	// Conflicts are refused here, not at apply; the original is excluded because the successor takes the slot it frees.
	if msg := s.topologyConflict(ctx, in.NetworkType, in.ParentInterface, in.VLANID, in.SubnetCIDR, id); msg != "" {
		jsonErr(w, http.StatusConflict, "topology_conflict", msg)
		return
	}

	poolsJSON, _ := json.Marshal(in.Pools)
	var replID string
	err = s.db.QueryRow(ctx, `INSERT INTO iam_v2.guest_network_replacements
		  (tenant_id, site_id, original_network_id, network_type, parent_interface, vlan_id,
		   subnet_cidr, gateway_ip, pools, new_name, reason, created_by)
		VALUES ($1,$2,$3,$4,$5,$6, NULLIF($7,'')::cidr, NULLIF($8,'')::inet, $9::jsonb, NULLIF($10,''), $11, $12)
		RETURNING id::text`,
		s.tenantID, s.siteID, id, in.NetworkType, in.ParentInterface, in.VLANID,
		in.SubnetCIDR, in.GatewayIP, string(poolsJSON), strings.TrimSpace(in.Name),
		strings.TrimSpace(in.Reason), s.actor(r)).Scan(&replID)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "replacement_exists",
				"this client network already has a change waiting to be applied; apply or cancel that one first")
			return
		}
		jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
		return
	}
	s.audit(r, "network.guest.replacement_staged", "guest_network", id, map[string]any{
		"replacement_id": replID, "reason": strings.TrimSpace(in.Reason),
		"to": map[string]any{"type": in.NetworkType, "parent": in.ParentInterface, "vlan": in.VLANID},
	})
	writeJSON(w, http.StatusCreated, map[string]any{
		"replacement_id": replID, "state": replStatePending, "original_network_id": id,
		"carries":               s.replacementCarries(ctx, id, in.SubnetCIDR != "" && in.SubnetCIDR != cur.subnet),
		"packages":              s.packagesLimitedTo(ctx, id),
		"affected":              s.consequencesFor(ctx, []string{id}),
		"external_requirements": externalRequirements(in.NetworkType, in.VLANID, in.ParentInterface),
		"next":                  "nothing has changed yet. Apply the configuration on Client networks, then confirm it.",
	})
}

func (s *server) replacementCarries(ctx context.Context, id string, readdressing bool) map[string]any {
	var pools, reservations, routes int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM dhcp_pools WHERE guest_network_id=$1`, id).Scan(&pools)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM dhcp_reservations WHERE guest_network_id=$1`, id).Scan(&reservations)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, id).Scan(&routes)
	if readdressing {
		reservations = 0 // a reservation is an address inside the old subnet; it cannot follow to a new one
	}
	return map[string]any{"pools": pools, "reservations": reservations, "pms_routes": routes}
}

// Goes through the scoped reader, not the rules table: svc_edged has no SELECT on iam_v2.package_eligibility_rules,
// and swallowing that "permission denied" would report "no packages affected".
func (s *server) packagesLimitedTo(ctx context.Context, networkID string) []string {
	if s.commerce == nil {
		return nil
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM iam_v2.internet_packages
		WHERE tenant_id=$1 AND site_id=$2 AND active AND is_system = false`, s.tenantID, s.siteID)
	if err != nil {
		return nil
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	var out []string
	for _, pid := range ids {
		cur, disabled, err := s.commerce.GetPackageCurrent(ctx, s.tenantID, s.siteID, pid)
		if err != nil || disabled {
			continue
		}
		if ruleNamesNetwork(cur.EligibilityRules, networkID) {
			name := cur.Code
			if d, ok := cur.Display["name"].(string); ok && strings.TrimSpace(d) != "" {
				name = d
			}
			out = append(out, name)
		}
	}
	return out
}

func ruleNamesNetwork(rules []iamv2.EligibilityRule, networkID string) bool {
	for _, rule := range rules {
		if rule.Type != iamv2.RuleSiteNetwork {
			continue
		}
		for _, v := range toStringSlice(rule.Value["guest_network_ids"]) {
			if strings.EqualFold(strings.TrimSpace(v), networkID) {
				return true
			}
		}
	}
	return false
}

func toStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		if ss, ok2 := v.([]string); ok2 {
			return ss
		}
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// States why a requested topology cannot stand beside what the appliance serves; exceptID is the network this change retires.
func (s *server) topologyConflict(ctx context.Context, netType, parent string, vlan *int, subnet, exceptID string) string {
	if netType == "untagged" {
		var name string
		err := s.db.QueryRow(ctx, `SELECT name FROM guest_networks
			WHERE tenant_id=$1 AND site_id=$2 AND enabled AND network_type='untagged'
			  AND parent_interface=$3 AND id::text <> $4 LIMIT 1`, s.tenantID, s.siteID, parent, exceptID).Scan(&name)
		if err == nil {
			return fmt.Sprintf("%s already carries an untagged client network (%q). A port carries at most one untagged network; use a tagged VLAN instead.", parent, name)
		}
	} else if vlan != nil {
		var name string
		err := s.db.QueryRow(ctx, `SELECT name FROM guest_networks
			WHERE tenant_id=$1 AND site_id=$2 AND enabled AND vlan_id=$3
			  AND parent_interface=$4 AND id::text <> $5 LIMIT 1`, s.tenantID, s.siteID, *vlan, parent, exceptID).Scan(&name)
		if err == nil {
			return fmt.Sprintf("VLAN %d is already used on %s by %q. Each VLAN id is unique on a port.", *vlan, parent, name)
		}
	}
	if strings.TrimSpace(subnet) != "" {
		var name string
		err := s.db.QueryRow(ctx, `SELECT name FROM guest_networks
			WHERE tenant_id=$1 AND site_id=$2 AND enabled AND subnet_cidr && $3::cidr
			  AND id::text <> $4 LIMIT 1`, s.tenantID, s.siteID, subnet, exceptID).Scan(&name)
		if err == nil {
			return fmt.Sprintf("subnet %s overlaps client network %q. Every client network needs its own, non-overlapping subnet.", subnet, name)
		}
	}
	// Also check against changes already waiting to be applied: two replacements onto the same VLAN/port or
	// overlapping subnets would otherwise both be accepted and abort the WHOLE apply with a uniqueness error.
	var other, otherReason string
	if netType == "untagged" {
		otherReason = fmt.Sprintf("%s untagged", parent)
		_ = s.db.QueryRow(ctx, `SELECT COALESCE(n.name, g.original_network_id::text)
			  FROM iam_v2.guest_network_replacements g
			  LEFT JOIN guest_networks n ON n.id = g.original_network_id
			 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state IN ('PENDING','APPLIED')
			   AND g.network_type='untagged' AND g.parent_interface=$3
			   AND g.original_network_id::text <> $4 LIMIT 1`,
			s.tenantID, s.siteID, parent, exceptID).Scan(&other)
	} else if vlan != nil {
		otherReason = fmt.Sprintf("VLAN %d on %s", *vlan, parent)
		_ = s.db.QueryRow(ctx, `SELECT COALESCE(n.name, g.original_network_id::text)
			  FROM iam_v2.guest_network_replacements g
			  LEFT JOIN guest_networks n ON n.id = g.original_network_id
			 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state IN ('PENDING','APPLIED')
			   AND g.network_type='vlan' AND g.vlan_id=$3 AND g.parent_interface=$4
			   AND g.original_network_id::text <> $5 LIMIT 1`,
			s.tenantID, s.siteID, *vlan, parent, exceptID).Scan(&other)
	}
	if other != "" {
		return fmt.Sprintf("a change already waiting to be applied moves %q onto %s. Apply or cancel that change first.", other, otherReason)
	}
	if strings.TrimSpace(subnet) != "" {
		_ = s.db.QueryRow(ctx, `SELECT COALESCE(n.name, g.original_network_id::text)
			  FROM iam_v2.guest_network_replacements g
			  LEFT JOIN guest_networks n ON n.id = g.original_network_id
			 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state IN ('PENDING','APPLIED')
			   AND g.subnet_cidr IS NOT NULL AND g.subnet_cidr && $3::cidr
			   AND g.original_network_id::text <> $4 LIMIT 1`,
			s.tenantID, s.siteID, subnet, exceptID).Scan(&other)
		if other != "" {
			return fmt.Sprintf("a change already waiting to be applied re-addresses %q into %s. Apply or cancel that change first.", other, subnet)
		}
	}
	return ""
}

func (s *server) listGuestNetworkReplacements(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	_ = s.reconcileStaleReplacements(ctx, r)
	rows, err := s.db.Query(ctx, `SELECT g.id::text, g.original_network_id::text, g.successor_network_id::text,
		       g.state, g.network_type, g.parent_interface, g.vlan_id, g.subnet_cidr::text, host(g.gateway_ip),
		       g.new_name, g.reason, g.created_by, g.created_at, COALESCE(n.name,'')
		  FROM iam_v2.guest_network_replacements g
		  LEFT JOIN guest_networks n ON n.id = g.original_network_id
		 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state IN ('PENDING','APPLIED')
		 ORDER BY g.created_at DESC`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []stagedReplacement{}
	for rows.Next() {
		var g stagedReplacement
		var created time.Time
		if err := rows.Scan(&g.ID, &g.OriginalID, &g.Successor, &g.State, &g.Type, &g.Parent, &g.VLANID,
			&g.Subnet, &g.Gateway, &g.NewName, &g.Reason, &g.CreatedBy, &created, &g.OriginalName); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		g.CreatedAt = created.UTC().Format(time.RFC3339)
		out = append(out, g)
	}
	writeList(w, out)
}

func (s *server) cancelGuestNetworkReplacement(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
		   SET state='CANCELLED', settled_at=now()
		 WHERE id=$1 AND tenant_id=$2 AND site_id=$3 AND state='PENDING'`, id, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusConflict, "not_pending",
			"that change is not waiting to be applied; an applied change is undone by rolling the configuration back")
		return
	}
	s.audit(r, "network.guest.replacement_cancelled", "guest_network", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

type materialisedReplacement struct {
	ReplacementID string
	OriginalID    string
	SuccessorID   string
	Bridge        string
}

// The only moment a replacement touches public.guest_networks.
func (s *server) materialisePendingReplacements(ctx context.Context, tx pgx.Tx, actor string) ([]materialisedReplacement, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, original_network_id::text, network_type, parent_interface,
		       vlan_id, subnet_cidr::text, host(gateway_ip), pools::text, COALESCE(new_name,'')
		  FROM iam_v2.guest_network_replacements
		 WHERE tenant_id=$1 AND site_id=$2 AND state='PENDING'
		 ORDER BY created_at FOR UPDATE`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	type pending struct {
		id, orig, netType, parent, subnet, gateway, poolsJSON, newName string
		vlan                                                           *int
	}
	var todo []pending
	for rows.Next() {
		var p pending
		var subnet, gateway *string
		if err := rows.Scan(&p.id, &p.orig, &p.netType, &p.parent, &p.vlan, &subnet, &gateway, &p.poolsJSON, &p.newName); err != nil {
			rows.Close()
			return nil, err
		}
		if subnet != nil {
			p.subnet = *subnet
		}
		if gateway != nil {
			p.gateway = *gateway
		}
		todo = append(todo, p)
	}
	rerr := rows.Err()
	rows.Close()
	if rerr != nil {
		return nil, fmt.Errorf("the staged client-network changes could not be read: %w", rerr)
	}

	var out []materialisedReplacement
	for _, p := range todo {
		var old guestNetworkInput
		var oldName, gatewayIP, subnet string
		var dnsRaw string
		err := tx.QueryRow(ctx, `SELECT name, COALESCE(description,''), COALESCE(ssid_label,''), network_type,
			       parent_interface, host(gateway_ip), subnet_cidr::text, dhcp_mode, dns_mode,
			       dns_servers::text, domain_name, lease_default_seconds, lease_min_seconds, lease_max_seconds,
			       captive_portal_enabled, internet_access_enabled, nat_enabled, client_isolation_enabled
			  FROM guest_networks WHERE id=$1 AND tenant_id=$2 AND site_id=$3 FOR UPDATE`,
			p.orig, s.tenantID, s.siteID).Scan(&oldName, &old.Description, &old.SSIDLabel, &old.NetworkType,
			&old.ParentInterface, &gatewayIP, &subnet, &old.DHCPMode, &old.DNSMode, &dnsRaw, &old.DomainName,
			&old.LeaseDefault, &old.LeaseMin, &old.LeaseMax, &old.CaptiveEnabled, &old.InternetEnabled,
			&old.NATEnabled, &old.ClientIsolation)
		if err != nil {
			return nil, fmt.Errorf("the client network a staged change names could not be read: %w", err)
		}
		succ := old
		succ.Name = oldName
		if p.newName != "" {
			succ.Name = p.newName
		}
		succ.NetworkType, succ.ParentInterface, succ.VLANID = p.netType, p.parent, p.vlan
		succ.DNSServers = parseDNSServers([]byte(dnsRaw))
		enabled := true
		succ.Enabled = &enabled
		readdressing := p.subnet != "" && p.subnet != subnet
		if readdressing {
			succ.SubnetCIDR, succ.GatewayIP = p.subnet, p.gateway
			if uerr := json.Unmarshal([]byte(p.poolsJSON), &succ.Pools); uerr != nil {
				return nil, fmt.Errorf("the DHCP pools recorded with this change could not be read: %w", uerr)
			}
		} else {
			succ.SubnetCIDR, succ.GatewayIP = subnet, gatewayIP
			pools, perr := s.loadPoolsTx(ctx, tx, p.orig)
			if perr != nil {
				return nil, fmt.Errorf("the DHCP pools of the client network being replaced could not be read: %w", perr)
			}
			succ.Pools = pools
		}

		// The original frees its slot and subnet first; the uniqueness indexes are partial on `enabled`.
		if _, err := tx.Exec(ctx, `UPDATE guest_networks SET enabled=false, updated_at=now() WHERE id=$1`, p.orig); err != nil {
			return nil, err
		}
		created, err := s.insertGuestNetwork(ctx, tx, succ, []string{p.orig})
		if err != nil {
			return nil, err
		}
		// relay_targets and walled_garden_profile are not on the shared input struct, so they are copied verbatim:
		// dropping relay_targets makes netcfg reject the WHOLE intent (relay mode needs targets) and every later apply fails.
		if _, err := tx.Exec(ctx, `UPDATE guest_networks tgt
			   SET relay_targets = src.relay_targets, walled_garden_profile = src.walled_garden_profile
			  FROM guest_networks src
			 WHERE tgt.id = $2 AND src.id = $1`, p.orig, created.ID); err != nil {
			return nil, fmt.Errorf("carry the relay and walled-garden settings onto the replacement: %w", err)
		}
		if !readdressing {
			if _, err := tx.Exec(ctx, `INSERT INTO dhcp_reservations (guest_network_id, mac, reserved_ip, hostname, description, enabled)
				SELECT $2, mac, reserved_ip, hostname, description, enabled FROM dhcp_reservations WHERE guest_network_id=$1`,
				p.orig, created.ID); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map
			  (tenant_id, site_id, guest_network_id, pms_interface_id, is_default, routing_mode)
			SELECT tenant_id, site_id, $2, pms_interface_id, is_default, routing_mode
			  FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, p.orig, created.ID); err != nil {
			return nil, err
		}
		// The successor is the same client network continuing under another topology: it keeps its own room sign-in policy, nothing from any other network.
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_policy (guest_network_id, tenant_id, site_id, mode, configured_by)
			SELECT $2::uuid, tenant_id, site_id, mode, configured_by FROM iam_v2.guest_network_pms_policy WHERE guest_network_id=$1::uuid`, p.orig, created.ID); err != nil {
			return nil, err
		}
		// Its sign-in narrowing and its portal design belong to the client network, not to the topology it sat on.
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.network_signin_overrides (network_id, tenant_id, site_id, disabled_methods, primary_method, remember_days)
			SELECT $2::uuid, tenant_id, site_id, disabled_methods, primary_method, remember_days FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, p.orig, created.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.portal_profile_assignments (network_id, profile_id, tenant_id, site_id)
			SELECT $2::uuid, profile_id, tenant_id, site_id FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, p.orig, created.ID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
			   SET state='APPLIED', successor_network_id=$2, applied_at=now() WHERE id=$1`, p.id, created.ID); err != nil {
			return nil, err
		}
		out = append(out, materialisedReplacement{ReplacementID: p.id, OriginalID: p.orig, SuccessorID: created.ID, Bridge: created.Bridge})
	}
	return out, nil
}

// Undoes materialisation: the successor goes (pools, reservations and PMS route cascade), the original is enabled
// again and the request is PENDING. Each request reverts in its own transaction so one stuck row cannot block the rest.
func (s *server) revertMaterialised(ctx context.Context, ids []string, reason string) error {
	var failures []string
	for _, replID := range ids {
		if err := s.revertOne(ctx, replID, reason); err != nil {
			failures = append(failures, replID+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func (s *server) revertOne(ctx context.Context, replID, reason string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, replID := range []string{replID} {
		var orig string
		var succ *string
		if err := tx.QueryRow(ctx, `SELECT original_network_id::text, successor_network_id::text
			  FROM iam_v2.guest_network_replacements
			 WHERE id=$1 AND tenant_id=$2 AND site_id=$3 AND state='APPLIED' FOR UPDATE`,
			replID, s.tenantID, s.siteID).Scan(&orig, &succ); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue // already settled or reverted
			}
			return err
		}
		if succ != nil {
			// A successor a guest already touched cannot be deleted and must not fail the revert: it serves during the
			// confirmation window, and sign-ins reference it with foreign keys that do not cascade. As on the ordinary
			// delete route it is kept, disabled; netd renders only enabled networks and the uniqueness indexes are
			// partial on `enabled`. The savepoint lets the transaction survive the failed DELETE.
			if _, err := tx.Exec(ctx, `SAVEPOINT drop_successor`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM guest_networks WHERE id=$1`, *succ); err == nil {
				if _, derr := tx.Exec(ctx, `DELETE FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, *succ); derr != nil {
					return derr
				}
				if _, derr := tx.Exec(ctx, `DELETE FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, *succ); derr != nil {
					return derr
				}
			} else {
				if !isFKViolation(err) {
					return err
				}
				if _, rerr := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT drop_successor`); rerr != nil {
					return rerr
				}
				if _, derr := tx.Exec(ctx, `UPDATE guest_networks SET enabled=false, updated_at=now() WHERE id=$1`, *succ); derr != nil {
					return derr
				}
				slogReplacement("successor_kept_as_history", replID, "a client signed in on it before the change was undone")
			}
			if _, err := tx.Exec(ctx, `RELEASE SAVEPOINT drop_successor`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_policy p WHERE p.guest_network_id=$1::uuid AND NOT EXISTS (SELECT 1 FROM public.guest_networks g WHERE g.id=p.guest_network_id)`, *succ); err != nil {
				return err
			}
		}
		// The original must actually come back: if it was deleted meanwhile (the delete route refuses only enabled
		// networks) a silent no-op would leave a PENDING request naming a missing network and kill every later apply.
		// Such a request is closed as REVERTED.
		tag, err := tx.Exec(ctx, `UPDATE guest_networks SET enabled=true, updated_at=now() WHERE id=$1`, orig)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			if _, cerr := tx.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
				   SET state='REVERTED', settled_at=now(), successor_network_id=NULL, revision_id=NULL
				 WHERE id=$1`, replID); cerr != nil {
				return cerr
			}
			slogReplacement("closed_unrevertable", replID,
				"the client network it replaced no longer exists, so the request cannot be applied again")
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
			   SET state='PENDING', successor_network_id=NULL, applied_at=NULL, revision_id=NULL WHERE id=$1`, replID); err != nil {
			return err
		}
		slogReplacement("reverted", replID, reason)
	}
	return tx.Commit(ctx)
}

// How long an APPLIED replacement may carry no revision id before the reconcile treats it as abandoned. netApply
// commits before netd mints the revision id, and a reconcile in that window used to revert the replacement while
// netd was building the successor's bridge. The grace exceeds netd's 120-second confirmation window.
const staleReplacementGrace = 10 * time.Minute

func (s *server) reconcileStaleReplacements(ctx context.Context, r *http.Request) error {
	if s.tenantID != "" && s.siteID != "" {
		if err := s.reconcileStaleChanges(ctx); err != nil {
			return err
		}
	}
	rows, err := s.db.Query(ctx, `SELECT g.id::text, COALESCE(v.state,''),
		       g.revision_id IS NULL AS unrecorded,
		       g.applied_at < now() - $3::interval AS past_grace
		  FROM iam_v2.guest_network_replacements g
		  LEFT JOIN network_config_revisions v ON v.id = g.revision_id
		 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state='APPLIED'`,
		s.tenantID, s.siteID, staleReplacementGrace.String())
	if err != nil {
		return err
	}
	var stale []string
	retry := false
	for rows.Next() {
		var id, state string
		var unrecorded, pastGrace bool
		if serr := rows.Scan(&id, &state, &unrecorded, &pastGrace); serr != nil {
			rows.Close()
			return fmt.Errorf("a staged client-network change could not be read: %w", serr)
		}
		if unrecorded {
			// The apply has not told us its revision yet. Only an apply that died leaves this standing.
			if pastGrace {
				stale = append(stale, id)
			}
			continue
		}
		switch state {
		case "applying", "pending_confirmation":
		case "active":
			// the apply was KEPT but this request never settled: a package refused to be carried forward.
			retry = true
		case "superseded":
			// Superseded means a newer configuration is live, not that this one failed: netd supersedes the previous
			// active revision when any later one is confirmed. Reverting here would delete a successor still on the wire.
			retry = true
		case "":
			// The named revision has vanished, so the successor can no longer be accounted for and must not be claimed.
			stale = append(stale, id)
		case "failed", "rolled_back":
			// The apply did not survive, so neither may the rows it created.
			stale = append(stale, id)
		default:
			// A draft or validated revision was never applied; revert is the safe reading of a state that should not exist.
			stale = append(stale, id)
		}
	}
	// A truncated result set is not an empty one; report it rather than revert half-way.
	rerr := rows.Err()
	rows.Close()
	if rerr != nil {
		return fmt.Errorf("the staged client-network changes could not be read: %w", rerr)
	}
	if len(stale) > 0 {
		// A failing revert is reported, not swallowed: the rollback route tells the operator, a read path logs and retries.
		if err := s.revertMaterialised(ctx, stale, "the apply was rolled back"); err != nil {
			slog.Error("client network replacements could not be reverted", "replacements", stale, "err", err)
			return fmt.Errorf("%d staged client-network change(s) could not be put back: %w", len(stale), err)
		}
		if r != nil {
			s.audit(r, "network.guest.replacement_reverted", "guest_network", "", map[string]any{"replacements": stale})
		}
	}
	// A settle withheld because a package could not be carried forward is retried here; no second confirm will come.
	if retry {
		_ = s.settleConfirmedReplacements(ctx, r, "")
	}
	return nil
}

func (s *server) settleConfirmedReplacements(ctx context.Context, r *http.Request, revisionID string) []map[string]any {
	// Only requests whose apply was kept. revisionID is the one being confirmed now (not 'active' yet); anything else
	// must be 'active' or 'superseded' (a newer revision was confirmed on top; the successor is still on the wire).
	rows, err := s.db.Query(ctx, `SELECT g.id::text, g.original_network_id::text, g.successor_network_id::text
		  FROM iam_v2.guest_network_replacements g
		  LEFT JOIN network_config_revisions v ON v.id = g.revision_id
		 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state='APPLIED'
		   AND (g.revision_id = NULLIF($3,'')::uuid OR v.state IN ('active','superseded'))`, s.tenantID, s.siteID, revisionID)
	if err != nil {
		return nil
	}
	type done struct{ repl, orig, succ string }
	var settled []done
	for rows.Next() {
		var d done
		var succ *string
		if serr := rows.Scan(&d.repl, &d.orig, &succ); serr != nil {
			slogReplacement("settle_unreadable", "", serr.Error())
			continue
		}
		if succ == nil {
			slogReplacement("settle_skipped", d.repl, "it is APPLIED but names no successor network")
			continue
		}
		d.succ = *succ
		settled = append(settled, d)
	}
	if rerr := rows.Err(); rerr != nil {
		slogReplacement("settle_unreadable", "", rerr.Error())
	}
	rows.Close()

	var out []map[string]any
	for _, d := range settled {
		// The row is claimed before anything is published and released only when the outcome is written: a revert
		// (FOR UPDATE) interleaving with a settle would publish package revisions naming a deleted network, and
		// two tabs could publish twice. Re-reading under the lock makes the loser do nothing.
		claim, cerr := s.claimReplacementForSettle(ctx, d.repl)
		if cerr != nil {
			slogReplacement("settle_skipped", d.repl, cerr.Error())
			continue
		}
		republished, failed, unknown := s.republishPackagesOnto(ctx, d.orig, d.succ)
		if unknown != nil {
			// An outage, not a refusal: settling now would lose the republish permanently, so stay APPLIED and retry on the next read.
			_ = claim.release(ctx)
			slogReplacement("settle_deferred", d.repl, unknown.Error())
			out = append(out, map[string]any{
				"replacement_id": d.repl, "replaced": d.orig, "successor": d.succ, "state": "APPLIED",
				"packages_republished": []string{}, "packages_not_republished": []map[string]string{},
				"next": "the configuration is live; the Internet packages could not be read just now (" +
					unknown.Error() + "), so this change settles on the next read of Client networks.",
			})
			continue
		}
		// Fail closed: CONFIRMED only when every dependent package was carried forward; otherwise it stays APPLIED
		// and is retried on the next read. Guests are not stranded meanwhile, since the retired network keeps its
		// logical identity (lineage), so rules naming it still match a device on the successor.
		if len(failed) > 0 {
			slogReplacement("settle_withheld", d.repl, fmt.Sprintf("%d package(s) could not be carried forward", len(failed)))
			if r != nil {
				s.audit(r, "network.guest.replacement_settle_withheld", "guest_network", d.succ, map[string]any{
					"replacement_id": d.repl, "replaces": d.orig,
					"packages_republished": republished, "packages_not_republished": failed,
				})
			}
			out = append(out, map[string]any{
				"replacement_id": d.repl, "replaced": d.orig, "successor": d.succ, "state": "APPLIED",
				"packages_republished": republished, "packages_not_republished": failed,
				"next": "the configuration is live, but this change is not finished: correct the packages listed and the change settles on its own.",
			})
			_ = claim.release(ctx)
			continue
		}
		// Report the stored state: a failed UPDATE must not be reported as CONFIRMED.
		settledNow := true
		if err := claim.confirm(ctx, revisionID); err != nil {
			slogReplacement("settle_failed", d.repl, err.Error())
			settledNow = false
		}
		if r != nil {
			s.audit(r, "network.guest.replacement_confirmed", "guest_network", d.succ, map[string]any{
				"replacement_id": d.repl, "replaces": d.orig,
				"packages_republished": republished,
			})
		}
		row := map[string]any{
			"replacement_id": d.repl, "replaced": d.orig, "successor": d.succ, "state": "CONFIRMED",
			"packages_republished": republished, "packages_not_republished": []map[string]string{},
		}
		if !settledNow {
			row["state"] = "APPLIED"
			row["next"] = "the configuration is live and the packages are carried forward, but this change could not be recorded as finished; it settles on the next read of Client networks."
		}
		out = append(out, row)
	}
	return out
}

type settleClaim struct {
	tx   pgx.Tx
	repl string
}

func (s *server) claimReplacementForSettle(ctx context.Context, replID string) (*settleClaim, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	var state string
	var succ *string
	if err := tx.QueryRow(ctx, `SELECT state, successor_network_id::text
		  FROM iam_v2.guest_network_replacements
		 WHERE id=$1 AND tenant_id=$2 AND site_id=$3 FOR UPDATE`,
		replID, s.tenantID, s.siteID).Scan(&state, &succ); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if state != "APPLIED" || succ == nil {
		_ = tx.Rollback(ctx)
		return nil, fmt.Errorf("it is no longer waiting to settle (state %s)", state)
	}
	// The successor must still exist: publishing a revision naming a deleted network makes the package eligible for nobody, irreversibly.
	var alive bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM guest_networks WHERE id=$1 AND enabled)`, *succ).Scan(&alive); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	if !alive {
		_ = tx.Rollback(ctx)
		return nil, errors.New("the successor client network is no longer enabled")
	}
	return &settleClaim{tx: tx, repl: replID}, nil
}

func (c *settleClaim) confirm(ctx context.Context, revisionID string) error {
	if _, err := c.tx.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
		   SET state='CONFIRMED', settled_at=now(), revision_id=COALESCE(revision_id, NULLIF($2,'')::uuid)
		 WHERE id=$1 AND state='APPLIED'`, c.repl, revisionID); err != nil {
		_ = c.tx.Rollback(ctx)
		return err
	}
	return c.tx.Commit(ctx)
}

func (c *settleClaim) release(ctx context.Context) error { return c.tx.Rollback(ctx) }

// Pre-flights the package forward for every APPLIED request, writing nothing, before netd is told to keep the
// configuration. Scoped to the revision being confirmed: one stuck replacement must not block every future confirm.
func (s *server) replacementsBlockingConfirm(ctx context.Context, revisionID string) []map[string]any {
	rows, err := s.db.Query(ctx, `SELECT id::text, original_network_id::text, successor_network_id::text
		  FROM iam_v2.guest_network_replacements
		 WHERE tenant_id=$1 AND site_id=$2 AND state='APPLIED' AND successor_network_id IS NOT NULL
		   AND revision_id = NULLIF($3,'')::uuid`,
		s.tenantID, s.siteID, revisionID)
	if err != nil {
		// A pre-flight that cannot run blocks the confirm; returning nil would fail open on a check meant to fail closed.
		return []map[string]any{{"packages_not_preservable": []map[string]string{
			{"package": "(unknown)", "why": "the staged changes could not be read: " + err.Error()}}}}
	}
	type pair struct{ repl, orig, succ string }
	var live []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.repl, &p.orig, &p.succ); err != nil {
			rows.Close()
			return []map[string]any{{"packages_not_preservable": []map[string]string{
				{"package": "(unknown)", "why": "a staged change could not be read: " + err.Error()}}}}
		}
		live = append(live, p)
	}
	rerr := rows.Err()
	rows.Close()
	if rerr != nil {
		return []map[string]any{{"packages_not_preservable": []map[string]string{
			{"package": "(unknown)", "why": "the staged changes could not be read: " + rerr.Error()}}}}
	}

	var blocking []map[string]any
	for _, p := range live {
		_, failed, unknown := s.forwardPackagesOnto(ctx, p.orig, p.succ, true)
		// An outage is not a refusal: blocking would make the watchdog tear down a correct change, and eligibility is
		// safe via the lineage identity. The settle withholds and retries instead.
		if unknown != nil {
			slogReplacement("preflight_unknown", p.repl, unknown.Error())
			continue
		}
		if len(failed) > 0 {
			blocking = append(blocking, map[string]any{
				"replacement_id": p.repl, "replaced": p.orig, "successor": p.succ,
				"packages_not_preservable": failed,
			})
		}
	}
	return blocking
}

// Publishes a new revision of every active package limited to the retired network, with the successor's id in
// its place. History is not rewritten; guests holding an entitlement keep the revision they were granted on.
func (s *server) republishPackagesOnto(ctx context.Context, oldID, newID string) ([]string, []map[string]string, error) {
	return s.forwardPackagesOnto(ctx, oldID, newID, false)
}

// With dryRun it writes nothing and reports which packages would refuse, which makes the confirm step fail closed.
func (s *server) forwardPackagesOnto(ctx context.Context, oldID, newID string, dryRun bool) (republished []string, failed []map[string]string, unknown error) {
	// Failing open loses the republish for good (a CONFIRMED replacement is never revisited), so errors are reported
	// as `unknown`: the caller can tell a refusal from an outage and withholds rather than settling on a guess.
	if s.commerce == nil {
		return nil, nil, errors.New("the package catalogue is not available on this appliance")
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM iam_v2.internet_packages
		WHERE tenant_id=$1 AND site_id=$2 AND active AND is_system = false`, s.tenantID, s.siteID)
	if err != nil {
		return nil, nil, fmt.Errorf("the Internet packages could not be listed: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("the Internet packages could not be read: %w", err)
		}
		ids = append(ids, id)
	}
	rerr := rows.Err()
	rows.Close()
	// A truncated result set is not an empty one.
	if rerr != nil {
		return nil, nil, fmt.Errorf("the Internet packages could not be read: %w", rerr)
	}

	for _, pid := range ids {
		cur, disabled, err := s.commerce.GetPackageCurrent(ctx, s.tenantID, s.siteID, pid)
		if err != nil {
			// Publishing without the rules would silently widen the package to everyone, so it is refused.
			failed = append(failed, map[string]string{"package": pid, "why": err.Error()})
			continue
		}
		if disabled || !ruleNamesNetwork(cur.EligibilityRules, oldID) {
			continue
		}
		// The current revision's contents with only the network id substituted: any field not carried would silently edit pricing, offer window or quota.
		spec := iamv2.PackagePublishSpec{
			TenantID: s.tenantID, SiteID: s.siteID, PackageCode: cur.Code,
			ServicePlanRevisionID: cur.ServicePlanRevisionID,
			PackageType:           cur.PackageType,
			Display:               cur.Display, DurationPolicy: cur.DurationPolicy,
			EligibilityRules:     substituteNetwork(cur.EligibilityRules, oldID, newID),
			GrantTiers:           cur.GrantTiers,
			DataAllocationPolicy: cur.DataAllocationPolicy,
			PriceMinor:           cur.PriceMinor, Currency: cur.Currency,
			AcquisitionMethods: cur.SettlementMethods, RoomChargeMappings: cur.RoomChargeMappings,
			PlanOverrides:       cur.PlanOverrides,
			Renewable:           cur.Renewable,
			MaxPurchasesPerStay: cur.MaxPurchasesPerStay,
			DryRun:              dryRun,
			PriorDurationPolicy: cur.DurationPolicy,
		}
		if cur.CurrencyExponent != nil {
			spec.CurrencyExponent = *cur.CurrencyExponent
		}
		// A sale window must be carried: a parse failure is a refusal, since publishing without it would widen the package silently.
		if vf, verr := parseVisibility(cur.VisibleFrom); verr != nil {
			failed = append(failed, map[string]string{"package": cur.Code, "why": "unreadable visible_from: " + verr.Error()})
			continue
		} else {
			spec.VisibleFrom = vf
		}
		if vu, verr := parseVisibility(cur.VisibleUntil); verr != nil {
			failed = append(failed, map[string]string{"package": cur.Code, "why": "unreadable visible_until: " + verr.Error()})
			continue
		} else {
			spec.VisibleUntil = vu
		}
		res, perr := s.commerce.PublishRevision(ctx, spec)
		if dryRun {
			if perr == nil && !res.Disabled && res.Reason == "validated" {
				republished = append(republished, cur.Code)
				continue
			}
			why := "refused"
			if perr != nil {
				why = perr.Error()
			} else if res.Disabled {
				why = "commerce authoring disabled"
			} else if res.Reason != "" {
				why = res.Reason
			}
			failed = append(failed, map[string]string{"package": cur.Code, "why": why})
			continue
		}
		if perr != nil || res.Disabled || res.Reason != "published" {
			why := "refused"
			if perr != nil {
				why = perr.Error()
			} else if res.Reason != "" {
				why = res.Reason
			}
			failed = append(failed, map[string]string{"package": cur.Code, "why": why})
			continue
		}
		republished = append(republished, cur.Code)
	}
	return republished, failed, nil
}

// The one failure that must never be quiet: the apply did not hold and the staged changes could not be put back,
// so the database alone claims the successor exists. The operator is told now with the exact reason.
func unrevertedStagedChanges(w http.ResponseWriter, staged []string, cause error, why string) {
	slog.Error("client network replacement could not be reverted",
		"why", why, "replacements", staged, "err", cause)
	jsonErr(w, http.StatusInternalServerError, "staged_changes_not_reverted",
		"the configuration was not kept ("+why+") and the staged client-network changes could not be put back: "+
			cause.Error()+". The live network is unchanged; reload Client networks, which retries this, and do not "+
			"apply again until the staged change reads as waiting to be applied.")
}

func describeBlockingPackages(blocking []map[string]any) string {
	var parts []string
	for _, b := range blocking {
		list, _ := b["packages_not_preservable"].([]map[string]string)
		for _, f := range list {
			parts = append(parts, fmt.Sprintf("%s (%s)", f["package"], f["why"]))
		}
	}
	if len(parts) == 0 {
		return "none named"
	}
	return strings.Join(parts, "; ")
}

// Absent is absent (no bound); a present value that will not parse is an error, never a silently dropped bound.
func parseVisibility(raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(*raw))
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func substituteNetwork(rules []iamv2.EligibilityRule, oldID, newID string) []iamv2.EligibilityRule {
	out := make([]iamv2.EligibilityRule, 0, len(rules))
	for _, rule := range rules {
		if rule.Type != iamv2.RuleSiteNetwork {
			out = append(out, rule)
			continue
		}
		ids := toStringSlice(rule.Value["guest_network_ids"])
		next := make([]any, 0, len(ids))
		seen := map[string]bool{}
		for _, v := range ids {
			id := strings.TrimSpace(v)
			if strings.EqualFold(id, oldID) {
				id = newID
			}
			if lower := strings.ToLower(id); !seen[lower] {
				seen[lower] = true
				next = append(next, id)
			}
		}
		value := map[string]any{}
		for k, v := range rule.Value {
			value[k] = v
		}
		value["guest_network_ids"] = next
		out = append(out, iamv2.EligibilityRule{Type: rule.Type, Value: value})
	}
	return out
}

func slogReplacement(event, id, detail string) {
	slog.Info("client network replacement", "event", event, "replacement_id", id, "detail", detail)
}
