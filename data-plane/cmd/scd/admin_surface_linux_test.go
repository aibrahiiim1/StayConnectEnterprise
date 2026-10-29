//go:build linux

package main

// The refusal itself, on the only platform where it exists (elsewhere requireAdminPeer allows everything). Each case
// pairs the refusal with the admission it must not break: a gate that refused everything would pass refusal tests alone.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"
)

func asPeer(method, path string, uid uint32, exe string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(r.Context(), peerKey{}, peerCred{
		PID: 4242, UID: uid, Exe: exe, Read: true,
	})
	return r.WithContext(ctx)
}

// currentUIDAsEdged points SCD_EDGED_USER at a real, resolvable, non-root account, so "edged's uid" is one the name
// service returns. The peer uid is synthetic (via asPeer), so whether the test process is root is irrelevant.
func currentUIDAsEdged(t *testing.T) uint32 {
	t.Helper()
	// Any of these exists on a Debian-family appliance, a CI runner and the golang image; "stayconnect" first.
	for _, name := range []string{"stayconnect", "daemon", "bin", "nobody", "sys"} {
		u, err := user.Lookup(name)
		if err != nil {
			continue
		}
		id, perr := strconv.ParseUint(u.Uid, 10, 32)
		if perr != nil || id == 0 {
			continue
		}
		t.Setenv("SCD_EDGED_USER", name)
		// edgedUID caches; reset it, or a stale value would invert every assertion below.
		resetEdgedUIDCache()
		t.Cleanup(resetEdgedUIDCache)
		return uint32(id)
	}
	t.Skip("no non-root system account could be resolved on this machine")
	return 0
}

func gate(t *testing.T, r *http.Request) (int, string) {
	t.Helper()
	var reached bool
	h := (&server{}).peerGate(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if reached && rec.Code != http.StatusOK {
		t.Fatalf("handler ran but the recorder shows %d", rec.Code)
	}
	return rec.Code, rec.Body.String()
}

func TestAForeignUIDCannotReachTheAdminSurface(t *testing.T) {
	mine := currentUIDAsEdged(t)
	foreign := mine + 1 // stayconnect-portald, on the appliance

	for _, path := range []string{
		"/v1/backup/restore",
		"/v1/license/install",
		"/v1/central/offline-package",
		"/v1/vouchers/0f8b9a0e-0000-0000-0000-000000000000/reveal",
		"/v1/voucher-key-generations",
	} {
		code, body := gate(t, asPeer("POST", path, foreign, "/opt/stayconnect/bin/portald"))
		if code != http.StatusForbidden {
			t.Errorf("%s from a foreign uid returned %d, not 403: the captive portal can call it", path, code)
		}
		if !strings.Contains(body, "administrative route") {
			t.Errorf("%s refused with an unhelpful message: %q", path, strings.TrimSpace(body))
		}
	}
}

// A gate that refused everything would pass the test above.
func TestEdgedItselfIsStillAdmitted(t *testing.T) {
	mine := currentUIDAsEdged(t)
	for _, path := range []string{
		"/v1/backup/restore",
		"/v1/license/install",
		"/v1/vouchers",
	} {
		if code, _ := gate(t, asPeer("POST", path, mine, "/opt/stayconnect/bin/edged")); code != http.StatusOK {
			t.Errorf("%s from edged's own uid returned %d: the gate has broken the admin surface", path, code)
		}
	}
}

// edged's uid alone is not enough: the hotel-admin node process once ran under the same uid with a group that opens
// scd.sock, so the image path must match too.
func TestSharingEdgedsUIDIsNotEnough(t *testing.T) {
	mine := currentUIDAsEdged(t)
	for _, exe := range []string{
		"/usr/bin/node",                // hotel-admin, as it actually ran
		"/opt/stayconnect/bin/portald", // and anything else that lands on this account
		"/usr/bin/curl",
		"/opt/stayconnect/bin/edged-copy", // a near-miss name must not pass
	} {
		for _, path := range []string{"/v1/backup/restore", "/v1/license/install", "/v1/central/offline-package"} {
			code, body := gate(t, asPeer("POST", path, mine, exe))
			if code != http.StatusForbidden {
				t.Errorf("%s from edged's uid running %s returned %d, not 403", path, exe, code)
			}
			if !strings.Contains(body, "edged") {
				t.Errorf("%s refused without saying who may call it: %q", path, strings.TrimSpace(body))
			}
		}
	}
}

// The guest surface is reachable by anything the socket's group admits, whatever binary it is.
func TestTheGuestSurfaceDoesNotCareWhichImageCalls(t *testing.T) {
	mine := currentUIDAsEdged(t)
	for _, exe := range []string{"/usr/bin/node", "/opt/stayconnect/bin/portald", "/usr/bin/curl"} {
		if code, _ := gate(t, asPeer("POST", "/v1/sessions/activate", mine, exe)); code != http.StatusOK {
			t.Errorf("a guest route refused %s (%d); guest authentication must not depend on the image", exe, code)
		}
	}
}

// The guest surface stays open to the foreign uid; if this fails, guests cannot sign in.
func TestTheGuestSurfaceStaysOpenToPortald(t *testing.T) {
	mine := currentUIDAsEdged(t)
	foreign := mine + 1
	for _, path := range []string{
		"/v1/health",
		"/v1/commerce/packages",
		"/v1/sessions/activate",
		"/v1/phase3/auth/pms/resolve",
		"/v1/phase5/auth/post-stay",
		"/v1/phase6/devices/list",
		"/v1/sessions/revoke",
	} {
		if code, body := gate(t, asPeer("POST", path, foreign, "/opt/stayconnect/bin/portald")); code != http.StatusOK {
			t.Errorf("%s refused for portald (%d: %s): guest authentication runs through these",
				path, code, strings.TrimSpace(body))
		}
	}
}

// Root is allowed on purpose: netd, acctd, keepalived and deploy scripts dial as root, and uid 0 can read the DEK from disk anyway.
func TestRootIsAllowed(t *testing.T) {
	currentUIDAsEdged(t)
	if code, _ := gate(t, asPeer("POST", "/v1/backup/run", 0, "/usr/bin/curl")); code != http.StatusOK {
		t.Errorf("root was refused an admin route (%d); the deployment scripts and root-owned daemons "+
			"depend on this", code)
	}
}

// An unknown peer must not reach an administrative route but must still reach a guest one.
func TestAnUnidentifiedPeerIsRefusedAdminAndAllowedGuest(t *testing.T) {
	currentUIDAsEdged(t)

	bare := httptest.NewRequest("POST", "/v1/backup/restore", nil) // no peerCred in the context at all
	if code, _ := gate(t, bare); code != http.StatusForbidden {
		t.Errorf("an unidentified peer got %d on /v1/backup/restore; it must fail closed", code)
	}

	bareGuest := httptest.NewRequest("POST", "/v1/sessions/activate", nil)
	if code, _ := gate(t, bareGuest); code != http.StatusOK {
		t.Errorf("an unidentified peer got %d on a guest route; the portal must keep working", code)
	}
}

// When scd cannot resolve who edged is, the admin surface closes.
func TestAnUnresolvableEdgedUserClosesTheAdminSurface(t *testing.T) {
	t.Setenv("SCD_EDGED_USER", "a-user-that-does-not-exist-on-this-machine-0f8b9a0e")
	resetEdgedUIDCache()
	t.Cleanup(resetEdgedUIDCache)

	if code, body := gate(t, asPeer("POST", "/v1/backup/restore", 4242, "/opt/stayconnect/bin/edged")); code != http.StatusForbidden {
		t.Errorf("unresolvable service user returned %d, not 403 (%s)", code, strings.TrimSpace(body))
	}
	// Root still gets through: uid 0 is checked before the lookup.
	if code, _ := gate(t, asPeer("POST", "/v1/backup/restore", 0, "/usr/bin/curl")); code != http.StatusOK {
		t.Errorf("root was locked out (%d) by an unresolvable service user", code)
	}
}

// With SCD_EDGED_USER unset the lookup is for "stayconnect".
func TestTheDefaultServiceUserIsStayconnect(t *testing.T) {
	src, err := os.ReadFile("peer_identity_linux.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `name = "stayconnect"`) {
		t.Error("the default service user is no longer \"stayconnect\"; edged runs as that user on the " +
			"appliance, and a different default silently refuses the whole admin surface there")
	}
}
