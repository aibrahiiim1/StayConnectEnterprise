package identity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// An appliance that downloads ONE offline activation request must not lose the online path.
//
// EnsureLocalKeypair writes an identity.json holding a keypair and no appliance id -- correct, because a
// factory-clean appliance has no id to claim yet. But load() succeeds on that file, and LoadOrEnroll used to
// return whatever load() gave it. The result: download a request, and the appliance never self-registers
// again. It sits at "awaiting enrollment" forever, having silently lost the path it was going to be
// activated by, and the only symptom is an appliance that never appears in the control panel.
func TestUnboundKeypairDoesNotCountAsEnrolled(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}

	id, err := s.EnsureLocalKeypair()
	if err != nil {
		t.Fatalf("EnsureLocalKeypair: %v", err)
	}
	if id.ApplianceID != "" {
		t.Fatal("a locally generated keypair must not invent an appliance id")
	}
	if _, err := os.Stat(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("identity.json should exist: %v", err)
	}

	// With no Central to reach, LoadOrEnroll must report "not enrolled" (nil) rather than handing back the
	// unbound keypair as though enrolment had happened.
	got, err := s.LoadOrEnroll(context.Background(), "", "", "SC-TEST", true)
	if err != nil {
		t.Fatalf("LoadOrEnroll: %v", err)
	}
	if got != nil {
		t.Fatalf("an unbound keypair must not be returned as an enrolled identity (got appliance_id=%q)",
			got.ApplianceID)
	}
}

// Once Central has bound an appliance id, that identity IS enrolled and must be returned as-is -- no
// re-registration, no new key.
func TestBoundIdentityIsReturnedUnchanged(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if _, err := s.EnsureLocalKeypair(); err != nil {
		t.Fatalf("EnsureLocalKeypair: %v", err)
	}
	if err := s.AdoptApplianceID("appliance-42"); err != nil {
		t.Fatalf("AdoptApplianceID: %v", err)
	}

	before, err := s.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	got, err := s.LoadOrEnroll(context.Background(), "https://central.invalid", "", "SC-TEST", true)
	if err != nil {
		t.Fatalf("LoadOrEnroll: %v", err)
	}
	if got == nil || got.ApplianceID != "appliance-42" {
		t.Fatalf("a bound identity must be returned unchanged, got %+v", got)
	}
	// The key must be the same one. A replaced key would orphan every certificate and signed document
	// already issued against it.
	if got.PublicKeyB64 != before.PublicKeyB64 {
		t.Fatal("LoadOrEnroll must not replace the identity key of an already-bound appliance")
	}
}
