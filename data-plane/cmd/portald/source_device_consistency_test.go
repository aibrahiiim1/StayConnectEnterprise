package main

// EVERY SIGN-IN PATH DERIVES THE SOURCE DEVICE THE SAME WAY.
//
// The cold-cache refusal was found on the PMS room path, but the mechanism was shared: eight other guest paths
// read the kernel's neighbour cache once and treated a miss as "this device is not on a guest network". Fixing
// one and leaving the rest would have left the same defect waiting on voucher, client account, OTP, social,
// post-stay, open access, purchase status and session activation -- and the next report would have looked like a
// new bug.
//
// This file pins the shared property at the source rather than re-testing nine handlers: every caller goes
// through handler.deviceMAC, and deviceMAC resolves a cold cache. The guard below is what stops a tenth path
// from being written against the raw cache read.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// portaldSources returns every non-test Go file in this package.
func portaldSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatal(err)
		}
		out[name] = string(raw)
	}
	if len(out) == 0 {
		t.Fatal("no portald sources were read; this test would pass vacuously")
	}
	return out
}

// NO SIGN-IN PATH READS THE NEIGHBOUR CACHE DIRECTLY.
//
// arp_resolve.go is the implementation and main.go holds exactly one deliberate exception: the landing page's
// support line, which names the device so a guest can read it to reception. That page is served to every
// captive-portal probe on the network every few seconds, it refuses nothing, and a missing address only omits a
// line -- so it must NOT pay the resolve window. Every path where a cold cache costs the guest something does.
func TestEverySignInPathResolvesTheSourceDeviceThroughOnePlace(t *testing.T) {
	const allowedInMain = 2 // the field declaration and the landing page's guarded best-effort read
	for name, src := range portaldSources(t) {
		if name == "arp_resolve.go" {
			continue
		}
		uses := 0
		for _, line := range strings.Split(src, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if strings.Contains(line, "h.arpCache(") {
				uses++
			}
		}
		switch name {
		case "main.go":
			if uses > allowedInMain {
				t.Errorf("main.go reads the neighbour cache directly %d times; only the landing page's support "+
					"line may, and it must stay best-effort (see the comment there)", uses)
			}
		default:
			if uses > 0 {
				t.Errorf("%s reads the neighbour cache directly %d time(s); sign-in paths must use "+
					"handler.deviceMAC so a cold cache is resolved rather than refused", name, uses)
			}
		}
	}
}

// ...AND THE RESOLVING LOOKUP IS ACTUALLY REACHED FROM THE PATHS THAT MATTER.
//
// A source check alone could pass with every call site deleted, so this asserts the callers are present: the
// nine guest-visible paths that derive the device from the connection.
func TestTheGuestPathsCallTheResolvingLookup(t *testing.T) {
	want := map[string]int{
		"acquisition.go":              2, // open/package access, and the purchase-status poll
		"email_handlers.go":           1, // OTP code
		"iamv2_commerce_session.go":   1, // session activation (admitting a device to an entitlement)
		"main.go":                     2, // voucher, client account
		"phase5_poststay_handlers.go": 1, // post-stay PIN
		"pms_phase3_handlers.go":      1, // PMS room sign-in
		"social_handlers.go":          2, // social start and callback
	}
	srcs := portaldSources(t)
	for file, n := range want {
		src, ok := srcs[file]
		if !ok {
			t.Errorf("%s is gone; the source-device property it carried needs re-checking", file)
			continue
		}
		got := strings.Count(src, "h.deviceMAC(")
		if got != n {
			t.Errorf("%s calls deviceMAC %d time(s), expected %d: a path was added or removed, so confirm it "+
				"resolves the device rather than reading the cache once", file, got, n)
		}
	}
}

// A DEVICE THAT CANNOT BE PLACED IS NEVER A CREDENTIAL ANSWER.
//
// The post-stay path is the one that had to change: its wording is deliberately uniform for every
// authentication outcome, because a departed guest with a PIN has no room or family name to re-check -- but that
// sentence still says "check your details", and for a device the appliance could not place that is advice about
// the one thing the guest cannot act on.
func TestADeviceFailureIsNeverAnsweredAsCredentials(t *testing.T) {
	src := portaldSources(t)["phase5_poststay_handlers.go"]
	if !strings.Contains(src, "func (h *handler) poststayDeviceFail(") {
		t.Fatal("post-stay has no device-specific refusal; a device that cannot be placed would be answered " +
			"with the authentication wording")
	}
	// originDevice keeps its `fail` parameter -- its four callers are different surfaces and each has its own
	// correct answer (the access-status poll says nothing at all; the device list has its own sentence). What
	// matters is that POST-STAY now hands it the DEVICE answer instead of its authentication wording.
	if !strings.Contains(src, "h.originDevice(w, r, b, h.poststayDeviceFail)") {
		t.Error("post-stay still derives its device with the authentication wording as the failure")
	}
	if strings.Contains(src, "h.originDevice(w, r, b, h.poststayFail)") {
		t.Error("post-stay's authentication wording is still wired to a device condition")
	}
	// ...and it is the TECHNICAL class, which is what decides the sentence the guest reads.
	if !strings.Contains(src, "\"poststay_\"+reason, classTechnical") {
		t.Error("the post-stay device refusal is not classified TECHNICAL")
	}
}
