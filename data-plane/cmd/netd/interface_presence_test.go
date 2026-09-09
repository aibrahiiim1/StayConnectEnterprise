package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// PRESENCE IS OBSERVED AT THE MOMENT OF THE DECISION, AND A FAILURE TO OBSERVE IT REFUSES.
//
// The design this replaces derived presence from the inventory: each row's last_seen_at against the newest
// one in the table. That is a RELATIVE reference and it fails open in the case that matters most — if netd
// stops sweeping, the table freezes as a whole, every surviving row stays newest-relative, and an interface
// that vanished afterwards reads as present forever. The stored observations remain internally consistent
// while being collectively wrong.
//
// These cases pin the replacement: the answer comes from discovery run now, and an unusable discovery is a
// refusal rather than a fallback.

// presenceApplier builds an applier whose discovery and inventory are both stated by the test.
func presenceApplier(discover func(ctx context.Context) ([]Interface, error)) *applier {
	return &applier{discoverFn: discover}
}

func TestPresenceComesFromLiveDiscoveryNotTheInventory(t *testing.T) {
	a := presenceApplier(func(context.Context) ([]Interface, error) {
		// The kernel says these two exist right now.
		return []Interface{{Name: "ens160"}, {Name: "ens192"}}, nil
	})
	present, _, err := a.presenceSets(context.Background())
	if err != nil {
		t.Fatalf("presenceSets: %v", err)
	}
	if !present["ens160"] || !present["ens192"] {
		t.Fatalf("every discovered interface must be present, got %v", present)
	}
	if len(present) != 2 {
		t.Fatalf("nothing beyond what discovery returned may be present, got %v", present)
	}
}

// THE GAP THIS FIX CLOSES. An interface the inventory still records — however recently, and however
// self-consistent the stored sweep looks — is NOT present if discovery does not see it now.
func TestVanishedInterfaceIsAbsentEvenIfTheInventoryLooksFresh(t *testing.T) {
	a := presenceApplier(func(context.Context) ([]Interface, error) {
		return []Interface{{Name: "ens160"}}, nil // vguest-h is gone
	})
	present, _, err := a.presenceSets(context.Background())
	if err != nil {
		t.Fatalf("presenceSets: %v", err)
	}
	if present["vguest-h"] {
		t.Fatal("an interface discovery cannot see is not present, whatever the inventory says about it")
	}
}

// A WEDGED NETD MUST NOT MAKE EVERYTHING PRESENT FOREVER. This is the scenario the reviewer identified: the
// last sweep is old, so a stale-relative check would still call its whole set present. Live discovery simply
// does not consult it.
func TestAnOldLastSweepCannotKeepAnInterfacePresent(t *testing.T) {
	// Discovery is what it is now; the age of any stored sweep is irrelevant to it by construction.
	a := presenceApplier(func(context.Context) ([]Interface, error) {
		return []Interface{{Name: "ens160"}}, nil
	})
	present, _, err := a.presenceSets(context.Background())
	if err != nil {
		t.Fatalf("presenceSets: %v", err)
	}
	for _, gone := range []string{"ens192", "vguest-h", "br-g-00d1fa1a"} {
		if present[gone] {
			t.Fatalf("%q was not discovered and must not be present, no matter how the inventory reads", gone)
		}
	}
}

// FAIL CLOSED. If presence cannot be established, the answer is an error — never a fallback to the last
// thing the inventory happened to record.
func TestDiscoveryFailureRefusesRatherThanFallingBack(t *testing.T) {
	boom := errors.New("ip: command not found")
	a := presenceApplier(func(context.Context) ([]Interface, error) { return nil, boom })

	present, known, err := a.presenceSets(context.Background())
	if err == nil {
		t.Fatal("a discovery failure must refuse; falling back to stored data is the fail-open this replaces")
	}
	if !errors.Is(err, boom) {
		t.Fatalf("the underlying cause must survive so an operator can act on it, got %v", err)
	}
	if !strings.Contains(err.Error(), "stale inventory") {
		t.Fatalf("the error must say why it refused rather than guessed, got %v", err)
	}
	if present != nil || known != nil {
		t.Fatal("a refusal must not hand back a partially-populated set that a caller could mistake for an answer")
	}
}

// AN EMPTY APPLIANCE IS NOT A PERMISSIVE ONE. Discovery returning nothing is a valid observation — nothing
// is present — and must not be confused with "unknown, so allow".
func TestDiscoveringNothingMeansNothingIsPresent(t *testing.T) {
	a := presenceApplier(func(context.Context) ([]Interface, error) { return nil, nil })
	present, _, err := a.presenceSets(context.Background())
	if err != nil {
		t.Fatalf("an empty appliance is an observation, not an error: %v", err)
	}
	if len(present) != 0 {
		t.Fatalf("nothing discovered means nothing present, got %v", present)
	}
	// And the validator refuses against it rather than skipping the check: an empty non-nil map is not nil.
	if present == nil {
		t.Fatal("the present set must be non-nil so validation performs the check instead of skipping it")
	}
}

// AN UNPLUGGED CABLE IS NOT AN ABSENT INTERFACE. Discovery reads link objects, so the NIC still appears with
// its carrier down — and it stays usable as a parent, which is what an operator configuring ahead of the
// cable expects.
func TestUnpluggedInterfaceIsStillPresent(t *testing.T) {
	a := presenceApplier(func(context.Context) ([]Interface, error) {
		return []Interface{{Name: "ens160", LinkState: "up"}, {Name: "ens192", LinkState: "down"}}, nil
	})
	present, _, err := a.presenceSets(context.Background())
	if err != nil {
		t.Fatalf("presenceSets: %v", err)
	}
	if !present["ens192"] {
		t.Fatal("a NIC with its cable out is still an interface; only a vanished link object is absent")
	}
}
