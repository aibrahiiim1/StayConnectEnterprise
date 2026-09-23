//go:build linux

package main

// THE REFUSAL ITSELF, ON THE ONLY PLATFORM WHERE IT EXISTS.
//
// admin_surface_test.go checks the classification and that the gate is wired. What it cannot check is the
// refusal: on a non-Linux build requireAdminPeer allows everything by design, so a portable test asserting
// "403" would pass for the wrong reason on Linux and fail on a workstation.
//
// A REFUSAL-ONLY TEST IS HALF A PROOF, and this file was written with that in mind: a gate that refused
// EVERYTHING would satisfy "portald is refused" perfectly while breaking the appliance. So each case below
// pairs the refusal with the admission it must not break -- the same request from edged's uid, and the same
// caller on a guest route.

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

// asPeer builds a request carrying the peer credentials the accept hook would have attached.
func asPeer(method, path string, uid uint32, exe string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	ctx := context.WithValue(r.Context(), peerKey{}, peerCred{
		PID: 4242, UID: uid, Exe: exe, Read: true,
	})
	return r.WithContext(ctx)
}

// currentUIDAsEdged points SCD_EDGED_USER at a real, resolvable, NON-ROOT account on this machine, so
// "edged's uid" is a uid the name service actually returns rather than a number this test invented.
//
// WHY NOT user.Current(). The obvious version of this helper used the test process's own user and skipped
// when that was root -- which meant it skipped in every container, including the CI container and the
// golang image used to check the Linux build, i.e. everywhere this Linux-only file can actually run. And
// the skip was unnecessary: the peer uid in these tests is SYNTHETIC, supplied through asPeer, so whether
// the test process is root has no bearing on what the gate sees. What matters is only that the configured
// service user resolves and is not uid 0.
func currentUIDAsEdged(t *testing.T) uint32 {
	t.Helper()
	// Any of these exists on a Debian-family appliance, a CI runner and the golang image. "stayconnect"
	// first, because on the appliance it is the real answer.
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
		// edgedUID caches, so a case that ran earlier in this binary may have resolved a different user.
		// Reset it: a stale cache would silently invert the result of every assertion below.
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
		"/v1/setup/enroll",
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

// THE OTHER HALF. A gate that refused everything would pass the test above.
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

// THE CASE A REVIEW CAUGHT: edged's OWN UID, A DIFFERENT IMAGE.
//
// After portald was given its own account this gate checked the uid alone, on the belief that edged was
// then the only thing holding it. stayconnect-hotel-admin.service was still running `/usr/bin/node
// server.js` as User=stayconnect -- uid 998 on the appliance, exactly edged's -- with a group that opens
// scd.sock. So the Node process rendering the admin web UI was admitted to /v1/backup/restore,
// /v1/license/install and /v1/setup/enroll.
//
// It has its own account now, and this asserts the in-process half: sharing the account is not enough.
func TestSharingEdgedsUIDIsNotEnough(t *testing.T) {
	mine := currentUIDAsEdged(t)
	for _, exe := range []string{
		"/usr/bin/node",                // hotel-admin, as it actually ran
		"/opt/stayconnect/bin/portald", // and anything else that lands on this account
		"/usr/bin/curl",
		"/opt/stayconnect/bin/edged-copy", // a near-miss name must not pass
	} {
		for _, path := range []string{"/v1/backup/restore", "/v1/license/install", "/v1/setup/enroll"} {
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

// AND THE GUEST SURFACE IS UNAFFECTED BY THE IMAGE. The guest routes are reachable by anything the socket's
// group admits, whatever binary it is -- that is what keeps the captive portal working.
func TestTheGuestSurfaceDoesNotCareWhichImageCalls(t *testing.T) {
	mine := currentUIDAsEdged(t)
	for _, exe := range []string{"/usr/bin/node", "/opt/stayconnect/bin/portald", "/usr/bin/curl"} {
		if code, _ := gate(t, asPeer("POST", "/v1/sessions/activate", mine, exe)); code != http.StatusOK {
			t.Errorf("a guest route refused %s (%d); guest authentication must not depend on the image", exe, code)
		}
	}
}

// AND THE GUEST SURFACE STAYS OPEN TO THE FOREIGN UID, which is the entire reason the socket group still
// admits portald. If this fails, guests cannot sign in.
func TestTheGuestSurfaceStaysOpenToPortald(t *testing.T) {
	mine := currentUIDAsEdged(t)
	foreign := mine + 1
	for _, path := range []string{
		"/v1/health",
		"/v1/commerce/packages",
		"/v1/sessions/activate",
		"/v1/phase3/auth/pms/resolve",
		"/v1/phase5/auth/post-stay-pin",
		"/v1/phase6/devices/list",
		"/v1/sessions/revoke",
	} {
		if code, body := gate(t, asPeer("POST", path, foreign, "/opt/stayconnect/bin/portald")); code != http.StatusOK {
			t.Errorf("%s refused for portald (%d: %s): guest authentication runs through these",
				path, code, strings.TrimSpace(body))
		}
	}
}

// ROOT IS ALLOWED, and it is allowed on purpose rather than by omission: netd, acctd, keepalived and the
// deployment scripts dial this socket as root, and uid 0 can read the DEK from disk regardless.
func TestRootIsAllowed(t *testing.T) {
	currentUIDAsEdged(t)
	if code, _ := gate(t, asPeer("POST", "/v1/backup/run", 0, "/usr/bin/curl")); code != http.StatusOK {
		t.Errorf("root was refused an admin route (%d); the deployment scripts and root-owned daemons "+
			"depend on this", code)
	}
}

// FAIL CLOSED WHEN THE PEER IS UNKNOWN. A connection whose credentials could not be read must not reach an
// administrative route, and must still reach a guest one -- refusing those would take the portal down over
// a diagnostic gap.
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

// AND WHEN SCD CANNOT RESOLVE WHO EDGED IS AT ALL, the admin surface closes rather than opening.
func TestAnUnresolvableEdgedUserClosesTheAdminSurface(t *testing.T) {
	t.Setenv("SCD_EDGED_USER", "a-user-that-does-not-exist-on-this-machine-0f8b9a0e")
	resetEdgedUIDCache()
	t.Cleanup(resetEdgedUIDCache)

	if code, body := gate(t, asPeer("POST", "/v1/backup/restore", 4242, "/opt/stayconnect/bin/edged")); code != http.StatusForbidden {
		t.Errorf("unresolvable service user returned %d, not 403 (%s)", code, strings.TrimSpace(body))
	}
	// Root still gets through: uid 0 is checked before the lookup, so a broken passwd entry does not lock
	// an administrator out of the appliance they own.
	if code, _ := gate(t, asPeer("POST", "/v1/backup/restore", 0, "/usr/bin/curl")); code != http.StatusOK {
		t.Errorf("root was locked out (%d) by an unresolvable service user", code)
	}
}

// THE DEFAULT SERVICE USER IS THE ONE THE APPLIANCE USES. If SCD_EDGED_USER is unset, the lookup must be
// for "stayconnect" -- not root, not the current user, not empty.
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
