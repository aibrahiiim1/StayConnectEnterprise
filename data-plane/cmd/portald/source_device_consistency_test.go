package main

// Every sign-in path derives the source device through handler.deviceMAC, which resolves a cold cache; the guard below
// stops a path being written against the raw neighbour-cache read.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

// arp_resolve.go implements the resolve; main.go has one deliberate exception, the landing page's support line, which is
// served to every captive probe, refuses nothing, and must not pay the resolve window.
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

// A source check alone could pass with every call site deleted, so assert the nine guest-visible callers are present.
func TestTheGuestPathsCallTheResolvingLookup(t *testing.T) {
	want := map[string]int{
		"acquisition.go":              2, // open/package access, and the purchase-status poll
		"account_handlers.go":         1, // every email-account route, through accountDevice
		"email_handlers.go":           2, // SMS/WhatsApp code request (binds the code to the device, 0110) and verify
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

// A device that cannot be placed is never a credential answer: the uniform post-stay wording says "check your details",
// which is advice the guest cannot act on for such a device.
func TestADeviceFailureIsNeverAnsweredAsCredentials(t *testing.T) {
	src := portaldSources(t)["phase5_poststay_handlers.go"]
	if !strings.Contains(src, "func (h *handler) poststayDeviceFail(") {
		t.Fatal("post-stay has no device-specific refusal; a device that cannot be placed would be answered " +
			"with the authentication wording")
	}
	// originDevice keeps its `fail` parameter (four callers, each with its own answer); post-stay must hand it the device answer, not authentication wording.
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
