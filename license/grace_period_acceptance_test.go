package license

import (
	"testing"
	"time"
)

func TestGracePeriodBoundariesAreExact(t *testing.T) {
	validUntil := time.Date(2026, 12, 1, 23, 59, 59, 0, time.UTC)
	d := testDoc(time.Date(2026, 8, 21, 15, 21, 45, 0, time.UTC))
	d.ValidUntil = validUntil
	d.GracePeriodDays = 30

	graceEnds := validUntil.AddDate(0, 0, 30)
	if got := Evaluate(d, validUntil, time.Time{}, false).GraceUntil; !got.Equal(graceEnds) {
		t.Fatalf("grace_ends_at: want %s got %s", graceEnds, got)
	}

	for _, c := range []struct {
		name string
		now  time.Time
		want State
	}{
		// Expiry is inclusive; a strict Before check would end access one second early.
		{"one second before valid_until", validUntil.Add(-time.Second), StateActive},
		{"exactly at valid_until", validUntil, StateActive},
		{"one second after valid_until", validUntil.Add(time.Second), StateGracePeriod},

		{"one day into grace", validUntil.AddDate(0, 0, 1), StateGracePeriod},
		{"one second before grace ends", graceEnds.Add(-time.Second), StateGracePeriod},
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
		// Guests retain the same access throughout renewal grace.
		{"first second of grace", validUntil.Add(time.Second), true, true, true},
		{"last second of grace", graceEnds, true, true, true},
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
		if FeatureEnabled(st, false) {
			t.Errorf("%s: an unentitled feature must stay off (state %s)", c.name, st)
		}
	}
}

// Cloud silence must not shorten the signed renewal grace period.
func TestCloudStalenessIsNotLicenceExpiry(t *testing.T) {
	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	d := testDoc(issued)
	d.ValidUntil = issued.AddDate(1, 0, 0)
	d.GracePeriodDays = 7
	d.OfflineGraceDays = 30

	if ev := Evaluate(d, d.ValidUntil.AddDate(0, 0, 9), time.Time{}, false); ev.State != StateExpired {
		t.Errorf("9 days past expiry with a 7-day grace: want Expired got %s", ev.State)
	}
	if ev := Evaluate(d, d.ValidUntil.AddDate(0, 0, 6), time.Time{}, false); ev.State != StateGracePeriod {
		t.Errorf("6 days past expiry with a 7-day grace: want GracePeriod got %s", ev.State)
	}
	if got := Evaluate(d, d.ValidUntil, time.Time{}, false).GraceUntil; !got.Equal(d.ValidUntil.AddDate(0, 0, 7)) {
		t.Errorf("grace_ends_at must derive from grace_period_days, got %s", got)
	}

	deepOffline := Evaluate(d, issued.AddDate(0, 6, 0), issued.AddDate(0, 6, 0).AddDate(0, 0, -100), false)
	if deepOffline.State != StateActive {
		t.Fatalf("100 days offline on a valid licence: want Active got %s", deepOffline.State)
	}
	if !deepOffline.CloudStale {
		t.Error("100 days offline must raise the CloudStale warning")
	}

	quiet := Evaluate(d, issued.AddDate(0, 6, 0), issued.AddDate(0, 6, 0).AddDate(0, 0, -20), false)
	if quiet.CloudStale {
		t.Error("20 days offline with a 30-day allowance must not warn")
	}
	if quiet.State != StateActive {
		t.Errorf("want Active got %s", quiet.State)
	}
}

func TestRenewalDuringGraceRestoresActiveAndSurvivesRestart(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	store := NewStore(dir, NewVerifier(s.PublicKey()))

	issued := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	original := testDoc(issued)
	original.ValidUntil = issued.AddDate(1, 0, 0)
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

	if _, err := reopened.Install(raw, inGrace.Add(2*time.Hour)); err == nil {
		t.Fatal("re-installing the superseded original must be rejected")
	}
}

// Clock rollback uses persisted high-water time so expiry cannot be reversed.
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

	wellPast := d.ValidUntil.AddDate(0, 0, 60)
	if ev, _ := store.Evaluate(wellPast); ev.State != StateExpired {
		t.Fatalf("past grace: want Expired got %s", ev.State)
	}

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

	// NTP corrections within tolerance must not trigger rollback protection.
	normal, err := store.Evaluate(wellPast.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if normal.ClockRollback {
		t.Error("an hour of NTP correction must not be reported as a clock rollback")
	}
}
