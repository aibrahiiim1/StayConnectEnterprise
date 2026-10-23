package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Presence comes from discovery run now, never from the inventory's last_seen_at: if sweeping stopped, every
// stored row would stay "newest" and a vanished interface would read as present forever. An unusable
// discovery is a refusal, not a fallback.

func presenceApplier(discover func(ctx context.Context) ([]Interface, error)) *applier {
	return &applier{discoverFn: discover}
}

func TestPresenceComesFromLiveDiscoveryNotTheInventory(t *testing.T) {
	a := presenceApplier(func(context.Context) ([]Interface, error) {
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

// An interface the inventory still records is not present if discovery does not see it now.
func TestVanishedInterfaceIsAbsentEvenIfTheInventoryLooksFresh(t *testing.T) {
	a := presenceApplier(func(context.Context) ([]Interface, error) {
		return []Interface{{Name: "ens160"}}, nil
	})
	present, _, err := a.presenceSets(context.Background())
	if err != nil {
		t.Fatalf("presenceSets: %v", err)
	}
	if present["vguest-h"] {
		t.Fatal("an interface discovery cannot see is not present, whatever the inventory says about it")
	}
}

// A wedged netd (old last sweep) must not make everything present forever; live discovery ignores the stored sweep.
func TestAnOldLastSweepCannotKeepAnInterfacePresent(t *testing.T) {
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

// Fail closed: if presence cannot be established the answer is an error, never a fallback to the inventory.
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

// Discovery returning nothing is a valid observation (nothing present), not "unknown, so allow".
func TestDiscoveringNothingMeansNothingIsPresent(t *testing.T) {
	a := presenceApplier(func(context.Context) ([]Interface, error) { return nil, nil })
	present, _, err := a.presenceSets(context.Background())
	if err != nil {
		t.Fatalf("an empty appliance is an observation, not an error: %v", err)
	}
	if len(present) != 0 {
		t.Fatalf("nothing discovered means nothing present, got %v", present)
	}
	// The validator refuses against it: an empty non-nil map is not nil.
	if present == nil {
		t.Fatal("the present set must be non-nil so validation performs the check instead of skipping it")
	}
}

// An unplugged cable is not an absent interface: the NIC still appears with carrier down and stays usable as a parent.
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
