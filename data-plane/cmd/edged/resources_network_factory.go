package main

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

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// The factory-prepared client network and the Access / Trunk mode of a port. Both stage their changes (resources_network_changes.go): nothing is live, and
// nothing in guest_networks is enabled, until the operator applies the configuration and the appliance keeps it.

type sysnetFacts struct {
	WAN struct {
		Interface string `json:"interface"`
		IP        string `json:"ip"`
		PrefixLen int    `json:"prefix_len"`
	} `json:"wan"`
	LAN struct {
		PhysicalInterface string `json:"physical_interface"`
	} `json:"lan"`
}

// systemFacts asks netd which port serves clients and which carries management; neither is ever assumed.
func (s *server) systemFacts(ctx context.Context) (sysnetFacts, error) {
	var f sysnetFacts
	if s.netd == nil {
		return f, errNetdAbsent
	}
	st, raw, err := s.netd.call(ctx, http.MethodGet, "/v1/system-network", nil)
	if err != nil {
		return f, err
	}
	if st >= 300 {
		return f, errNetdAbsent
	}
	return f, json.Unmarshal(raw, &f)
}

func (f sysnetFacts) wanCIDR() string {
	if ip := net.ParseIP(f.WAN.IP); ip != nil && f.WAN.PrefixLen > 0 {
		if _, n, err := net.ParseCIDR(fmt.Sprintf("%s/%d", f.WAN.IP, f.WAN.PrefixLen)); err == nil {
			return n.String()
		}
	}
	return ""
}

func cidrsOverlap(a, b string) bool {
	_, x, e1 := net.ParseCIDR(a)
	_, y, e2 := net.ParseCIDR(b)
	return e1 == nil && e2 == nil && (x.Contains(y.IP) || y.Contains(x.IP))
}

type factoryInfo struct {
	State        string            `json:"state"`
	Reason       string            `json:"reason"`
	Template     factoryTemplateJS `json:"template"`
	LANInterface string            `json:"lan_interface,omitempty"`
	NetworkID    string            `json:"network_id,omitempty"`
	Conflicts    []string          `json:"conflicts"`
	Checklist    []string          `json:"checklist"`
}

type factoryTemplateJS struct {
	Name       string      `json:"name"`
	Mode       string      `json:"mode"`
	SubnetCIDR string      `json:"subnet_cidr"`
	GatewayIP  string      `json:"gateway_ip"`
	DHCP       string      `json:"dhcp"`
	Pool       netcfg.Pool `json:"pool"`
	Services   []string    `json:"services"`
	PMS        string      `json:"pms"`
}

func factoryTemplateView() factoryTemplateJS {
	return factoryTemplateJS{
		Name: netcfg.FactoryName, Mode: "LAN Access (untagged)", SubnetCIDR: netcfg.FactorySubnet, GatewayIP: netcfg.FactoryGateway, DHCP: "Local",
		Pool:     netcfg.Pool{StartIP: netcfg.FactoryPoolStart, EndIP: netcfg.FactoryPoolEnd},
		Services: []string{"Appliance DNS", "Sign-in page", "Network address translation", "Internet access", "Site default portal profile"},
		PMS:      "No PMS is assigned. Room sign-in is not offered here until a PMS is mapped to this network.",
	}
}

var factoryChecklist = []string{
	"No other DHCP server serves the LAN segment this port is connected to.",
	"The switch port and the virtual switch port group deliver the untagged (native) VLAN to this appliance.",
	"The access point or controller puts the Wi-Fi network on that untagged VLAN.",
}

func (s *server) factoryState(ctx context.Context) factoryInfo {
	out := factoryInfo{Template: factoryTemplateView(), Conflicts: []string{}, Checklist: factoryChecklist}
	if s.tenantID == "" || s.siteID == "" {
		out.State, out.Reason = "awaiting_assignment", "This appliance has no signed site assignment yet. The network is prepared and becomes available to activate once it is assigned."
		return out
	}
	// A factory network that was already staged or activated is described by its own status; there is nothing more to offer.
	var network, state string
	err := s.db.QueryRow(ctx, `SELECT network_id::text, state FROM iam_v2.guest_network_changes
		 WHERE tenant_id=$1 AND site_id=$2 AND group_kind='FACTORY_ACTIVATE' AND state <> 'CANCELLED' ORDER BY created_at DESC LIMIT 1`,
		s.tenantID, s.siteID).Scan(&network, &state)
	if err == nil {
		out.NetworkID = network
		if state == "CONFIRMED" {
			out.State, out.Reason = "activated", "The factory network was activated."
		} else {
			out.State, out.Reason = "staged", "Activation is staged. Review and apply it from Client networks."
		}
		return out
	}
	facts, ferr := s.systemFacts(ctx)
	if ferr != nil || facts.LAN.PhysicalInterface == "" {
		out.State, out.Reason = "lan_unknown", "The appliance could not say which port serves clients, so nothing can be prepared on it yet."
		return out
	}
	out.LANInterface = facts.LAN.PhysicalInterface
	var onPort int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM guest_networks WHERE tenant_id=$1 AND site_id=$2 AND parent_interface=$3`, s.tenantID, s.siteID, out.LANInterface).Scan(&onPort)
	if onPort > 0 {
		out.State, out.Reason = "adopted", "This appliance already has client networks on the LAN port. They are kept exactly as they are; nothing is created or changed."
		return out
	}
	rows, qerr := s.db.Query(ctx, `SELECT name, text(subnet_cidr) FROM guest_networks WHERE tenant_id=$1 AND site_id=$2 AND enabled`, s.tenantID, s.siteID)
	if qerr == nil {
		for rows.Next() {
			var name, subnet string
			if rows.Scan(&name, &subnet) == nil {
				if cidrsOverlap(netcfg.FactorySubnet, subnet) {
					out.Conflicts = append(out.Conflicts, fmt.Sprintf("%s (%s) already uses addresses in %s. The factory address is never forced onto a conflicting network.", name, subnet, netcfg.FactorySubnet))
				}
				if strings.EqualFold(strings.TrimSpace(name), netcfg.FactoryName) {
					out.Conflicts = append(out.Conflicts, fmt.Sprintf("A network is already called %q.", netcfg.FactoryName))
				}
			}
		}
		rows.Close()
	}
	if wan := facts.wanCIDR(); wan != "" && cidrsOverlap(netcfg.FactorySubnet, wan) {
		out.Conflicts = append(out.Conflicts, fmt.Sprintf("The WAN/management network %s overlaps %s. The factory address is never forced onto a conflicting network.", wan, netcfg.FactorySubnet))
	}
	if len(out.Conflicts) > 0 {
		out.State, out.Reason = "blocked", "The factory addresses conflict with this site's networks. Resolve the conflict first; the factory subnet cannot be changed."
		return out
	}
	out.State, out.Reason = "prepared", "Prepared, not active. Nothing serves clients until you activate it and the change is confirmed."
	return out
}

func (s *server) getFactory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	writeJSON(w, http.StatusOK, s.factoryState(ctx))
}

// activateFactory stages the factory network: the row is created switched off and a switch-on is staged. Applying and confirming is what makes it serve.
func (s *server) activateFactory(w http.ResponseWriter, r *http.Request) {
	var in struct {
		NoOtherDHCP bool `json:"no_other_dhcp_server"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if !in.NoOtherDHCP {
		jsonErr(w, http.StatusBadRequest, "acknowledgement_required", "confirm that no other DHCP server serves this LAN segment: two DHCP servers on one segment give clients conflicting addresses")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	info := s.factoryState(ctx)
	switch info.State {
	case "prepared":
	case "awaiting_assignment":
		writeAwaitingAssignment(w, "client network")
		return
	default:
		jsonErr(w, http.StatusConflict, "not_available", info.Reason+" "+strings.Join(info.Conflicts, " "))
		return
	}
	tmpl := netcfg.Factory(info.LANInterface)
	off := false
	input := guestNetworkInput{
		Name: tmpl.Name, NetworkType: tmpl.NetworkType, ParentInterface: tmpl.ParentInterface, GatewayIP: tmpl.GatewayIP, SubnetCIDR: tmpl.SubnetCIDR,
		DHCPMode: string(tmpl.DHCPMode), DNSMode: tmpl.DNSMode, DomainName: tmpl.DomainName,
		LeaseDefault: tmpl.LeaseDefault, LeaseMin: tmpl.LeaseMin, LeaseMax: tmpl.LeaseMax,
		Enabled: &off, Pools: tmpl.Pools,
	}
	t, f := true, true
	input.CaptiveEnabled, input.InternetEnabled, input.NATEnabled, input.ClientIsolation = &t, &t, &t, &f
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	defer tx.Rollback(ctx)
	created, err := s.insertGuestNetwork(ctx, tx, input, nil)
	if err != nil {
		writeNetworkInsertErr(w, err)
		return
	}
	group, err := s.stageChanges(ctx, tx, "FACTORY_ACTIVATE", []changeItem{{NetworkID: created.ID, Kind: changeEnable}})
	if err != nil {
		writeStageErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "network.factory.activation_staged", "guest_network", created.ID, map[string]any{"subnet": netcfg.FactorySubnet, "port": info.LANInterface, "group": group})
	writeJSON(w, http.StatusCreated, map[string]any{"network_id": created.ID, "group_id": group, "next": "apply",
		"message": "Activation is staged. Review and apply it from Client networks; the network serves clients only after the change is confirmed."})
}

func writeStageErr(w http.ResponseWriter, err error) {
	var ce errChangeExists
	if errors.As(err, &ce) {
		jsonErr(w, http.StatusConflict, "change_exists", ce.Error())
		return
	}
	writeNetworkInsertErr(w, err)
}

// ---- ports: Access / Trunk ------------------------------------------------------------------------------------------------------

type portNetwork struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	VLANID     int    `json:"vlan_id,omitempty"`
	SubnetCIDR string `json:"subnet_cidr"`
	Enabled    bool   `json:"enabled"`
	Clients    int    `json:"active_clients"`
}

type portInfo struct {
	Name      string        `json:"name"`
	Protected bool          `json:"protected"`
	Mode      string        `json:"mode"` // access | trunk | unused
	Native    *portNetwork  `json:"native,omitempty"`
	VLANs     []portNetwork `json:"vlans"`
	// Networks on this port that are switched off but still part of the set-up (archived history is not listed).
	SwitchedOff []portNetwork `json:"switched_off"`
	Staged      int           `json:"staged_changes"`
}

func (s *server) loadPorts(ctx context.Context) ([]portInfo, error) {
	facts, _ := s.systemFacts(ctx)
	rows, err := s.db.Query(ctx, `SELECT id::text, name, parent_interface, network_type, COALESCE(vlan_id,0), text(subnet_cidr), enabled
		  FROM guest_networks WHERE tenant_id=$1 AND site_id=$2 ORDER BY parent_interface, COALESCE(vlan_id,0), name`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	by := map[string]*portInfo{}
	var order []string
	get := func(name string) *portInfo {
		p, ok := by[name]
		if !ok {
			p = &portInfo{Name: name, Mode: "unused", VLANs: []portNetwork{}, SwitchedOff: []portNetwork{}, Protected: name == facts.WAN.Interface && name != ""}
			by[name] = p
			order = append(order, name)
		}
		return p
	}
	if facts.LAN.PhysicalInterface != "" {
		get(facts.LAN.PhysicalInterface)
	}
	type netRow struct {
		n      portNetwork
		parent string
		typ    string
	}
	var all []netRow
	for rows.Next() {
		var r netRow
		if err := rows.Scan(&r.n.ID, &r.n.Name, &r.parent, &r.typ, &r.n.VLANID, &r.n.SubnetCIDR, &r.n.Enabled); err != nil {
			return nil, err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	archived := s.archivedSet(ctx)
	for _, r := range all {
		_ = s.db.QueryRow(ctx, activeOnNetworkSQL, r.n.ID).Scan(&r.n.Clients)
		p := get(r.parent)
		if !r.n.Enabled {
			if !archived[r.n.ID] {
				p.SwitchedOff = append(p.SwitchedOff, r.n)
			}
			continue
		}
		if r.typ == "vlan" {
			p.VLANs = append(p.VLANs, r.n)
		} else {
			n := r.n
			p.Native = &n
		}
	}
	for _, p := range by {
		switch {
		case len(p.VLANs) > 0:
			p.Mode = "trunk"
		case p.Native != nil:
			p.Mode = "access"
		}
		_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_network_changes c JOIN guest_networks g ON g.id=c.network_id
			 WHERE c.tenant_id=$1 AND c.site_id=$2 AND c.state IN ('PENDING','APPLIED') AND g.parent_interface=$3`, s.tenantID, s.siteID, p.Name).Scan(&p.Staged)
	}
	sort.Strings(order)
	out := make([]portInfo, 0, len(order))
	for _, n := range order {
		out = append(out, *by[n])
	}
	return out, nil
}

func (s *server) listPorts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	if s.tenantID == "" || s.siteID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ports": []portInfo{}, "awaiting_assignment": true})
		return
	}
	ports, err := s.loadPorts(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ports": ports})
}

type vlanRequest struct {
	VLANID     int           `json:"vlan_id"`
	Name       string        `json:"name"`
	SubnetCIDR string        `json:"subnet_cidr"`
	GatewayIP  string        `json:"gateway_ip"`
	Pools      []netcfg.Pool `json:"pools"`
}

type portModeRequest struct {
	Mode    string        `json:"mode"`
	VLANs   []vlanRequest `json:"vlans"`
	Preview bool          `json:"preview"`
}

type plannedNetwork struct {
	Name       string        `json:"name"`
	VLANID     int           `json:"vlan_id"`
	SubnetCIDR string        `json:"subnet_cidr"`
	GatewayIP  string        `json:"gateway_ip"`
	Pools      []netcfg.Pool `json:"pools"`
}

type portModePlan struct {
	Mode       string           `json:"mode"`
	Port       string           `json:"port"`
	Create     []plannedNetwork `json:"create"`
	SwitchOff  []portNetwork    `json:"switch_off"`
	KeepNative *portNetwork     `json:"keep_native,omitempty"`
	Impact     []string         `json:"impact"`
	Switch     []string         `json:"switch_requirements"`
	Conflicts  []string         `json:"conflicts"`
	// What depends on the networks this change touches (clients, PMS routing, sign-in methods, packages), and what the switch, access points and hypervisor must provide.
	Affected []networkConsequence `json:"affected"`
	External []string             `json:"external_requirements"`
}

// suggestSubnet is the first 10.<n>.0.0/24 from the VLAN id up that overlaps nothing taken.
func suggestSubnet(vlan int, taken []string) string {
	for i := 0; i < 250; i++ {
		c := fmt.Sprintf("10.%d.0.0/24", (vlan+i)%250)
		clash := false
		for _, t := range taken {
			if cidrsOverlap(c, t) {
				clash = true
				break
			}
		}
		if !clash {
			return c
		}
	}
	return ""
}

func (s *server) setPortMode(w http.ResponseWriter, r *http.Request) {
	iface := chi.URLParam(r, "iface")
	var in portModeRequest
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "client network")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	ports, err := s.loadPorts(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	var port *portInfo
	for i := range ports {
		if ports[i].Name == iface {
			port = &ports[i]
		}
	}
	if port == nil {
		jsonErr(w, http.StatusNotFound, "not_found", "this port carries no client network and is not the client port")
		return
	}
	if port.Protected {
		jsonErr(w, http.StatusConflict, "protected_interface", "this is the WAN/management port; client networks are never placed on it")
		return
	}
	facts, _ := s.systemFacts(ctx)

	var plan portModePlan
	switch in.Mode {
	case "trunk":
		plan, err = s.planTrunk(ctx, port, in.VLANs, facts)
	case "access":
		plan, err = s.planAccess(port)
	default:
		jsonErr(w, http.StatusBadRequest, "bad_request", `mode must be "trunk" or "access"`)
		return
	}
	if err != nil {
		jsonErr(w, http.StatusConflict, "mode_change_refused", err.Error())
		return
	}
	plan.Affected, plan.External = s.portPlanImpact(ctx, port, &plan)
	if in.Preview {
		writeJSON(w, http.StatusOK, plan)
		return
	}
	if len(plan.Conflicts) > 0 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "conflicts", "message": "the change cannot be staged: " + strings.Join(plan.Conflicts, " "), "plan": plan})
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	defer tx.Rollback(ctx)
	var items []changeItem
	var created []string
	for _, c := range plan.Create {
		off := false
		inp := guestNetworkInput{
			Name: c.Name, NetworkType: "vlan", ParentInterface: port.Name, GatewayIP: c.GatewayIP, SubnetCIDR: c.SubnetCIDR,
			Enabled: &off, Pools: c.Pools,
		}
		v := c.VLANID
		inp.VLANID = &v
		normalizeGuestNetworkInput(&inp)
		got, ierr := s.insertGuestNetwork(ctx, tx, inp, nil)
		if ierr != nil {
			writeNetworkInsertErr(w, ierr)
			return
		}
		created = append(created, got.ID)
		items = append(items, changeItem{NetworkID: got.ID, Kind: changeEnable})
	}
	for _, n := range plan.SwitchOff {
		items = append(items, changeItem{NetworkID: n.ID, Kind: changeDisable})
	}
	group, err := s.stageChanges(ctx, tx, "PORT_MODE", items)
	if err != nil {
		writeStageErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "network.port.mode_staged", "port", port.Name, map[string]any{"mode": in.Mode, "group": group, "created": created, "switched_off": len(plan.SwitchOff)})
	writeJSON(w, http.StatusCreated, map[string]any{"group_id": group, "network_ids": created, "plan": plan, "next": "apply"})
}

func (s *server) planTrunk(ctx context.Context, port *portInfo, vlans []vlanRequest, facts sysnetFacts) (portModePlan, error) {
	plan := portModePlan{Mode: "trunk", Port: port.Name, Create: []plannedNetwork{}, SwitchOff: []portNetwork{}, Conflicts: []string{}, Impact: []string{}, Switch: []string{}}
	if len(vlans) == 0 || len(vlans) > maxBatchNetworks {
		return plan, fmt.Errorf("send between 1 and %d VLANs", maxBatchNetworks)
	}
	// Every subnet already taken: enabled networks, and the WAN.
	var taken []string
	rows, err := s.db.Query(ctx, `SELECT text(subnet_cidr) FROM guest_networks WHERE tenant_id=$1 AND site_id=$2 AND enabled`, s.tenantID, s.siteID)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			taken = append(taken, c)
		}
	}
	rows.Close()
	if w := facts.wanCIDR(); w != "" {
		taken = append(taken, w)
	}
	usedVLAN := map[int]bool{}
	for _, v := range port.VLANs {
		usedVLAN[v.VLANID] = true
	}
	seen := map[int]bool{}
	var added []string
	for _, v := range vlans {
		if v.VLANID < 1 || v.VLANID > 4094 {
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("VLAN %d is outside 1-4094.", v.VLANID))
			continue
		}
		if usedVLAN[v.VLANID] || seen[v.VLANID] {
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("VLAN %d is already used on %s.", v.VLANID, port.Name))
			continue
		}
		seen[v.VLANID] = true
		name := strings.TrimSpace(v.Name)
		if name == "" {
			name = fmt.Sprintf("VLAN %d", v.VLANID)
		}
		if why := netcfg.CheckNetworkName(name); why != "" {
			plan.Conflicts = append(plan.Conflicts, why)
			continue
		}
		subnet, gw, pools := v.SubnetCIDR, v.GatewayIP, v.Pools
		if subnet == "" {
			subnet = suggestSubnet(v.VLANID, append(append([]string{}, taken...), added...))
			if subnet == "" {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("No free subnet could be suggested for VLAN %d.", v.VLANID))
				continue
			}
		}
		_, ipn, perr := net.ParseCIDR(subnet)
		if perr != nil || ipn.IP.To4() == nil {
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%q is not an IPv4 subnet.", subnet))
			continue
		}
		if iss := netcfg.CheckClientSubnetMask(ipn); len(iss) > 0 {
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("VLAN %d: %s", v.VLANID, iss[0].Message))
			continue
		}
		base := ipn.IP.To4()
		if gw == "" {
			gw = net.IPv4(base[0], base[1], base[2], base[3]+1).String()
		}
		if len(pools) == 0 {
			if ones, _ := ipn.Mask.Size(); ones == 24 {
				pools = []netcfg.Pool{{StartIP: net.IPv4(base[0], base[1], base[2], 100).String(), EndIP: net.IPv4(base[0], base[1], base[2], 250).String()}}
			} else {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("Give VLAN %d an address pool.", v.VLANID))
				continue
			}
		}
		clash := false
		for _, t := range append(append([]string{}, taken...), added...) {
			if cidrsOverlap(subnet, t) {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%s (VLAN %d) overlaps %s, which is already in use.", subnet, v.VLANID, t))
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		added = append(added, subnet)
		plan.Create = append(plan.Create, plannedNetwork{Name: name, VLANID: v.VLANID, SubnetCIDR: subnet, GatewayIP: gw, Pools: pools})
	}
	if port.Native != nil {
		n := *port.Native
		plan.KeepNative = &n
		plan.Impact = append(plan.Impact, fmt.Sprintf("%s keeps serving its %d connected client(s) on the untagged (native) VLAN. They are not interrupted by this change.", n.Name, n.Clients))
	} else {
		plan.Impact = append(plan.Impact, "The port has no untagged network; only the tagged VLANs below will be served.")
	}
	var ids []string
	for _, c := range plan.Create {
		ids = append(ids, fmt.Sprintf("%d", c.VLANID))
	}
	plan.Switch = append(plan.Switch,
		fmt.Sprintf("Allow VLAN %s on the switch port facing this appliance (trunk allowed list) and on the virtual switch port group. The appliance cannot verify this.", strings.Join(ids, ", ")),
		"Keep delivering the untagged (native) VLAN on that port if a native network is kept.",
		"Map each Wi-Fi network (SSID) to its VLAN on the access point or controller.")
	plan.Impact = append(plan.Impact, "Applying is not guaranteed to be uninterrupted: the appliance reconfigures the port. If the change does not hold, it is rolled back automatically and the previous networks are restored.")
	return plan, nil
}

func (s *server) planAccess(port *portInfo) (portModePlan, error) {
	plan := portModePlan{Mode: "access", Port: port.Name, Create: []plannedNetwork{}, SwitchOff: []portNetwork{}, Conflicts: []string{}, Impact: []string{}, Switch: []string{}}
	if len(port.VLANs) == 0 {
		return plan, fmt.Errorf("%s has no tagged VLAN networks, so it is already in Access mode", port.Name)
	}
	if port.Native == nil {
		return plan, fmt.Errorf("%s has no untagged network to keep. Move one network to untagged first with Change VLAN or port, then convert the port to Access", port.Name)
	}
	n := *port.Native
	plan.KeepNative = &n
	clients := 0
	for _, v := range port.VLANs {
		plan.SwitchOff = append(plan.SwitchOff, v)
		clients += v.Clients
	}
	plan.Impact = append(plan.Impact,
		fmt.Sprintf("%d client(s) on the tagged VLAN network(s) lose service when this is applied. Their access and allowance are kept; they join %s and sign in again.", clients, n.Name),
		"The switched-off networks keep their settings; switching them back on needs no new activation.",
		"Applying is not guaranteed to be uninterrupted. If the change does not hold, it is rolled back automatically.")
	plan.Switch = append(plan.Switch, "The switch port and port group must deliver the untagged (native) VLAN. Tagged VLANs are no longer served and can be removed from the trunk.")
	return plan, nil
}

// portPlanImpact lists the existing networks a port-mode change touches and what must be provided outside OneGate for the resulting topology.
func (s *server) portPlanImpact(ctx context.Context, port *portInfo, plan *portModePlan) ([]networkConsequence, []string) {
	var ids []string
	for _, n := range plan.SwitchOff {
		ids = append(ids, n.ID)
	}
	if plan.KeepNative != nil {
		ids = append(ids, plan.KeepNative.ID)
	}
	if port.Native != nil {
		ids = append(ids, port.Native.ID)
	}
	ext := []string{}
	if plan.Mode == "trunk" {
		for _, c := range plan.Create {
			v := c.VLANID
			ext = append(ext, externalRequirements("vlan", &v, port.Name)[:3]...)
		}
		if len(ext) > 0 {
			ext = append(ext, "OneGate does not change your switch, access points or hypervisor. Make these changes there yourself; until they are made, clients on these networks cannot reach them.")
		}
	} else {
		ext = externalRequirements("untagged", nil, port.Name)
	}
	return s.consequencesFor(ctx, ids), ext
}
