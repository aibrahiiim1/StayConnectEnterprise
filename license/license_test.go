package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func testDoc(issued time.Time) *Document {
	return &Document{
		LicenseID:          "11111111-1111-1111-1111-111111111111",
		TenantID:           "22222222-2222-2222-2222-222222222222",
		SiteID:             "33333333-3333-3333-3333-333333333333",
		ApplianceIDs:       []string{"44444444-4444-4444-4444-444444444444"},
		CommercialPlanCode: "enterprise",
		Status:             DocActive,
		IssuedAt:           issued,
		ValidUntil:         issued.AddDate(1, 0, 0),
		OfflineGraceDays:   30,
		Features:           Features{PMS: true, PaidWiFi: true, EmailOTP: true},
		Limits:             Limits{MaxConcurrentGuestSessions: 1500, MaxLocalOperators: 20},
		SchemaVersion:      CurrentSchemaVersion,
	}
}

func newSigner(t *testing.T) *Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return NewSigner(priv)
}

func TestSignVerifyRoundTrip(t *testing.T) {
	s := newSigner(t)
	v := NewVerifier(s.PublicKey())
	doc := testDoc(time.Now().UTC())

	env, err := s.Sign(doc)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	got, err := v.Verify(env)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.LicenseID != doc.LicenseID || !got.Features.PMS || got.Limits.MaxConcurrentGuestSessions != 1500 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestVerifyRejectsTamperedPayload(t *testing.T) {
	s := newSigner(t)
	v := NewVerifier(s.PublicKey())
	env, _ := s.Sign(testDoc(time.Now().UTC()))

	// Flip the plan code inside the payload.
	payload, _ := base64.StdEncoding.DecodeString(env.PayloadB64)
	tampered := []byte(string(payload))
	for i := range tampered {
		if tampered[i] == 'e' { // enterprise -> Xnterprise (any byte flip works)
			tampered[i] = 'X'
			break
		}
	}
	env.PayloadB64 = base64.StdEncoding.EncodeToString(tampered)
	if _, err := v.Verify(env); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("want ErrBadSignature, got %v", err)
	}
}

func TestVerifyRejectsUnknownKey(t *testing.T) {
	s1, s2 := newSigner(t), newSigner(t)
	v := NewVerifier(s2.PublicKey())
	env, _ := s1.Sign(testDoc(time.Now().UTC()))
	if _, err := v.Verify(env); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}
}

func TestSignRefusesInvalidDoc(t *testing.T) {
	s := newSigner(t)
	d := testDoc(time.Now().UTC())
	d.SiteID = ""
	if _, err := s.Sign(d); err == nil {
		t.Fatal("want error for missing site_id")
	}
	d2 := testDoc(time.Now().UTC())
	d2.SchemaVersion = 99
	if _, err := s.Sign(d2); err == nil {
		t.Fatal("want error for unknown schema version")
	}
}

func TestEvaluateTimeline(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// Simple model (v3): Active → Grace(grace_period_days) → Expired; there
	// is no Restricted window.
	d := testDoc(issued) // valid until 2027-01-01
	d.GracePeriodDays = 30
	cases := []struct {
		now  time.Time
		want State
	}{
		{issued.AddDate(0, 6, 0), StateActive},
		{d.ValidUntil.Add(-time.Hour), StateActive},
		{d.ValidUntil.Add(time.Hour), StateGracePeriod},
		{d.ValidUntil.AddDate(0, 0, 29), StateGracePeriod},
		{d.ValidUntil.AddDate(0, 0, 31), StateExpired},
		{d.ValidUntil.AddDate(0, 0, 61), StateExpired},
	}
	for _, c := range cases {
		ev := Evaluate(d, c.now, time.Time{}, false)
		if ev.State != c.want {
			t.Errorf("v3 at %s: want %s got %s", c.now, c.want, ev.State)
		}
	}

	// Legacy documents (schema v2) keep the historical Restricted window.
	l := testDoc(issued)
	l.SchemaVersion = 2
	legacy := []struct {
		now  time.Time
		want State
	}{
		{l.ValidUntil.AddDate(0, 0, 29), StateGracePeriod},
		{l.ValidUntil.AddDate(0, 0, 31), StateRestricted},
		{l.ValidUntil.AddDate(0, 0, 59), StateRestricted},
		{l.ValidUntil.AddDate(0, 0, 61), StateExpired},
	}
	for _, c := range legacy {
		ev := Evaluate(l, c.now, time.Time{}, false)
		if ev.State != c.want {
			t.Errorf("legacy at %s: want %s got %s", c.now, c.want, ev.State)
		}
	}

	// ValidFrom in the future → not yet valid → no new authorization.
	f := testDoc(issued)
	f.ValidFrom = issued.AddDate(0, 1, 0)
	if ev := Evaluate(f, issued.AddDate(0, 0, 15), time.Time{}, false); ev.State != StateExpired {
		t.Errorf("pre-valid_from: want Expired got %s", ev.State)
	}
	// Suspended and Revoked override the timeline.
	d.Status = DocSuspended
	if ev := Evaluate(d, issued.AddDate(0, 6, 0), time.Time{}, false); ev.State != StateSuspended {
		t.Errorf("suspended: got %s", ev.State)
	}
	d.Status = DocActive
	if ev := Evaluate(d, issued.AddDate(0, 6, 0), time.Time{}, true); ev.State != StateRevoked {
		t.Errorf("revoked: got %s", ev.State)
	}
}

func TestStateBehavior(t *testing.T) {
	if !StateActive.AllowsNewSessions() || !StateGracePeriod.AllowsNewSessions() {
		t.Fatal("Active/Grace must allow new sessions")
	}
	if !StateRestricted.AllowsNewSessions() {
		t.Fatal("legacy Restricted keeps basic guest access alive")
	}
	// Final simple model: Suspended blocks NEW authorization (existing
	// sessions run out naturally; portal/DHCP/DNS/admin stay up).
	if StateSuspended.AllowsNewSessions() {
		t.Fatal("Suspended must refuse new sessions")
	}
	if StateExpired.AllowsNewSessions() || StateRevoked.AllowsNewSessions() {
		t.Fatal("Expired/Revoked must refuse new sessions")
	}
	if StateRestricted.AllowsProvisioning() {
		t.Fatal("Restricted must block provisioning")
	}
	if !FeatureEnabled(StateGracePeriod, true) || FeatureEnabled(StateRestricted, true) || FeatureEnabled(StateActive, false) {
		t.Fatal("feature gating wrong")
	}
}

func TestStoreInstallEvaluateAndRollback(t *testing.T) {
	s := newSigner(t)
	v := NewVerifier(s.PublicKey())
	dir := t.TempDir()
	store := NewStore(dir, v)
	now := time.Now().UTC()

	envNew, _ := s.Sign(testDoc(now))
	rawNew, _ := envNew.Encode()
	if _, err := store.Install(rawNew, now); err != nil {
		t.Fatalf("install: %v", err)
	}
	ev, err := store.Evaluate(now.Add(time.Hour))
	if err != nil || ev.State != StateActive {
		t.Fatalf("evaluate: %v %s", err, ev.State)
	}

	// Rollback: installing an envelope with an OLDER issued_at must fail.
	envOld, _ := s.Sign(testDoc(now.AddDate(0, -6, 0)))
	rawOld, _ := envOld.Encode()
	if _, err := store.Install(rawOld, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("want ErrRollback, got %v", err)
	}

	// Re-installing the same (equal issued_at) document is fine (idempotent).
	if _, err := store.Install(rawNew, now); err != nil {
		t.Fatalf("idempotent reinstall: %v", err)
	}
}

func TestStoreClockRollbackProtection(t *testing.T) {
	s := newSigner(t)
	store := NewStore(t.TempDir(), NewVerifier(s.PublicKey()))
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	env, _ := s.Sign(testDoc(issued)) // valid until 2027-01-01

	// Appliance has observed time well past expiry+2×grace.
	seen := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	raw, _ := env.Encode()
	if _, err := store.Install(raw, seen); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Evaluate(seen); err != nil {
		t.Fatal(err)
	}
	// Clock set back to mid-validity: evaluation must use the high-water
	// mark, not the rolled-back clock.
	rolled := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	ev, err := store.Evaluate(rolled)
	if err != nil {
		t.Fatal(err)
	}
	if !ev.ClockRollback {
		t.Fatal("expected ClockRollback flag")
	}
	if ev.State == StateActive {
		t.Fatalf("rolled-back clock must not resurrect an expired license, got %s", ev.State)
	}
}

func TestStoreRevocation(t *testing.T) {
	s := newSigner(t)
	store := NewStore(t.TempDir(), NewVerifier(s.PublicKey()))
	now := time.Now().UTC()
	doc := testDoc(now)
	env, _ := s.Sign(doc)
	raw, _ := env.Encode()
	if _, err := store.Install(raw, now); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRevocation(doc.LicenseID); err != nil {
		t.Fatal(err)
	}
	ev, err := store.Evaluate(now)
	if err != nil {
		t.Fatal(err)
	}
	if ev.State != StateRevoked {
		t.Fatalf("want Revoked, got %s", ev.State)
	}
}

func TestCloudStaleWarning(t *testing.T) {
	now := time.Now().UTC()
	d := testDoc(now)
	ev := Evaluate(d, now, now.AddDate(0, 0, -40), false) // grace 30d, last validation 40d ago
	if ev.State != StateActive {
		t.Fatalf("doc valid: state must stay Active, got %s", ev.State)
	}
	if !ev.CloudStale {
		t.Fatal("want CloudStale warning")
	}
}

// TestVersionAntiRollback proves the monotonic license_version rules: an older
// version, a superseded document, a different document at the same version, and
// a revoked document are all rejected — even with a valid signature and
// unexpired dates — while re-importing the byte-identical current document
// stays idempotent. State survives a Store reopen (restart/reboot).
func TestVersionAntiRollback(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))
	now := time.Now().UTC()

	mk := func(ver int64, id string, maxGuests int) []byte {
		d := testDoc(now.Add(-time.Hour))
		d.LicenseID = id
		d.LicenseVersion = ver
		d.MaxConcurrentOnlineGuests = maxGuests
		d.GracePeriodDays = 7
		env, err := s.Sign(d)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := env.Encode()
		return raw
	}
	v1 := mk(1, "aaaaaaaa-0000-0000-0000-000000000001", 100)
	v2 := mk(2, "aaaaaaaa-0000-0000-0000-000000000002", 150)
	v3 := mk(3, "aaaaaaaa-0000-0000-0000-000000000003", 50)

	if _, err := store.Install(v1, now); err != nil {
		t.Fatalf("v1 install: %v", err)
	}
	if _, err := store.Install(v2, now); err != nil {
		t.Fatalf("v2 install: %v", err)
	}
	// Replay v1 (older version, higher validity still fine) → rejected.
	if _, err := store.Install(v1, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("v1 replay: want ErrRollback got %v", err)
	}
	// v3 lowers the limit — must be accepted (higher version wins).
	if _, err := store.Install(v3, now); err != nil {
		t.Fatalf("v3 install: %v", err)
	}
	// Replay v2 with the HIGHER limit → rejected.
	if _, err := store.Install(v2, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("v2 replay after limit reduction: want ErrRollback got %v", err)
	}
	// Idempotent re-import of the current document is allowed.
	if _, err := store.Install(v3, now); err != nil {
		t.Fatalf("idempotent v3 re-import: %v", err)
	}
	// A DIFFERENT document at the already-accepted version → rejected.
	forged := mk(3, "aaaaaaaa-0000-0000-0000-00000000000f", 9999)
	if _, err := store.Install(forged, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("same-version different-doc: want ErrRollback got %v", err)
	}
	// Store reopen (scd restart / reboot): protection persists.
	store2 := NewStore(dir, NewVerifier(s.PublicKey()))
	if _, err := store2.Install(v1, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("v1 replay after reopen: want ErrRollback got %v", err)
	}
	// Revoked current document can never be re-installed.
	if err := store2.AddRevocation("aaaaaaaa-0000-0000-0000-000000000003"); err != nil {
		t.Fatal(err)
	}
	if _, err := store2.Install(v3, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("revoked re-install: want ErrRollback got %v", err)
	}
	// Legacy unversioned (v0) document after a versioned one → rejected.
	legacy := mk(0, "aaaaaaaa-0000-0000-0000-000000000000", 0)
	if _, err := store2.Install(legacy, now); !errors.Is(err, ErrRollback) {
		t.Fatalf("legacy unversioned after versioned: want ErrRollback got %v", err)
	}
}
