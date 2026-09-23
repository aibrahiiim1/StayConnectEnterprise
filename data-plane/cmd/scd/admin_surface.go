package main

// WHICH OF SCD'S ROUTES ARE ADMINISTRATIVE, AND WHY ONE SOCKET NEEDED TO LEARN THE DIFFERENCE.
//
// scd listens on one unix socket, /run/stayconnect/scd.sock, mode 0660, owned root:stayconnect. Reaching it
// is a single yes-or-no fact: group membership. Behind it sit 56 routes of two completely different kinds.
//
//	GUEST-FACING, called by portald -- the captive portal, which listens on *:8380, i.e. on every guest
//	VLAN. Commerce, session activation, the Phase-3/5/6 authentication paths, tenant branding.
//
//	ADMINISTRATIVE, called by edged after it has authenticated an operator, checked the resource
//	permission and, for the code routes, re-verified the password. Vouchers and their codes. Backups,
//	INCLUDING /v1/backup/restore. Licence install and refresh. Appliance enrolment. Certificate rotation.
//
// Both sets were reachable by anything in the stayconnect group, and portald ran as User=stayconnect. So the
// network-facing process could call /v1/backup/restore, /v1/license/install and /v1/setup/enroll directly --
// no operator, no permission, no step-up, because all of those live in edged and nothing proved they had
// happened. The voucher code routes were the ones that made this urgent, and they were never the worst of it.
//
// TWO CHANGES CLOSE IT, AND NEITHER IS SUFFICIENT ALONE:
//
//	1. portald gets its OWN user (deploy/systemd/stayconnect-portald.service). Under a shared uid no
//	   check here means anything, because a process can ptrace the one it shares a uid with.
//	2. This gate: an administrative route requires the peer to BE edged, by uid. Group membership gets you
//	   the guest surface and nothing else.
//
// WHY A CLASSIFIER AND NOT A SECOND SOCKET. A second listener with disjoint routes is the more structural
// answer and remains the better eventual shape. It is not what this does, for one honest reason: portald,
// acctd, netd, keepalived and three deployment scripts all dial this path today, and moving the boundary
// into the filesystem means changing every one of them at the same time as changing the trust model. This
// keeps the socket and moves the boundary into the process identity, which is the part that was missing.
//
// DEFAULT DENY. classifyRoute returns adminRoute for anything it does not recognise, so a route added later
// and forgotten here is refused to non-edged callers rather than silently exposed. That is the safe
// direction and also the INCONVENIENT one -- a new GUEST route that nobody classifies breaks the portal --
// which is why admin_surface_test.go walks the real router and fails when any registered route is not
// named in one of the two lists below. The runtime fails closed; the test stops it reaching the appliance.

import (
	"net/http"
	"strings"
)

type routeClass int

const (
	// adminRoute: edged only. The default for anything unrecognised.
	adminRoute routeClass = iota
	// guestRoute: any process the socket's group admits. portald, and the root-owned daemons and scripts.
	guestRoute
)

// guestPrefixes is the surface a process other than edged is allowed to reach.
//
// EVERY ENTRY WAS DERIVED FROM A CALLER, not from the route's name. The callers were enumerated across
// cmd/portald, cmd/acctd, cmd/netd, cmd/pmsd and deploy/ before this list existed:
//
//	portald          commerce, sessions/{activate,authorize*,status}, auth/otp/issue, auth/social/start,
//	                 tenant/{auth-methods,branding}, phase3/{access/status,auth/pms/*}, phase5/*, phase6/*
//	acctd (root)     sessions/revoke -- quota enforcement revoking a live session
//	netd (root)      health
//	keepalived       health, as its VRRP liveness check
//	deploy scripts   health, phase6/devices/{list,release}
//
// sessions/revoke is here BECAUSE acctd calls it, and that is a real weakening worth naming: portald can
// revoke a guest session it did not create. It is bounded -- revoking access denies service to a guest and
// discloses nothing, creates nothing and authorises nothing -- and the alternative today would be to refuse
// acctd, which enforces quotas. A separate guest socket would let these two differ; one socket cannot.
var guestPrefixes = []string{
	"/v1/health",
	"/metrics",
	"/v1/commerce/",
	"/v1/sessions/activate",
	"/v1/sessions/authorize",
	"/v1/sessions/status",
	"/v1/sessions/revoke",
	"/v1/auth/otp/",
	"/v1/auth/social/",
	"/v1/tenant/",
	"/v1/phase3/access/",
	"/v1/phase3/auth/",
	"/v1/phase5/",
	"/v1/phase6/",
}

// adminPrefixes exists for the TEST, not for the runtime: classification already defaults to admin, so this
// list adds no enforcement. What it adds is the ability to distinguish "administrative, and we know it" from
// "unclassified, and the default caught it" -- so the test can insist that every route on the router was
// actually considered by somebody.
//
// /v1/phase3/signin-attempts/ is administrative although its siblings are not: it is the operator's view of
// failed guest sign-ins, read by edged for the Guest sign-in attempts screen. The prefix sits between two
// guest prefixes and is listed here deliberately.
var adminPrefixes = []string{
	"/v1/vouchers",
	"/v1/voucher-key-generations",
	"/v1/admin/",
	"/v1/backup/",
	"/v1/license/",
	"/v1/hotel-admin-cert/",
	"/v1/maintenance",
	"/v1/cloud/",
	"/v1/setup/",
	"/v1/phase3/signin-attempts/",
}

func classifyRoute(path string) routeClass {
	for _, p := range guestPrefixes {
		if path == p || strings.HasPrefix(path, p) {
			return guestRoute
		}
	}
	return adminRoute
}

// peerGate is scd's socket middleware: it decides, per request, whether the process on the other end is
// allowed to ask for this.
//
// IT RUNS BEFORE ROUTING, on purpose. chi's Use middleware sees the raw URL path and not the matched
// pattern, which for a gate is the stronger position: it cannot be fooled by a route that matches something
// other than what it appears to, and an unrouted path (404) is classified and refused just the same.
func (s *server) peerGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if classifyRoute(r.URL.Path) == adminRoute && !requireAdminPeer(w, r) {
			return // requireAdminPeer has written the refusal
		}
		next.ServeHTTP(w, r)
	})
}
