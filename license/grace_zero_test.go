package license

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

// X-4: a v3+ licence signed with grace_period_days = 0 has NO grace. It used to fall back to the 30-day
// offline allowance, so "no grace" silently meant a month.
func TestGraceZeroIsHonouredOnASignedLicence(t *testing.T) {
	s := newSigner(t)
	issued := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	d := testDoc(issued)
	d.ValidUntil = time.Date(2026, 12, 1, 23, 59, 59, 0, time.UTC)
	d.OfflineGraceDays = 30
	d.GracePeriodDays = 0
	env, err := s.Sign(d)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := base64.StdEncoding.DecodeString(env.PayloadB64)
	if err != nil || !strings.Contains(string(payload), `"valid_until"`) {
		t.Fatalf("payload not decodable: %v", err)
	}
	if strings.Contains(string(payload), `"grace_period_days"`) {
		t.Fatal("precondition: a zero grace is omitted from the signed bytes (omitempty)")
	}
	got, err := NewVerifier(s.PublicKey()).Verify(env)
	if err != nil {
		t.Fatal(err)
	}
	if g := got.EffectiveGraceDays(); g != 0 {
		t.Fatalf("v%d licence with grace 0 must have no grace, got %d days", got.SchemaVersion, g)
	}
	if st := Evaluate(got, d.ValidUntil, time.Time{}, false).State; st != StateActive {
		t.Fatalf("at valid_until: %s", st)
	}
	if st := Evaluate(got, d.ValidUntil.Add(time.Second), time.Time{}, false).State; st != StateExpired {
		t.Fatalf("one second after valid_until with no grace: want Expired, got %s", st)
	}
	// The offline allowance keeps its own meaning: cloud staleness, never the renewal grace.
	if ev := Evaluate(got, d.ValidUntil.Add(-time.Hour), d.ValidUntil.Add(-40*24*time.Hour), false); !ev.CloudStale || ev.State != StateActive {
		t.Fatalf("offline_grace_days still bounds staleness only: %+v", ev)
	}
}

// Licences already in the field keep evaluating exactly as before: an explicit grace is unchanged, and a
// v1/v2 document (which never carried a grace period) still uses the offline allowance.
func TestGraceCompatibilityForExistingLicences(t *testing.T) {
	d := testDoc(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	d.GracePeriodDays, d.OfflineGraceDays = 30, 30
	if g := d.EffectiveGraceDays(); g != 30 {
		t.Fatalf("explicit grace: %d", g)
	}
	for _, v := range []int{1, 2} {
		legacy := testDoc(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
		legacy.SchemaVersion, legacy.GracePeriodDays, legacy.OfflineGraceDays = v, 0, 21
		if g := legacy.EffectiveGraceDays(); g != 21 {
			t.Fatalf("v%d document must keep the offline grace as its renewal grace, got %d", v, g)
		}
	}
}
