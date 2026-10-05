package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// CLIENT NETWORKS: ONE INSERT, MANY NETWORKS AT ONCE, AND A SAFE WAY TO CHANGE TOPOLOGY.
//
// TOPOLOGY STAYS IMMUTABLE IN PLACE (immutableTopologyChange): the apply diff is keyed on the bridge, so a
// network whose VLAN or port changed underneath its bridge would be rendered as one thing while the live bridge
// carried another. What an operator moving a LAN from untagged to a VLAN trunk actually needs is a REPLACEMENT:
// a new network with the new topology, carrying everything configured on the old one, while the old one is
// disabled (kept as history -- sign-ins and device appearances name it). Both are drafts until the operator
// applies them through the same validate -> apply -> confirm pipeline, with the same health checks and the same
// automatic rollback (which now also rebuilds a bridge the failed apply removed).
//
// MANY VLANS ON ONE TRUNK are many networks, each with its own subnet and its own DHCP scope -- never one
// overlapping scope. The batch endpoint creates them in one transaction (all or none) and refuses overlap up
// front, so an operator can lay out a trunk in one step and apply it once.

type createdNetwork struct {
	ID, Bridge, PortalURL string
	VLAN                  int
}

// errSubnetConflict reports an enabled network whose subnet overlaps the one being created.
type errSubnetConflict struct{ subnet, other string }

func (e errSubnetConflict) Error() string {
	return fmt.Sprintf("subnet %s overlaps client network %q; every client network needs its own, non-overlapping subnet", e.subnet, e.other)
}

func normalizeGuestNetworkInput(in *guestNetworkInput) {
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
}

func writeNetworkInsertErr(w http.ResponseWriter, err error) {
	var sc errSubnetConflict
	if errors.As(err, &sc) {
		jsonErr(w, http.StatusConflict, "subnet_overlap", sc.Error())
		return
	}
	jsonErr(w, http.StatusBadRequest, "bad_request", pgErr(err))
}

// uniqueBridgeName keeps the readable br-g<vlan> while it is free and otherwise derives a stable suffix from the
// network id. br-g<vlan> used to be the only choice, so a disabled VLAN-20 network (kept as history) blocked every
// new VLAN 20 -- on its own port or another -- with a raw constraint error. Names stay <= 15 characters (IFNAMSIZ)
// and keep the br-g prefix netd recognises as its own.
func uniqueBridgeName(ctx context.Context, tx pgx.Tx, networkType string, vlan int, id string) (string, error) {
	base := netcfg.BridgeNameFor(networkType, vlan, id)
	candidates := []string{base}
	if networkType == "vlan" && vlan > 0 {
		for salt := 0; salt < 8; salt++ {
			h := fnv.New32a()
			_, _ = fmt.Fprintf(h, "%s/%d", id, salt)
			candidates = append(candidates, fmt.Sprintf("br-g%d-%04x", vlan, h.Sum32()&0xffff))
		}
	}
	for _, c := range candidates {
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM guest_networks WHERE bridge_name=$1)`, c).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return c, nil
		}
	}
	return "", errors.New("no free bridge name for this network; contact support")
}

// subnetOverlap returns the name of an enabled network (other than those in exclude) whose subnet overlaps.
func (s *server) subnetOverlap(ctx context.Context, tx pgx.Tx, subnet string, exclude []string) (string, error) {
	if exclude == nil {
		exclude = []string{}
	}
	var name string
	err := tx.QueryRow(ctx, `SELECT name FROM guest_networks
		WHERE tenant_id=$1 AND site_id=$2 AND enabled AND subnet_cidr && $3::cidr AND NOT (id::text = ANY($4::text[]))
		LIMIT 1`, s.tenantID, s.siteID, subnet, exclude).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// insertGuestNetwork inserts one network (and its pools) inside tx. exclude lists networks that will not be
// enabled alongside it (the one a replacement retires), for the overlap check.
func (s *server) insertGuestNetwork(ctx context.Context, tx pgx.Tx, in guestNetworkInput, exclude []string) (createdNetwork, error) {
	vlan := 0
	if in.VLANID != nil {
		vlan = *in.VLANID
	}
	newID := newUUID()
	if boolOr(in.Enabled, true) && in.SubnetCIDR != "" {
		other, err := s.subnetOverlap(ctx, tx, in.SubnetCIDR, exclude)
		if err != nil {
			return createdNetwork{}, err
		}
		if other != "" {
			return createdNetwork{}, errSubnetConflict{subnet: in.SubnetCIDR, other: other}
		}
	}
	bridge, err := uniqueBridgeName(ctx, tx, in.NetworkType, vlan, newID)
	if err != nil {
		return createdNetwork{}, err
	}
	portalURL := netcfg.PortalURLFor(in.GatewayIP, 8380)
	dnsRaw := jsonOrEmptyArray(in.DNSServers)
	if _, err := tx.Exec(ctx, `
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
		in.DHCPMode, in.DNSMode, dnsRaw, in.DomainName, in.LeaseDefault, in.LeaseMin,
		in.LeaseMax, boolOr(in.CaptiveEnabled, true), boolOr(in.InternetEnabled, true), boolOr(in.NATEnabled, true),
		boolOr(in.ClientIsolation, false), portalURL); err != nil {
		return createdNetwork{}, err
	}
	if err := insertPools(ctx, tx, newID, in.Pools); err != nil {
		return createdNetwork{}, err
	}
	return createdNetwork{ID: newID, Bridge: bridge, PortalURL: portalURL, VLAN: vlan}, nil
}

func jsonOrEmptyArray(v []string) string {
	if len(v) == 0 {
		return "[]"
	}
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = fmt.Sprintf("%q", x)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// ---------------------------------------------------------------------------------------- batch create ------

const maxBatchNetworks = 32

// createGuestNetworkBatch creates several networks -- typically several VLANs on one trunk -- in ONE transaction.
// Each keeps its own subnet and DHCP scope; overlap with each other or with any enabled network is refused, and so
// is a VLAN repeated on the same port. Nothing is created unless everything is.
func (s *server) createGuestNetworkBatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Networks []guestNetworkInput `json:"networks"`
	}
	if err := decodeJSON(r, &body); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if len(body.Networks) == 0 || len(body.Networks) > maxBatchNetworks {
		jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("send between 1 and %d networks", maxBatchNetworks))
		return
	}
	seenVLAN := map[string]int{}
	for i := range body.Networks {
		normalizeGuestNetworkInput(&body.Networks[i])
		n := body.Networks[i]
		if n.NetworkType == "vlan" && n.VLANID != nil {
			key := fmt.Sprintf("%s/%d", n.ParentInterface, *n.VLANID)
			if j, dup := seenVLAN[key]; dup {
				jsonErr(w, http.StatusBadRequest, "duplicate_vlan",
					fmt.Sprintf("networks %d and %d both use VLAN %d on %s", j+1, i+1, *n.VLANID, n.ParentInterface))
				return
			}
			seenVLAN[key] = i
		}
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "tx begin")
		return
	}
	defer tx.Rollback(ctx)
	out := make([]map[string]any, 0, len(body.Networks))
	for i, in := range body.Networks {
		created, err := s.insertGuestNetwork(ctx, tx, in, nil)
		if err != nil {
			var sc errSubnetConflict
			if errors.As(err, &sc) {
				jsonErr(w, http.StatusConflict, "subnet_overlap", fmt.Sprintf("network %d (%s): %s", i+1, in.Name, sc.Error()))
				return
			}
			jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("network %d (%s): %s", i+1, in.Name, pgErr(err)))
			return
		}
		out = append(out, map[string]any{"id": created.ID, "name": in.Name, "bridge_name": created.Bridge, "portal_url": created.PortalURL})
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	for i, n := range out {
		s.audit(r, "network.guest.created", "guest_network", n["id"].(string),
			map[string]any{"name": body.Networks[i].Name, "batch": len(out)})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"networks": out})
}

// loadPoolsTx reads a network's DHCP pools. IT REPORTS FAILURE, because returning nil meant a replacement
// carried the original's addressing with NO POOL -- a live subnet whose gateway answers and whose DHCP hands out
// nothing, created silently by a read error on the common (non-readdressing) path.
func (s *server) loadPoolsTx(ctx context.Context, tx pgx.Tx, id string) ([]netcfg.Pool, error) {
	rows, err := tx.Query(ctx, `SELECT host(start_ip), host(end_ip) FROM dhcp_pools WHERE guest_network_id=$1 ORDER BY sort_order`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []netcfg.Pool
	for rows.Next() {
		var p netcfg.Pool
		if err := rows.Scan(&p.StartIP, &p.EndIP); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func parseDNSServers(raw []byte) []string {
	str := strings.TrimSpace(string(raw))
	str = strings.TrimPrefix(strings.TrimSuffix(str, "]"), "[")
	if strings.TrimSpace(str) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(str, ",") {
		p = strings.Trim(strings.TrimSpace(p), `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
