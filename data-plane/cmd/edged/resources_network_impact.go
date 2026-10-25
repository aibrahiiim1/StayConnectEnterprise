package main

// What a topology change (Access to Trunk, a VLAN change, replacing a network) does to the people and settings that depend on a client network. Read-only: it
// describes the consequences so the operator can confirm them before anything is applied; it moves no client, merges no PMS routing and changes no policy.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

type networkConsequence struct {
	NetworkID        string           `json:"network_id"`
	Name             string           `json:"name"`
	Enabled          bool             `json:"enabled"`
	ActiveClients    int              `json:"active_clients"`
	ActiveSessions   int              `json:"active_sessions"`
	RoomEntitlements int              `json:"live_room_entitlements"`
	RoomSignIn       *roomSignInBrief `json:"room_sign_in,omitempty"`
	SignInMethodsOff []string         `json:"sign_in_methods_switched_off"`
	PackagesLimited  []string         `json:"packages_limited_to_this_network"`
	Notes            []string         `json:"notes"`
}

func (s *server) networkConsequence(ctx context.Context, id string, brief map[string]*roomSignInBrief) (networkConsequence, error) {
	c := networkConsequence{NetworkID: id, SignInMethodsOff: []string{}, PackagesLimited: []string{}, Notes: []string{}}
	var bridge string
	if err := s.db.QueryRow(ctx, `SELECT COALESCE(name,''), enabled, COALESCE(bridge_name,'') FROM guest_networks WHERE id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid`,
		id, s.tenantID, s.siteID).Scan(&c.Name, &c.Enabled, &bridge); err != nil {
		return c, err
	}
	_ = s.db.QueryRow(ctx, activeOnNetworkSQL, id).Scan(&c.ActiveClients)
	if bridge != "" {
		_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.sessions WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND state='active' AND ended IS NULL AND ingress_interface=$3`,
			s.tenantID, s.siteID, bridge).Scan(&c.ActiveSessions)
		_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.sessions se JOIN iam_v2.entitlements e ON e.id=se.entitlement_id AND e.tenant_id=se.tenant_id AND e.site_id=se.site_id
			WHERE se.tenant_id=$1::uuid AND se.site_id=$2::uuid AND se.state='active' AND se.ended IS NULL AND se.ingress_interface=$3 AND e.pms_interface_id IS NOT NULL`,
			s.tenantID, s.siteID, bridge).Scan(&c.RoomEntitlements)
	}
	var off []string
	if err := s.db.QueryRow(ctx, `SELECT disabled_methods FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, id).Scan(&off); err == nil && off != nil {
		c.SignInMethodsOff = off
	}
	if pk := s.packagesLimitedTo(ctx, id); pk != nil {
		c.PackagesLimited = pk
	}
	if b := brief[id]; b != nil {
		c.RoomSignIn = b
	}
	if c.ActiveClients > 0 {
		c.Notes = append(c.Notes, fmt.Sprintf("%d client(s) are online on it. Their access and allowance are kept; they get a new address and sign in again where the sign-in is not remembered.", c.ActiveClients))
	}
	if c.RoomSignIn != nil && len(c.RoomSignIn.Labels) > 0 {
		c.Notes = append(c.Notes, "Its room sign-in routing ("+strings.Join(c.RoomSignIn.Labels, ", ")+") stays with this network. It is never merged with another network's routing, and no guest moves to a different PMS.")
	}
	if len(c.PackagesLimited) > 0 {
		c.Notes = append(c.Notes, "Packages limited to this network by their rules ("+strings.Join(c.PackagesLimited, ", ")+") are not widened to any other network.")
	}
	return c, nil
}

func (s *server) consequencesFor(ctx context.Context, ids []string) []networkConsequence {
	brief := map[string]*roomSignInBrief{}
	if a, err := s.roomSignInAssessment(ctx); err == nil {
		for _, n := range a.Networks {
			brief[n.NetworkID] = briefOf(n)
		}
	}
	out := []networkConsequence{}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if c, err := s.networkConsequence(ctx, id, brief); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// externalRequirements is what the SWITCH, the access points and the hypervisor must provide for the target topology. OneGate configures none of it.
func externalRequirements(networkType string, vlan *int, parent string) []string {
	out := []string{}
	if networkType == "vlan" && vlan != nil {
		out = append(out,
			fmt.Sprintf("Switch: VLAN %d must be allowed and carried tagged on the trunk that reaches %s.", *vlan, parent),
			"Hypervisor (ESXi): the port group feeding that interface must pass tagged frames (VLAN ID 4095, all VLANs) so OneGate receives VLAN "+strconv.Itoa(*vlan)+".",
			fmt.Sprintf("Wireless: the SSID or access-point profile for these clients must tag VLAN %d.", *vlan))
	} else {
		out = append(out,
			"Switch: the port that reaches "+parent+" must deliver this network untagged (an access port on the client VLAN, or a trunk with it as the native VLAN).",
			"Hypervisor (ESXi): the port group feeding that interface must deliver untagged frames (VLAN ID 0 / None), or 4095 if it also carries tagged VLANs.",
			"Wireless: the SSID or access-point profile for these clients must be untagged on that network.")
	}
	return append(out, "OneGate does not change your switch, access points or hypervisor. Make these changes there yourself; until they are made, clients on this network cannot reach it.")
}

// GET /network/guest-networks/{id}/impact[?to_type=vlan&to_vlan=60&to_parent=ens192]
func (s *server) getNetworkImpact(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	cs := s.consequencesFor(ctx, []string{id})
	if len(cs) == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found")
		return
	}
	out := map[string]any{"affected": cs}
	q := r.URL.Query()
	if t := q.Get("to_type"); t != "" {
		var vlan *int
		if v, err := strconv.Atoi(q.Get("to_vlan")); err == nil {
			vlan = &v
		}
		parent := q.Get("to_parent")
		if parent == "" {
			var cur string
			if err := s.db.QueryRow(ctx, `SELECT parent_interface FROM guest_networks WHERE id=$1::uuid`, id).Scan(&cur); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
				return
			}
			parent = cur
		}
		out["external_requirements"] = externalRequirements(t, vlan, parent)
	}
	writeJSON(w, http.StatusOK, out)
}
