package license

import (
	"testing"
	"time"
)

// THE GRACE PERIOD, PROVEN AT THE INSTANT IT TURNS.
//
// The existing timeline test walks the lifecycle in hours and days, which proves the shape but not the
// edges: it asks what happens an hour before expiry and a day either side of the grace end, and never asks
// what happens AT valid_until or AT grace_ends_at. Those two instants are exactly where an off-by-one lives,
// and they are the two a hotel would actually feel -- a property that goes dark one second early, or keeps
// authorizing one day late, is the whole failure mode this window exists to prevent.
//
// The numbers here are the ones on the appliance's real signed licence (grace_period_days = 30,
// valid_until = 2026-12-01T23:59:59Z), so the boundaries asserted below are the boundaries that property
// will actually cross, not a convenient round number.
func TestGracePeriodBoundariesAreExact(t *testing.T) {
	validUntil := time.Date(2026, 12, 1, 23, 59, 59, 0, time.UTC)
	d := testDoc(time.Date(2026, 8, 21, 15, 21, 45, 0, time.UTC))
	d.ValidUntil = validUntil
	d.GracePeriodDays = 30

	graceEnds := validUntil.AddDate(0, 0, 30) // 2026-12-31T23:59:59Z
	if got := Evaluate(d, validUntil, time.Time{}, false).GraceUntil; !got.Equal(graceEnds) {
		t.Fatalf("grace_ends_at: want %s got %s", graceEnds, got)
	}

	for _, c := range []struct {
		name string
		now  time.Time
		want State
	}{
		// THE LAST INSTANT OF VALIDITY IS STILL VALID. `!now.After(ValidUntil)` makes valid_until itself
		// Active; a strict `Before` would have ended the licence a second early, every time.
		{"one second before valid_until", validUntil.Add(-time.Second), StateActive},
		{"exactly at valid_until", validUntil, StateActive},
		{"one second after valid_until", validUntil.Add(time.Second), StateGracePeriod},

		{"one day into grace", validUntil.AddDate(0, 0, 1), StateGracePeriod},
		{"one second before grace ends", graceEnds.Add(-time.Second), StateGracePeriod},
		// THE LAST INSTANT OF GRACE IS STILL GRACE, by the same rule and for the same reason.
		{"exactly at grace_ends_at", graceEnds, StateGracePeriod},
		{"one second after grace ends", graceEnds.Add(time.Second), StateExpired},

		{"a month past grace", graceEnds.AddDate(0, 1, 0), StateExpired},
	} {
		ev := Evaluate(d, c.now, time.Time{}, false)
		if ev.State != c.want {
			t.Errorf("%s (%s): want %s got %s", c.name, c.now.Format(time.RFC3339), c.want, ev.State)
		}
	}
}

// WHAT A HOTEL MAY DO IN EACH STATE, asserted from the EVALUATED state rather than the enum.
//
// TestStateBehavior already pins the enum's answers. This asks the question the way the running appliance
// does -- evaluate a real document at a real instant, then ask that result what is allowed -- because the
// defect that would matter is not "Grace allows sessions" being wrong in a switch, it is the timeline
// handing back the wrong state at 00:00:01 and every downstream answer being correct about the wrong thing.
func TestWhatIsAllowedAtEachBoundary(t *testing.T) {
	validUntil := time.Date(2026, 12, 1, 23, 59, 59, 0, time.UTC)
	d := testDoc(time.Date(2026, 8, 21, 15, 21, 45, 0, time.UTC))
	d.ValidUntil = validUntil
	d.GracePeriodDays = 30
	graceEnds := validUntil.AddDate(0, 0, 30)

	for _, c := range []struct {
		name                            string
		now                             time.Time
		newSessions, provision, feature bool
	}{
		{"active, the second before expiry", validUntil, true, true, true},
		// GRACE IS DELIBERATELY INDISTINGUISHABLE TO A GUEST. Nothing a guest or a receptionist can do
		// changes at the moment of expiry; that is the entire point of the window.
		{"first second of grace", validUntil.Add(time.Second), true, true, true},
		{"last second of grace", graceEnds, true, true, true},
		// AND AT EXPIRY ONLY *NEW* AUTHORIZATION STOPS.
		{"first second of expiry", graceEnds.Add(time.Second), false, false, false},
	} {
		st := Evaluate(d, c.now, time.Time{}, false).State
		if got := st.AllowsNewSessions(); got != c.newSessions {
			t.Errorf("%s: AllowsNewSessions want %v got %v (state %s)", c.name, c.newSessions, got, st)
		}
		if got := st.AllowsProvisioning(); got != c.provision {
			t.Errorf("%s: AllowsProvisioning want %v got %v (state %s)", c.name, c.provision, got, st)
		}
		if got := FeatureEnabled(st, true); got != c.feature {
			t.Errorf("%s: FeatureEnabled(entitled) want %v got %v (state %s)", c.name, c.feature, got, st)
		}
		// An unentitled feature is off in EVERY state, including Active. Grace is not a promotion.
		if FeatureEnabled(st, false) {
			t.Errorf("%s: an unentitled feature must stay off (state %s)", c.name, st)
		}
	}
}

// THE RENEWAL GRACE AND THE OFFLINE ALLOWANCE ARE TWO DIFFERENT NUMBERS.
//
// On the appliance's real licence they are both 30, which makes them indistinguishable in production and is
// precisely why this test sets them apart. They answer different questions: grace_period_days is how long a
// hotel keeps working after its licence lapses, and offline_grace_days is how long the appliance may go
// without reaching the cloud before somebody should be told. Conflating them would mean a hotel that loses
// its internet connection watches its licence "expire" while the signed document on disk is still valid for
// months -- a self-inflicted outage caused by a network fault.
func TestCloudStalenessIsNotLicenceExpiry(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := testDoc(issued)
	d.ValidUntil = issued.AddDate(1, 0, 0)
	d.GracePeriodDays = 7  // the renewal window
	d.OfflineGraceDays = 30 // the cloud-silence allowance

	// THE STATE FOLLOWS grace_period_days, not the offline allowance. Nine days past expiry is beyond the
	// 7-day renewal window and well inside the 30-day offline one; if the wrong field drove the timeline
	// this would still read GracePeriod.
	if ev := Evaluate(d, d.ValidUntil.AddDate(0, 0, 9), time.Time{}, false); ev.State != StateExpired {
		t.Errorf("9 days past expiry with a 7-day grace: want Expired got %s", ev.State)
	}
	if ev := Evaluate(d, d.ValidUntil.AddDate(0, 0, 6), time.Time{}, false); ev.State != StateGracePeriod {
		t.Errorf("6 days past expiry with a 7-day grace: want GracePeriod got %s", ev.State)
	}
	if got := Evaluate(d, d.ValidUntil, time.Time{}, false).GraceUntil; !got.Equal(d.ValidUntil.AddDate(0, 0, 7)) {
		t.Errorf("grace_ends_at must derive from grace_period_days, got %s", got)
	}

	// AND CLOUD SILENCE NEVER MOVES THE STATE. A hotel six months into a valid licence that has not reached
	// the control plane for 100 days is Active-with-a-warning, not expired.
	deepOffline := Evaluate(d, issued.AddDate(0, 6, 0), issued.AddDate(0, 6, 0).AddDate(0, 0, -100), false)
	if deepOffline.State != StateActive {
		t.Fatalf("100 days offline on a valid licence: want Active got %s", deepOffline.State)
	}
	if !deepOffline.CloudStale {
		t.Error("100 days offline must raise the CloudStale warning")
	}

	// The warning is keyed to the OFFLINE allowance: 20 days of silence is inside 30 and must not warn,
	// even though it is far outside the 7-day renewal grace.
	quiet := Evaluate(d, issued.AddDate(0, 6, 0), issued.AddDate(0, 6, 0).AddDate(0, 0, -20), false)
	if quiet.CloudStale {
		t.Error("20 days offline with a 30-day allowance must not warn")
	}
	if quiet.State != StateActive {
		t.Errorf("want Active got %s", quiet.State)
	}
}

// RENEWING DURING GRACE PUTS THE HOTEL BACK TO ACTIVE, and the renewed licence survives a restart.
//
// This is the path a real renewal takes: the appliance is already past valid_until and running on grace when
// a new document arrives. It goes through the Store rather than Evaluate directly, because that is what the
// appliance does -- the anti-rollback rules, the on-disk state and the reload after a restart are all part of
// whether a renewal actually sticks.
func TestRenewalDuringGraceRestoresActiveAndSurvivesRestart(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))

	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	original := testDoc(issued)
	original.ValidUntil = issued.AddDate(1, 0, 0) // 2027-01-01
	original.GracePeriodDays = 30
	original.LicenseVersion = 1
	env, err := s.Sign(original)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(raw, issued); err != nil {
		t.Fatalf("install original: %v", err)
	}

	// Ten days past expiry: in grace, still serving guests.
	inGrace := original.ValidUntil.AddDate(0, 0, 10)
	ev, err := store.Evaluate(inGrace)
	if err != nil {
		t.Fatal(err)
	}
	if ev.State != StateGracePeriod {
		t.Fatalf("before renewal: want GracePeriod got %s", ev.State)
	}
	if !ev.State.AllowsNewSessions() {
		t.Fatal("grace must still authorize guests, or the window is pointless")
	}

	// THE RENEWAL ARRIVES. Higher version, later issue date, extended validity.
	renewed := testDoc(inGrace)
	renewed.LicenseID = original.LicenseID
	renewed.ValidUntil = inGrace.AddDate(1, 0, 0)
	renewed.GracePeriodDays = 30
	renewed.LicenseVersion = 2
	renewEnv, err := s.Sign(renewed)
	if err != nil {
		t.Fatal(err)
	}
	rrraw, err := renewEnv.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(rrraw, inGrace); err != nil {
		t.Fatalf("install renewal: %v", err)
	}

	ev, err = store.Evaluate(inGrace.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if ev.State != StateActive {
		t.Fatalf("after renewal: want Active got %s", ev.State)
	}

	// RESTART. A fresh Store over the same directory is exactly what a reboot produces, and the renewal must
	// still be in force -- a licence that only holds until the next power cut has not been installed.
	reopened := NewStore(dir, NewVerifier(s.PublicKey()))
	ev, err = reopened.Evaluate(inGrace.Add(time.Hour))
	if err != nil {
		t.Fatalf("after restart: %v", err)
	}
	if ev.State != StateActive {
		t.Fatalf("after restart: want Active got %s", ev.State)
	}
	if ev.Doc.LicenseVersion != 2 {
		t.Fatalf("after restart: want the renewed document (v2), got v%d", ev.Doc.LicenseVersion)
	}

	// AND THE SUPERSEDED DOCUMENT CANNOT COME BACK, which is what makes the renewal meaningful: without
	// this, anybody who kept the old file could reinstate the old expiry date.
	if _, err := reopened.Install(raw, inGrace.Add(2*time.Hour)); err == nil {
		t.Fatal("re-installing the superseded original must be rejected")
	}
}

// A CLOCK PUSHED BACKWARDS DOES NOT BUY A HOTEL MORE LICENCE.
//
// The store remembers the furthest point in time it has ever seen. Winding the appliance clock back past the
// tolerance does not return an expired licence to grace: the evaluation uses the high-water mark instead and
// says so. This is asserted through the real Store because the protection lives there, not in Evaluate.
func TestClockRollbackCannotResurrectAnExpiredLicence(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))

	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := testDoc(issued)
	d.ValidUntil = issued.AddDate(1, 0, 0)
	d.GracePeriodDays = 30
	env, err := s.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := env.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Install(raw, issued); err != nil {
		t.Fatal(err)
	}

	// Let the appliance observe a time well past the end of grace: it is Expired and the high-water mark
	// now records that instant.
	wellPast := d.ValidUntil.AddDate(0, 0, 60)
	if ev, _ := store.Evaluate(wellPast); ev.State != StateExpired {
		t.Fatalf("past grace: want Expired got %s", ev.State)
	}

	// Now wind the clock back into the grace window and ask again.
	rolled, err := store.Evaluate(d.ValidUntil.AddDate(0, 0, 5))
	if err != nil {
		t.Fatal(err)
	}
	if rolled.State != StateExpired {
		t.Fatalf("clock rolled back into grace: want Expired got %s", rolled.State)
	}
	if !rolled.ClockRollback {
		t.Error("the rollback must be reported, not silently absorbed -- an operator needs to know the clock is wrong")
	}

	// A SMALL BACKWARD STEP IS NOT AN ATTACK. NTP corrections of a few seconds or hours are normal and must
	// not raise the flag, or it would cry wolf on every healthy appliance.
	normal, err := store.Evaluate(wellPast.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if normal.ClockRollback {
		t.Error("an hour of NTP correction must not be reported as a clock rollback")
	}
}
