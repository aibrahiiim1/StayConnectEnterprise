package main

// WHO MAY CALL WHICH NETD ROUTE.
//
// netd's socket is root:stayconnect 0660, and the stayconnect group is not one caller: portald holds it as a
// supplementary group (it needs scd's socket for guest sign-in), so before this file every netd route except the
// Phase-3 plan submission answered the captive portal's runtime identity -- guest-network apply, confirm and
// rollback, and the system-network (WAN/LAN) apply with an actor the caller typed into the body (audit B-2,
// confirmed live: /v1/health and /v1/leases answered stayconnect-portald).
//
// Every route now names the callers it serves, as the KERNEL reports them (SO_PEERCRED, the same mechanism the
// Phase-3 producer check uses -- phase3_control.go), and anything else is refused with 403. The lists come from
// the unit's environment and FAIL CLOSED: a list that is unset, empty or names an account that does not resolve
// admits nobody.
//
// THE INVENTORY, FROM THE CODE (2026-10-16):
//
//	edged   (User=stayconnect)  every guest-network, DHCP-lease, interface, portal-address and system-network route
//	                             (cmd/edged/resources_network.go, resources_sysnetwork.go, resources_overview.go,
//	                             client_devices_admin.go, resources_portal_https.go, health_checks.go)
//	acctd   (User=root)          /v1/phase3/shaping and /v1/phase3/shaping/epochs, authenticated separately against
//	                             NETD_PHASE3_PRODUCER_UID (phase3_control.go)
//	scd     (User=root)          /v1/flows/end, the kernel half of a staff disconnect (cmd/scd/main.go denyAtGate)
//	scripts (root)               scripts/phase19-*.sh drive validate/apply/confirm/rollback/leases as root
//	portald, pmsd, hotel-admin   NOTHING. portald never dials netd; neither does pmsd or the console process.
//
// Root is named explicitly where it is admitted (NETD_ADMIN_CALLERS=stayconnect,root in the shipped unit) rather
// than being a silent exception: a root process can rewrite netd's environment anyway, so excluding it buys
// nothing, but admitting it is a decision the unit file states.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os/user"
	"sort"
	"strconv"
	"strings"
)

// routeClass is a set of routes that serve the same callers.
type routeClass string

const (
	// classAdmin: guest-network and system-network configuration, the reads that feed the console, health.
	classAdmin routeClass = "admin"
	// classFlows: ending an address's flows. scd's staff-disconnect path; nothing else needs it.
	classFlows routeClass = "flows"
)

// callerPolicy is the resolved per-class allowlist plus the one identity whose body-supplied actor is believed.
type callerPolicy struct {
	allowed map[routeClass]map[int]bool
	// edgedUID is the console backend. Only edged authenticates operators, so only edged's statement of WHO
	// asked for a change is recorded as the actor. -1 = unknown: no caller's body actor is believed.
	edgedUID int
	// problems explains every name that did not resolve, for the startup log.
	problems []string
}

// lookupUID resolves an account name or a numeric uid. Replaceable in tests.
var lookupUID = func(name string) (int, error) {
	if n, err := strconv.Atoi(name); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("negative uid %d", n)
		}
		return n, nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(u.Uid)
}

func resolveList(raw string) (map[int]bool, []string) {
	out := map[int]bool{}
	var problems []string
	for _, f := range strings.Split(raw, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		uid, err := lookupUID(f)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%q does not resolve (%v)", f, err))
			continue
		}
		out[uid] = true
	}
	return out, problems
}

// loadCallerPolicy reads the caller lists from the environment.
//
//	NETD_ADMIN_CALLERS   accounts (names or uids) admitted to the admin routes
//	NETD_FLOW_CALLERS    accounts admitted to /v1/flows/end
//	NETD_EDGED_CALLER    the console backend's account, whose body-supplied actor is recorded
func loadCallerPolicy(getenv func(string) string) callerPolicy {
	p := callerPolicy{allowed: map[routeClass]map[int]bool{}, edgedUID: -1}
	for class, key := range map[routeClass]string{classAdmin: "NETD_ADMIN_CALLERS", classFlows: "NETD_FLOW_CALLERS"} {
		set, problems := resolveList(getenv(key))
		p.allowed[class] = set
		for _, pr := range problems {
			p.problems = append(p.problems, key+": "+pr)
		}
		if len(set) == 0 {
			p.problems = append(p.problems, key+" admits nobody: every "+string(class)+" route is refused")
		}
	}
	if raw := strings.TrimSpace(getenv("NETD_EDGED_CALLER")); raw != "" {
		if uid, err := lookupUID(raw); err == nil {
			p.edgedUID = uid
		} else {
			p.problems = append(p.problems, fmt.Sprintf("NETD_EDGED_CALLER %q does not resolve (%v): no actor is believed from a request body", raw, err))
		}
	}
	sort.Strings(p.problems)
	return p
}

// callerFrom returns the kernel-verified caller of this request.
func callerFrom(ctx context.Context) (producerIdentity, bool) {
	id, ok := ctx.Value(peerConnKey{}).(producerIdentity)
	return id, ok
}

// gate wraps a handler so only the class's callers reach it.
func (p callerPolicy) gate(class routeClass, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := callerFrom(r.Context())
		if !ok {
			// No kernel statement of the caller (credentials unreadable, or not a unix connection). Never "probably
			// edged".
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "caller identity unavailable"})
			return
		}
		if !p.allowed[class][id.UID] {
			slog.Warn("netd: refused a caller that does not serve this route",
				"route", r.URL.Path, "class", string(class), "uid", id.UID, "pid", id.PID)
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": fmt.Sprintf("uid %d may not call %s", id.UID, r.URL.Path)})
			return
		}
		h(w, r)
	}
}

// actorFor decides what an audit row records as the actor of a change. edged authenticated an operator and
// says who; anything else admitted to the route (root tooling) is recorded as itself, whatever its body claims.
func (p callerPolicy) actorFor(ctx context.Context, bodyActor, bodyActorID, bodySourceIP string) (actor, actorID, sourceIP string) {
	id, ok := callerFrom(ctx)
	if ok && p.edgedUID >= 0 && id.UID == p.edgedUID {
		return bodyActor, bodyActorID, bodySourceIP
	}
	name := "unknown"
	if ok {
		name = strconv.Itoa(id.UID)
		if u, err := user.LookupId(name); err == nil {
			name = u.Username + "(" + name + ")"
		}
	}
	return "local:" + name, "", "local"
}
