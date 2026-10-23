package main

// scd's one unix socket (mode 0660, root:stayconnect) admits anything in the group, but its routes are of two kinds:
// guest-facing (called by portald, which listens on every guest VLAN) and administrative (called by edged after operator
// auth, permission and step-up: vouchers, backup restore, licence install, enrolment, certificate rotation).
// Two things close the gap and neither suffices alone: portald runs as its own user (a shared uid can ptrace the
// process, so no check here would mean anything), and an administrative route requires the peer to be edged by uid.
//
// Default deny: classifyRoute returns adminRoute for anything unrecognised, so a forgotten route is refused rather than
// exposed. A new guest route nobody classifies breaks the portal, which is why admin_surface_test.go walks the real
// router and fails on any route not named in the lists below.

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

// guestPrefixes is the surface a process other than edged may reach; each entry was derived from a caller (portald,
// acctd, netd, keepalived, deploy scripts), not from the route name.
// sessions/revoke is here because acctd (root) calls it for quota enforcement: portald can therefore revoke a guest
// session it did not create, which only denies service and discloses or authorises nothing.
var guestPrefixes = []string{
	"/v1/health",
	"/metrics",
	"/v1/commerce/",
	"/v1/sessions/activate",
	"/v1/sessions/authorize",
	// The remembered device: portald asks whether a device is remembered and revokes on sign-out.
	"/v1/sessions/resume-",
	"/v1/sessions/status",
	"/v1/sessions/revoke",
	"/v1/auth/otp/",
	"/v1/auth/social/",
	// Email accounts: each acts on one account the caller proves (password, device-bound token or single-use emailed
	// token) or answers the same for everyone (forgot). The operator's resend is under /v1/admin/.
	"/v1/auth/account/",
	"/v1/tenant/",
	"/v1/phase3/access/",
	"/v1/phase3/auth/",
	"/v1/phase5/",
	"/v1/phase6/",
	// A client's devices: each acts only on the package proven by the requesting device, through iam_v2.client_device_action, which records every attempt.
	"/v1/devices/",
	// The portal HTTPS state: hostnames and a reason, nothing secret and nothing that changes anything. Its siblings under /v1/portal/tls/ (kick, renew) are administrative.
	"/v1/portal/tls-state",
}

// adminPrefixes exists for the test, not enforcement (the default is already admin): it distinguishes "known
// administrative" from "unclassified". /v1/phase3/signin-attempts/ is administrative although its siblings are not
// (the operator's view of failed guest sign-ins); it sits between two guest prefixes deliberately.
var adminPrefixes = []string{
	"/v1/vouchers",
	"/v1/voucher-batches",
	"/v1/voucher-key-generations",
	"/v1/admin/",
	"/v1/backup/",
	// The erasure ledger: edged commits every erasure through it; never a guest route.
	"/v1/ledger/",
	"/v1/license/",
	"/v1/hotel-admin-cert/",
	"/v1/maintenance",
	"/v1/central/",
	"/v1/phase3/signin-attempts/",
	// The module resolver's full state is operator information; the portal never needs it.
	"/v1/modules",
	// Card payment administration: provider credentials are sealed here, where the key is; the portal never reaches it.
	"/v1/payment/",
	// The portal certificate agent's kick and renew: "Renew now" spends Central's issuance budget, so the portal must never trigger it.
	"/v1/portal/tls/",
	// Social sign-in administration: the write-only provider secret is sealed here; the portal's social routes are the separate /v1/auth/social/ guest prefix.
	"/v1/social/",
}

func classifyRoute(path string) routeClass {
	for _, p := range guestPrefixes {
		if path == p || strings.HasPrefix(path, p) {
			return guestRoute
		}
	}
	return adminRoute
}

// peerGate runs before routing on purpose: chi's Use sees the raw URL path, so a route cannot fool it, and an unrouted
// 404 path is classified and refused just the same.
func (s *server) peerGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if classifyRoute(r.URL.Path) == adminRoute && !requireAdminPeer(w, r) {
			return // requireAdminPeer has written the refusal
		}
		next.ServeHTTP(w, r)
	})
}
