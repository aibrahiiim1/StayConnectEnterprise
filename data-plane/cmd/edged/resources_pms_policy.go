package main

// The room sign-in policy of a client network: NONE, DEDICATED (exactly one PMS interface) or SHARED_STRICT (an explicitly selected set of two or three). The policy
// and the interfaces it names are written together in one transaction, only after the operator has seen a preview of what the change affects and confirmed that
// exact preview (based_on). Nothing here starts, stops or reconfigures PMS traffic, posts anything, copies a route from another network or widens package
// eligibility; and nothing ever means "every active PMS": the set is always the one the operator chose.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsresolve"
)

type routingIface struct {
	ID        string `json:"pms_interface_id"`
	Label     string `json:"label"`
	Lifecycle string `json:"lifecycle_state"`
	Published bool   `json:"published"`
}

type routingState struct {
	NetworkID    string         `json:"network_id"`
	Name         string         `json:"name"`
	Enabled      bool           `json:"enabled"`
	Archived     bool           `json:"archived"`
	IsDefault    bool           `json:"is_default"`
	Mode         string         `json:"mode"` // "" = undecided
	Interfaces   []routingIface `json:"interfaces"`
	ConfiguredAt string         `json:"configured_at,omitempty"`
	ConfiguredBy string         `json:"configured_by,omitempty"`
	Fingerprint  string         `json:"fingerprint"`
}

func routingFingerprint(mode string, ids []string) string {
	c := append([]string(nil), ids...)
	sort.Strings(c)
	sum := sha256.Sum256([]byte("v1|" + mode + "|" + strings.Join(c, ",")))
	return hex.EncodeToString(sum[:8])
}

func (s *server) loadRouting(ctx context.Context, q queryer, networkID string) (routingState, error) {
	var st routingState
	st.Interfaces = []routingIface{}
	var cfgAt *string
	err := q.QueryRow(ctx, `SELECT g.id::text, COALESCE(g.name,''), g.enabled, COALESCE(p.mode,''), COALESCE(p.configured_by,''),
		       to_char(p.configured_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
		  FROM public.guest_networks g
		  LEFT JOIN iam_v2.guest_network_pms_policy p ON p.guest_network_id = g.id
		 WHERE g.id=$1::uuid AND g.tenant_id=$2::uuid AND g.site_id=$3::uuid`, networkID, s.tenantID, s.siteID).
		Scan(&st.NetworkID, &st.Name, &st.Enabled, &st.Mode, &st.ConfiguredBy, &cfgAt)
	if err != nil {
		return st, err
	}
	if cfgAt != nil {
		st.ConfiguredAt = *cfgAt
	}
	rows, err := q.Query(ctx, `SELECT i.id::text, COALESCE(NULLIF(i.display_label,''), i.connector_kind), i.lifecycle_state, i.current_revision_id IS NOT NULL
		  FROM iam_v2.guest_network_pms_map m JOIN iam_v2.pms_interfaces i ON i.tenant_id=m.tenant_id AND i.site_id=m.site_id AND i.id=m.pms_interface_id
		 WHERE m.tenant_id=$1::uuid AND m.site_id=$2::uuid AND m.guest_network_id=$3::uuid ORDER BY i.id`, s.tenantID, s.siteID, networkID)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var i routingIface
		if err := rows.Scan(&i.ID, &i.Label, &i.Lifecycle, &i.Published); err != nil {
			return st, err
		}
		st.Interfaces = append(st.Interfaces, i)
		ids = append(ids, i.ID)
	}
	if err := rows.Err(); err != nil {
		return st, err
	}
	// A route with no policy row is the earlier single-interface meaning: DEDICATED.
	if st.Mode == "" && len(st.Interfaces) == 1 {
		st.Mode = policyDedicated
	}
	st.Archived = s.archivedSet(ctx)[networkID]
	st.IsDefault = s.defaultNetworkID(ctx) == networkID
	st.Fingerprint = routingFingerprint(st.Mode, ids)
	return st, nil
}

type routingChoice struct {
	Mode         string   `json:"mode"`
	InterfaceIDs []string `json:"pms_interface_ids"`
	BasedOn      string   `json:"based_on"`
	// The earlier shape: one interface, no preview fingerprint. Still accepted; it can only ever mean DEDICATED.
	LegacyInterface string `json:"pms_interface_id"`
	RoutingMode     string `json:"routing_mode"`
}

func (c routingChoice) normalised() (mode string, ids []string, legacy bool) {
	mode = strings.ToUpper(strings.TrimSpace(c.Mode))
	if mode == "" && strings.TrimSpace(c.LegacyInterface) != "" {
		return policyDedicated, []string{strings.TrimSpace(c.LegacyInterface)}, true
	}
	seen := map[string]bool{}
	for _, id := range c.InterfaceIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return mode, ids, false
}

type routingNote struct {
	Code        string `json:"code"`
	Text        string `json:"text"`
	InterfaceID string `json:"pms_interface_id,omitempty"`
}

type routingImpactInterface struct {
	routingIface
	InHouseStays int      `json:"in_house_stays"`
	RoomSignInOK bool     `json:"room_auth_ready"`
	HealthKnown  bool     `json:"health_known"`
	Reason       string   `json:"room_auth_reason,omitempty"`
	AlsoUsedBy   []string `json:"also_used_by"`
	LiveSessions int      `json:"live_sessions_on_this_network"`
}

type routingPlan struct {
	Network   routingState    `json:"network"`
	Proposed  routingProposed `json:"proposed"`
	Added     []routingIface  `json:"added"`
	Removed   []routingIface  `json:"removed"`
	Blockers  []routingNote   `json:"blockers"`
	Warnings  []routingNote   `json:"warnings"`
	Impact    routingImpact   `json:"impact"`
	Guarantee []string        `json:"guarantees"`
}

type routingProposed struct {
	Mode       string                   `json:"mode"`
	Interfaces []routingImpactInterface `json:"interfaces"`
}

type routingImpact struct {
	ActiveSessions   int      `json:"active_sessions"`
	RoomEntitlements int      `json:"live_room_entitlements"`
	OverlappingRooms int      `json:"room_numbers_in_more_than_one_selected_pms"`
	SignInMethodsOff []string `json:"sign_in_methods_switched_off"`
	NetworkWillServe bool     `json:"network_serves_clients"`
}

func (s *server) planRouting(ctx context.Context, cur routingState, mode string, ids []string) (routingPlan, error) {
	plan := routingPlan{Network: cur, Added: []routingIface{}, Removed: []routingIface{}, Blockers: []routingNote{}, Warnings: []routingNote{},
		Proposed: routingProposed{Mode: mode, Interfaces: []routingImpactInterface{}},
		Guarantee: []string{
			"No PMS traffic is started or stopped, and nothing is posted to any PMS.",
			"Package eligibility is not changed, and no route is copied from another client network.",
			"Guests who are online keep their access. Their entitlements stay with the stay that earned them; only the next room sign-in on this network follows the new choice.",
			"Every selected PMS keeps its own rooms and guests; they are never merged, and a guest is never asked which PMS they belong to.",
		}}
	block := func(code, text string) { plan.Blockers = append(plan.Blockers, routingNote{Code: code, Text: text}) }
	warn := func(code, text, iface string) {
		plan.Warnings = append(plan.Warnings, routingNote{Code: code, Text: text, InterfaceID: iface})
	}

	switch mode {
	case policyNone:
		if len(ids) != 0 {
			block("NONE_TAKES_NO_INTERFACES", "No PMS interface can be selected when room sign-in is switched off for this network.")
		}
	case policyDedicated:
		if len(ids) != 1 {
			block("DEDICATED_NEEDS_ONE", "Dedicated routing needs exactly one PMS interface.")
		}
	case policyShared:
		if len(ids) < 2 {
			block("SHARED_NEEDS_TWO", "A shared network needs at least two PMS interfaces. For one interface use a dedicated route.")
		}
		if len(ids) > pmsresolve.MaxSharedCandidates {
			block("SHARED_TOO_MANY", fmt.Sprintf("A network can check at most %d PMS interfaces. Every one is asked on each sign-in, so the set is kept small on purpose.", pmsresolve.MaxSharedCandidates))
		}
	default:
		block("MODE_UNKNOWN", "Choose No room sign-in, one dedicated PMS, or a shared strict set.")
		return plan, nil
	}
	if cur.Archived {
		block("NETWORK_ARCHIVED", "This network is archived. Restore it from the archive before changing its room sign-in.")
	}

	byID := map[string]routingIface{}
	rows, err := s.db.Query(ctx, `SELECT i.id::text, COALESCE(NULLIF(i.display_label,''), i.connector_kind), i.lifecycle_state, i.current_revision_id IS NOT NULL
		FROM iam_v2.pms_interfaces i WHERE i.tenant_id=$1::uuid AND i.site_id=$2::uuid`, s.tenantID, s.siteID)
	if err != nil {
		return plan, err
	}
	for rows.Next() {
		var i routingIface
		if err := rows.Scan(&i.ID, &i.Label, &i.Lifecycle, &i.Published); err != nil {
			rows.Close()
			return plan, err
		}
		byID[i.ID] = i
	}
	rows.Close()

	var chosen []routingIface
	for _, id := range ids {
		if !uuidRe.MatchString(id) {
			block("INTERFACE_NOT_FOUND", "A selected PMS interface does not exist at this site.")
			continue
		}
		i, ok := byID[id]
		if !ok {
			block("INTERFACE_NOT_FOUND", "A selected PMS interface does not exist at this site.")
			continue
		}
		chosen = append(chosen, i)
		switch {
		case i.Lifecycle != "ACTIVE":
			plan.Blockers = append(plan.Blockers, routingNote{Code: "INTERFACE_NOT_IN_SERVICE", InterfaceID: id,
				Text: i.Label + " is not in service, so a network routed to it could not check anybody. Put it back in service first."})
		case !i.Published:
			plan.Blockers = append(plan.Blockers, routingNote{Code: "INTERFACE_NOT_PUBLISHED", InterfaceID: id,
				Text: i.Label + " has no published configuration — publish one before routing a client network to it."})
		}
	}

	have := map[string]bool{}
	for _, i := range cur.Interfaces {
		have[i.ID] = true
	}
	want := map[string]bool{}
	for _, i := range chosen {
		want[i.ID] = true
		if !have[i.ID] {
			plan.Added = append(plan.Added, i)
		}
	}
	for _, i := range cur.Interfaces {
		if !want[i.ID] {
			plan.Removed = append(plan.Removed, i)
		}
	}

	// Pairs of selected interfaces that may be the same property. Two sources feed it: conflicts that were recorded, and pairs whose published revisions carry the same
	// source fingerprint (the detection the source-conflicts page uses, because nothing records those rows by itself). Equal fingerprints are the contract's CRITICAL
	// duplicate source: a network checking both would count one property's guests twice, so it is not applied until the conflict is resolved.
	if len(chosen) >= 2 {
		recorded := map[string]bool{}
		crows, cerr := s.db.Query(ctx, `SELECT interface_a::text, interface_b::text, COALESCE(severity,''), COALESCE(resolution,'')
			FROM iam_v2.pms_source_conflicts WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND interface_a = ANY($3::uuid[]) AND interface_b = ANY($3::uuid[])`,
			s.tenantID, s.siteID, ids)
		if cerr == nil {
			for crows.Next() {
				var a, b, sev, res string
				if crows.Scan(&a, &b, &sev, &res) != nil {
					continue
				}
				recorded[a+"/"+b] = true
				text := fmt.Sprintf("%s and %s may be the same property's PMS (a possible duplicate source). Sharing one network between them would double-count its guests.", byID[a].Label, byID[b].Label)
				if strings.EqualFold(sev, "CRITICAL") && res == "" {
					block("DUPLICATE_SOURCE", text+" Resolve the source conflict first.")
				} else {
					warn("DUPLICATE_SOURCE", text, "")
				}
			}
			crows.Close()
		}
		drows, derr := s.db.Query(ctx, `
			SELECT a.id::text, b.id::text
			  FROM iam_v2.pms_interfaces a
			  JOIN iam_v2.pms_interface_revisions ra ON ra.id = a.current_revision_id
			  JOIN iam_v2.pms_interfaces b ON b.tenant_id = a.tenant_id AND b.site_id = a.site_id AND a.id < b.id
			  JOIN iam_v2.pms_interface_revisions rb ON rb.id = b.current_revision_id
			 WHERE a.tenant_id=$1::uuid AND a.site_id=$2::uuid AND a.id = ANY($3::uuid[]) AND b.id = ANY($3::uuid[])
			   AND ra.source_fingerprint IS NOT NULL AND ra.source_fingerprint = rb.source_fingerprint`, s.tenantID, s.siteID, ids)
		if derr == nil {
			for drows.Next() {
				var a, b string
				if drows.Scan(&a, &b) != nil || recorded[a+"/"+b] {
					continue
				}
				block("DUPLICATE_SOURCE", fmt.Sprintf("%s and %s point at the same source (their published configurations carry the same source fingerprint), so they are very likely one property's PMS. A network that checks both would count its guests twice. Resolve it under Duplicate sources first.", byID[a].Label, byID[b].Label))
			}
			drows.Close()
		}
	}

	// impact
	pimp := routingProposed{Mode: mode, Interfaces: []routingImpactInterface{}}
	var bridge string
	_ = s.db.QueryRow(ctx, `SELECT COALESCE(bridge_name,'') FROM public.guest_networks WHERE id=$1::uuid`, cur.NetworkID).Scan(&bridge)
	others := s.otherUsers(ctx, cur.NetworkID)
	for _, i := range chosen {
		ii := routingImpactInterface{routingIface: i, AlsoUsedBy: others[i.ID]}
		if ii.AlsoUsedBy == nil {
			ii.AlsoUsedBy = []string{}
		}
		_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.stays WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND pms_interface_id=$3::uuid AND status IN ('IN_HOUSE','POST_STAY_ACTIVE')`,
			s.tenantID, s.siteID, i.ID).Scan(&ii.InHouseStays)
		if h, herr := s.interfaceHealthRow(ctx, i.ID); herr == nil {
			ii.HealthKnown, ii.RoomSignInOK, ii.Reason = true, h.RoomAuthReady, h.RoomAuthReason
		}
		if bridge != "" {
			_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.sessions se JOIN iam_v2.entitlements e ON e.id=se.entitlement_id AND e.tenant_id=se.tenant_id AND e.site_id=se.site_id
				WHERE se.tenant_id=$1::uuid AND se.site_id=$2::uuid AND se.state='active' AND se.ended IS NULL AND se.ingress_interface=$3 AND e.pms_interface_id=$4::uuid`,
				s.tenantID, s.siteID, bridge, i.ID).Scan(&ii.LiveSessions)
		}
		pimp.Interfaces = append(pimp.Interfaces, ii)
		if len(i.ID) > 0 && ii.HealthKnown && !ii.RoomSignInOK && i.Lifecycle == "ACTIVE" && i.Published {
			text := i.Label + " cannot answer room sign-in right now: " + roomAuthWord(ii.Reason) + "."
			if mode == policyShared {
				text += " On a shared network every selected PMS must be able to answer, so until it can, no guest on this network can sign in with a room number."
			}
			warn("INTERFACE_NOT_READY", text, i.ID)
		}
		if !ii.HealthKnown && i.Lifecycle == "ACTIVE" {
			warn("INTERFACE_HEALTH_UNKNOWN", "The state of "+i.Label+" could not be read, so nothing is claimed about it.", i.ID)
		}
		if len(ii.AlsoUsedBy) > 0 {
			warn("INTERFACE_ALSO_USED", i.Label+" is also used by "+strings.Join(ii.AlsoUsedBy, ", ")+". That is allowed: each network keeps its own policy, and this change touches only this network.", i.ID)
		}
	}
	plan.Proposed = pimp

	if bridge != "" {
		_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.sessions se WHERE se.tenant_id=$1::uuid AND se.site_id=$2::uuid AND se.state='active' AND se.ended IS NULL AND se.ingress_interface=$3`,
			s.tenantID, s.siteID, bridge).Scan(&plan.Impact.ActiveSessions)
		_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.sessions se JOIN iam_v2.entitlements e ON e.id=se.entitlement_id AND e.tenant_id=se.tenant_id AND e.site_id=se.site_id
			WHERE se.tenant_id=$1::uuid AND se.site_id=$2::uuid AND se.state='active' AND se.ended IS NULL AND se.ingress_interface=$3 AND e.pms_interface_id IS NOT NULL`,
			s.tenantID, s.siteID, bridge).Scan(&plan.Impact.RoomEntitlements)
	}
	plan.Impact.NetworkWillServe = cur.Enabled
	plan.Impact.SignInMethodsOff = []string{}
	var off []string
	if err := s.db.QueryRow(ctx, `SELECT disabled_methods FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, cur.NetworkID).Scan(&off); err == nil && off != nil {
		plan.Impact.SignInMethodsOff = off
	}
	if len(ids) >= 2 {
		_ = s.db.QueryRow(ctx, `SELECT count(*)::int FROM (SELECT normalized_room_number FROM iam_v2.stays
			WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND pms_interface_id = ANY($3::uuid[]) AND status IN ('IN_HOUSE','POST_STAY_ACTIVE')
			GROUP BY normalized_room_number HAVING count(DISTINCT pms_interface_id) >= 2) x`, s.tenantID, s.siteID, ids).Scan(&plan.Impact.OverlappingRooms)
		if plan.Impact.OverlappingRooms > 0 {
			warn("ROOM_NUMBERS_OVERLAP", fmt.Sprintf("%d room number(s) are in house in more than one selected PMS. A guest in one of them is checked against both, and is asked for a second detail (their reservation number) rather than being matched to one PMS by room number alone.", plan.Impact.OverlappingRooms), "")
		}
	}

	switch {
	case !cur.Enabled && !cur.Archived:
		warn("NETWORK_OFF", "This network is switched off. The choice is kept and takes effect when the network serves clients.", "")
	}
	if cur.Mode != "" && cur.Mode != mode {
		warn("POLICY_CHANGES", fmt.Sprintf("This network's room sign-in changes from %s to %s.", policyWords(cur.Mode), policyWords(mode)), "")
	}
	if len(plan.Removed) > 0 && len(chosen) > 0 && mode == policyDedicated && len(cur.Interfaces) == 1 {
		warn("MOVES_TO_ANOTHER_PMS", fmt.Sprintf("New room sign-ins on this network are now checked only against %s, not %s. Guests staying in %s's rooms must use another sign-in method here.", chosen[0].Label, plan.Removed[0].Label, plan.Removed[0].Label), plan.Removed[0].ID)
	}
	if mode == policyNone && plan.Impact.RoomEntitlements > 0 {
		warn("LIVE_ROOM_SESSIONS", fmt.Sprintf("%d client(s) are online with room-based access. They keep it until it ends; new room sign-ins stop.", plan.Impact.RoomEntitlements), "")
	}
	return plan, nil
}

// otherUsers: for each interface, the other networks (enabled, not archived) whose routing names it.
func (s *server) otherUsers(ctx context.Context, exceptNetwork string) map[string][]string {
	out := map[string][]string{}
	archived := s.archivedSet(ctx)
	rows, err := s.db.Query(ctx, `SELECT m.pms_interface_id::text, g.id::text, COALESCE(g.name,'') FROM iam_v2.guest_network_pms_map m
		JOIN public.guest_networks g ON g.id = m.guest_network_id
		WHERE m.tenant_id=$1::uuid AND m.site_id=$2::uuid AND g.enabled AND m.guest_network_id <> $3::uuid ORDER BY g.name`, s.tenantID, s.siteID, exceptNetwork)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var iface, id, name string
		if rows.Scan(&iface, &id, &name) == nil && !archived[id] {
			out[iface] = append(out[iface], name)
		}
	}
	return out
}

func (s *server) pmsRoutingPreview(w http.ResponseWriter, r *http.Request) {
	gnID := strings.TrimSpace(chi.URLParam(r, "guest_network_id"))
	if !uuidRe.MatchString(gnID) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
	var in routingChoice
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	cur, err := s.loadRouting(ctx, s.db, gnID)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	mode, ids, _ := in.normalised()
	plan, err := s.planRouting(ctx, cur, mode, ids)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (s *server) setPMSRoute(w http.ResponseWriter, r *http.Request) {
	gnID := strings.TrimSpace(chi.URLParam(r, "guest_network_id"))
	if !uuidRe.MatchString(gnID) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
	var in routingChoice
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	mode, ids, legacy := in.normalised()
	if legacy {
		if rm := strings.TrimSpace(in.RoutingMode); rm != "" && rm != "MAPPED" {
			jsonErr(w, http.StatusBadRequest, "validation", "routing_mode must be MAPPED. Routing a network to every active PMS is not offered: choose the interfaces explicitly.")
			return
		}
	} else if strings.TrimSpace(in.BasedOn) == "" {
		jsonErr(w, http.StatusBadRequest, "validation", "based_on is required: preview the change first and confirm that preview.")
		return
	}
	if mode == "" {
		jsonErr(w, http.StatusBadRequest, "validation", "mode is required (NONE, DEDICATED or SHARED_STRICT) with pms_interface_ids")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, "pms-routing:"+gnID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "lock failed")
		return
	}
	cur, err := s.loadRouting(ctx, tx, gnID)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if !legacy && in.BasedOn != cur.Fingerprint {
		jsonErr(w, http.StatusConflict, "routing_changed", "This network's room sign-in changed after the preview was made. Preview it again before confirming.")
		return
	}
	plan, err := s.planRouting(ctx, cur, mode, ids)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if len(plan.Blockers) > 0 {
		for _, b := range plan.Blockers {
			if b.Code == "INTERFACE_NOT_FOUND" {
				jsonErr(w, http.StatusNotFound, "not_found", "PMS interface not found at this site")
				return
			}
		}
		writeJSON(w, http.StatusConflict, map[string]any{"error": "validation", "message": plan.Blockers[0].Text, "blockers": plan.Blockers})
		return
	}

	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND guest_network_id=$3::uuid AND NOT (pms_interface_id = ANY($4::uuid[]))`,
		s.tenantID, s.siteID, gnID, idsOrEmpty(ids)); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route replace failed")
		return
	}
	// The first interface (by id) is the network's default; the unique index allows exactly one.
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_network_pms_map SET is_default=false WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND guest_network_id=$3::uuid AND is_default`,
		s.tenantID, s.siteID, gnID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route write failed")
		return
	}
	for n, id := range ids {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map (tenant_id, site_id, guest_network_id, pms_interface_id, is_default, routing_mode)
			VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,'MAPPED')
			ON CONFLICT (guest_network_id, pms_interface_id) DO UPDATE SET is_default=EXCLUDED.is_default, routing_mode='MAPPED'`,
			s.tenantID, s.siteID, gnID, id, n == 0); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "route write failed")
			return
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_policy (guest_network_id, tenant_id, site_id, mode, configured_by)
		VALUES ($1::uuid,$2::uuid,$3::uuid,$4,$5)
		ON CONFLICT (guest_network_id) DO UPDATE SET mode=EXCLUDED.mode, configured_at=now(), configured_by=EXCLUDED.configured_by`,
		gnID, s.tenantID, s.siteID, mode, s.actor(r)); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route write failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		var pe interface{ SQLState() string }
		if errors.As(err, &pe) && pe.SQLState() == "23514" {
			jsonErr(w, http.StatusConflict, "validation", "the interfaces do not match the chosen policy")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "pms_routing.policy_set", "guest_network", gnID, map[string]any{
		"guest_network_name": cur.Name, "mode": mode, "pms_interface_ids": ids,
		"previous_mode": cur.Mode, "previous_pms_interface_ids": ifaceIDs(cur.Interfaces),
	})
	updated, err := s.loadRouting(ctx, s.db, gnID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func idsOrEmpty(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func ifaceIDs(in []routingIface) []string {
	out := []string{}
	for _, i := range in {
		out = append(out, i.ID)
	}
	return out
}

// Removes the network's routing and its policy, so the network is undecided again. The way to say "no room sign-in here" is NONE; this is for undoing a choice.
func (s *server) clearPMSRoute(w http.ResponseWriter, r *http.Request) {
	gnID := strings.TrimSpace(chi.URLParam(r, "guest_network_id"))
	if !uuidRe.MatchString(gnID) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1 AND site_id=$2 AND guest_network_id=$3`, s.tenantID, s.siteID, gnID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route delete failed")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_policy WHERE guest_network_id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid`, gnID, s.tenantID, s.siteID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route delete failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "pms_routing.cleared", "guest_network", gnID, map[string]any{"removed": tag.RowsAffected()})
	writeJSON(w, http.StatusOK, map[string]any{"guest_network_id": gnID, "removed": tag.RowsAffected()})
}

type policyOut struct {
	routingState
	Mode string `json:"mode"`
}

// listPMSRouting: the rows (as before), the explicit policy of each network, and the networks nobody has decided about.
func (s *server) listPMSRouting(w http.ResponseWriter, r *http.Request) {
	routes, err := s.routesForInterface(r, "")
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	unmapped, err := s.unmappedGuestNetworks(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	policies := []routingState{}
	archived := s.archivedSet(ctx)
	rows, err := s.db.Query(ctx, `SELECT id::text FROM public.guest_networks WHERE tenant_id=$1::uuid AND site_id=$2::uuid ORDER BY enabled DESC, name, id`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	var nets []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !archived[id] {
			nets = append(nets, id)
		}
	}
	rows.Close()
	for _, id := range nets {
		st, err := s.loadRouting(ctx, s.db, id)
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		policies = append(policies, st)
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes, "policies": policies, "unmapped_guest_networks": unmapped,
		"limits": map[string]any{"max_shared_interfaces": pmsresolve.MaxSharedCandidates}})
}

func (s *server) getRoomSignInReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	a, err := s.roomSignInAssessment(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, a)
}
