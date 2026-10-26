package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/netstatus"
)

// The overview tells the operator what each client network IS, derived from the revisions the appliance actually confirmed and what it observes now, not from what
// was saved. The rules live in internal/netstatus; this file gathers the facts.

type netdDiagCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"`
	Detail string `json:"detail"`
	Where  string `json:"where"`
}

type netdNetworkDiag struct {
	NetworkID string          `json:"network_id"`
	Name      string          `json:"name"`
	Checks    []netdDiagCheck `json:"checks"`
	Findings  []string        `json:"findings"`
	Outside   []string        `json:"outside"`
}

type netdDiagReport struct {
	Revision       string            `json:"revision"`
	Networks       []netdNetworkDiag `json:"networks"`
	Server         []netdDiagCheck   `json:"server"`
	NoConfirmedCfg bool              `json:"no_confirmed_configuration"`
}

// servingChecks are the appliance-side checks whose failure means a running network is not serving its clients.
var servingChecks = map[string]bool{"port_link": true, "vlan_device": true, "bridge": true, "dhcp_bound": true, "dhcp_config": true, "dhcp_pool": true, "dns": true}

type revisionInfo struct {
	ID          string     `json:"id"`
	Seq         int64      `json:"seq"`
	State       string     `json:"state,omitempty"`
	ConfirmedAt *time.Time `json:"confirmed_at,omitempty"`
	Deadline    *time.Time `json:"confirm_deadline,omitempty"`
	Reason      string     `json:"reason,omitempty"`
}

type overviewNetwork struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	NetworkType     string             `json:"network_type"`
	VLANID          int                `json:"vlan_id,omitempty"`
	ParentInterface string             `json:"parent_interface"`
	BridgeName      string             `json:"bridge_name"`
	SubnetCIDR      string             `json:"subnet_cidr"`
	GatewayIP       string             `json:"gateway_ip"`
	DHCPMode        string             `json:"dhcp_mode"`
	SavedEnabled    bool               `json:"saved_enabled"`
	Running         bool               `json:"running"`
	EverActivated   bool               `json:"ever_activated"`
	Status          netstatus.Status   `json:"status"`
	Reason          string             `json:"reason"`
	Changes         []netstatus.Change `json:"changes"`
	ActiveClients   int                `json:"active_clients"`
	PMSInterfaces   []string           `json:"pms_interfaces"`
	HealthKnown     bool               `json:"health_known"`
	Attention       []string           `json:"attention"`
	Archived        bool               `json:"archived"`
	IsDefault       bool               `json:"is_default"`
	// What room sign-in does on this network, from the one assessment every screen reads. Nil when it could not be assessed.
	RoomSignIn *roomSignInBrief `json:"room_sign_in,omitempty"`
}

type roomSignInBrief struct {
	Policy  string   `json:"policy"`
	State   string   `json:"state"`
	Summary string   `json:"summary"`
	Labels  []string `json:"interfaces"`
}

func briefOf(n rsNetwork) *roomSignInBrief {
	b := &roomSignInBrief{Policy: n.Policy, State: n.State, Summary: n.Summary, Labels: []string{}}
	for _, i := range n.Interfaces {
		b.Labels = append(b.Labels, i.Label)
	}
	return b
}

type networkOverviewResp struct {
	Active               *revisionInfo     `json:"active_revision"`
	InFlight             *revisionInfo     `json:"in_flight_revision"`
	ChangesBlocked       bool              `json:"changes_blocked"`
	BlockedDetail        string            `json:"blocked_detail,omitempty"`
	DiagnosticsAvailable bool              `json:"diagnostics_available"`
	Networks             []overviewNetwork `json:"networks"`
}

func (s *server) networkOverview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	_ = s.reconcileStaleReplacements(ctx, r)
	diag, diagErr := s.netdDiagnostics(r.Context(), 0)
	out, err := s.buildNetworkOverview(ctx, diag, diagErr == nil)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "overview query failed")
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// netdDiagnostics asks netd for its observations; sample (milliseconds) also measures frames received over that long.
func (s *server) netdDiagnostics(ctx context.Context, sampleMillis int) (netdDiagReport, error) {
	var rep netdDiagReport
	if s.netd == nil {
		return rep, errNetdAbsent
	}
	path := "/v1/diagnostics"
	if sampleMillis > 0 {
		path += "?sample_ms=" + strconv.Itoa(sampleMillis)
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(sampleMillis)*time.Millisecond+8*time.Second)
	defer cancel()
	st, raw, err := s.netd.call(cctx, http.MethodGet, path, nil)
	if err != nil {
		return rep, err
	}
	if st >= 300 {
		return rep, errNetdAbsent
	}
	return rep, json.Unmarshal(raw, &rep)
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

const errNetdAbsent = simpleErr("the network service did not answer")

func (s *server) buildNetworkOverview(ctx context.Context, diag netdDiagReport, haveDiag bool) (networkOverviewResp, error) {
	out := networkOverviewResp{Networks: []overviewNetwork{}, DiagnosticsAvailable: haveDiag}
	if s.tenantID == "" || s.siteID == "" {
		return out, nil // awaiting the signed assignment: there is no site to describe yet
	}

	saved, err := s.loadSavedIntents(ctx)
	if err != nil {
		return out, err
	}
	active, activeIntent, err := s.loadRevision(ctx, `state='active'`, "")
	if err != nil {
		return out, err
	}
	inflight, inflightIntent, err := s.loadRevision(ctx, `state IN ('applying','pending_confirmation')`, "")
	if err != nil {
		return out, err
	}
	var failed *netstatus.Revision
	if active != nil {
		fr, fi, ferr := s.loadRevision(ctx, `state IN ('failed','rolled_back') AND seq > `+strconv.FormatInt(active.Seq, 10), "")
		if ferr != nil {
			return out, ferr
		}
		if fr != nil {
			failed = &netstatus.Revision{Intent: fi, Reason: fr.Reason}
		}
	} else {
		fr, fi, ferr := s.loadRevision(ctx, `state IN ('failed','rolled_back')`, "")
		if ferr != nil {
			return out, ferr
		}
		if fr != nil {
			failed = &netstatus.Revision{Intent: fi, Reason: fr.Reason}
		}
	}
	out.Active, out.InFlight = active, inflight

	ever := map[string]bool{}
	rows, err := s.db.Query(ctx, `SELECT DISTINCT e->>'id' FROM network_config_revisions r, jsonb_array_elements(r.intent) e
		 WHERE r.state IN ('active','superseded') AND jsonb_typeof(r.intent)='array' AND COALESCE((e->>'enabled')::boolean,false)`)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ever[id] = true
		}
	}
	rows.Close()

	pms := map[string][]string{}
	prows, err := s.db.Query(ctx, `SELECT guest_network_id::text, pms_interface_id::text FROM iam_v2.guest_network_pms_map
		 WHERE tenant_id=$1 AND site_id=$2 ORDER BY pms_interface_id`, s.tenantID, s.siteID)
	if err == nil {
		for prows.Next() {
			var n, p string
			if prows.Scan(&n, &p) == nil {
				pms[n] = append(pms[n], p)
			}
		}
		prows.Close()
	}

	stagedBy := map[string]string{}
	if srows, serr := s.db.Query(ctx, `SELECT network_id::text, kind FROM iam_v2.guest_network_changes
		 WHERE tenant_id=$1 AND site_id=$2 AND state='PENDING'`, s.tenantID, s.siteID); serr == nil {
		for srows.Next() {
			var n, k string
			if srows.Scan(&n, &k) == nil {
				stagedBy[n] = k
			}
		}
		srows.Close()
	}

	diagBy := map[string]netdNetworkDiag{}
	for _, d := range diag.Networks {
		diagBy[d.NetworkID] = d
	}
	for _, c := range diag.Server {
		if c.ID == "changes_blocked" && c.State == "fail" {
			out.ChangesBlocked, out.BlockedDetail = true, c.Detail
		}
	}

	var inflightRev *netstatus.Revision
	if inflight != nil {
		inflightRev = &netstatus.Revision{Intent: inflightIntent}
	}
	_ = s.archiveRetired(ctx, s.db)
	archivedIDs, defaultID := s.archivedSet(ctx), s.defaultNetworkID(ctx)
	roomBy := map[string]*roomSignInBrief{}
	if a, aerr := s.roomSignInAssessment(ctx); aerr == nil {
		for _, rn := range a.Networks {
			roomBy[rn.NetworkID] = briefOf(rn)
		}
	}
	for _, n := range saved {
		var running *netcfg.GuestNetwork
		for i := range activeIntent {
			if activeIntent[i].ID == n.ID {
				running = &activeIntent[i]
			}
		}
		health := netstatus.Health{}
		var attention []string
		if d, ok := diagBy[n.ID]; ok && haveDiag {
			health.Known = true
			for _, c := range d.Checks {
				if c.State == "fail" && servingChecks[c.ID] {
					health.Failing = append(health.Failing, c.ID)
				}
			}
			attention = d.Findings
		}
		var staged *netstatus.Staged
		if k, ok := stagedBy[n.ID]; ok {
			staged = &netstatus.Staged{Kind: k}
		}
		res := netstatus.Derive(netstatus.Input{Staged: staged, Saved: n, Running: running, EverActivated: ever[n.ID], InFlight: inflightRev, LastFailed: failed, Health: health})
		item := overviewNetwork{
			ID: n.ID, Name: n.Name, NetworkType: n.NetworkType, VLANID: n.VLANID, ParentInterface: n.ParentInterface, BridgeName: n.BridgeName,
			SubnetCIDR: n.SubnetCIDR, GatewayIP: n.GatewayIP, DHCPMode: string(n.DHCPMode), SavedEnabled: n.Enabled,
			Running: running != nil && running.Enabled, EverActivated: ever[n.ID], Status: res.Status, Reason: res.Reason, Changes: res.Changes,
			PMSInterfaces: append([]string{}, pms[n.ID]...), HealthKnown: health.Known, Attention: append([]string{}, attention...),
		}
		if item.Changes == nil {
			item.Changes = []netstatus.Change{}
		}
		_ = s.db.QueryRow(ctx, activeOnNetworkSQL, n.ID).Scan(&item.ActiveClients)
		item.Archived, item.IsDefault = archivedIDs[n.ID], n.ID == defaultID
		item.RoomSignIn = roomBy[n.ID]
		out.Networks = append(out.Networks, item)
	}
	return out, nil
}

// loadSavedIntents reads the editable rows in the shape netd applies them in, so a saved row and a running revision compare field for field.
func (s *server) loadSavedIntents(ctx context.Context) ([]netcfg.GuestNetwork, error) {
	rows, err := s.db.Query(ctx, `
        SELECT id::text, name, enabled, network_type, parent_interface, COALESCE(vlan_id,0), bridge_name,
               host(gateway_ip), text(subnet_cidr), masklen(subnet_cidr), dhcp_mode, dns_mode, dns_servers, domain_name,
               lease_default_seconds, lease_min_seconds, lease_max_seconds, captive_portal_enabled, internet_access_enabled,
               nat_enabled, client_isolation_enabled
          FROM guest_networks WHERE tenant_id=$1 AND site_id=$2 ORDER BY COALESCE(vlan_id,0), name`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nets []netcfg.GuestNetwork
	for rows.Next() {
		var n netcfg.GuestNetwork
		var dns []byte
		if err := rows.Scan(&n.ID, &n.Name, &n.Enabled, &n.NetworkType, &n.ParentInterface, &n.VLANID, &n.BridgeName, &n.GatewayIP, &n.SubnetCIDR, &n.PrefixLen,
			&n.DHCPMode, &n.DNSMode, &dns, &n.DomainName, &n.LeaseDefault, &n.LeaseMin, &n.LeaseMax, &n.CaptiveEnabled, &n.InternetEnabled,
			&n.NATEnabled, &n.ClientIsolation); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(dns, &n.DNSServers)
		nets = append(nets, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range nets {
		nets[i].Pools = s.loadPoolsFor(ctx, nets[i].ID)
	}
	return nets, nil
}

// loadRevision returns the newest revision matching the state condition, with its intent; nil when there is none.
func (s *server) loadRevision(ctx context.Context, cond, _ string) (*revisionInfo, []netcfg.GuestNetwork, error) {
	var info revisionInfo
	var raw []byte
	var reason *string
	err := s.db.QueryRow(ctx, `SELECT id::text, seq, state, confirmed_at, confirm_deadline, failure_reason, intent
		  FROM network_config_revisions WHERE `+cond+` ORDER BY seq DESC LIMIT 1`).
		Scan(&info.ID, &info.Seq, &info.State, &info.ConfirmedAt, &info.Deadline, &reason, &raw)
	if isNoRows(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if reason != nil {
		info.Reason = *reason
	}
	var intent []netcfg.GuestNetwork
	_ = json.Unmarshal(raw, &intent)
	return &info, intent, nil
}

// networkDiagnostics serves GET /network/guest-networks/{id}/diagnostics[?sample_ms=N].
func (s *server) networkDiagnostics(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	sample := 0
	if v := r.URL.Query().Get("sample_ms"); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' || n > 5000 {
				jsonErr(w, http.StatusBadRequest, "bad_request", "sample_ms must be a number between 0 and 5000")
				return
			}
			n = n*10 + int(c-'0')
		}
		if n > 5000 {
			jsonErr(w, http.StatusBadRequest, "bad_request", "sample_ms must be a number between 0 and 5000")
			return
		}
		sample = n
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var name string
	if err := s.db.QueryRow(ctx, `SELECT name FROM guest_networks WHERE id=$1`, id).Scan(&name); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
		return
	}
	diag, err := s.netdDiagnostics(r.Context(), sample)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", "the appliance's network service did not answer, so nothing could be observed")
		return
	}
	for _, d := range diag.Networks {
		if d.NetworkID == id {
			writeJSON(w, http.StatusOK, map[string]any{"network": d, "server": diag.Server, "revision": diag.Revision, "sample_ms": sample})
			return
		}
	}
	// A network that is not part of the confirmed configuration has nothing to observe yet; say so rather than inventing checks.
	writeJSON(w, http.StatusOK, map[string]any{"network": netdNetworkDiag{NetworkID: id, Name: name, Checks: []netdDiagCheck{}, Findings: []string{},
		Outside: []string{}}, "server": diag.Server, "revision": diag.Revision, "not_running": true, "sample_ms": sample})
}
