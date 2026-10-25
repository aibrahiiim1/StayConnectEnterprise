package main

// ONE authoritative answer to "can a guest sign in with their room number, and where?". The dashboard's Needs attention, PMS Connections, PMS Routing, the Default
// Network card and Client networks all read this and word it; none derives it again.
//
// Room sign-in is a fact about a CLIENT NETWORK and the PMS interfaces its explicit policy names, not about a PMS in general:
//   - NONE: the network deliberately offers no room sign-in. Never a problem, whatever any PMS is doing.
//   - DEDICATED: usable when its one interface is in service and its room-auth readiness holds.
//   - SHARED_STRICT: usable only when EVERY selected interface can answer. Under the STRICT contract (Phase 0, section 8) one candidate that cannot answer means
//     "no PMS has this guest" cannot be proven, so the network fails closed for every guest. The assessment says exactly that rather than "partially working".
//   - no policy: undecided. It is a problem only where a PMS exists that could serve the network.
// PMS transport health stays a separate fact (the interface's own screens). Unknown evidence stays unknown: an interface whose health could not be read never makes a
// network fail, and never makes it healthy.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsresolve"
)

const (
	policyNone      = "NONE"
	policyDedicated = "DEDICATED"
	policyShared    = "SHARED_STRICT"
	policyUndecided = ""
)

// network states
const (
	rsOff        = "OFF"       // room sign-in deliberately not offered here
	rsReady      = "READY"     // every interface the policy names can answer
	rsBlocked    = "BLOCKED"   // guests on this network cannot sign in with a room number
	rsUnknown    = "UNKNOWN"   // evidence could not be read
	rsUndecided  = "UNDECIDED" // no policy, and a PMS exists that could serve it
	rsNotSet     = "NOT_SET"   // no policy, but no PMS is in service to route to: nothing to decide yet
	rsBadRouting = "MISCONFIGURED"
	rsInactive   = "INACTIVE" // the network is switched off; its routing is kept
)

type rsInterface struct {
	ID          string `json:"pms_interface_id"`
	Label       string `json:"label"`
	Lifecycle   string `json:"lifecycle_state"`
	Published   bool   `json:"published"`
	HealthKnown bool   `json:"health_known"`
	RoomReady   bool   `json:"room_auth_ready"`
	Reason      string `json:"room_auth_reason,omitempty"`
	Source      string `json:"room_auth_source,omitempty"`
	Transport   string `json:"transport_status,omitempty"`
}

type rsReason struct {
	Code        string `json:"code"`
	Text        string `json:"text"`
	InterfaceID string `json:"pms_interface_id,omitempty"`
	Interface   string `json:"pms_interface_label,omitempty"`
}

type rsNetwork struct {
	NetworkID  string        `json:"network_id"`
	Name       string        `json:"name"`
	Enabled    bool          `json:"enabled"`
	IsDefault  bool          `json:"is_default"`
	Policy     string        `json:"policy"` // NONE | DEDICATED | SHARED_STRICT | "" (undecided)
	Legacy     bool          `json:"legacy_unclassified,omitempty"`
	State      string        `json:"state"`
	Interfaces []rsInterface `json:"interfaces"`
	Reasons    []rsReason    `json:"reasons"`
	Summary    string        `json:"summary"`
}

type rsIssue struct {
	ID        string `json:"id"`
	Severity  string `json:"severity"` // warn | err
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Href      string `json:"href"`
	Action    string `json:"action"`
	NetworkID string `json:"network_id,omitempty"`
}

type rsInterfaceSummary struct {
	rsInterface
	// Serving: enabled, non-archived networks whose policy names this interface. ArchivedRoutes: routes it keeps to archived or switched-off networks, which are
	// history and never a reason to warn on their own.
	Serving        []string `json:"serving_networks"`
	ArchivedRoutes int      `json:"archived_or_off_routes"`
	State          string   `json:"state"` // SERVING | IMPAIRED | NOT_USED | UNKNOWN | NOT_IN_SERVICE
}

type roomSignInAssessment struct {
	GeneratedAt time.Time            `json:"generated_at"`
	Overall     string               `json:"overall"` // READY | PARTIAL | BLOCKED | NOT_OFFERED | UNKNOWN
	Networks    []rsNetwork          `json:"networks"`
	Interfaces  []rsInterfaceSummary `json:"interfaces"`
	Issues      []rsIssue            `json:"issues"`
}

// rsNetworkFacts are the inputs for one network. Mapped is the interface ids its routing rows name.
type rsNetworkFacts struct {
	ID, Name      string
	Enabled       bool
	Archived      bool
	IsDefault     bool
	RoomMethodOff bool // the network's own sign-in settings switch the room method off
	Policy        string
	Mapped        []string
}

var roomAuthWords = map[string]string{
	roomAuthNotActive:      "its PMS interface is not in service",
	roomAuthNoRevision:     "its PMS interface has no published configuration",
	roomAuthContinuityGap:  "updates from the PMS were missed",
	roomAuthContinuityNone: "no updates have arrived from the PMS yet",
	roomAuthNotInSync:      "the guest list is still loading",
	roomAuthFeedSilent:     "the PMS has stopped responding",
	roomAuthRevisionUnpin:  "the PMS interface is being reconfigured",
	roomAuthNeverSynced:    "the guest list has not been received from the PMS yet",
	roomAuthResyncInFlight: "the guest list is being refreshed",
	roomAuthMaterializing:  "the guest list is being applied",
}

func roomAuthWord(code string) string {
	if w, ok := roomAuthWords[code]; ok {
		return w
	}
	return "the PMS is not able to answer"
}

// assessRoomSignIn is pure: facts in, verdict out. A maximum of three interfaces per network is the supported set (pmsresolve.MaxSharedCandidates).
func assessRoomSignIn(nets []rsNetworkFacts, ifaces map[string]rsInterface, now time.Time) roomSignInAssessment {
	out := roomSignInAssessment{GeneratedAt: now, Networks: []rsNetwork{}, Interfaces: []rsInterfaceSummary{}, Issues: []rsIssue{}}

	anyServable := false
	for _, i := range ifaces {
		if i.Lifecycle == "ACTIVE" && i.Published {
			anyServable = true
		}
	}
	serving := map[string][]string{}
	offRoutes := map[string]int{}

	for _, f := range nets {
		n := rsNetwork{NetworkID: f.ID, Name: f.Name, Enabled: f.Enabled, IsDefault: f.IsDefault, Policy: f.Policy,
			Interfaces: []rsInterface{}, Reasons: []rsReason{}}
		ids := append([]string(nil), f.Mapped...)
		sort.Strings(ids)
		for _, id := range ids {
			i, ok := ifaces[id]
			if !ok {
				i = rsInterface{ID: id, Label: id, Lifecycle: "UNKNOWN"}
			}
			n.Interfaces = append(n.Interfaces, i)
		}
		live := f.Enabled && !f.Archived
		if !live {
			for _, id := range ids {
				offRoutes[id]++
			}
		} else if f.Policy != policyNone {
			for _, id := range ids {
				serving[id] = append(serving[id], f.Name)
			}
		}

		// A policy-less network keeps the one meaning it always had: exactly one mapped interface is that interface.
		policy := f.Policy
		if policy == policyUndecided && len(ids) == 1 {
			policy, n.Legacy = policyDedicated, true
		}
		n.Policy = policy
		switch {
		case !live:
			n.State, n.Summary = rsInactive, "This network is switched off or archived. Its room sign-in routing is kept as it is."
		case policy == policyNone || f.RoomMethodOff:
			n.State = rsOff
			if f.RoomMethodOff && policy != policyNone {
				n.Summary = "Room sign-in is switched off in this network's sign-in methods."
			} else {
				n.Summary = "Room sign-in is not offered on this network, by choice. Guests use the other sign-in methods."
			}
		case policy == policyUndecided && len(ids) == 0:
			if anyServable {
				n.State = rsUndecided
				n.Summary = "No one has decided how room sign-in works on this network, so it is not offered."
				n.Reasons = append(n.Reasons, rsReason{Code: "UNDECIDED", Text: "No room sign-in routing has been chosen for this network."})
			} else {
				n.State = rsNotSet
				n.Summary = "No PMS connection is in service, so there is nothing to route room sign-in to."
			}
		case policy == policyUndecided || (policy == policyDedicated && len(ids) != 1) || (policy == policyShared && (len(ids) < 2 || len(ids) > pmsresolve.MaxSharedCandidates)):
			n.State = rsBadRouting
			n.Summary = "The room sign-in routing of this network does not match its policy, so room sign-in is refused until it is set again."
			n.Reasons = append(n.Reasons, rsReason{Code: "POLICY_MISMATCH", Text: fmt.Sprintf("The policy names %s but %d interface(s) are mapped.", policyWords(policy), len(ids))})
		default:
			assessCandidates(&n, policy)
		}
		out.Networks = append(out.Networks, n)
	}

	// per-interface summary
	var idList []string
	for id := range ifaces {
		idList = append(idList, id)
	}
	sort.Slice(idList, func(a, b int) bool {
		x, y := ifaces[idList[a]], ifaces[idList[b]]
		if x.Label != y.Label {
			return x.Label < y.Label
		}
		return x.ID < y.ID
	})
	for _, id := range idList {
		i := ifaces[id]
		s := rsInterfaceSummary{rsInterface: i, Serving: serving[id], ArchivedRoutes: offRoutes[id]}
		if s.Serving == nil {
			s.Serving = []string{}
		}
		switch {
		case i.Lifecycle != "ACTIVE":
			s.State = "NOT_IN_SERVICE"
		case len(s.Serving) == 0:
			s.State = "NOT_USED"
		case !i.HealthKnown:
			s.State = "UNKNOWN"
		case !i.RoomReady:
			s.State = "IMPAIRED"
		default:
			s.State = "SERVING"
		}
		out.Interfaces = append(out.Interfaces, s)
	}

	// issues and overall
	offered, ready, blocked, unknown := 0, 0, 0, 0
	for _, n := range out.Networks {
		switch n.State {
		case rsReady:
			offered++
			ready++
		case rsBlocked, rsBadRouting, rsUndecided:
			offered++
			blocked++
			out.Issues = append(out.Issues, issueFor(n))
		case rsUnknown:
			offered++
			unknown++
		}
	}
	switch {
	case offered == 0:
		out.Overall = "NOT_OFFERED"
	case blocked == 0 && unknown == 0:
		out.Overall = "READY"
	case blocked == 0:
		out.Overall = "UNKNOWN"
	case ready == 0 && unknown == 0:
		out.Overall = "BLOCKED"
	default:
		out.Overall = "PARTIAL"
	}
	return out
}

func policyWords(p string) string {
	switch p {
	case policyDedicated:
		return "one dedicated interface"
	case policyShared:
		return "a shared set of two or three interfaces"
	case policyNone:
		return "no room sign-in"
	}
	return "no decision"
}

// assessCandidates judges an in-service routed network by its candidates. Dedicated and shared differ only in how many must answer: all of them, in both.
func assessCandidates(n *rsNetwork, policy string) {
	var bad, unk []rsReason
	for _, i := range n.Interfaces {
		switch {
		case i.Lifecycle != "ACTIVE":
			bad = append(bad, rsReason{Code: roomAuthNotActive, InterfaceID: i.ID, Interface: i.Label, Text: i.Label + " is not in service"})
		case !i.Published:
			bad = append(bad, rsReason{Code: roomAuthNoRevision, InterfaceID: i.ID, Interface: i.Label, Text: i.Label + " has no published configuration"})
		case !i.HealthKnown:
			unk = append(unk, rsReason{Code: "UNKNOWN", InterfaceID: i.ID, Interface: i.Label, Text: "The state of " + i.Label + " could not be read"})
		case !i.RoomReady:
			bad = append(bad, rsReason{Code: i.Reason, InterfaceID: i.ID, Interface: i.Label, Text: i.Label + ": " + roomAuthWord(i.Reason)})
		}
	}
	switch {
	case len(bad) > 0:
		n.State, n.Reasons = rsBlocked, append(bad, unk...)
		if policy == policyShared {
			n.Summary = "Guests on this network cannot sign in with their room number. This network checks every selected PMS, and one of them cannot answer, so none can be trusted to say a guest is absent."
		} else {
			n.Summary = "Guests on this network cannot sign in with their room number until its PMS interface is ready."
		}
	case len(unk) > 0:
		n.State, n.Reasons = rsUnknown, unk
		n.Summary = "The state of a PMS interface on this network could not be read, so nothing is claimed about room sign-in here."
	default:
		n.State = rsReady
		if policy == policyShared {
			n.Summary = "Room sign-in checks all " + fmt.Sprint(len(n.Interfaces)) + " selected PMS interfaces strictly, and all can answer."
		} else {
			n.Summary = "Room sign-in checks " + n.Interfaces[0].Label + " and it can answer."
		}
	}
}

func issueFor(n rsNetwork) rsIssue {
	it := rsIssue{ID: "room-signin:" + n.NetworkID, Severity: "warn", NetworkID: n.NetworkID,
		Title: "Room sign-in is unavailable on " + n.Name, Href: "/pms-routing", Action: "Open PMS routing"}
	var parts []string
	for _, r := range n.Reasons {
		parts = append(parts, r.Text)
	}
	it.Detail = n.Summary
	if len(parts) > 0 {
		it.Detail += " " + strings.Join(parts, "; ") + "."
	}
	switch n.State {
	case rsBlocked:
		it.Severity = "err"
		// A feed problem is fixed on the PMS connection; a routing problem on the routing page.
		for _, r := range n.Reasons {
			if r.InterfaceID != "" && r.Code != roomAuthNotActive && r.Code != roomAuthNoRevision {
				it.Href, it.Action = "/pms-interfaces", "Open PMS connections"
			}
		}
	case rsUndecided:
		it.Title = "Choose how room sign-in works on " + n.Name
	case rsBadRouting:
		it.Severity = "err"
	}
	return it
}

// ---- loading ------------------------------------------------------------------------------------------------------------------------------------------

func (s *server) roomSignInFacts(ctx context.Context) ([]rsNetworkFacts, map[string]rsInterface, error) {
	archived := s.archivedSet(ctx)
	def := s.defaultNetworkID(ctx)
	rows, err := s.db.Query(ctx, `
		SELECT g.id::text, COALESCE(g.name,''), g.enabled,
		       COALESCE(p.mode, ''),
		       COALESCE((SELECT 'pms' = ANY(o.disabled_methods) FROM iam_v2.network_signin_overrides o WHERE o.network_id = g.id), false),
		       COALESCE((SELECT array_agg(m.pms_interface_id::text ORDER BY m.pms_interface_id) FROM iam_v2.guest_network_pms_map m
		                  WHERE m.tenant_id=g.tenant_id AND m.site_id=g.site_id AND m.guest_network_id=g.id), '{}')
		  FROM public.guest_networks g
		  LEFT JOIN iam_v2.guest_network_pms_policy p ON p.guest_network_id = g.id
		 WHERE g.tenant_id=$1::uuid AND g.site_id=$2::uuid
		 ORDER BY g.enabled DESC, g.name, g.id`, s.tenantID, s.siteID)
	if err != nil {
		return nil, nil, err
	}
	var nets []rsNetworkFacts
	for rows.Next() {
		var f rsNetworkFacts
		if err := rows.Scan(&f.ID, &f.Name, &f.Enabled, &f.Policy, &f.RoomMethodOff, &f.Mapped); err != nil {
			rows.Close()
			return nil, nil, err
		}
		f.Archived, f.IsDefault = archived[f.ID], f.ID == def
		nets = append(nets, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	irows, err := s.db.Query(ctx, `SELECT i.id::text, COALESCE(NULLIF(i.display_label,''), i.connector_kind), i.lifecycle_state, i.current_revision_id IS NOT NULL
		  FROM iam_v2.pms_interfaces i WHERE i.tenant_id=$1::uuid AND i.site_id=$2::uuid AND i.lifecycle_state <> 'DECOMMISSIONED'`, s.tenantID, s.siteID)
	if err != nil {
		return nil, nil, err
	}
	ifaces := map[string]rsInterface{}
	for irows.Next() {
		var i rsInterface
		if err := irows.Scan(&i.ID, &i.Label, &i.Lifecycle, &i.Published); err != nil {
			irows.Close()
			return nil, nil, err
		}
		ifaces[i.ID] = i
	}
	irows.Close()
	// A decommissioned interface can still be named by an old route; it is reported as out of service rather than missing.
	for id, i := range ifaces {
		h, herr := s.interfaceHealthRow(ctx, id)
		if herr == nil {
			i.HealthKnown, i.RoomReady, i.Reason, i.Source, i.Transport = true, h.RoomAuthReady, h.RoomAuthReason, h.RoomAuthSource, h.Transport
		}
		ifaces[id] = i
	}
	return nets, ifaces, nil
}

func (s *server) roomSignInAssessment(ctx context.Context) (roomSignInAssessment, error) {
	nets, ifaces, err := s.roomSignInFacts(ctx)
	if err != nil {
		return roomSignInAssessment{}, err
	}
	// Names for interfaces that are routed but decommissioned: out of service, never missing.
	for _, n := range nets {
		for _, id := range n.Mapped {
			if _, ok := ifaces[id]; !ok {
				ifaces[id] = rsInterface{ID: id, Label: "Retired PMS interface", Lifecycle: "DECOMMISSIONED"}
			}
		}
	}
	return assessRoomSignIn(nets, ifaces, time.Now().UTC()), nil
}
