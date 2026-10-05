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

// A CLIENT NETWORK'S TOPOLOGY CHANGE IS STAGED, THEN APPLIED, THEN SETTLED (migration 0105).
//
// Topology stays immutable in place: edged refuses a PUT that changes a network's type, port or VLAN, because
// the applied configuration is keyed on the network's BRIDGE and a row whose VLAN moved underneath its bridge
// would be rendered as one thing while the live port carried another. The supported change is a REPLACEMENT,
// and these are its three moments:
//
//	STAGE      POST /guest-networks/{id}/replace records the request and NOTHING else. No row in
//	           public.guest_networks moves, so guest authentication, PMS routing, which network a device is
//	           attributed to and the wire are all exactly as they were. It can be cancelled.
//
//	APPLY      POST /network/apply materialises every staged request first, in one transaction -- the successor
//	           row is created carrying the original's addressing, pools, reservations, settings and PMS route,
//	           and the original is disabled -- and only then asks netd to render and apply. netd answers
//	           synchronously: if validation, the apply itself or a health check failed, the materialisation is
//	           reverted in the same request, so the database and the appliance agree again before the operator
//	           sees the error.
//
//	SETTLE     Confirm keeps it: the request becomes CONFIRMED and every Internet Package whose CURRENT
//	           revision was limited to the original network is republished forward onto the successor, through
//	           the product's own versioned publish, so eligibility keeps meaning what the hotel intended.
//	           Rollback -- by the operator, or by the watchdog when the confirmation window expires -- puts the
//	           rows back and leaves the request PENDING for another attempt.
//
// WHY THE WATCHDOG NEEDS A RECONCILE. netd rolls an unconfirmed apply back on its own, without telling edged,
// so a request can be APPLIED here while the wire is already back on the original. reconcileStaleReplacements
// closes that: any APPLIED request whose revision is no longer in flight is reverted. It is idempotent and
// runs on the paths the operator is already using (listing networks, applying, reading a revision), so the
// window between the watchdog firing and the database agreeing is as long as one page poll.

const (
	replStatePending   = "PENDING"
	replStateApplied   = "APPLIED"
	replStateConfirmed = "CONFIRMED"
	replStateCancelled = "CANCELLED"
)

type stagedReplacement struct {
	ID         string  `json:"id"`
	OriginalID string  `json:"original_network_id"`
	Successor  *string `json:"successor_network_id,omitempty"`
	State      string  `json:"state"`
	Type       string  `json:"network_type"`
	Parent     string  `json:"parent_interface"`
	VLANID     *int    `json:"vlan_id,omitempty"`
	Subnet     *string `json:"subnet_cidr,omitempty"`
	Gateway    *string `json:"gateway_ip,omitempty"`
	NewName    *string `json:"new_name,omitempty"`
	Reason     string  `json:"reason"`
	CreatedBy  string  `json:"created_by"`
	CreatedAt  string  `json:"created_at"`
	// What the operator must know before applying, recomputed on every read.
	OriginalName string `json:"original_network_name,omitempty"`
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

// stageGuestNetworkReplacement records a topology change. It writes nothing outside iam_v2: the client network
// keeps serving its clients, keeps its identity and keeps its PMS route until the operator applies.
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

	// CONFLICTS ARE REFUSED HERE, not three screens later. The successor will take the slot the original frees,
	// so the original is excluded from the comparison.
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
		"carries":  s.replacementCarries(ctx, id, in.SubnetCIDR != "" && in.SubnetCIDR != cur.subnet),
		"packages": s.packagesLimitedTo(ctx, id),
		"next":     "nothing has changed yet. Apply the configuration on Client networks, then confirm it.",
	})
}

// replacementCarries counts what the successor will inherit, so the operator sees it before applying.
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

// packagesLimitedTo names the Internet Packages whose CURRENT revision is limited to one client network.
//
// IT GOES THROUGH THE SCOPED READER, NOT THE RULES TABLE. svc_edged holds INSERT but no SELECT on
// iam_v2.package_eligibility_rules, so the obvious join returns "permission denied" -- and an earlier version
// of this swallowed that error and reported "no packages affected", which is the most misleading answer
// available. iam_v2.p2_package_current_conditions is the reader the authoring form already uses.
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

// topologyConflict states, in one sentence an operator can act on, why a requested topology cannot stand beside
// what this appliance already serves. exceptID is the network about to be retired by this very change.
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
	// ...AND AGAINST THE OTHER CHANGES ALREADY WAITING TO BE APPLIED.
	//
	// This only ever compared the request against networks that are ENABLED NOW, so two replacements of two
	// different networks onto the same VLAN and port -- or onto overlapping subnets -- both accepted. The one
	// live-request index is keyed on original_network_id, so it does not catch it either. The collision then
	// surfaced at the apply, as a uniqueness error that aborted the WHOLE apply: every operator's network change
	// failed, with a message naming a VLAN rather than the staged request responsible, until somebody found and
	// cancelled it. Two operators working on two floors was enough.
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

// listGuestNetworkReplacements returns the live requests (PENDING or APPLIED) for this site.
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

// cancelGuestNetworkReplacement drops a request that was never applied.
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

// ---------------------------------------------------------------------------------- apply / settle ---------

type materialisedReplacement struct {
	ReplacementID string
	OriginalID    string
	SuccessorID   string
	Bridge        string
}

// materialisePendingReplacements turns every staged request into rows, inside the caller's transaction. It is
// the ONLY moment a replacement touches public.guest_networks.
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

		// The original frees its VLAN/port slot and its subnet first; both uniqueness indexes are partial on
		// `enabled`, so the successor can take them in the same transaction.
		if _, err := tx.Exec(ctx, `UPDATE guest_networks SET enabled=false, updated_at=now() WHERE id=$1`, p.orig); err != nil {
			return nil, err
		}
		created, err := s.insertGuestNetwork(ctx, tx, succ, []string{p.orig})
		if err != nil {
			return nil, err
		}
		// THE TWO COLUMNS THE COPY DOES NOT GO THROUGH. relay_targets and walled_garden_profile exist on
		// guest_networks but not on the input struct the create route and insertGuestNetwork share -- there is no
		// API that sets them -- so a replacement silently dropped both. For relay_targets that fails closed and
		// loudly, but in the worst possible place: netcfg refuses a relay-mode network with no targets, so netd
		// rejects THE WHOLE INTENT and every later apply, including unrelated ones, fails naming a network the
		// operator never created. walled_garden_profile was dropped with no validation at all. Copied verbatim
		// here, because a replacement changes a network's topology and nothing else about it.
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
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
			   SET state='APPLIED', successor_network_id=$2, applied_at=now() WHERE id=$1`, p.id, created.ID); err != nil {
			return nil, err
		}
		out = append(out, materialisedReplacement{ReplacementID: p.id, OriginalID: p.orig, SuccessorID: created.ID, Bridge: created.Bridge})
	}
	return out, nil
}

// revertMaterialised undoes materialisation: the successor row goes (its pools, reservations and PMS route
// cascade with it), the original is enabled again, and the request waits as PENDING for another attempt.
// It reverts each request in a TRANSACTION OF ITS OWN. One shared transaction meant a single request that could
// not be put back -- its slot retaken, its original deleted -- aborted the revert of every other one in the
// batch, so one stuck row kept the whole appliance's database disagreeing with the wire.
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
			// A SUCCESSOR A GUEST ALREADY TOUCHED CANNOT BE DELETED, AND MUST NOT FAIL THE REVERT.
			//
			// The successor is live and serving for the whole 120-second confirmation window, so a guest can
			// sign in on it before the operator decides. That writes an auth context, a device appearance and
			// possibly a PMS resolution naming this network -- foreign keys with NO cascade, deliberately,
			// because they are that guest's history. The DELETE then fails, and because it failed inside this
			// transaction the WHOLE revert used to abort: the database went on claiming the successor was the
			// live network while netd had already put the original back on the wire. One guest connecting
			// during the window was enough to produce exactly the disagreement this lifecycle exists to
			// prevent -- and the harder the appliance was being used, the likelier it was.
			//
			// The product already has the right answer for this, on the ordinary delete route: a network with
			// client history is KEPT, disabled, as part of that history. Disabled is what the wire reflects
			// (netd renders only enabled networks) and both uniqueness indexes are partial on `enabled`, so
			// the original can take its VLAN, port and subnet straight back. The savepoint is what lets this
			// transaction survive the failed DELETE and say so.
			if _, err := tx.Exec(ctx, `SAVEPOINT drop_successor`); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `DELETE FROM guest_networks WHERE id=$1`, *succ); err != nil {
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
		}
		// AND THE ORIGINAL MUST ACTUALLY COME BACK. RowsAffected was not checked, so if the original had been
		// deleted in the meantime -- it is disabled while the replacement is applied, and the delete route only
		// refuses ENABLED networks -- this was a silent no-op: the request went back to PENDING naming a network
		// that no longer existed, and every subsequent apply by every operator then died in the materialise with
		// "the client network a staged change names could not be read". A request that cannot be put back is
		// closed as REVERTED instead of pretending it is ready to try again.
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

// staleReplacementGrace is how long an APPLIED replacement may carry NO revision id before this reconcile
// treats it as abandoned.
//
// IT EXISTS TO CLOSE A RACE THAT DESTROYED LIVE APPLIES. netApply materialises in one transaction, COMMITS, then
// calls netd, and only then records which revision carries the change — it cannot record it sooner, because netd
// mints the id. In that window the row reads (state=APPLIED, revision_id=NULL), and this reconcile used to read
// a NULL revision as "the revision is gone" and revert the replacement. Any concurrent request that reconciles —
// a second operator's Client networks page, or the apply's own retry — would therefore delete the successor
// network and re-enable the original WHILE NETD WAS BUILDING THE SUCCESSOR'S BRIDGE, leaving the database and the
// wire describing different networks, which is the one outcome this whole staged lifecycle exists to prevent.
//
// So a NULL revision is "not recorded YET" until this grace has passed. It is comfortably longer than netd's
// 120-second confirmation window, so an apply that is merely slow is never mistaken for an abandoned one; after
// it, the only way to still hold a NULL revision is for edged to have died between the commit and the update,
// and reverting is then the correct, fail-safe answer. This is an internal reconciliation bound, not an
// operational value a hotel administrator would tune.
const staleReplacementGrace = 10 * time.Minute

// reconcileStaleReplacements reverts anything the watchdog rolled back behind edged's back, and retries a settle
// that was withheld. Idempotent.
func (s *server) reconcileStaleReplacements(ctx context.Context, r *http.Request) error {
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
			// still in flight
		case "active":
			// the apply was KEPT but this request never settled: a package refused to be carried forward.
			retry = true
		case "superseded":
			// SUPERSEDED MEANS A NEWER CONFIGURATION IS LIVE, NOT THAT THIS ONE FAILED.
			//
			// netd marks the previous active revision superseded whenever ANY later revision is confirmed
			// (store.markActive). So a replacement whose settle was withheld -- which is exactly the state this
			// reconcile exists to retry, and which by design sits in APPLIED with an 'active' revision -- became
			// 'superseded' the moment the operator made any unrelated network change and confirmed it. This
			// branch used to fall through to "the apply did not survive" and REVERT it: the successor row was
			// deleted and the original re-enabled, while the successor was still on the wire, rendered by the
			// newer revision that had just been confirmed. The next apply then tore the live bridge down.
			//
			// A superseded revision was confirmed before it was superseded, so its replacement is in exactly the
			// position an 'active' one is: kept on the wire, waiting to settle.
			retry = true
		case "":
			// The revision row this replacement names has VANISHED — it was never written, or something
			// deleted it. The successor on the wire can no longer be accounted for by any revision, so the
			// database must not go on claiming it.
			stale = append(stale, id)
		case "failed", "rolled_back":
			// The apply did not survive, so neither may the rows it created.
			stale = append(stale, id)
		default:
			// draft or validated: a revision in these states was never applied, so a replacement cannot
			// legitimately be pointing at one. Unreachable through the product (the id is recorded only after
			// an apply returns), and reverting is the safe reading of a state that should not exist.
			stale = append(stale, id)
		}
	}
	// A TRUNCATED RESULT SET IS NOT AN EMPTY ONE, and here that difference is a missed revert. Reported rather
	// than acted on half-way: reverting the rows that happened to arrive, while silently not knowing about the
	// rest, is worse than saying the reconcile did not run.
	rerr := rows.Err()
	rows.Close()
	if rerr != nil {
		return fmt.Errorf("the staged client-network changes could not be read: %w", rerr)
	}
	if len(stale) > 0 {
		// A REVERT THAT FAILS IS REPORTED, NOT SWALLOWED. The caller decides what to do with it: the rollback
		// route tells the operator outright, while a read path logs and leaves the next read to retry. Either
		// way the appliance never claims it converged when it did not.
		if err := s.revertMaterialised(ctx, stale, "the apply was rolled back"); err != nil {
			slog.Error("client network replacements could not be reverted", "replacements", stale, "err", err)
			return fmt.Errorf("%d staged client-network change(s) could not be put back: %w", len(stale), err)
		}
		if r != nil {
			s.audit(r, "network.guest.replacement_reverted", "guest_network", "", map[string]any{"replacements": stale})
		}
	}
	// A settle WITHHELD because a package could not be carried forward is retried here, on the operator's own
	// next read of the networks. Without this, correcting the package would leave the change APPLIED forever:
	// the configuration is already confirmed, so no second confirm is coming.
	if retry {
		_ = s.settleConfirmedReplacements(ctx, r, "")
	}
	return nil
}

// settleConfirmedReplacements marks applied requests CONFIRMED and republishes the Internet Packages that were
// limited to each original network forward onto its successor. It returns what it did, for the response.
func (s *server) settleConfirmedReplacements(ctx context.Context, r *http.Request, revisionID string) []map[string]any {
	// ONLY requests whose apply was actually KEPT. revisionID is the revision being confirmed in this very
	// request (its row is not 'active' yet); anything else must already be 'active' or 'superseded', which is how
	// a settle withheld earlier for a package that could not be carried forward is retried later without a
	// second confirm — nothing else would ever come back for it. SUPERSEDED counts because it means a newer
	// configuration was confirmed on top, not that this one failed: the successor is still on the wire.
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
			// APPLIED with no successor cannot happen through the apply path; it would mean a row was edited
			// outside it. Named rather than silently skipped.
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
		// THE ROW IS CLAIMED BEFORE ANYTHING IS PUBLISHED, AND RELEASED ONLY WHEN THE OUTCOME IS WRITTEN.
		//
		// This loop used to read its rows, publish package revisions, and then UPDATE with no state guard and no
		// lock -- while revertMaterialised holds FOR UPDATE on the same rows for exactly the opposite purpose.
		// A GET's reconcile reverting a replacement could therefore interleave with a POST's settle: the revert
		// deleted the successor and set the request back to PENDING, and the settle went on to publish package
		// revisions naming a guest_network_id that NO LONGER EXISTED and then stamp the request CONFIRMED with a
		// NULL successor. Those packages were then eligible for nobody, with no lineage row to save them, over a
		// change that never happened on the wire. Two browser tabs polling the networks list could also both
		// pass this point and publish the same revision twice.
		//
		// Claiming the row first makes the revert and the settle take turns, and re-reading the state under that
		// lock means the loser does nothing rather than acting on what it read a moment ago.
		claim, cerr := s.claimReplacementForSettle(ctx, d.repl)
		if cerr != nil {
			slogReplacement("settle_skipped", d.repl, cerr.Error())
			continue
		}
		republished, failed, unknown := s.republishPackagesOnto(ctx, d.orig, d.succ)
		if unknown != nil {
			// NOT A REFUSAL -- AN OUTAGE. Settling now would lose the republish permanently (a CONFIRMED
			// replacement is never revisited), so the request stays APPLIED and is retried on the next read.
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
		// FAIL-CLOSED. A replacement becomes CONFIRMED only when every dependent package was carried forward.
		// If one refused, the request stays APPLIED: the operator sees the exact package and the exact reason,
		// and the settle is retried on their next read of the networks (reconcileStaleReplacements) once they
		// have corrected it. Leaving it APPLIED is what keeps "this change is finished" from meaning two
		// different things on the wire and in the catalogue.
		//
		// What this does NOT do is leave guests stranded in the meantime: the retired network keeps its logical
		// identity (0106), so a rule still naming it is satisfied by a device on the successor whether the
		// republish has happened or not. The republish makes the CURRENT revision say what it means; lineage is
		// what makes the pinned ones keep working.
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
		// THE REPORTED STATE IS THE STORED STATE. This used to log a failed UPDATE and then report CONFIRMED
		// anyway, so an operator could be told a change was finished while the row still read APPLIED. The
		// packages are already forward by now, so the next reconcile converges (the republish finds nothing
		// left to move and marks it), but what is reported is what is true.
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

// settleClaim is a held transaction over one replacement row, with its state verified under the lock.
type settleClaim struct {
	tx   pgx.Tx
	repl string
}

// claimReplacementForSettle locks the row and re-reads it. It refuses unless the request is still APPLIED and
// still names the successor the caller is about to publish packages onto.
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
	// AND THE SUCCESSOR MUST STILL EXIST. Publishing a package revision that names a deleted network makes that
	// package eligible for nobody, on an immutable revision, which is not a failure anyone can undo.
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

// confirm records the settled state inside the claim and releases it.
func (c *settleClaim) confirm(ctx context.Context, revisionID string) error {
	if _, err := c.tx.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
		   SET state='CONFIRMED', settled_at=now(), revision_id=COALESCE(revision_id, NULLIF($2,'')::uuid)
		 WHERE id=$1 AND state='APPLIED'`, c.repl, revisionID); err != nil {
		_ = c.tx.Rollback(ctx)
		return err
	}
	return c.tx.Commit(ctx)
}

// release drops the claim without recording anything, leaving the request APPLIED for the next attempt.
func (c *settleClaim) release(ctx context.Context) error { return c.tx.Rollback(ctx) }

// replacementsBlockingConfirm pre-flights the package forward for every APPLIED request, writing nothing. It is
// called BEFORE netd is told to keep the configuration, so a package that cannot be carried forward refuses the
// confirm outright rather than leaving the wire permanent and the catalogue behind it.
// It is SCOPED TO THE REVISION BEING CONFIRMED. Without that scope one stuck replacement -- a settle withheld
// earlier, whose revision is already active -- blocked the confirmation of EVERY future revision, for good: each
// later apply was then rolled back by the watchdog at 120 seconds, the appliance lost the ability to change its
// networking at all, and the refusal named a package that had nothing to do with the change the operator had
// just made. A confirm answers for its own revision.
func (s *server) replacementsBlockingConfirm(ctx context.Context, revisionID string) []map[string]any {
	rows, err := s.db.Query(ctx, `SELECT id::text, original_network_id::text, successor_network_id::text
		  FROM iam_v2.guest_network_replacements
		 WHERE tenant_id=$1 AND site_id=$2 AND state='APPLIED' AND successor_network_id IS NOT NULL
		   AND revision_id = NULLIF($3,'')::uuid`,
		s.tenantID, s.siteID, revisionID)
	if err != nil {
		// A PRE-FLIGHT THAT CANNOT RUN BLOCKS THE CONFIRM. It used to return nil, which reads as "nothing stands
		// in the way" -- failing open on the one check whose whole purpose is to fail closed.
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
		// AN OUTAGE IS NOT A REFUSAL, AND MUST NOT HOLD THE WIRE HOSTAGE. If the catalogue cannot be read at all,
		// blocking the confirm would mean the watchdog tears down a correct network change because a package
		// service is unavailable -- and eligibility is not at risk meanwhile, because the retired network keeps
		// its logical identity (0106) whether the republish has happened or not. The settle withholds instead and
		// retries, which is where that decision belongs.
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

// republishPackagesOnto publishes a NEW revision of every active package whose CURRENT revision is limited to
// the retired network, with the successor's id in place of it. History is never rewritten: each package gains
// a forward revision, exactly as an operator editing it would, so future guests are offered what the hotel
// intended. Guests already holding an entitlement keep the revision they were granted on.
func (s *server) republishPackagesOnto(ctx context.Context, oldID, newID string) ([]string, []map[string]string, error) {
	return s.forwardPackagesOnto(ctx, oldID, newID, false)
}

// forwardPackagesOnto is republishPackagesOnto with a dryRun switch. With dryRun it writes nothing and reports
// exactly which packages WOULD refuse, which is what makes the confirm step fail-closed: a replacement must not
// become permanent while a package that depends on the retired network could not be carried forward.
func (s *server) forwardPackagesOnto(ctx context.Context, oldID, newID string, dryRun bool) (republished []string, failed []map[string]string, unknown error) {
	// FAILING OPEN HERE LOSES THE REPUBLISH FOR GOOD. Both of these used to `return nil, nil`, which the settle
	// reads as "there was nothing to carry forward" and then marks the replacement CONFIRMED -- and a CONFIRMED
	// replacement is excluded from the reconcile and from the settle, so nothing ever comes back for it. One
	// transient error on one SELECT permanently left the hotel's packages naming a retired network id, with only
	// the lineage view (0106) still making them work. They are reported as `unknown` instead: the caller can tell
	// "this package refuses" (a decision) from "I could not find out" (an outage), and withholds rather than
	// settling on a guess.
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
	// A TRUNCATED RESULT SET IS NOT AN EMPTY ONE. Without this, a connection dropped mid-iteration looked exactly
	// like a site with no packages.
	if rerr != nil {
		return nil, nil, fmt.Errorf("the Internet packages could not be read: %w", rerr)
	}

	for _, pid := range ids {
		cur, disabled, err := s.commerce.GetPackageCurrent(ctx, s.tenantID, s.siteID, pid)
		if err != nil {
			// ErrPackageConditionsUnreadable lands here: publishing without the rules would silently widen the
			// package to everyone, so it is refused and reported instead.
			failed = append(failed, map[string]string{"package": pid, "why": err.Error()})
			continue
		}
		if disabled || !ruleNamesNetwork(cur.EligibilityRules, oldID) {
			continue
		}
		// EVERYTHING THE CURRENT REVISION SAYS, with the network id substituted and NOTHING else touched.
		// This is the whole contract of an automatic republish: the operator did not ask for a package change,
		// they asked for a cabling change, so any field this spec fails to carry is a silent edit to their
		// pricing, their offer window or their quota on an immutable revision they cannot correct.
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
		}
		if cur.CurrencyExponent != nil {
			spec.CurrencyExponent = *cur.CurrencyExponent
		}
		// A SALE WINDOW IS NOT OPTIONAL TO CARRY. This used to swallow the parse error, and the reader used to
		// hand back a format time.RFC3339 cannot parse -- so a seasonal package came out of a replacement
		// permanently visible. The reader now renders RFC3339; a value that still will not parse is a refusal,
		// not a dropped field, because publishing without the window would widen the package silently.
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
			// The dry run's own success word, so a caller cannot mistake "it would publish" for "it published".
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

// unreverted StagedChanges answers the one failure that must never be quiet: the apply did not hold AND the
// staged client-network changes could not be put back.
//
// This used to be logged and then hidden behind netd's own "the apply failed" response, which is the most
// misleading pair of facts available: the operator reads a failure, assumes nothing changed, and the database is
// meanwhile the only thing claiming the successor network exists. The reconcile on the next read will try again
// -- it is idempotent, and the revision is not 'active', so the replacement is still classified as stale -- but
// the operator is told NOW, with the exact reason, because the appliance cannot promise it converged.
func unrevertedStagedChanges(w http.ResponseWriter, staged []string, cause error, why string) {
	slog.Error("client network replacement could not be reverted",
		"why", why, "replacements", staged, "err", cause)
	jsonErr(w, http.StatusInternalServerError, "staged_changes_not_reverted",
		"the configuration was not kept ("+why+") and the staged client-network changes could not be put back: "+
			cause.Error()+". The live network is unchanged; reload Client networks, which retries this, and do not "+
			"apply again until the staged change reads as waiting to be applied.")
}

// describeBlockingPackages renders "Lobby free (invalid_duration_policy); Premium (no_grant_tiers)".
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

// parseVisibility turns the reader's RFC3339 sale-window bound into a time. Absent is absent (no bound); a
// present value that will not parse is an error, never a silently dropped bound.
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
