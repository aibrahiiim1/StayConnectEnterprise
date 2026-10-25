package main

// Archived client networks and the Default Network.
//
// A confirmed Restore Factory Defaults or Full Client Networking Reset archives the networks it switched off (settleConfirmedChanges). Archived is a
// presentation state: the network is still switched off, still has its history, identity, PMS routing, package-rule references and audit trail, and can be
// restored from the archive. The Default Network is the prepared network a reset put in place (or the one activation created): the single entry point a guest
// meets after a reset.

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// archivedSet is the networks that are archived: marked, and still switched off. A network that is serving again is not archived whatever the marker says.
func (s *server) archivedSet(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	if s.tenantID == "" || s.siteID == "" {
		return out
	}
	rows, err := s.db.Query(ctx, `SELECT a.network_id::text FROM iam_v2.network_archive a JOIN guest_networks g ON g.id = a.network_id
		 WHERE a.tenant_id=$1::uuid AND a.site_id=$2::uuid AND NOT g.enabled`, s.tenantID, s.siteID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out[id] = true
		}
	}
	return out
}

// defaultNetworkID is the prepared network when it is the one serving: it exists, is switched on and is not archived. "" otherwise.
func (s *server) defaultNetworkID(ctx context.Context) string {
	id := s.factoryNetworkID(ctx)
	if id == "" {
		return ""
	}
	var enabled bool
	if err := s.db.QueryRow(ctx, `SELECT enabled FROM guest_networks WHERE id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid`, id, s.tenantID, s.siteID).Scan(&enabled); err != nil || !enabled {
		return ""
	}
	return id
}

type archivedOut struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	ArchivedAt string `json:"archived_at"`
	By         string `json:"retired_by"`
	HasHistory bool   `json:"has_history"`
	Routed     bool   `json:"has_pms_routing"`
}

var groupKindLabel = map[string]string{"FACTORY_RESET": "Restore factory defaults", "FULL_RESET": "Full client networking reset"}

func (s *server) mountArchive(r chi.Router) {
	r.Get("/archive", s.listArchive)
	r.Post("/guest-networks/{id}/unarchive", s.unarchiveNetwork)
	r.Get("/default", s.getDefaultReadiness)
}

func (s *server) listArchive(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	_ = s.archiveRetired(ctx, s.db)
	rows, err := s.db.Query(ctx, `SELECT g.id::text, g.name, to_char(a.archived_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'), a.group_kind,
		   EXISTS (SELECT 1 FROM iam_v2.auth_resolutions x WHERE x.guest_network_id = g.id)
		   OR EXISTS (SELECT 1 FROM iam_v2.device_network_appearances x WHERE x.guest_network_id = g.id),
		   EXISTS (SELECT 1 FROM iam_v2.guest_network_pms_map m WHERE m.guest_network_id = g.id)
		FROM iam_v2.network_archive a JOIN guest_networks g ON g.id = a.network_id
		WHERE a.tenant_id=$1::uuid AND a.site_id=$2::uuid AND NOT g.enabled ORDER BY a.archived_at DESC, g.name`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []archivedOut{}
	for rows.Next() {
		var o archivedOut
		var kind string
		if err := rows.Scan(&o.ID, &o.Name, &o.ArchivedAt, &kind, &o.HasHistory, &o.Routed); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		o.By = groupKindLabel[kind]
		out = append(out, o)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "count": len(out)})
}

// unarchiveNetwork takes a network out of the archive. It stays switched off; switching it on is the ordinary, separate action.
func (s *server) unarchiveNetwork(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidPattern.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such client network")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx, `DELETE FROM iam_v2.network_archive WHERE network_id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid`, id, s.tenantID, s.siteID)
	if err == nil && tag.RowsAffected() > 0 {
		// Touching the row is what stops the catch-up from putting it straight back: it only archives networks untouched since the reset.
		_, err = s.db.Exec(ctx, `UPDATE guest_networks SET updated_at = now() WHERE id=$1::uuid`, id)
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "restore failed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "this network is not archived")
		return
	}
	s.audit(r, "network.unarchived", "guest_network", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"status": "restored", "enabled": false, "note": "The network is back in the main list, still switched off."})
}

// ---- the Default Network ------------------------------------------------------------------------------------------------------

type readinessItem struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"` // ok | attention | unresolved
	Detail string `json:"detail"`
}

type pmsCandidate struct {
	InterfaceID string   `json:"pms_interface_id"`
	Label       string   `json:"label"`
	RoutedFrom  []string `json:"routed_from"`
	Preview     string   `json:"preview"`
}

type pinnedPackage struct {
	Code     string   `json:"code"`
	Revision int      `json:"revision"`
	Networks []string `json:"networks"`
}

type defaultReadiness struct {
	NetworkID     string          `json:"network_id"`
	Name          string          `json:"name"`
	Present       bool            `json:"present"`
	Items         []readinessItem `json:"items"`
	PMSCandidates []pmsCandidate  `json:"pms_candidates"`
	PinnedOnOld   []pinnedPackage `json:"packages_pinned_to_other_networks"`
}

// getDefaultReadiness says what stands between the Default Network and being the coherent guest-service entry point. It changes nothing: PMS routing and package
// eligibility are never altered here, only described, because widening either is the operator's decision.
func (s *server) getDefaultReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	out := defaultReadiness{Items: []readinessItem{}, PMSCandidates: []pmsCandidate{}, PinnedOnOld: []pinnedPackage{}}
	id := s.defaultNetworkID(ctx)
	if id == "" {
		out.Items = append(out.Items, readinessItem{ID: "default", Label: "Default Network", State: "attention",
			Detail: "No prepared network is serving. Activate it, or apply and keep a reset, from Client networks."})
		writeJSON(w, http.StatusOK, out)
		return
	}
	n, err := s.readNetwork(ctx, s.db, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out.NetworkID, out.Name, out.Present = id, n.Name, true
	add := func(i, l, st, d string) {
		out.Items = append(out.Items, readinessItem{ID: i, Label: l, State: st, Detail: d})
	}

	if n.DHCPMode == "local" && len(n.Pools) > 0 {
		add("dhcp", "DHCP", "ok", fmt.Sprintf("Local DHCP, %s – %s.", n.Pools[0].StartIP, n.Pools[0].EndIP))
	} else {
		add("dhcp", "DHCP", "attention", "This network does not hand out addresses itself.")
	}
	if n.DNSMode == "appliance" {
		add("dns", "DNS", "ok", "The appliance answers DNS for this network.")
	} else {
		add("dns", "DNS", "attention", "This network uses custom DNS servers.")
	}
	if n.CaptiveEnabled {
		var profile *string
		_ = s.db.QueryRow(ctx, `SELECT p.name FROM iam_v2.portal_profile_assignments a JOIN iam_v2.portal_profiles p ON p.id=a.profile_id WHERE a.network_id=$1::uuid`, id).Scan(&profile)
		if profile != nil {
			add("portal", "Client Portal", "ok", "Shows the portal profile “"+*profile+"”.")
		} else {
			add("portal", "Client Portal", "ok", "Shows the site's default portal design.")
		}
	} else {
		add("portal", "Client Portal", "attention", "The captive portal is off on this network, so clients are not asked to sign in.")
	}
	ov, _ := s.signinSummary(ctx, id)
	add("signin", "Sign-in", "ok", ov)

	// PMS routing. The routing of retired networks is kept on them, untouched. A route to the Default Network is made only by the operator, one interface at
	// a time, with the preview below confirmed.
	var routed int
	_ = s.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1::uuid`, id).Scan(&routed)
	var cands []pmsCandidate
	var cerr error
	if routed == 0 {
		cands, cerr = s.pmsCandidatesFor(ctx, id)
		if cands != nil {
			out.PMSCandidates = cands
		}
	}
	switch {
	case routed > 0:
		add("pms", "PMS routing", "ok", "Room sign-in on this network checks the PMS interface routed to it (PMS routing).")
	case cerr != nil:
		add("pms", "PMS routing", "attention", "PMS routing could not be read, so nothing is claimed about it.")
	case len(cands) > 0:
		names := make([]string, 0, len(cands))
		for _, c := range cands {
			names = append(names, c.Label)
		}
		add("pms", "PMS routing", "unresolved", "Room sign-in is not offered on this network: no PMS interface is routed to it. "+strings.Join(names, ", ")+
			" is routed only to archived or switched-off networks. Nothing was copied; choose below whether to route it here.")
	default:
		add("pms", "PMS routing", "attention", "No PMS interface is routed to this network, so room sign-in is not offered on it. No retired network had a route to carry over.")
	}

	pinned, perr := s.packagesPinnedElsewhere(ctx, id)
	if pinned != nil {
		out.PinnedOnOld = pinned
	}
	switch {
	case perr != nil:
		add("packages", "Package eligibility", "attention", "Package rules could not be read, so nothing is claimed about them.")
	case len(pinned) > 0:
		add("packages", "Package eligibility", "unresolved", fmt.Sprintf("%d package(s) are limited to other networks by their rules and are not offered here. Their rules were not widened: add this network in a new revision of the package if they should be offered.", len(pinned)))
	default:
		add("packages", "Package eligibility", "ok", "No package rule limits a package to other networks only; packages are offered here according to their other rules.")
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) signinSummary(ctx context.Context, network string) (string, error) {
	var off []string
	var primary *string
	err := s.db.QueryRow(ctx, `SELECT disabled_methods, primary_method FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, network).Scan(&off, &primary)
	if err != nil {
		return "Offers the site's sign-in methods.", nil
	}
	if len(off) == 0 && primary == nil {
		return "Offers the site's sign-in methods.", nil
	}
	return fmt.Sprintf("Offers the site's sign-in methods, narrowed here (%d switched off).", len(off)), nil
}

// pmsCandidatesFor lists, per PMS interface, the retired networks it is routed to when it is routed to no serving network. Each interface is its own candidate;
// two independent interfaces are never offered together, and none is chosen for the operator.
func (s *server) pmsCandidatesFor(ctx context.Context, defaultID string) ([]pmsCandidate, error) {
	rows, err := s.db.Query(ctx, `
		SELECT i.id::text, COALESCE(NULLIF(i.display_label,''), i.connector_kind), array_agg(g.name ORDER BY g.name)
		  FROM iam_v2.guest_network_pms_map m
		  JOIN guest_networks g ON g.id = m.guest_network_id
		  JOIN iam_v2.pms_interfaces i ON i.id = m.pms_interface_id AND i.current_revision_id IS NOT NULL
		 WHERE m.tenant_id=$1::uuid AND m.site_id=$2::uuid AND m.guest_network_id <> $3::uuid
		 GROUP BY i.id, 2
		HAVING NOT bool_or(g.enabled)
		 ORDER BY 2`, s.tenantID, s.siteID, defaultID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []pmsCandidate{}
	for rows.Next() {
		var c pmsCandidate
		if err := rows.Scan(&c.InterfaceID, &c.Label, &c.RoutedFrom); err != nil {
			return nil, err
		}
		c.Preview = fmt.Sprintf("Room sign-in on the Default Network would check %s. Its routes to %s stay as they are, on those networks; nothing is moved or deleted. Guests are only checked against this one interface here.",
			c.Label, strings.Join(c.RoutedFrom, ", "))
		out = append(out, c)
	}
	return out, rows.Err()
}

// packagesPinnedElsewhere lists packages whose CURRENT revision has a SITE_NETWORK rule that names networks but not this one. Revisions are immutable and are
// never edited here. It reads through the scoped reader: the Admin Console's database role has no SELECT on the rules table, and swallowing that refusal
// would report "no package is limited" when the truth is "not known".
func (s *server) packagesPinnedElsewhere(ctx context.Context, defaultID string) ([]pinnedPackage, error) {
	if s.commerce == nil {
		return nil, fmt.Errorf("the packages module is not enabled on this appliance")
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM iam_v2.internet_packages WHERE tenant_id=$1::uuid AND site_id=$2::uuid AND active AND COALESCE(is_system,false) = false ORDER BY code`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	names := map[string]string{}
	if nr, nerr := s.db.Query(ctx, `SELECT id::text, name FROM guest_networks WHERE tenant_id=$1::uuid AND site_id=$2::uuid`, s.tenantID, s.siteID); nerr == nil {
		for nr.Next() {
			var id, n string
			if nr.Scan(&id, &n) == nil {
				names[id] = n
			}
		}
		nr.Close()
	}
	out := []pinnedPackage{}
	for _, pid := range ids {
		cur, disabled, err := s.commerce.GetPackageCurrent(ctx, s.tenantID, s.siteID, pid)
		if err != nil {
			return nil, err
		}
		if disabled {
			continue
		}
		var named []string
		onDefault := false
		for _, rule := range cur.EligibilityRules {
			if rule.Type != iamv2.RuleSiteNetwork {
				continue
			}
			for _, v := range toStringSlice(rule.Value["guest_network_ids"]) {
				v = strings.TrimSpace(v)
				if strings.EqualFold(v, defaultID) {
					onDefault = true
				}
				if n, ok := names[strings.ToLower(v)]; ok {
					named = append(named, n)
				} else if n, ok := names[v]; ok {
					named = append(named, n)
				} else {
					named = append(named, "a network that no longer exists")
				}
			}
		}
		if len(named) == 0 || onDefault {
			continue
		}
		sort.Strings(named)
		out = append(out, pinnedPackage{Code: cur.Code, Revision: cur.RevisionNo, Networks: named})
	}
	return out, nil
}

// archiveRetired archives every network that is switched off and was left behind by the latest confirmed Restore Factory Defaults or Full Reset: the networks it
// switched off and those already switched off before it, and nothing the operator has touched since (a network edited, switched on and off again, or taken
// out of the archive after that reset is not archived again). Idempotent, and cheap enough to run on the reads that show the archive, which is also how a reset
// confirmed before this existed is caught up. The Default Network is serving, so it is never in it.
func (s *server) archiveRetired(ctx context.Context, q queryer) error {
	if s.tenantID == "" || s.siteID == "" {
		return nil
	}
	_, err := q.Exec(ctx, `
		WITH last AS (
		  SELECT c.group_id, c.group_kind, max(c.settled_at) AS at FROM iam_v2.guest_network_changes c
		   WHERE c.tenant_id=$1::uuid AND c.site_id=$2::uuid AND c.state='CONFIRMED' AND c.group_kind IN ('FACTORY_RESET','FULL_RESET') AND c.settled_at IS NOT NULL
		   GROUP BY c.group_id, c.group_kind ORDER BY max(c.settled_at) DESC LIMIT 1)
		INSERT INTO iam_v2.network_archive (network_id, tenant_id, site_id, group_id, group_kind, archived_at)
		SELECT g.id, g.tenant_id, g.site_id, l.group_id, l.group_kind, l.at
		  FROM last l JOIN guest_networks g ON g.tenant_id=$1::uuid AND g.site_id=$2::uuid
		 WHERE NOT g.enabled AND g.updated_at <= l.at AND g.created_at < l.at
		   AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_network_changes later WHERE later.network_id = g.id AND later.created_at > l.at AND later.state <> 'CANCELLED')
		ON CONFLICT (network_id) DO NOTHING`, s.tenantID, s.siteID)
	return err
}
