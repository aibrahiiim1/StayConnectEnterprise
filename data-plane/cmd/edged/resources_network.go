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
	r.Post("/guest-networks/batch", s.createGuestNetworkBatch)
	r.Get("/guest-networks/{id}", s.getGuestNetwork)
	r.Put("/guest-networks/{id}", s.updateGuestNetwork)
	r.Delete("/guest-networks/{id}", s.deleteGuestNetwork)
	r.Post("/guest-networks/{id}/disable", s.disableGuestNetwork)
	r.Post("/guest-networks/{id}/enable", s.enableGuestNetwork)
	r.Post("/guest-networks/{id}/replace", s.stageGuestNetworkReplacement)
	r.Get("/guest-network-replacements", s.listGuestNetworkReplacements)
	r.Delete("/guest-network-replacements/{id}", s.cancelGuestNetworkReplacement)
	r.Get("/guest-networks/{id}/status", s.guestNetworkStatus)

	// validate / apply operate on the whole intent (all networks) via netd.
	//
	// THERE IS NO "ADOPT". A POST /network/adopt used to declare the editable guest_networks rows to be the
	// ACTIVE revision without applying them, without validating them, and without a single check that the live
	// Linux network matched -- see cmd/netd/store.go markActive for what that cost. It is retired: a client
	// network configuration becomes active by being applied, health-checked and confirmed, and by no other route.
	r.Post("/validate", s.netValidate)
	r.Post("/apply", s.netApply)

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

	// The Cloud Connection page and the setup wizard that used to live here (/cloud*, /setup/*) are replaced
	// by /edge/v1/central/* (resources_cloud.go).

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
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	defer tx.Rollback(ctx)

	// THE TOPOLOGY FIELDS ARE IMMUTABLE, AND SAYING SO IS PART OF BEING IMMUTABLE.
	//
	// network_type, vlan_id, parent_interface and bridge_name are decided at create and cannot be changed:
	// delete and recreate is the supported lifecycle, the UI says so in prose, and the comment that used to
	// sit here said so too. What the comment did NOT do was tell the CALLER. The request struct accepts all
	// four, the UPDATE statement below names none of them, and the handler answered
	// 200 {"status":"updated"}. A client that PUT {"vlan_id": 30} against a VLAN-20 network was told its
	// change had been applied, and nothing anywhere had changed.
	//
	// Worse than the lie is what a client would do next. A hand-edited row would not take effect either: the
	// apply diff is keyed on bridge_name (apply_ops.go), bridge_name is computed once at create, and so the
	// existing bridge is found present, createNetwork is never called, the VLAN sub-interface for the new id
	// is never made -- while the rendered netplan and nft DESCRIBE the new id. The nft fingerprint would then
	// report "converged" against a kernel whose bridge carries the wrong tag.
	//
	// So a change to any of the four is REFUSED, with the supported lifecycle named in the message. An
	// unchanged echo is accepted: a client that GETs the object, edits one field and PUTs the whole thing
	// back is doing nothing wrong, and refusing it would break the honest case to punish the dishonest one.
	// The comparison is therefore against what is stored, not against presence in the body.
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
	// dns_servers: SQL NULL when the caller did not supply the field, so COALESCE preserves what is stored.
	//
	// It used to be `string(json.Marshal(in.DNSServers))`, which for a nil slice is the four bytes `null` --
	// JSON null, not SQL NULL. COALESCE does not fall through a JSON null, so every PUT that omitted
	// dns_servers OVERWROTE the column with JSON null. A custom resolver list survived exactly until the
	// next unrelated edit of the same network, and then the render had nothing to write.
	//
	// nil means "not supplied, keep it"; an empty array means "clear it", which is a real instruction and is
	// passed through as `[]`.
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
	//
	// BOTH READS ARE CHECKED. They were discarded, so a failed read produced enabled=false and active=0 -- which
	// is precisely "it is safe to delete". The two guards that stop an operator from deleting a live client
	// network out from under its guests were therefore disabled by any error that reached them.
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
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions se
	           JOIN iam_v2.device_network_appearances a ON a.device_id = se.device_id
	          WHERE a.guest_network_id=$1 AND se.state='active'`, id).Scan(&active); err != nil {
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
	if err != nil {
		// A NETWORK THAT HAS SEEN CLIENTS IS PART OF THEIR HISTORY. Sign-ins, resolutions and device
		// appearances name the network they happened on (foreign keys with no cascade, deliberately), so such a
		// network cannot be deleted -- that used to surface as a bare "delete failed" 500. It stays disabled:
		// it is not rendered, frees its VLAN/interface for a new network, and keeps the history readable.
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

// enableGuestNetwork is the way back from disableGuestNetwork: it stages the network as enabled and touches
// nothing else (a PUT carrying only "enabled" would clear the description and SSID label). Like disabling, it
// takes effect on the next apply.
func (s *server) enableGuestNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx, `UPDATE guest_networks SET enabled=true, updated_at=now() WHERE id=$1`, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "enable failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "guest network not found")
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
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions se
	           JOIN iam_v2.device_network_appearances a ON a.device_id = se.device_id
	          WHERE a.guest_network_id=$1 AND se.state='active'`, id).Scan(&active)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "bridge_name": bridge, "enabled": enabled, "active_clients": active,
	})
}

// ---- validate / apply (proxy to netd) ----

func (s *server) netValidate(w http.ResponseWriter, r *http.Request) {
	s.netd.proxy(w, r, http.MethodPost, "/v1/validate", map[string]string{"actor": s.actor(r), "summary": "validate"})
}

// netApply materialises every staged client-network replacement, then asks netd to render and apply the whole
// intent. netd answers synchronously, so a validation failure, a failed apply or a failed health check is
// undone here, in the same request: the database and the appliance never disagree about which network is live.
func (s *server) netApply(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Summary string `json:"summary"`
	}
	_ = decodeJSON(r, &in)
	ctx, cancel := dbCtx(r)

	// ONE APPLY AT A TIME, FOR THIS SITE.
	//
	// Nothing serialized materialise-commit + netd-apply as a unit, and the two interleave badly. Request A
	// commits its materialisation and calls netd; request B calls netd a moment later; netd's LoadIntent now
	// includes A's successor, and whichever of the two loses netd's single in-flight slot gets a 500. When that
	// is A, A reverts ITS OWN materialisation -- deleting the successor row -- while B's apply has already built
	// that successor's bridge and is awaiting confirmation. The wire then holds a bridge no enabled row
	// describes, and the database holds an original that is not on the wire.
	//
	// It also makes the /v1/pending fallback below trustworthy: with one apply in flight, the pending revision
	// netd names can only be this one's.
	//
	// A SESSION-scoped advisory lock on a DEDICATED CONNECTION, not a transaction one: it has to outlive the
	// materialise transaction and cover the netd call, and a session lock belongs to the connection that took
	// it -- so it is taken and released on a connection held for the whole handler. Releasing it through the
	// pool would reach some other connection and quietly fail, leaving the appliance unable to apply anything.
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

	s.audit(r, "network.apply", "network", "", map[string]any{"summary": in.Summary, "replacements": staged})
	st, raw, err := s.netd.call(r.Context(), http.MethodPost, "/v1/apply",
		map[string]string{"actor": s.actor(r), "summary": in.Summary})

	// EVERYTHING BELOW RUNS ON A FRESH CONTEXT OF ITS OWN, NOT ON THE ONE THE MATERIALISE USED.
	//
	// dbCtx gives ten seconds; the netd client deliberately allows a hundred, because an apply does netplan
	// generate, two systemctl restarts, bridge creation and health checks. So by the time netd answers, the
	// context the materialise ran under is almost always ALREADY EXPIRED -- and every recovery path below used
	// it. The consequence was the worst available: a successful apply whose revision id could not be recorded
	// (context deadline exceeded), whose revert then also failed on the same dead context, leaving the successor
	// live and confirmed on the wire while the database held it as APPLIED with no revision -- which this very
	// file's reconcile then reverted ten minutes later, tearing down a working network.
	//
	// It is derived from Background, not from the request: a revert must finish whether or not the operator is
	// still waiting, and a closed browser tab is not a reason to leave the database describing a network that is
	// not there.
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err != nil {
		if rerr := s.revertMaterialised(ctx, staged, "netd could not be reached"); rerr != nil {
			unrevertedStagedChanges(w, staged, rerr, "the appliance's network service could not be reached")
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
		if rerr := s.revertMaterialised(ctx, staged, "the apply did not hold ("+res.State+")"); rerr != nil {
			unrevertedStagedChanges(w, staged, rerr, "the configuration did not hold ("+res.State+")")
			return
		}
	case len(staged) > 0:
		// WHICH REVISION CARRIES THEM, OR NO APPLY AT ALL. Everything that notices a watchdog rollback later
		// keys on this id, so a replacement that never records one is a successor on the wire the database
		// cannot account for.
		//
		// A successful apply states its revision; this asks netd directly when the answer did not arrive
		// (a body edged could not parse, a field a future netd renames), because GUESSING is the one thing
		// that may not happen here, and /v1/pending is the same question netd's own watchdog asks.
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
			// netd answered that the apply was fine and there is no revision to confirm. Those two statements
			// cannot both be true, so edged does not know what the appliance did -- and must not leave the
			// database claiming a successor network on that basis.
			if rerr := s.revertMaterialised(ctx, staged, "the apply reported no revision to confirm"); rerr != nil {
				unrevertedStagedChanges(w, staged, rerr, "the apply named no revision")
				return
			}
			jsonErr(w, http.StatusBadGateway, "apply_unidentifiable",
				"the appliance applied the configuration but did not say which revision carries it, so the staged client-network changes were put back. Check Client networks and apply again.")
			return
		}
		if _, uerr := s.db.Exec(ctx, `UPDATE iam_v2.guest_network_replacements SET revision_id=$2::uuid
			 WHERE id = ANY($1::uuid[]) AND state='APPLIED'`, staged, rev); uerr != nil {
			// THE RECORD IS THE WHOLE SAFETY NET. Without it nothing can tell later whether this apply was
			// kept or rolled back, so the change is put back now rather than left unaccountable.
			slog.Error("client network replacement revision could not be recorded", "err", uerr)
			if rerr := s.revertMaterialised(ctx, staged, "the apply's revision could not be recorded"); rerr != nil {
				unrevertedStagedChanges(w, staged, rerr, "the apply's revision could not be recorded")
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

// ---- DHCP ----

func (s *server) dhcpLeases(w http.ResponseWriter, r *http.Request) {
	// Unpaged callers get netd's answer untouched, as they always did. A paged caller gets one page of it,
	// searched and ordered here (dhcp_paging.go).
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
	// Paged only when asked: a client network's own page reads every reservation it has, as it always did.
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
	// The search matches what the screen shows a reservation by, the network's name included.
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

// confirmRevision keeps the applied configuration. Only now does a replacement become permanent: the request
// is settled and the Internet Packages that named the retired network are republished forward onto its
// successor, so eligibility keeps meaning what the hotel intended.
func (s *server) confirmRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// FAIL-CLOSED, BEFORE THE WIRE IS MADE PERMANENT. Confirming tells netd to keep this configuration; after
	// that the retired client network is gone for good. So the packages that depend on it are pre-flighted
	// first, writing nothing: if one could not be carried forward, the confirm is refused and the appliance is
	// left inside its confirmation window, where the operator can still correct the package and confirm, or let
	// the watchdog roll the whole change back. Confirming anyway would be exactly the half-finished state this
	// whole staged lifecycle exists to prevent.
	{
		ctx, cancel := dbCtx(r)
		if blocking := s.replacementsBlockingConfirm(ctx, id); len(blocking) > 0 {
			cancel()
			// The reason is IN THE MESSAGE, not only in a structured field. Every confirm button in the console
			// renders the message; an operator told "packages_not_preservable" and nothing else has no idea
			// which package or why, and the only lever they would have left is rolling back a change that was
			// otherwise correct.
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
	// THE AUDIT FOLLOWS THE ACTION. It used to be written before the netd call, so a 409, a 500 or a timeout
	// still left a record asserting the revision had been confirmed -- in the log an operator reads to find out
	// what was done to the live network, which is the wrong direction to be wrong in.
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

// rollbackRevision undoes the applied configuration. The appliance goes back to the previous confirmed
// revision and so does this database: every replacement the apply materialised is reverted, leaving the
// original client network enabled and the request waiting for another attempt.
func (s *server) rollbackRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	st, raw, err := s.netd.call(r.Context(), http.MethodPost, "/v1/rollback",
		map[string]string{"revision_id": id, "actor": s.actor(r)})
	if err != nil {
		s.audit(r, "network.revision.rollback_failed", "network_revision", id, map[string]any{"error": err.Error()})
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", err.Error())
		return
	}
	// Recorded with what actually happened, for the reason given on the confirm route above.
	s.audit(r, "network.revision.rolledback", "network_revision", id, map[string]any{"undone": st < 300, "status": st})
	if st < 300 {
		// A BACKGROUND-DERIVED CONTEXT, because netd has ALREADY changed the wire. If the operator's connection
		// drops while this runs, the database must still be brought back in line with the configuration that is
		// now live; a closed browser tab is not a reason to leave the two disagreeing until somebody happens to
		// open the networks page.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// THE OPERATOR ASKED FOR THIS ONE, so they are told if it did not finish. netd has put the previous
		// configuration back on the wire; if the staged client-network rows could not follow, the database is
		// the only thing still claiming the successor and saying "rolled back" would be false.
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

// immutableTopologyChange returns a message describing the refusal, or "" when the request asks for no
// topology change at all.
//
// It compares against what is STORED rather than against presence in the body, so a client that GETs the
// object, edits one setting and PUTs the whole thing back is accepted. Only an actual change is refused.
//
// bridge_name is not in guestNetworkInput and so cannot be asked for; it is derived from the other three at
// create and is named in the message because an operator reading it needs to know it moves too.
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
