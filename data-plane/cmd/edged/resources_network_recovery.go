package main

// Recovery of the client networks: Restore Last Working, Restore Factory Defaults and Full Client Networking Reset. Each is staged like an activation
// (resources_network_changes.go): nothing moves until the operator applies, and a failed apply, a rollback or a lapsed confirmation puts every setting back.
// Networks are never deleted, so their identity and history survive; what is switched off stays on the list. Guest records, licensing, the WAN/management port,
// the PMS configuration and the portal designs themselves are not touched by any of them.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

const (
	opRestoreLastWorking = "restore_last_working"
	opFactoryDefaults    = "factory_defaults"
	opFullReset          = "full_reset"
)

var recoveryGroupKind = map[string]string{opRestoreLastWorking: "RESTORE", opFactoryDefaults: "FACTORY_RESET", opFullReset: "FULL_RESET"}

type signinState struct {
	Disabled []string `json:"disabled_methods"`
	Primary  string   `json:"primary_method"`
	Remember *int     `json:"remember_days"`
}

type layerState struct {
	PortalProfile *string      `json:"portal_profile"`
	Signin        *signinState `json:"signin"`
	PMS           []string     `json:"pms_interfaces"`
}

type reviewSpec struct {
	Layer    string `json:"layer"`
	Detail   string `json:"detail"`
	PausePMS bool   `json:"pause_pms"`
}

type layerTarget struct {
	SetPortal     bool         `json:"set_portal,omitempty"`
	PortalProfile *string      `json:"portal_profile,omitempty"`
	SetSignin     bool         `json:"set_signin,omitempty"`
	Signin        *signinState `json:"signin,omitempty"`
	Review        []reviewSpec `json:"review,omitempty"`
}

type recoveryTarget struct {
	Row    *netcfg.GuestNetwork `json:"row,omitempty"`
	Layers *layerTarget         `json:"layers,omitempty"`
	// Factory marks the prepared network a reset put in place; Created marks a network that reset itself inserted, so cancelling it before any apply removes it again.
	Factory bool `json:"factory,omitempty"`
	Created bool `json:"created,omitempty"`
}

type recoveryBefore struct {
	Row       *netcfg.GuestNetwork `json:"row,omitempty"`
	Layers    *layerState          `json:"layers,omitempty"`
	ReviewIDs []string             `json:"review_ids,omitempty"`
}

type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ---- reading a network and its layers -------------------------------------------------------------------------------------------

const networkSelect = `SELECT id::text, name, COALESCE(ssid_label,''), enabled, network_type, parent_interface, COALESCE(vlan_id,0), bridge_name,
	host(gateway_ip), text(subnet_cidr), masklen(subnet_cidr), dhcp_mode, dns_mode, COALESCE(dns_servers,'[]'::jsonb), domain_name,
	lease_default_seconds, lease_min_seconds, lease_max_seconds, COALESCE(relay_targets,'[]'::jsonb), captive_portal_enabled, internet_access_enabled,
	nat_enabled, client_isolation_enabled FROM guest_networks`

func scanNetwork(row pgx.Row) (netcfg.GuestNetwork, error) {
	var n netcfg.GuestNetwork
	var dns, relay []byte
	err := row.Scan(&n.ID, &n.Name, &n.SSIDLabel, &n.Enabled, &n.NetworkType, &n.ParentInterface, &n.VLANID, &n.BridgeName,
		&n.GatewayIP, &n.SubnetCIDR, &n.PrefixLen, &n.DHCPMode, &n.DNSMode, &dns, &n.DomainName,
		&n.LeaseDefault, &n.LeaseMin, &n.LeaseMax, &relay, &n.CaptiveEnabled, &n.InternetEnabled, &n.NATEnabled, &n.ClientIsolation)
	if err != nil {
		return n, err
	}
	_ = json.Unmarshal(dns, &n.DNSServers)
	_ = json.Unmarshal(relay, &n.RelayTargets)
	return n, nil
}

func (s *server) readNetwork(ctx context.Context, q queryer, id string) (netcfg.GuestNetwork, error) {
	n, err := scanNetwork(q.QueryRow(ctx, networkSelect+` WHERE id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid`, id, s.tenantID, s.siteID))
	if err != nil {
		return n, err
	}
	rows, err := q.Query(ctx, `SELECT host(start_ip), host(end_ip) FROM dhcp_pools WHERE guest_network_id=$1::uuid ORDER BY sort_order, start_ip`, id)
	if err != nil {
		return n, err
	}
	defer rows.Close()
	for rows.Next() {
		var p netcfg.Pool
		if err := rows.Scan(&p.StartIP, &p.EndIP); err != nil {
			return n, err
		}
		n.Pools = append(n.Pools, p)
	}
	return n, rows.Err()
}

func (s *server) siteNetworkIDs(ctx context.Context, q queryer) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT id::text FROM guest_networks WHERE tenant_id=$1::uuid AND site_id=$2::uuid ORDER BY COALESCE(vlan_id,0), name`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *server) readLayers(ctx context.Context, q queryer) (map[string]layerState, error) {
	out := map[string]layerState{}
	ids, err := s.siteNetworkIDs(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		out[id] = layerState{PMS: []string{}}
	}
	rows, err := q.Query(ctx, `SELECT network_id::text, profile_id::text FROM iam_v2.portal_profile_assignments WHERE tenant_id=$1::uuid AND site_id=$2::uuid`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n, p string
		if err := rows.Scan(&n, &p); err != nil {
			rows.Close()
			return nil, err
		}
		if l, ok := out[n]; ok {
			pp := p
			l.PortalProfile = &pp
			out[n] = l
		}
	}
	rows.Close()
	orows, err := q.Query(ctx, `SELECT network_id::text, disabled_methods, COALESCE(primary_method,''), remember_days FROM iam_v2.network_signin_overrides WHERE tenant_id=$1::uuid AND site_id=$2::uuid`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var n string
		var st signinState
		if err := orows.Scan(&n, &st.Disabled, &st.Primary, &st.Remember); err != nil {
			orows.Close()
			return nil, err
		}
		sort.Strings(st.Disabled)
		if l, ok := out[n]; ok {
			l.Signin = &st
			out[n] = l
		}
	}
	orows.Close()
	prows, err := q.Query(ctx, `SELECT guest_network_id::text, pms_interface_id::text FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1::uuid AND site_id=$2::uuid ORDER BY 2`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer prows.Close()
	for prows.Next() {
		var n, p string
		if err := prows.Scan(&n, &p); err != nil {
			return nil, err
		}
		if l, ok := out[n]; ok {
			l.PMS = append(l.PMS, p)
			out[n] = l
		}
	}
	return out, prows.Err()
}

// captureLayers records, for the revision just confirmed, what each network is layered with. Failure to record never undoes the confirmation.
func (s *server) captureLayers(ctx context.Context, revisionID string) error {
	if s.tenantID == "" || s.siteID == "" || revisionID == "" {
		return nil
	}
	layers, err := s.readLayers(ctx, s.db)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(layers)
	_, err = s.db.Exec(ctx, `INSERT INTO iam_v2.network_layer_snapshots (revision_id, tenant_id, site_id, layers) VALUES ($1::uuid,$2::uuid,$3::uuid,$4::jsonb)
		ON CONFLICT (revision_id) DO UPDATE SET layers = EXCLUDED.layers, taken_at = now()`, revisionID, s.tenantID, s.siteID, string(raw))
	return err
}

func (s *server) lastLayerSnapshot(ctx context.Context) (map[string]layerState, bool) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT layers FROM iam_v2.network_layer_snapshots WHERE tenant_id=$1::uuid AND site_id=$2::uuid ORDER BY taken_at DESC LIMIT 1`, s.tenantID, s.siteID).Scan(&raw)
	if err != nil {
		return nil, false
	}
	var out map[string]layerState
	if json.Unmarshal(raw, &out) != nil {
		return nil, false
	}
	return out, true
}

// ---- applying and putting back (inside the apply transaction) -----------------------------------------------------------

// settingsDiffer lists the operator-editable settings of a network that differ. Identity (type, port, VLAN, bridge) is not among them: it changes only by a replacement.
func settingsDiffer(cur, want netcfg.GuestNetwork) []string {
	var d []string
	add := func(name string, differs bool) {
		if differs {
			d = append(d, name)
		}
	}
	add("name", cur.Name != want.Name)
	add("SSID label", cur.SSIDLabel != want.SSIDLabel)
	add("gateway address", cur.GatewayIP != want.GatewayIP)
	add("subnet", cur.SubnetCIDR != want.SubnetCIDR)
	add("DHCP mode", cur.DHCPMode != want.DHCPMode)
	add("DNS mode", cur.DNSMode != want.DNSMode)
	add("DNS servers", strings.Join(cur.DNSServers, ",") != strings.Join(want.DNSServers, ","))
	add("domain name", cur.DomainName != want.DomainName)
	add("lease times", cur.LeaseDefault != want.LeaseDefault || cur.LeaseMin != want.LeaseMin || cur.LeaseMax != want.LeaseMax)
	add("relay targets", strings.Join(cur.RelayTargets, ",") != strings.Join(want.RelayTargets, ","))
	add("captive portal", cur.CaptiveEnabled != want.CaptiveEnabled)
	add("internet access", cur.InternetEnabled != want.InternetEnabled)
	add("address translation", cur.NATEnabled != want.NATEnabled)
	add("client isolation", cur.ClientIsolation != want.ClientIsolation)
	add("DHCP pools", poolKey(cur.Pools) != poolKey(want.Pools))
	return d
}

func poolKey(ps []netcfg.Pool) string {
	k := make([]string, 0, len(ps))
	for _, p := range ps {
		k = append(k, p.StartIP+"-"+p.EndIP)
	}
	sort.Strings(k)
	return strings.Join(k, ",")
}

func (s *server) writeSettings(ctx context.Context, tx pgx.Tx, id string, n netcfg.GuestNetwork) error {
	dns, _ := json.Marshal(n.DNSServers)
	relay, _ := json.Marshal(n.RelayTargets)
	if string(dns) == "null" {
		dns = []byte("[]")
	}
	if string(relay) == "null" {
		relay = []byte("[]")
	}
	if _, err := tx.Exec(ctx, `UPDATE guest_networks SET name=$2, ssid_label=NULLIF($3,''), gateway_ip=$4::inet, subnet_cidr=$5::cidr,
		gateway_cidr=$19::inet, dhcp_mode=$6, dns_mode=$7, dns_servers=$8::jsonb, domain_name=$9,
		lease_default_seconds=$10, lease_min_seconds=$11, lease_max_seconds=$12, relay_targets=$13::jsonb,
		captive_portal_enabled=$14, internet_access_enabled=$15, nat_enabled=$16, client_isolation_enabled=$17, portal_url=$18, updated_at=now()
		WHERE id=$1::uuid`, id, n.Name, n.SSIDLabel, n.GatewayIP, n.SubnetCIDR, string(n.DHCPMode), n.DNSMode, string(dns), n.DomainName,
		n.LeaseDefault, n.LeaseMin, n.LeaseMax, string(relay), n.CaptiveEnabled, n.InternetEnabled, n.NATEnabled, n.ClientIsolation,
		netcfg.PortalURLFor(n.GatewayIP, 8380), gatewayCIDR(n)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM dhcp_pools WHERE guest_network_id=$1::uuid`, id); err != nil {
		return err
	}
	for i, p := range n.Pools {
		if _, err := tx.Exec(ctx, `INSERT INTO dhcp_pools (guest_network_id, start_ip, end_ip, sort_order) VALUES ($1::uuid,$2::inet,$3::inet,$4)`, id, p.StartIP, p.EndIP, i); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) writeSignin(ctx context.Context, tx pgx.Tx, id string, st *signinState) error {
	if st == nil {
		_, err := tx.Exec(ctx, `DELETE FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, id)
		return err
	}
	off := st.Disabled
	if off == nil {
		off = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.network_signin_overrides (network_id, tenant_id, site_id, disabled_methods, primary_method, remember_days)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4,NULLIF($5,''),$6)
		ON CONFLICT (network_id) DO UPDATE SET disabled_methods=EXCLUDED.disabled_methods, primary_method=EXCLUDED.primary_method, remember_days=EXCLUDED.remember_days, updated_at=now()`,
		id, s.tenantID, s.siteID, off, st.Primary, st.Remember)
	return err
}

func (s *server) writePortal(ctx context.Context, tx pgx.Tx, id string, profile *string) error {
	if profile == nil {
		_, err := tx.Exec(ctx, `DELETE FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, id)
		return err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO iam_v2.portal_profile_assignments (network_id, profile_id, tenant_id, site_id)
		SELECT $1::uuid, p.id, p.tenant_id, p.site_id FROM iam_v2.portal_profiles p WHERE p.id=$2::uuid AND p.tenant_id=$3::uuid AND p.site_id=$4::uuid
		ON CONFLICT (network_id) DO UPDATE SET profile_id=EXCLUDED.profile_id, assigned_at=now()`, id, *profile, s.tenantID, s.siteID)
	if err == nil && tag.RowsAffected() == 0 {
		_, err = tx.Exec(ctx, `DELETE FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, id)
	}
	return err
}

// applyRecoveryTarget writes the staged settings and layers and returns what they replaced, so a revert can put it all back.
func (s *server) applyRecoveryTarget(ctx context.Context, tx pgx.Tx, changeID, network string, t recoveryTarget) (recoveryBefore, error) {
	var b recoveryBefore
	if t.Row != nil {
		cur, err := s.readNetwork(ctx, tx, network)
		if err != nil {
			return b, err
		}
		b.Row = &cur
		if err := s.writeSettings(ctx, tx, network, *t.Row); err != nil {
			return b, err
		}
	}
	if t.Layers == nil {
		return b, nil
	}
	cur, err := s.readLayers(ctx, tx)
	if err != nil {
		return b, err
	}
	l := cur[network]
	b.Layers = &l
	if t.Layers.SetPortal {
		if err := s.writePortal(ctx, tx, network, t.Layers.PortalProfile); err != nil {
			return b, err
		}
	}
	if t.Layers.SetSignin {
		if err := s.writeSignin(ctx, tx, network, t.Layers.Signin); err != nil {
			return b, err
		}
	}
	for _, rv := range t.Layers.Review {
		var id string
		err := tx.QueryRow(ctx, `INSERT INTO iam_v2.network_review_items (tenant_id, site_id, network_id, layer, detail, paused_pms, change_id)
			VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7::uuid) ON CONFLICT (network_id, layer) WHERE resolved_at IS NULL DO NOTHING RETURNING id::text`,
			s.tenantID, s.siteID, network, rv.Layer, rv.Detail, rv.PausePMS, changeID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return b, err
		}
		b.ReviewIDs = append(b.ReviewIDs, id)
		if rv.PausePMS {
			// The pause is the appliance's only when the operator had not already switched room sign-in off here; otherwise acknowledging must not lift theirs.
			var already bool
			if err := tx.QueryRow(ctx, `SELECT COALESCE('pms' = ANY(disabled_methods), false) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, network).Scan(&already); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return b, err
			}
			if already {
				if _, err := tx.Exec(ctx, `UPDATE iam_v2.network_review_items SET paused_pms=false WHERE id=$1::uuid`, id); err != nil {
					return b, err
				}
			} else if err := s.pausePMS(ctx, tx, network); err != nil {
				return b, err
			}
		}
	}
	return b, nil
}

func (s *server) pausePMS(ctx context.Context, tx pgx.Tx, network string) error {
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.network_signin_overrides (network_id, tenant_id, site_id, disabled_methods)
		VALUES ($1::uuid,$2::uuid,$3::uuid,ARRAY['pms'])
		ON CONFLICT (network_id) DO UPDATE SET disabled_methods = (SELECT array_agg(DISTINCT m ORDER BY m) FROM unnest(iam_v2.network_signin_overrides.disabled_methods || ARRAY['pms']) m), updated_at=now()`,
		network, s.tenantID, s.siteID)
	return err
}

// revertRecoveryTarget puts back what applyRecoveryTarget replaced.
func (s *server) revertRecoveryTarget(ctx context.Context, tx pgx.Tx, network string, b recoveryBefore) error {
	if b.Row != nil {
		if err := s.writeSettings(ctx, tx, network, *b.Row); err != nil {
			return err
		}
	}
	if b.Layers != nil {
		if err := s.writePortal(ctx, tx, network, b.Layers.PortalProfile); err != nil {
			return err
		}
		if err := s.writeSignin(ctx, tx, network, b.Layers.Signin); err != nil {
			return err
		}
	}
	if len(b.ReviewIDs) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.network_review_items WHERE id = ANY($1::uuid[])`, b.ReviewIDs); err != nil {
			return err
		}
	}
	return nil
}

// ---- the plan ----------------------------------------------------------------------------------------------------------------

type planNetwork struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Action  string   `json:"action"`
	Changes []string `json:"changes,omitempty"`
	Clients int      `json:"active_clients"`
}

type reviewLine struct {
	NetworkID string `json:"network_id"`
	Network   string `json:"network"`
	Layer     string `json:"layer"`
	State     string `json:"state"`
	Detail    string `json:"detail"`
}

type recoveryPlan struct {
	Op              string        `json:"op"`
	Summary         string        `json:"summary"`
	Networks        []planNetwork `json:"networks"`
	Review          []reviewLine  `json:"review"`
	Cannot          []string      `json:"cannot_restore"`
	Conflicts       []string      `json:"conflicts"`
	Protected       []string      `json:"protected"`
	SwitchChecklist []string      `json:"switch_checklist"`
	TypedName       string        `json:"confirm_site_name,omitempty"`
	Stageable       bool          `json:"stageable"`
	items           []stagedItem
}

type stagedItem struct {
	NetworkID string
	Kind      string
	Target    recoveryTarget
	Create    *netcfg.GuestNetwork
}

var protectedData = []string{
	"Client networks are never deleted: their identity and history stay, and what is switched off remains on the list.",
	"Guest records, sessions, devices and packages.",
	"Licensing and the signed site assignment.",
	"The WAN and management port and its address.",
	"PMS configuration and its routing.",
	"The portal designs themselves (only which network shows which can be reset).",
}

func recoveryChecklist() []string {
	return []string{
		"The switch port and the virtual switch port group pass the VLANs the restored networks use. The appliance cannot verify this.",
		"The access point or controller maps each Wi-Fi network to the VLAN it did before.",
		"If a restored network was the untagged one, the switch port delivers the untagged VLAN to this appliance.",
	}
}

func (s *server) clientsOn(ctx context.Context, id string) int {
	var n int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions se JOIN guest_networks g ON g.id=$1::uuid
		 WHERE se.state='active' AND se.ended IS NULL AND se.ip IS NOT NULL AND se.ip <<= g.subnet_cidr`, id).Scan(&n)
	return n
}

func (s *server) activeIntent(ctx context.Context) ([]netcfg.GuestNetwork, bool) {
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT intent FROM network_config_revisions WHERE state='active' ORDER BY seq DESC LIMIT 1`).Scan(&raw)
	if err != nil || len(raw) == 0 {
		return nil, false
	}
	var out []netcfg.GuestNetwork
	if json.Unmarshal(raw, &out) != nil {
		return nil, false
	}
	return out, true
}

func (s *server) factoryNetworkID(ctx context.Context) string {
	var id string
	if s.db.QueryRow(ctx, `SELECT c.network_id::text FROM iam_v2.guest_network_changes c JOIN guest_networks g ON g.id=c.network_id
		 WHERE c.tenant_id=$1::uuid AND c.site_id=$2::uuid AND c.state <> 'CANCELLED'
		   AND (c.group_kind='FACTORY_ACTIVATE' OR c.target->>'factory' = 'true') ORDER BY c.created_at DESC LIMIT 1`,
		s.tenantID, s.siteID).Scan(&id) == nil {
		return id
	}
	_ = s.db.QueryRow(ctx, `SELECT id::text FROM guest_networks WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND subnet_cidr = $3::cidr AND lower(name)=lower($4) LIMIT 1`,
		s.tenantID, s.siteID, netcfg.FactorySubnet, netcfg.FactoryName).Scan(&id)
	return id
}

func equalStrings(a, b []string) bool {
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, ",") == strings.Join(y, ",")
}

func sameSignin(a, b *signinState) bool {
	if a == nil || b == nil {
		return a == b
	}
	return equalStrings(a.Disabled, b.Disabled) && a.Primary == b.Primary && ((a.Remember == nil && b.Remember == nil) || (a.Remember != nil && b.Remember != nil && *a.Remember == *b.Remember))
}

func samePortal(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func (s *server) profileExists(ctx context.Context, id string) bool {
	var one int
	return s.db.QueryRow(ctx, `SELECT 1 FROM iam_v2.portal_profiles WHERE id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid`, id, s.tenantID, s.siteID).Scan(&one) == nil
}

func (s *server) planRecovery(ctx context.Context, op string) (*recoveryPlan, error) {
	plan := &recoveryPlan{Op: op, Networks: []planNetwork{}, Review: []reviewLine{}, Cannot: []string{}, Conflicts: []string{}, Protected: protectedData, SwitchChecklist: recoveryChecklist()}
	ids, err := s.siteNetworkIDs(ctx, s.db)
	if err != nil {
		return nil, err
	}
	cur := map[string]netcfg.GuestNetwork{}
	for _, id := range ids {
		n, err := s.readNetwork(ctx, s.db, id)
		if err != nil {
			return nil, err
		}
		cur[id] = n
	}
	layers, err := s.readLayers(ctx, s.db)
	if err != nil {
		return nil, err
	}
	add := func(n netcfg.GuestNetwork, action string, changes []string) {
		plan.Networks = append(plan.Networks, planNetwork{ID: n.ID, Name: n.Name, Action: action, Changes: changes, Clients: s.clientsOn(ctx, n.ID)})
	}

	switch op {
	case opRestoreLastWorking:
		plan.Summary = "Put every client network back to the configuration the appliance last confirmed working. Unconfirmed edits to settings are discarded; networks created since stay, switched off."
		intent, ok := s.activeIntent(ctx)
		if !ok {
			plan.Cannot = append(plan.Cannot, "Nothing has been confirmed on this appliance yet, so there is no last working configuration to return to. Restore Factory Defaults is available instead.")
			return plan, nil
		}
		snap, haveSnap := s.lastLayerSnapshot(ctx)
		inIntent := map[string]bool{}
		for _, want := range intent {
			inIntent[want.ID] = true
			have, exists := cur[want.ID]
			if !exists {
				plan.Cannot = append(plan.Cannot, fmt.Sprintf("%s was deleted after it was last confirmed and cannot be brought back from here.", want.Name))
				continue
			}
			if have.NetworkType != want.NetworkType || have.ParentInterface != want.ParentInterface || have.VLANID != want.VLANID || have.BridgeName != want.BridgeName {
				plan.Cannot = append(plan.Cannot, fmt.Sprintf("%s was moved to another port or VLAN after it was last confirmed. That is changed with a replacement, not restored here.", want.Name))
				continue
			}
			diffs := settingsDiffer(have, want)
			var tgt recoveryTarget
			if len(diffs) > 0 {
				w := want
				tgt.Row = &w
			}
			lt := s.layerReview(ctx, plan, have, layers[want.ID], snap, haveSnap)
			if lt != nil {
				tgt.Layers = lt
			}
			switch {
			case have.Enabled != want.Enabled:
				kind, action := "DISABLE", "switch_off"
				if want.Enabled {
					kind, action = "ENABLE", "switch_on"
				}
				plan.items = append(plan.items, stagedItem{NetworkID: want.ID, Kind: kind, Target: tgt})
				add(have, action, diffs)
			case tgt.Row != nil || tgt.Layers != nil:
				plan.items = append(plan.items, stagedItem{NetworkID: want.ID, Kind: "SETTINGS", Target: tgt})
				add(have, "restore_settings", diffs)
			}
		}
		for _, id := range ids {
			if !inIntent[id] && cur[id].Enabled {
				plan.items = append(plan.items, stagedItem{NetworkID: id, Kind: "DISABLE"})
				add(cur[id], "switch_off", []string{"created or switched on after the last confirmation"})
			}
		}
		if !haveSnap {
			plan.Review = append(plan.Review, reviewLine{State: "NO_RECORD", Layer: "all", Detail: "No record was kept of the portal designs, sign-in options and PMS routing at the last confirmation, so those are left as they are."})
		}

	case opFactoryDefaults, opFullReset:
		plan.Summary = "Return the client networks to the prepared network (" + netcfg.FactorySubnet + ") and switch every other client network off. Nothing is deleted."
		if op == opFullReset {
			plan.Summary += " Which portal design each network shows and each network's sign-in options are also reset to the site's."
			plan.TypedName = "site"
		}
		facts, ferr := s.systemFacts(ctx)
		if ferr != nil || facts.LAN.PhysicalInterface == "" {
			plan.Conflicts = append(plan.Conflicts, "The appliance could not say which port serves clients, so the prepared network cannot be put back safely right now.")
			return plan, nil
		}
		lan := facts.LAN.PhysicalInterface
		if lan == facts.WAN.Interface {
			plan.Conflicts = append(plan.Conflicts, "The client port is the WAN/management port. Client networks are never placed on it.")
		}
		if wan := facts.wanCIDR(); wan != "" && cidrsOverlap(netcfg.FactorySubnet, wan) {
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("The WAN/management network %s overlaps %s. The factory address is never forced onto a conflicting network.", wan, netcfg.FactorySubnet))
		}
		want := netcfg.Factory(lan)
		fid := s.factoryNetworkID(ctx)
		// The prepared network is put back in place when it is still the untagged network on the client port. When it was converted to a VLAN, moved, or is
		// gone, a new one is made with the prepared settings and switched on as part of the same change: the old row stays, switched off, with its history.
		usable := false
		if have, ok := cur[fid]; ok && have.ParentInterface == lan && have.NetworkType == netcfg.FactoryNetworkTyp {
			usable = true
		}
		var factoryID string
		if usable {
			have := cur[fid]
			factoryID = fid
			want.ID, want.BridgeName = fid, have.BridgeName
			diffs := settingsDiffer(have, want)
			tgt := recoveryTarget{Factory: true, Layers: &layerTarget{SetPortal: true, SetSignin: true}}
			if len(diffs) > 0 {
				tgt.Row = &want
			}
			switch {
			case !have.Enabled:
				plan.items = append(plan.items, stagedItem{NetworkID: fid, Kind: "ENABLE", Target: tgt})
				add(have, "switch_on", diffs)
			case len(diffs) > 0 || layers[fid].PortalProfile != nil || layers[fid].Signin != nil:
				plan.items = append(plan.items, stagedItem{NetworkID: fid, Kind: "SETTINGS", Target: tgt})
				add(have, "restore_settings", diffs)
			}
		} else {
			w := want
			plan.items = append(plan.items, stagedItem{Kind: "ENABLE", Create: &w, Target: recoveryTarget{Factory: true, Created: true, Row: &w}})
			plan.Networks = append(plan.Networks, planNetwork{Name: want.Name, Action: "create_and_switch_on",
				Changes: []string{"untagged on " + lan + ", " + netcfg.FactorySubnet + ", local DHCP"}})
			if fid != "" {
				plan.Review = append(plan.Review, reviewLine{NetworkID: fid, Network: cur[fid].Name, Layer: "all", State: "CHANGED",
					Detail: "The prepared network was changed so it is no longer the untagged network on the client port. It stays, switched off, with its history; a new one with the prepared settings takes its place."})
			}
		}
		for _, id := range ids {
			if id == factoryID {
				continue
			}
			n := cur[id]
			var tg recoveryTarget
			if op == opFullReset {
				tg.Layers = &layerTarget{SetPortal: true, SetSignin: true}
			}
			switch {
			case n.Enabled:
				plan.items = append(plan.items, stagedItem{NetworkID: id, Kind: "DISABLE", Target: tg})
				add(n, "switch_off", nil)
			case op == opFullReset && (layers[id].PortalProfile != nil || layers[id].Signin != nil):
				plan.items = append(plan.items, stagedItem{NetworkID: id, Kind: "SETTINGS", Target: tg})
				add(n, "restore_settings", []string{"portal design and sign-in options back to the site's"})
			}
		}
	default:
		return nil, errors.New("unknown recovery operation")
	}
	return plan, nil
}

// layerReview compares what a network is layered with now against the last confirmation, layer by layer, and returns what to write.
func (s *server) layerReview(ctx context.Context, plan *recoveryPlan, n netcfg.GuestNetwork, now layerState, snap map[string]layerState, have bool) *layerTarget {
	if !have {
		return nil
	}
	was, known := snap[n.ID]
	if !known {
		return nil
	}
	lt := &layerTarget{}
	line := func(layer, state, detail string) {
		plan.Review = append(plan.Review, reviewLine{NetworkID: n.ID, Network: n.Name, Layer: layer, State: state, Detail: detail})
	}
	switch {
	case samePortal(now.PortalProfile, was.PortalProfile):
		line("portal_profile", "UNCHANGED", "The portal design shown here is the same as at the last confirmation.")
	case was.PortalProfile != nil && !s.profileExists(ctx, *was.PortalProfile):
		lt.SetPortal = true
		line("portal_profile", "MISSING", "The portal profile this network showed no longer exists, so it shows the site default. Restored – review needed.")
		lt.Review = append(lt.Review, reviewSpec{Layer: "portal_profile", Detail: "The profile this network showed was deleted; it now shows the site default."})
	default:
		lt.SetPortal, lt.PortalProfile = true, was.PortalProfile
		line("portal_profile", "CHANGED", "The portal design was changed after the last confirmation and is put back. Restored – review needed.")
		lt.Review = append(lt.Review, reviewSpec{Layer: "portal_profile", Detail: "The portal design was put back to what it was at the last confirmation."})
	}
	if sameSignin(now.Signin, was.Signin) {
		line("signin", "UNCHANGED", "The sign-in options on this network are the same as at the last confirmation.")
	} else {
		lt.SetSignin, lt.Signin = true, was.Signin
		line("signin", "CHANGED", "The sign-in options were changed after the last confirmation and are put back. Restored – review needed.")
		lt.Review = append(lt.Review, reviewSpec{Layer: "signin", Detail: "The sign-in options were put back to what they were at the last confirmation."})
	}
	if equalStrings(now.PMS, was.PMS) {
		line("pms_routing", "UNCHANGED", "The PMS routing of this network is the same as at the last confirmation.")
	} else {
		line("pms_routing", "CHANGED", "The PMS routing of this network differs from the last confirmation. It is not changed here: room sign-in is paused on this network only until you review it under PMS routing. Restored – review needed.")
		lt.Review = append(lt.Review, reviewSpec{Layer: "pms_routing", Detail: "PMS routing differs from the last confirmation. Room sign-in is paused on this network until this is acknowledged.", PausePMS: true})
	}
	if !lt.SetPortal && !lt.SetSignin && len(lt.Review) == 0 {
		return nil
	}
	return lt
}

// ---- routes --------------------------------------------------------------------------------------------------------------------

func (s *server) mountRecovery(r chi.Router) {
	r.Get("/recovery", s.getRecovery)
	r.Post("/recovery/preview", s.previewRecovery)
	r.Post("/recovery/stage", s.stageRecovery)
	r.Get("/recovery/review", s.listReview)
	r.Post("/recovery/review/{id}/acknowledge", s.acknowledgeReview)
}

func (s *server) getRecovery(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	out := map[string]any{"operations": []map[string]any{}}
	var revSeq *int64
	var at *string
	_ = s.db.QueryRow(ctx, `SELECT seq, to_char(COALESCE(confirmed_at, created_at) AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"') FROM network_config_revisions WHERE state='active' ORDER BY seq DESC LIMIT 1`).Scan(&revSeq, &at)
	out["last_working"] = map[string]any{"revision": revSeq, "confirmed_at": at, "available": revSeq != nil}
	out["factory"] = map[string]any{"available": s.factoryNetworkID(ctx) != "", "subnet": netcfg.FactorySubnet}
	var open int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.network_review_items WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND resolved_at IS NULL`, s.tenantID, s.siteID).Scan(&open)
	out["review_open"] = open
	writeJSON(w, http.StatusOK, out)
}

type recoveryReq struct {
	Op       string `json:"op"`
	SiteName string `json:"confirm_site_name"`
}

func (s *server) previewRecovery(w http.ResponseWriter, r *http.Request) {
	var in recoveryReq
	if err := decodeJSON(r, &in); err != nil || recoveryGroupKind[in.Op] == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "op must be restore_last_working, factory_defaults or full_reset")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "network recovery")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	plan, err := s.planRecovery(ctx, in.Op)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the recovery plan could not be built")
		return
	}
	if plan.TypedName != "" {
		plan.TypedName = s.siteDisplayName(ctx)
	}
	plan.Stageable = len(plan.Conflicts) == 0 && len(plan.Cannot) == 0 && len(plan.items) > 0
	writeJSON(w, http.StatusOK, plan)
}

func (s *server) siteDisplayName(ctx context.Context) string {
	var name string
	_ = s.db.QueryRow(ctx, `SELECT name FROM sites WHERE id=$1::uuid`, s.siteID).Scan(&name)
	return name
}

func (s *server) stageRecovery(w http.ResponseWriter, r *http.Request) {
	var in recoveryReq
	if err := decodeJSON(r, &in); err != nil || recoveryGroupKind[in.Op] == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "op must be restore_last_working, factory_defaults or full_reset")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "network recovery")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	if in.Op == opFullReset {
		if name := s.siteDisplayName(ctx); name == "" || strings.TrimSpace(in.SiteName) != name {
			jsonErr(w, http.StatusBadRequest, "confirmation_required", "type the site's name exactly to confirm a full client networking reset")
			return
		}
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('client-network-recovery:' || $1))`, s.siteID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "lock")
		return
	}
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_network_changes WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND state IN ('PENDING','APPLIED')
		AND group_kind IN ('RESTORE','FACTORY_RESET','FULL_RESET')`, s.tenantID, s.siteID).Scan(&live); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if live > 0 {
		jsonErr(w, http.StatusConflict, "recovery_in_progress", "A recovery is already staged or waiting to be confirmed. Apply or cancel it first.")
		return
	}
	plan, err := s.planRecovery(ctx, in.Op)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the recovery plan could not be built")
		return
	}
	if len(plan.Conflicts) > 0 {
		jsonErr(w, http.StatusConflict, "conflict", strings.Join(plan.Conflicts, " "))
		return
	}
	// A recovery is all or nothing: part of a restore is a configuration nobody confirmed.
	if len(plan.Cannot) > 0 {
		jsonErr(w, http.StatusConflict, "cannot_restore", "Nothing was staged, because part of this recovery cannot be done. "+strings.Join(plan.Cannot, " "))
		return
	}
	if len(plan.items) == 0 {
		jsonErr(w, http.StatusConflict, "nothing_to_do", "Nothing needs to change: the client networks already match.")
		return
	}
	var group string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&group); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "id")
		return
	}
	for i := range plan.items {
		it := &plan.items[i]
		if it.Create != nil {
			off := false
			c := it.Create
			inp := guestNetworkInput{
				Name: c.Name, NetworkType: c.NetworkType, ParentInterface: c.ParentInterface, GatewayIP: c.GatewayIP, SubnetCIDR: c.SubnetCIDR,
				DHCPMode: string(c.DHCPMode), DNSMode: c.DNSMode, DomainName: c.DomainName,
				LeaseDefault: c.LeaseDefault, LeaseMin: c.LeaseMin, LeaseMax: c.LeaseMax, Enabled: &off, Pools: c.Pools,
			}
			t, iso := true, c.ClientIsolation
			inp.CaptiveEnabled, inp.InternetEnabled, inp.NATEnabled, inp.ClientIsolation = &t, &t, &t, &iso
			created, ierr := s.insertGuestNetwork(ctx, tx, inp, nil)
			if ierr != nil {
				writeNetworkInsertErr(w, ierr)
				return
			}
			it.NetworkID = created.ID
			it.Target.Row.ID = created.ID
		}
		raw, _ := json.Marshal(it.Target)
		var target any
		if string(raw) != "{}" {
			target = string(raw)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_changes (tenant_id, site_id, network_id, kind, group_id, group_kind, target)
			VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5::uuid,$6,$7::jsonb)`, s.tenantID, s.siteID, it.NetworkID, it.Kind, group, recoveryGroupKind[in.Op], target); err != nil {
			if isUniqueViolation(err) {
				writeStageErr(w, errChangeExists{network: it.NetworkID})
				return
			}
			jsonErr(w, http.StatusInternalServerError, "internal", "the recovery could not be staged")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "network.recovery.staged", "guest_network_change", group, map[string]any{"op": in.Op, "networks": len(plan.items)})
	writeJSON(w, http.StatusCreated, map[string]any{"group_id": group, "next": "apply", "plan": plan,
		"message": "Staged. Nothing has changed yet: review and apply it from Client networks, then keep it before the confirmation window ends."})
}

type reviewOut struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	Network   string `json:"network"`
	Layer     string `json:"layer"`
	Detail    string `json:"detail"`
	PausedPMS bool   `json:"room_sign_in_paused"`
	CreatedAt string `json:"created_at"`
}

func (s *server) listReview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT i.id::text, i.network_id::text, COALESCE(g.name,''), i.layer, i.detail, i.paused_pms, to_char(i.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM iam_v2.network_review_items i LEFT JOIN guest_networks g ON g.id=i.network_id
		WHERE i.tenant_id=$1::uuid AND i.site_id=$2::uuid AND i.resolved_at IS NULL ORDER BY i.created_at`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []reviewOut{}
	for rows.Next() {
		var o reviewOut
		if err := rows.Scan(&o.ID, &o.NetworkID, &o.Network, &o.Layer, &o.Detail, &o.PausedPMS, &o.CreatedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// acknowledgeReview closes an item. If it paused room sign-in on its network, the pause is lifted only when nothing else still holds it.
func (s *server) acknowledgeReview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidPattern.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such review item")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	defer tx.Rollback(ctx)
	var network string
	var paused bool
	if err := tx.QueryRow(ctx, `UPDATE iam_v2.network_review_items SET resolved_at=now() WHERE id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid AND resolved_at IS NULL
		RETURNING network_id::text, paused_pms`, id, s.tenantID, s.siteID).Scan(&network, &paused); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such open review item")
		return
	}
	if paused {
		var stillHeld bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.network_review_items WHERE network_id=$1::uuid AND paused_pms AND resolved_at IS NULL)`, network).Scan(&stillHeld); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		if !stillHeld {
			if _, err := tx.Exec(ctx, `UPDATE iam_v2.network_signin_overrides SET disabled_methods = array_remove(disabled_methods,'pms'), updated_at=now() WHERE network_id=$1::uuid`, network); err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "could not lift the pause")
				return
			}
			if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid AND cardinality(disabled_methods)=0 AND primary_method IS NULL AND remember_days IS NULL`, network); err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "could not lift the pause")
				return
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "network.recovery.review_acknowledged", "guest_network", network, map[string]any{"item": id})
	writeJSON(w, http.StatusOK, map[string]any{"acknowledged": true})
}

func gatewayCIDR(n netcfg.GuestNetwork) string {
	if _, ipn, err := net.ParseCIDR(n.SubnetCIDR); err == nil {
		ones, _ := ipn.Mask.Size()
		return fmt.Sprintf("%s/%d", n.GatewayIP, ones)
	}
	return n.GatewayIP
}
