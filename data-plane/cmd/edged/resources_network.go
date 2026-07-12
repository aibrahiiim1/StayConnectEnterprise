package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// Networking resource (Phase 19). guest-network CRUD is site-DB owned by edged;
// validate/apply/confirm/rollback and interface/lease/revision reads proxy to
// the privileged netd daemon. All apply/rollback are audited.
//
// Mounted at /edge/v1/network (single "network" permission key), with the
// sub-paths the spec calls /guest-networks, /dhcp, /interfaces, /revisions.

func (s *server) networkRoutes() http.Handler {
	r := chi.NewRouter()

	// interfaces (from netd discovery, cached in DB)
	r.Get("/interfaces", s.netInterfaces)
	r.Patch("/interfaces/{name}/role", s.netInterfaceRole)

	// guest networks
	r.Get("/guest-networks", s.listGuestNetworks)
	r.Post("/guest-networks", s.createGuestNetwork)
	r.Get("/guest-networks/{id}", s.getGuestNetwork)
	r.Put("/guest-networks/{id}", s.updateGuestNetwork)
	r.Delete("/guest-networks/{id}", s.deleteGuestNetwork)
	r.Post("/guest-networks/{id}/disable", s.disableGuestNetwork)
	r.Get("/guest-networks/{id}/status", s.guestNetworkStatus)

	// validate / apply operate on the whole intent (all networks) via netd.
	r.Post("/validate", s.netValidate)
	r.Post("/apply", s.netApply)
	r.Post("/adopt", s.netAdopt)

	// system (WAN/LAN) network — the appliance's own base networking. GET =
	// network.view; POST = network.change/apply/rollback (permWrite). Apply and
	// rollback additionally re-authenticate the operator's password.
	r.Get("/system", s.sysNetGet)
	r.Get("/system/history", s.sysNetHistory)
	r.Get("/system/diagnostics", s.sysNetDiagnostics)
	r.Post("/system/validate", s.sysNetValidate)
	r.Post("/system/apply", s.sysNetApply)
	r.Post("/system/confirm", s.sysNetConfirm)
	r.Post("/system/rollback", s.sysNetRollback)

	// Cloud Connection (carryover F) — real appliance<->Central status. GET =
	// network.view; POST actions = network.change (Hotel IT / Site Admin).
	r.Get("/cloud", s.cloudStatus)
	r.Post("/cloud/test", s.cloudTest)
	r.Post("/cloud/refresh-license", s.cloudRefreshLicense)

	// Local enrollment wizard (Phase 6). Status is read-only; enroll submission
	// is Hotel-IT gated + audited. Mgmt-network-only (edged binds mgmt IP).
	r.Get("/setup/status", s.setupStatus)
	r.With(s.requireRole("network", permWrite)).Post("/setup/enroll", s.setupEnroll)
	r.With(s.requireRole("network", permWrite)).Post("/setup/offline-import", s.setupOfflineImport)

	// DHCP
	r.Get("/dhcp/leases", s.dhcpLeases)
	r.Get("/dhcp/reservations", s.listReservations)
	r.Post("/dhcp/reservations", s.createReservation)
	r.Put("/dhcp/reservations/{id}", s.updateReservation)
	r.Delete("/dhcp/reservations/{id}", s.deleteReservation)

	// revisions
	r.Get("/revisions", s.listRevisions)
	r.Get("/revisions/{id}", s.getRevision)
	r.Post("/revisions/{id}/confirm", s.confirmRevision)
	r.Post("/revisions/{id}/rollback", s.rollbackRevision)

	return r
}

func (s *server) actor(r *http.Request) string {
	if sess := sessFrom(r.Context()); sess != nil {
		return sess.OperatorID
	}
	return ""
}

// ---- interfaces ----

func (s *server) netInterfaces(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodGet, "/v1/interfaces", nil)
}

func (s *server) netInterfaceRole(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var in struct {
		Role string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "role required")
		return
	}
	valid := map[string]bool{"guest_access": true, "guest_trunk": true, "ha_sync": true, "unused": true}
	if !valid[in.Role] {
		jsonErr(w, http.StatusBadRequest, "bad_request", "role must be guest_access|guest_trunk|ha_sync|unused (management/wan are protected)")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx, `
        UPDATE network_interfaces SET role=$2, updated_at=now()
         WHERE name=$1 AND is_protected=false`, name, in.Role)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusConflict, "protected", "interface not found or protected (management/WAN)")
		return
	}
	s.audit(r, "network.interface.role", "interface", name, map[string]any{"role": in.Role})
	writeJSON(w, http.StatusOK, map[string]string{"name": name, "role": in.Role})
}

// ---- guest networks CRUD ----

type guestNetworkRow struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Description     *string          `json:"description,omitempty"`
	SSIDLabel       *string          `json:"ssid_label,omitempty"`
	Enabled         bool             `json:"enabled"`
	NetworkType     string           `json:"network_type"`
	ParentInterface string           `json:"parent_interface"`
	VLANID          *int             `json:"vlan_id,omitempty"`
	BridgeName      string           `json:"bridge_name"`
	GatewayIP       string           `json:"gateway_ip"`
	SubnetCIDR      string           `json:"subnet_cidr"`
	DHCPMode        string           `json:"dhcp_mode"`
	DNSMode         string           `json:"dns_mode"`
	DomainName      string           `json:"domain_name"`
	LeaseDefault    int              `json:"lease_default_seconds"`
	LeaseMin        int              `json:"lease_min_seconds"`
	LeaseMax        int              `json:"lease_max_seconds"`
	CaptiveEnabled  bool             `json:"captive_portal_enabled"`
	InternetEnabled bool             `json:"internet_access_enabled"`
	NATEnabled      bool             `json:"nat_enabled"`
	ClientIsolation bool             `json:"client_isolation_enabled"`
	PortalURL       *string          `json:"portal_url,omitempty"`
	Pools           []netcfg.Pool    `json:"pools"`
	Reservations    []reservationRow `json:"reservations,omitempty"`
}

type reservationRow struct {
	ID         string  `json:"id,omitempty"`
	MAC        string  `json:"mac"`
	ReservedIP string  `json:"reserved_ip"`
	Hostname   *string `json:"hostname,omitempty"`
	Enabled    bool    `json:"enabled"`
}

const gnCols = `id::text, name, description, ssid_label, enabled, network_type,
  parent_interface, vlan_id, bridge_name, host(gateway_ip), text(subnet_cidr),
  dhcp_mode, dns_mode, domain_name, lease_default_seconds, lease_min_seconds,
  lease_max_seconds, captive_portal_enabled, internet_access_enabled,
  nat_enabled, client_isolation_enabled, portal_url`

func scanGuestNetwork(row interface{ Scan(...any) error }) (guestNetworkRow, error) {
	var g guestNetworkRow
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.SSIDLabel, &g.Enabled, &g.NetworkType,
		&g.ParentInterface, &g.VLANID, &g.BridgeName, &g.GatewayIP, &g.SubnetCIDR,
		&g.DHCPMode, &g.DNSMode, &g.DomainName, &g.LeaseDefault, &g.LeaseMin,
		&g.LeaseMax, &g.CaptiveEnabled, &g.InternetEnabled, &g.NATEnabled,
		&g.ClientIsolation, &g.PortalURL)
	return g, err
}

func (s *server) listGuestNetworks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT `+gnCols+` FROM guest_networks ORDER BY COALESCE(vlan_id,0), name`)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []guestNetworkRow
	for rows.Next() {
		g, err := scanGuestNetwork(rows)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, g)
	}
	// attach pool summaries
	for i := range out {
		out[i].Pools = s.loadPoolsFor(ctx, out[i].ID)
	}
	writeList(w, out)
}

func (s *server) getGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	g, err := scanGuestNetwork(s.db.QueryRow(ctx, `SELECT `+gnCols+` FROM guest_networks WHERE id=$1`, id))
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "guest network not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	g.Pools = s.loadPoolsFor(ctx, id)
	g.Reservations = s.loadReservationsFor(ctx, id)
	writeJSON(w, http.StatusOK, g)
}

type guestNetworkInput struct {
	Name            string        `json:"name"`
	Description     string        `json:"description"`
	SSIDLabel       string        `json:"ssid_label"`
	Enabled         *bool         `json:"enabled"`
	NetworkType     string        `json:"network_type"`
	ParentInterface string        `json:"parent_interface"`
	VLANID          *int          `json:"vlan_id"`
	GatewayIP       string        `json:"gateway_ip"`
	SubnetCIDR      string        `json:"subnet_cidr"`
	DHCPMode        string        `json:"dhcp_mode"`
	DNSMode         string        `json:"dns_mode"`
	DNSServers      []string      `json:"dns_servers"`
	DomainName      string        `json:"domain_name"`
	LeaseDefault    int           `json:"lease_default_seconds"`
	LeaseMin        int           `json:"lease_min_seconds"`
	LeaseMax        int           `json:"lease_max_seconds"`
	CaptiveEnabled  *bool         `json:"captive_portal_enabled"`
	InternetEnabled *bool         `json:"internet_access_enabled"`
	NATEnabled      *bool         `json:"nat_enabled"`
	ClientIsolation *bool         `json:"client_isolation_enabled"`
	Pools           []netcfg.Pool `json:"pools"`
}

func (s *server) createGuestNetwork(w http.ResponseWriter, r *http.Request) {
	var in guestNetworkInput
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.NetworkType == "" {
		in.NetworkType = "untagged"
	}
	if in.DHCPMode == "" {
		in.DHCPMode = "local"
	}
	if in.DNSMode == "" {
		in.DNSMode = "appliance"
	}
	if in.DomainName == "" {
		in.DomainName = "guest.local"
	}
	if in.LeaseDefault == 0 {
		in.LeaseDefault, in.LeaseMin, in.LeaseMax = 3600, 900, 7200
	}
	vlan := 0
	if in.VLANID != nil {
		vlan = *in.VLANID
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	// enforce max_guest_access_plans-equivalent? networks are commercially
	// gated by max_appliances/HA elsewhere; no explicit cap here beyond DB.
	newID := newUUID()
	bridge := netcfg.BridgeNameFor(in.NetworkType, vlan, newID)
	portalURL := netcfg.PortalURLFor(in.GatewayIP, 8380)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	defer tx.Rollback(ctx)

	dnsRaw, _ := json.Marshal(in.DNSServers)
	_, err = tx.Exec(ctx, `
        INSERT INTO guest_networks (id, tenant_id, site_id, name, description, ssid_label, enabled,
            network_type, parent_interface, vlan_id, bridge_name, gateway_cidr, gateway_ip, subnet_cidr,
            dhcp_mode, dns_mode, dns_servers, domain_name, lease_default_seconds, lease_min_seconds,
            lease_max_seconds, captive_portal_enabled, internet_access_enabled, nat_enabled,
            client_isolation_enabled, portal_url)
        VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),$7,$8,$9,$10,$11,
            ($12 || '/' || masklen($13::cidr))::inet, $12::inet, $13::cidr,
            $14,$15,$16::jsonb,$17,$18,$19,$20,$21,$22,$23,$24,$25)
    `, newID, s.tenantID, s.siteID, in.Name, in.Description, in.SSIDLabel, boolOr(in.Enabled, true),
		in.NetworkType, in.ParentInterface, nullIfZero(vlan), bridge,
		in.GatewayIP, in.SubnetCIDR,
		in.DHCPMode, in.DNSMode, string(dnsRaw), in.DomainName, in.LeaseDefault, in.LeaseMin,
		in.LeaseMax, boolOr(in.CaptiveEnabled, true), boolOr(in.InternetEnabled, true), boolOr(in.NATEnabled, true),
		boolOr(in.ClientIsolation, false), portalURL)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
		return
	}
	if err := insertPools(ctx, tx, newID, in.Pools); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "network.guest.created", "guest_network", newID, map[string]any{"name": in.Name, "vlan": vlan})
	writeJSON(w, http.StatusCreated, map[string]any{"id": newID, "bridge_name": bridge, "portal_url": portalURL})
}

func (s *server) updateGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in guestNetworkInput
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
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
	// Update mutable fields (not type/vlan/parent/bridge — those are immutable
	// once created; delete + recreate to change topology).
	portalURL := netcfg.PortalURLFor(in.GatewayIP, 8380)
	dnsRaw, _ := json.Marshal(in.DNSServers)
	tag, err := tx.Exec(ctx, `
        UPDATE guest_networks SET
          name=COALESCE(NULLIF($2,''),name), description=NULLIF($3,''), ssid_label=NULLIF($4,''),
          gateway_ip=COALESCE(NULLIF($5,'')::inet, gateway_ip),
          subnet_cidr=COALESCE(NULLIF($6,'')::cidr, subnet_cidr),
          gateway_cidr = CASE WHEN $5<>'' AND $6<>'' THEN ($5 || '/' || masklen($6::cidr))::inet ELSE gateway_cidr END,
          dhcp_mode=COALESCE(NULLIF($7,''),dhcp_mode), dns_mode=COALESCE(NULLIF($8,''),dns_mode),
          dns_servers=COALESCE($9::jsonb, dns_servers), domain_name=COALESCE(NULLIF($10,''),domain_name),
          lease_default_seconds=COALESCE(NULLIF($11,0),lease_default_seconds),
          lease_min_seconds=COALESCE(NULLIF($12,0),lease_min_seconds),
          lease_max_seconds=COALESCE(NULLIF($13,0),lease_max_seconds),
          captive_portal_enabled=COALESCE($14,captive_portal_enabled),
          internet_access_enabled=COALESCE($15,internet_access_enabled),
          nat_enabled=COALESCE($16,nat_enabled),
          client_isolation_enabled=COALESCE($17,client_isolation_enabled),
          portal_url=CASE WHEN $5<>'' THEN $18 ELSE portal_url END,
          enabled=COALESCE($19, enabled),
          updated_at=now()
        WHERE id=$1`,
		id, in.Name, in.Description, in.SSIDLabel, in.GatewayIP, in.SubnetCIDR,
		in.DHCPMode, in.DNSMode, string(dnsRaw), in.DomainName, in.LeaseDefault, in.LeaseMin,
		in.LeaseMax, in.CaptiveEnabled, in.InternetEnabled, in.NATEnabled, in.ClientIsolation, portalURL,
		in.Enabled)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "guest network not found")
		return
	}
	// replace pools if provided
	if in.Pools != nil {
		if _, err := tx.Exec(ctx, `DELETE FROM dhcp_pools WHERE guest_network_id=$1`, id); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "pool clear")
			return
		}
		if err := insertPools(ctx, tx, id, in.Pools); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "network.guest.updated", "guest_network", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "updated"})
}

func (s *server) deleteGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	// only allow deleting a disabled network with no active sessions
	var enabled bool
	var active int
	_ = s.db.QueryRow(ctx, `SELECT enabled FROM guest_networks WHERE id=$1`, id).Scan(&enabled)
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE guest_network_id=$1 AND state='active'`, id).Scan(&active)
	if enabled {
		jsonErr(w, http.StatusConflict, "conflict", "disable the network before deleting it")
		return
	}
	if active > 0 {
		jsonErr(w, http.StatusConflict, "conflict", "network has active sessions")
		return
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM guest_networks WHERE id=$1`, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "guest network not found")
		return
	}
	s.audit(r, "network.guest.deleted", "guest_network", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) disableGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	if _, err := s.db.Exec(ctx, `UPDATE guest_networks SET enabled=false, updated_at=now() WHERE id=$1`, id); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "disable failed")
		return
	}
	s.audit(r, "network.guest.disabled", "guest_network", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "disabled", "note": "apply to activate the change"})
}

func (s *server) guestNetworkStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var bridge string
	var enabled bool
	if err := s.db.QueryRow(ctx, `SELECT bridge_name, enabled FROM guest_networks WHERE id=$1`, id).Scan(&bridge, &enabled); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "guest network not found")
		return
	}
	var active int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE guest_network_id=$1 AND state='active'`, id).Scan(&active)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "bridge_name": bridge, "enabled": enabled, "active_clients": active,
	})
}

// ---- validate / apply / adopt (proxy to netd) ----

func (s *server) netValidate(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodPost, "/v1/validate", map[string]string{"actor": s.actor(r), "summary": "validate"})
}

func (s *server) netApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Summary string `json:"summary"`
	}
	_ = decodeJSON(r, &in)
	s.audit(r, "network.apply", "network", "", map[string]any{"summary": in.Summary})
	s.netd.proxy(w, r, http.MethodPost, "/v1/apply", map[string]string{"actor": s.actor(r), "summary": in.Summary})
}

func (s *server) netAdopt(w http.ResponseWriter, r *http.Request) {
	s.audit(r, "network.adopt", "network", "", nil)
	s.netd.proxy(w, r, http.MethodPost, "/v1/adopt", map[string]string{"actor": s.actor(r), "summary": "adopt current"})
}

// ---- DHCP ----

func (s *server) dhcpLeases(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodGet, "/v1/leases", nil)
}

func (s *server) listReservations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	gnFilter := r.URL.Query().Get("guest_network_id")
	q := `SELECT id::text, guest_network_id::text, text(mac), host(reserved_ip), hostname, enabled
            FROM dhcp_reservations`
	args := []any{}
	if gnFilter != "" {
		q += ` WHERE guest_network_id=$1`
		args = append(args, gnFilter)
	}
	q += ` ORDER BY reserved_ip`
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	type resFull struct {
		reservationRow
		GuestNetworkID string `json:"guest_network_id"`
	}
	var out []resFull
	for rows.Next() {
		var x resFull
		if err := rows.Scan(&x.ID, &x.GuestNetworkID, &x.MAC, &x.ReservedIP, &x.Hostname, &x.Enabled); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, x)
	}
	writeList(w, out)
}

func (s *server) createReservation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		GuestNetworkID string `json:"guest_network_id"`
		MAC            string `json:"mac"`
		ReservedIP     string `json:"reserved_ip"`
		Hostname       string `json:"hostname"`
		Enabled        *bool  `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil || in.GuestNetworkID == "" || in.MAC == "" || in.ReservedIP == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "guest_network_id, mac and reserved_ip required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var id string
	err := s.db.QueryRow(ctx, `
        INSERT INTO dhcp_reservations (guest_network_id, mac, reserved_ip, hostname, enabled)
        VALUES ($1,$2::macaddr,$3::inet,NULLIF($4,''),$5)
        RETURNING id::text`, in.GuestNetworkID, in.MAC, in.ReservedIP, in.Hostname, boolOr(in.Enabled, true)).Scan(&id)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
		return
	}
	s.audit(r, "network.dhcp.reservation.created", "dhcp_reservation", id, map[string]any{"mac": in.MAC, "ip": in.ReservedIP})
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *server) updateReservation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		ReservedIP string `json:"reserved_ip"`
		Hostname   string `json:"hostname"`
		Enabled    *bool  `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx, `
        UPDATE dhcp_reservations SET
          reserved_ip=COALESCE(NULLIF($2,'')::inet, reserved_ip),
          hostname=NULLIF($3,''), enabled=COALESCE($4,enabled), updated_at=now()
        WHERE id=$1`, id, in.ReservedIP, in.Hostname, in.Enabled)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "reservation not found")
		return
	}
	s.audit(r, "network.dhcp.reservation.updated", "dhcp_reservation", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *server) deleteReservation(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx, `DELETE FROM dhcp_reservations WHERE id=$1`, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "reservation not found")
		return
	}
	s.audit(r, "network.dhcp.reservation.deleted", "dhcp_reservation", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ---- revisions ----

func (s *server) listRevisions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
        SELECT id::text, seq, state, summary, created_at, applied_at, confirmed_at, confirm_deadline, failure_reason
          FROM network_config_revisions ORDER BY seq DESC LIMIT 50`)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, state string
		var seq int64
		var summary, failure *string
		var created, applied, confirmed, deadline *time.Time
		if err := rows.Scan(&id, &seq, &state, &summary, &created, &applied, &confirmed, &deadline, &failure); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, map[string]any{
			"id": id, "seq": seq, "state": state, "summary": summary,
			"created_at": created, "applied_at": applied, "confirmed_at": confirmed,
			"confirm_deadline": deadline, "failure_reason": failure,
		})
	}
	writeList(w, out)
}

func (s *server) getRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var validation, intent []byte
	var state string
	var seq int64
	if err := s.db.QueryRow(ctx,
		`SELECT seq, state, validation, intent FROM network_config_revisions WHERE id=$1`, id).
		Scan(&seq, &state, &validation, &intent); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "revision not found")
		return
	}
	// events + health
	evs, _ := s.db.Query(ctx, `SELECT phase, ok, detail, at FROM network_apply_events WHERE revision_id=$1 ORDER BY at`, id)
	var events []map[string]any
	if evs != nil {
		for evs.Next() {
			var phase string
			var ok bool
			var detail []byte
			var at time.Time
			_ = evs.Scan(&phase, &ok, &detail, &at)
			events = append(events, map[string]any{"phase": phase, "ok": ok, "detail": json.RawMessage(detail), "at": at})
		}
		evs.Close()
	}
	hcs, _ := s.db.Query(ctx, `SELECT check_name, ok, detail, at FROM network_health_checks WHERE revision_id=$1 ORDER BY at`, id)
	var health []map[string]any
	if hcs != nil {
		for hcs.Next() {
			var name, detail string
			var ok bool
			var at time.Time
			_ = hcs.Scan(&name, &ok, &detail, &at)
			health = append(health, map[string]any{"check_name": name, "ok": ok, "detail": detail, "at": at})
		}
		hcs.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "seq": seq, "state": state,
		"validation": json.RawMessage(validation), "intent": json.RawMessage(intent),
		"events": events, "health": health,
	})
}

func (s *server) confirmRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.audit(r, "network.revision.confirmed", "network_revision", id, nil)
	s.netd.proxy(w, r, http.MethodPost, "/v1/confirm", map[string]string{"revision_id": id, "actor": s.actor(r)})
}

func (s *server) rollbackRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	s.audit(r, "network.revision.rolledback", "network_revision", id, nil)
	s.netd.proxy(w, r, http.MethodPost, "/v1/rollback", map[string]string{"revision_id": id, "actor": s.actor(r)})
}

// ---- helpers ----

func (s *server) loadPoolsFor(ctx context.Context, id string) []netcfg.Pool {
	rows, err := s.db.Query(ctx, `SELECT host(start_ip), host(end_ip) FROM dhcp_pools WHERE guest_network_id=$1 ORDER BY sort_order, start_ip`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []netcfg.Pool
	for rows.Next() {
		var p netcfg.Pool
		if rows.Scan(&p.StartIP, &p.EndIP) == nil {
			out = append(out, p)
		}
	}
	return out
}

func (s *server) loadReservationsFor(ctx context.Context, id string) []reservationRow {
	rows, err := s.db.Query(ctx, `SELECT id::text, text(mac), host(reserved_ip), hostname, enabled FROM dhcp_reservations WHERE guest_network_id=$1`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []reservationRow
	for rows.Next() {
		var x reservationRow
		if rows.Scan(&x.ID, &x.MAC, &x.ReservedIP, &x.Hostname, &x.Enabled) == nil {
			out = append(out, x)
		}
	}
	return out
}

func insertPools(ctx context.Context, tx pgx.Tx, networkID string, pools []netcfg.Pool) error {
	for i, p := range pools {
		if _, err := tx.Exec(ctx,
			`INSERT INTO dhcp_pools (guest_network_id, start_ip, end_ip, sort_order) VALUES ($1,$2::inet,$3::inet,$4)`,
			networkID, p.StartIP, p.EndIP, i); err != nil {
			return err
		}
	}
	return nil
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func boolOr(p *bool, d bool) bool {
	if p != nil {
		return *p
	}
	return d
}

func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func pgErr(err error) string {
	msg := err.Error()
	// surface a friendlier hint for the common constraint hits
	switch {
	case strings.Contains(msg, "guest_networks_vlan_parent_uniq"):
		return "that VLAN id is already used on this interface"
	case strings.Contains(msg, "guest_networks_untagged_parent_uniq"):
		return "that interface already has an untagged guest network"
	case strings.Contains(msg, "dhcp_reservations_guest_network_id_mac"):
		return "a reservation for that MAC already exists on this network"
	case strings.Contains(msg, "dhcp_reservations_guest_network_id_reserved_ip"):
		return "that IP is already reserved on this network"
	}
	return msg
}
