package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// Guest-network CRUD is site-DB owned by edged; validate/apply/confirm/rollback and interface/lease/revision
// reads proxy to the privileged netd daemon. All apply/rollback are audited. Single "network" permission key.

func (s *server) networkRoutes() http.Handler {
	r := chi.NewRouter()

	r.Get("/interfaces", s.netInterfaces)
	r.Patch("/interfaces/{name}/role", s.netInterfaceRole)

	r.Get("/guest-networks", s.listGuestNetworks)
	r.Post("/guest-networks", s.createGuestNetwork)
	r.Post("/guest-networks/batch", s.createGuestNetworkBatch)
	r.Get("/guest-networks/{id}", s.getGuestNetwork)
	r.Put("/guest-networks/{id}", s.updateGuestNetwork)
	r.Delete("/guest-networks/{id}", s.deleteGuestNetwork)
	r.Post("/guest-networks/{id}/disable", s.disableGuestNetwork)
	r.Post("/guest-networks/{id}/enable", s.enableGuestNetwork)
	r.Post("/guest-networks/{id}/replace", s.stageGuestNetworkReplacement)
	r.Get("/guest-networks/{id}/impact", s.getNetworkImpact)
	r.Get("/guest-network-replacements", s.listGuestNetworkReplacements)
	r.Delete("/guest-network-replacements/{id}", s.cancelGuestNetworkReplacement)
	r.Get("/guest-networks/{id}/status", s.guestNetworkStatus)
	s.mountRecovery(r)
	s.mountArchive(r)
	r.Get("/guest-networks/{id}/sign-in", s.getNetworkSignin)
	r.Put("/guest-networks/{id}/sign-in", s.putNetworkSignin)
	r.Get("/guest-networks/{id}/diagnostics", s.networkDiagnostics)
	r.Get("/overview", s.networkOverview)
	r.Get("/factory", s.getFactory)
	r.Post("/factory/activate", s.activateFactory)
	r.Get("/changes", s.listStagedChanges)
	r.Delete("/changes/{group_id}", s.cancelStagedGroup)
	r.Get("/ports", s.listPorts)
	r.Post("/ports/{iface}/mode", s.setPortMode)

	// There is no "adopt": declaring the editable rows ACTIVE without validating or applying them is retired. A
	// client network configuration becomes active only by being applied, health-checked and confirmed.
	r.Post("/validate", s.netValidate)
	r.Post("/apply", s.netApply)

	// System (WAN/LAN) network: GET = network.view; POST = permWrite. Apply and rollback re-authenticate the password.
	r.Get("/system", s.sysNetGet)
	r.Get("/system/history", s.sysNetHistory)
	r.Get("/system/diagnostics", s.sysNetDiagnostics)
	r.Post("/system/validate", s.sysNetValidate)
	r.Post("/system/apply", s.sysNetApply)
	r.Post("/system/confirm", s.sysNetConfirm)
	r.Post("/system/rollback", s.sysNetRollback)

	r.Get("/dhcp/leases", s.dhcpLeases)
	r.Get("/dhcp/reservations", s.listReservations)
	r.Post("/dhcp/reservations", s.createReservation)
	r.Put("/dhcp/reservations/{id}", s.updateReservation)
	r.Delete("/dhcp/reservations/{id}", s.deleteReservation)

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
	Archived        bool             `json:"archived"`
	IsDefault       bool             `json:"is_default"`
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
	if ctx, cancel := dbCtx(r); true {
		_ = s.reconcileStaleReplacements(ctx, r)
		cancel()
	}
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
	_ = s.archiveRetired(ctx, s.db)
	archived, def := s.archivedSet(ctx), s.defaultNetworkID(ctx)
	for i := range out {
		out[i].Pools = s.loadPoolsFor(ctx, out[i].ID)
		out[i].Archived, out[i].IsDefault = archived[out[i].ID], out[i].ID == def
	}
	writeList(w, out)
}

func (s *server) getGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	g, err := scanGuestNetwork(s.db.QueryRow(ctx, `SELECT `+gnCols+` FROM guest_networks WHERE id=$1`, id))
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
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
	normalizeGuestNetworkInput(&in)
	if in.Name == "" {
		jsonErr(w, http.StatusBadRequest, "invalid_name", "network name is required")
		return
	}
	if writeNetworkTextRefusal(w, in.Name, in.DomainName) {
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
	created, err := s.insertGuestNetwork(ctx, tx, in, nil)
	if err != nil {
		writeNetworkInsertErr(w, err)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "network.guest.created", "guest_network", created.ID, map[string]any{"name": in.Name, "vlan": created.VLAN})
	writeJSON(w, http.StatusCreated, map[string]any{"id": created.ID, "bridge_name": created.Bridge, "portal_url": created.PortalURL})
}

func (s *server) updateGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in guestNetworkInput
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if writeNetworkTextRefusal(w, in.Name, in.DomainName) {
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

	// The topology fields (network_type, vlan_id, parent_interface, bridge_name) are immutable: the apply diff is
	// keyed on bridge_name (apply_ops.go), so an edited row would never create the new VLAN sub-interface while the
	// rendered netplan and nft described it, and the nft fingerprint would report "converged" against a bridge with
	// the wrong tag. A change to any of them is refused, naming delete-and-recreate. An unchanged echo is accepted,
	// so the comparison is against what is stored, not against presence in the body.
	var curType, curParent string
	var curVLAN *int
	if err := tx.QueryRow(ctx,
		`SELECT network_type, parent_interface, vlan_id FROM guest_networks WHERE id=$1`, id).
		Scan(&curType, &curParent, &curVLAN); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
		return
	}
	if immutable := immutableTopologyChange(in, curType, curParent, curVLAN); immutable != "" {
		jsonErr(w, http.StatusConflict, "immutable_topology", immutable)
		return
	}

	portalURL := netcfg.PortalURLFor(in.GatewayIP, 8380)
	// dns_servers: nil (not supplied) must stay SQL NULL so COALESCE keeps the stored value; a nil slice marshals to
	// JSON null, which COALESCE does not fall through and would overwrite the column. An empty array clears it.
	var dnsArg any
	if in.DNSServers != nil {
		raw, merr := json.Marshal(in.DNSServers)
		if merr != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "dns_servers is not encodable")
			return
		}
		dnsArg = string(raw)
	}
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
		in.DHCPMode, in.DNSMode, dnsArg, in.DomainName, in.LeaseDefault, in.LeaseMin,
		in.LeaseMax, in.CaptiveEnabled, in.InternetEnabled, in.NATEnabled, in.ClientIsolation, portalURL,
		in.Enabled)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
		return
	}
	// The edited network may not collide with another live one: checked on what is now stored, as the next apply sees it.
	if cerr := s.enabledConflict(ctx, tx, id); cerr != nil {
		writeNetworkInsertErr(w, cerr)
		return
	}
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

// Counts sessions live on this network now: an active session whose address is inside its subnet. Joining every
// device that ever appeared double-counted moved devices and blocked deleting an unused network.
const activeOnNetworkSQL = `SELECT count(*) FROM iam_v2.sessions se
	  JOIN guest_networks g ON g.id = $1
	 WHERE se.state = 'active' AND se.ended IS NULL AND se.ip IS NOT NULL AND se.ip <<= g.subnet_cidr`

func (s *server) deleteGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Both reads are checked: a failed read must not look like enabled=false, active=0 ("safe to delete").
	var enabled bool
	var active int
	if err := s.db.QueryRow(ctx, `SELECT enabled FROM guest_networks WHERE id=$1`, id).Scan(&enabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			jsonErr(w, http.StatusNotFound, "not_found", "no such client network")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal",
			"whether this client network is still in use could not be determined, so it was not deleted")
		return
	}
	if err := s.db.QueryRow(ctx, activeOnNetworkSQL, id).Scan(&active); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal",
			"whether this client network still has clients online could not be determined, so it was not deleted")
		return
	}
	if enabled {
		jsonErr(w, http.StatusConflict, "conflict", "disable the network before deleting it")
		return
	}
	if active > 0 {
		jsonErr(w, http.StatusConflict, "conflict", "network has active sessions")
		return
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM guest_networks WHERE id=$1`, id)
	if err == nil {
		_, _ = s.db.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_policy WHERE guest_network_id=$1::uuid`, id)
	}
	if err != nil {
		// A network that has seen clients is part of their history: sign-ins, resolutions and device appearances
		// reference it (foreign keys without cascade), so it cannot be deleted. It stays disabled.
		if isFKViolation(err) {
			jsonErr(w, http.StatusConflict, "kept_as_history",
				"this network has client history (sign-ins and devices recorded on it), so it is kept, disabled, "+
					"as part of that history. A disabled network is not served and does not block its VLAN or interface.")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
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

// The way back from disable: stages the network as enabled and touches nothing else (a PUT carrying only
// "enabled" would clear the description and SSID label). Takes effect on the next apply.
func (s *server) enableGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "enable failed")
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE guest_networks SET enabled=true, updated_at=now() WHERE id=$1`, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "enable failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "guest network not found")
		return
	}
	if cerr := s.enabledConflict(ctx, tx, id); cerr != nil {
		writeNetworkInsertErr(w, cerr)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "enable failed")
		return
	}
	s.audit(r, "network.guest.enabled", "guest_network", id, nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "enabled", "note": "apply to activate the change"})
}

func (s *server) guestNetworkStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var bridge string
	var enabled bool
	if err := s.db.QueryRow(ctx, `SELECT bridge_name, enabled FROM guest_networks WHERE id=$1`, id).Scan(&bridge, &enabled); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
		return
	}
	var active int
	_ = s.db.QueryRow(ctx, activeOnNetworkSQL, id).Scan(&active)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "bridge_name": bridge, "enabled": enabled, "active_clients": active,
	})
}

func (s *server) netValidate(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodPost, "/v1/validate", map[string]string{"actor": s.actor(r), "summary": "validate"})
}

// Materialises every staged replacement, then asks netd to render and apply the whole intent. A validation
// failure, failed apply or failed health check is undone in the same request, so the database and the appliance never disagree.
func (s *server) netApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Summary  string `json:"summary"`
		Password string `json:"password"`
	}
	_ = decodeJSON(r, &in)
	// Step-up: an apply changes what every client network forwards, so it needs the operator's password before anything is staged.
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation failed")
		return
	}
	ctx, cancel := dbCtx(r)

	// One apply at a time per site. Without it, two applies interleave: the loser of netd's single in-flight slot
	// reverts its own materialisation (deleting the successor row) while the other has already built that
	// successor's bridge, leaving a bridge no enabled row describes. It also makes the /v1/pending fallback
	// trustworthy. The lock is SESSION-scoped on a DEDICATED connection (it must outlive the materialise
	// transaction and cover the netd call); releasing through the pool would hit another connection and silently
	// fail, leaving the appliance unable to apply anything.
	lockConn, lerr := s.db.Acquire(context.Background())
	if lerr != nil {
		cancel()
		jsonErr(w, http.StatusInternalServerError, "internal", "the apply lock could not be taken")
		return
	}
	defer lockConn.Release()
	lockKey := "stayconnect:network-apply:" + s.siteID
	lockCtx, lockCancel := context.WithTimeout(context.Background(), 15*time.Second)
	var locked bool
	lockErr := lockConn.QueryRow(lockCtx, `SELECT pg_try_advisory_lock(hashtext($1))`, lockKey).Scan(&locked)
	lockCancel()
	if lockErr != nil || !locked {
		cancel()
		if lockErr != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the apply lock could not be taken")
			return
		}
		jsonErr(w, http.StatusConflict, "apply_in_progress",
			"another network configuration apply is already running on this appliance. Wait for it to finish or be "+
				"rolled back, then apply again.")
		return
	}
	defer func() {
		rel, relCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer relCancel()
		if _, err := lockConn.Exec(rel, `SELECT pg_advisory_unlock(hashtext($1))`, lockKey); err != nil {
			slog.Error("the network apply lock could not be released", "err", err)
		}
	}()

	_ = s.reconcileStaleReplacements(ctx, r)

	tx, err := s.db.Begin(ctx)
	if err != nil {
		cancel()
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	mats, merr := s.materialisePendingReplacements(ctx, tx, s.actor(r))
	if merr != nil {
		_ = tx.Rollback(ctx)
		cancel()
		writeNetworkInsertErr(w, merr)
		return
	}
	chg, cerr := s.materialisePendingChanges(ctx, tx)
	if cerr != nil {
		_ = tx.Rollback(ctx)
		cancel()
		writeNetworkInsertErr(w, cerr)
		return
	}
	if err := tx.Commit(ctx); err != nil {
		_ = tx.Rollback(ctx)
		cancel()
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	cancel() // the materialise is committed; the netd call and the recovery below get their own contexts
	staged := make([]string, 0, len(mats))
	for _, m := range mats {
		staged = append(staged, m.ReplacementID)
	}
	// Everything the apply materialised is put back together if it does not hold.
	revertAll := func(reason string) error {
		return errors.Join(s.revertMaterialised(ctx, staged, reason), s.revertChanges(ctx, chg, reason))
	}
	everything := append(append([]string{}, staged...), chg...)

	st, raw, err := s.netd.call(r.Context(), http.MethodPost, "/v1/apply",
		map[string]string{"actor": s.actor(r), "summary": in.Summary})
	// The audit follows the outcome, as confirm and rollback do, so an apply that never reached the appliance is not logged as one.
	{
		detail := map[string]any{"summary": in.Summary, "replacements": staged, "changes": chg, "status": st}
		var out struct {
			RevisionID         string   `json:"revision_id"`
			State              string   `json:"state"`
			RollbackIncomplete []string `json:"rollback_incomplete"`
		}
		if err != nil {
			detail["error"] = err.Error()
		} else if json.Unmarshal(raw, &out) == nil {
			detail["revision_id"], detail["state"] = out.RevisionID, out.State
			if len(out.RollbackIncomplete) > 0 {
				// The rollback ran but did not restore everything: the audit trail must not read as a clean rollback.
				detail["rollback_incomplete"] = out.RollbackIncomplete
			}
		}
		action := "network.apply"
		if err != nil || st >= 300 || out.State == "failed" || out.State == "rolled_back" {
			action = "network.apply_failed"
		}
		s.audit(r, action, "network", out.RevisionID, detail)
	}

	// Everything below runs on a fresh context derived from Background: dbCtx allows ten seconds but the netd call
	// allows a hundred, so the materialise context is usually expired by then and recovery would fail on it,
	// leaving a successor live on the wire while the database held it APPLIED with no revision. A revert must
	// finish whether or not the operator is still waiting.
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err != nil {
		if rerr := revertAll("netd could not be reached"); rerr != nil {
			unrevertedStagedChanges(w, everything, rerr, "the appliance's network service could not be reached")
			return
		}
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", err.Error())
		return
	}
	var res struct {
		RevisionID string `json:"revision_id"`
		State      string `json:"state"`
	}
	_ = json.Unmarshal(raw, &res)
	switch {
	case st >= 300, res.State == "failed", res.State == "rolled_back":
		if rerr := revertAll("the apply did not hold (" + res.State + ")"); rerr != nil {
			unrevertedStagedChanges(w, everything, rerr, "the configuration did not hold ("+res.State+")")
			return
		}
	case len(staged)+len(chg) > 0:
		// The revision id is what everything noticing a watchdog rollback later keys on. If netd's answer did not
		// carry it, ask netd (/v1/pending) rather than guess.
		rev := res.RevisionID
		if rev == "" {
			if _, praw, perr := s.netd.call(r.Context(), http.MethodGet, "/v1/pending", nil); perr == nil {
				var p struct {
					RevisionID string `json:"revision_id"`
				}
				_ = json.Unmarshal(praw, &p)
				rev = p.RevisionID
			}
		}
		if rev == "" {
			// netd said the apply was fine yet there is no revision to confirm; edged does not know what the
			// appliance did and must not leave the database claiming a successor on that basis.
			if rerr := revertAll("the apply reported no revision to confirm"); rerr != nil {
				unrevertedStagedChanges(w, everything, rerr, "the apply named no revision")
				return
			}
			jsonErr(w, http.StatusBadGateway, "apply_unidentifiable",
				"the appliance applied the configuration but did not say which revision carries it, so the staged client-network changes were put back. Check Client networks and apply again.")
			return
		}
		_, uerr := s.db.Exec(ctx, `UPDATE iam_v2.guest_network_replacements SET revision_id=$2::uuid
			 WHERE id = ANY($1::uuid[]) AND state='APPLIED'`, staged, rev)
		if uerr == nil {
			uerr = s.recordChangesRevision(ctx, chg, rev)
		}
		if uerr != nil {
			// Without the record nothing can tell later whether this apply was kept or rolled back, so it is put back now.
			slog.Error("client network replacement revision could not be recorded", "err", uerr)
			if rerr := revertAll("the apply's revision could not be recorded"); rerr != nil {
				unrevertedStagedChanges(w, everything, rerr, "the apply's revision could not be recorded")
				return
			}
			jsonErr(w, http.StatusInternalServerError, "revision_not_recorded",
				"the staged client-network changes could not be tied to the applied configuration and were put back. Apply again.")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

func (s *server) dhcpLeases(w http.ResponseWriter, r *http.Request) {
	// Unpaged callers get netd's answer untouched; a paged caller gets one page, searched and ordered here (dhcp_paging.go).
	if !hasPageParams(r.URL.Query()) {
		s.netd.proxy(w, r, http.MethodGet, "/v1/leases", nil)
		return
	}
	pg, ok := readPage(w, r, 0)
	if !ok {
		return
	}
	search, ok := readSearch(w, r, dhcpSearchHeader)
	if !ok {
		return
	}
	st, raw, err := s.netd.call(r.Context(), http.MethodGet, "/v1/leases", nil)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", err.Error())
		return
	}
	if st != http.StatusOK {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(st)
		_, _ = w.Write(raw)
		return
	}
	out, err := pageLeases(raw, search, pg)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "dhcp_leases_unreadable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) listReservations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	paged := hasPageParams(r.URL.Query())
	pg, ok := readPage(w, r, 0)
	if !ok {
		return
	}
	search, ok := readSearch(w, r, dhcpSearchHeader)
	if !ok {
		return
	}
	gnFilter := r.URL.Query().Get("guest_network_id")
	where := ` WHERE true`
	args := []any{}
	if gnFilter != "" {
		args = append(args, gnFilter)
		where += fmt.Sprintf(` AND d.guest_network_id=$%d`, len(args))
	}
	if search != "" {
		args = append(args, likePattern(search))
		where += fmt.Sprintf(` AND (text(d.mac) ILIKE $%[1]d OR host(d.reserved_ip) ILIKE $%[1]d
		    OR COALESCE(d.hostname,'') ILIKE $%[1]d OR COALESCE(gn.name,'') ILIKE $%[1]d)`, len(args))
	}
	from := ` FROM dhcp_reservations d LEFT JOIN guest_networks gn ON gn.id = d.guest_network_id`
	q := `SELECT d.id::text, d.guest_network_id::text, text(d.mac), host(d.reserved_ip), d.hostname, d.enabled` +
		from + where + ` ORDER BY d.reserved_ip, d.id`
	if paged {
		q += fmt.Sprintf(` LIMIT %d OFFSET %d`, pg.Fetch(), pg.Offset())
	}
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
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if !paged {
		writeList(w, out)
		return
	}
	rows.Close()
	out, more := trimPage(out, pg)
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int`+from+where, args...).Scan(&total); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, newPagedList(out, more, pg, &total))
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
	if writeHostnameRefusal(w, in.Hostname) {
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
	if writeHostnameRefusal(w, in.Hostname) {
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

// Keeps the applied configuration: only now does a replacement become permanent, and the Internet Packages that
// named the retired network are republished forward onto its successor.
func (s *server) confirmRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
	}
	_ = decodeJSON(r, &in)
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation failed")
		return
	}
	// Fail closed before the wire is made permanent: confirming retires the old client network for good, so the
	// dependent packages are pre-flighted first, writing nothing. If one cannot be carried forward the confirm is
	// refused and the appliance stays inside its confirmation window.
	{
		ctx, cancel := dbCtx(r)
		if blocking := s.replacementsBlockingConfirm(ctx, id); len(blocking) > 0 {
			cancel()
			// The reason is in the message, not only a structured field: every confirm button renders the message.
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": "packages_not_preservable",
				"message": "this change cannot be kept yet. These Internet Packages are limited to the client network being replaced and could not be carried forward onto its successor: " +
					describeBlockingPackages(blocking) +
					". Correct them in Internet packages and confirm again, or roll back.",
				"blocking": blocking,
			})
			return
		}
		cancel()
	}
	// The audit follows the action, so a 409, 500 or timeout does not leave a record claiming the revision was confirmed.
	st, raw, err := s.netd.call(r.Context(), http.MethodPost, "/v1/confirm",
		map[string]string{"revision_id": id, "actor": s.actor(r)})
	if err != nil {
		s.audit(r, "network.revision.confirm_failed", "network_revision", id, map[string]any{"error": err.Error()})
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", err.Error())
		return
	}
	s.audit(r, "network.revision.confirmed", "network_revision", id, map[string]any{"kept": st < 300, "status": st})
	if st < 300 {
		ctx, cancel := dbCtx(r)
		defer cancel()
		if cerr := s.settleConfirmedChanges(ctx, id); cerr != nil {
			slog.Error("staged client-network changes could not be settled after a confirm; the next read retries", "err", cerr)
		}
		if settled := s.settleConfirmedReplacements(ctx, r, id); len(settled) > 0 {
			out := map[string]any{}
			_ = json.Unmarshal(raw, &out)
			out["replacements"] = settled
			if merged, merr := json.Marshal(out); merr == nil {
				raw = merged
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

// Undoes the applied configuration: the appliance returns to the previous confirmed revision and every
// replacement the apply materialised is reverted, leaving the original network enabled.
func (s *server) rollbackRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Password string `json:"password"`
	}
	_ = decodeJSON(r, &in)
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation failed")
		return
	}
	st, raw, err := s.netd.call(r.Context(), http.MethodPost, "/v1/rollback",
		map[string]string{"revision_id": id, "actor": s.actor(r)})
	if err != nil {
		s.audit(r, "network.revision.rollback_failed", "network_revision", id, map[string]any{"error": err.Error()})
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", err.Error())
		return
	}
	s.audit(r, "network.revision.rolledback", "network_revision", id, map[string]any{"undone": st < 300, "status": st})
	if st < 300 {
		// Background-derived context: netd has already changed the wire, so the database must be brought into line even if the operator disconnects.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// The operator asked for this, so they are told if it did not finish: if the staged rows could not follow, the database still claims the successor.
		if rerr := s.reconcileStaleReplacements(ctx, r); rerr != nil {
			jsonErr(w, http.StatusInternalServerError, "staged_changes_not_reverted",
				"the live network was rolled back, but "+rerr.Error()+". The live network is unchanged by this; "+
					"reload Client networks, which retries it.")
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}

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
	switch {
	case strings.Contains(msg, "guest_networks_vlan_parent_uniq"):
		return "that VLAN id is already used on this interface"
	case strings.Contains(msg, "guest_networks_untagged_parent_uniq"):
		return "that interface already has an untagged client network"
	case strings.Contains(msg, "guest_networks_bridge_uniq"):
		return "another client network already uses that bridge name; try again"
	case strings.Contains(msg, "dhcp_reservations_guest_network_id_mac"):
		return "a reservation for that MAC already exists on this network"
	case strings.Contains(msg, "dhcp_reservations_guest_network_id_reserved_ip"):
		return "that IP is already reserved on this network"
	}
	return msg
}

// Returns a refusal message, or "" when no topology change is requested. Compares against what is stored, so a
// GET-edit-PUT of the whole object is accepted. bridge_name is derived at create and named in the message because it moves too.
func immutableTopologyChange(in guestNetworkInput, curType, curParent string, curVLAN *int) string {
	const lifecycle = " Topology is fixed at creation: disable the network, apply, delete it, and create a " +
		"new one with the topology you want. Changing it in place would leave the rendered configuration " +
		"describing one VLAN while the live bridge still carries another."

	if in.NetworkType != "" && in.NetworkType != curType {
		return "network_type cannot be changed (this network is " + curType + ")." + lifecycle
	}
	if in.ParentInterface != "" && in.ParentInterface != curParent {
		return "parent_interface cannot be changed (this network is on " + curParent + ")." + lifecycle
	}
	if in.VLANID != nil {
		switch {
		case curVLAN == nil:
			return "vlan_id cannot be set on an untagged network." + lifecycle
		case *in.VLANID != *curVLAN:
			return fmt.Sprintf("vlan_id cannot be changed (this network is VLAN %d).%s", *curVLAN, lifecycle)
		}
	}
	return ""
}
