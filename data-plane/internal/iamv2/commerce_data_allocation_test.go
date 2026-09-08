package iamv2

// THE APPROVED PER-STAY-NIGHT ALLOWANCE.
//
// The worked example the Product Owner approved is 1 GB a night, a 5 GB floor and a 20 GB ceiling. Every
// number in it is asserted below, because the clamp order is the part that is easy to get subtly wrong: the
// floor lifts short stays and the ceiling caps long ones, and swapping them turns a 25-night stay into 25 GB.

import (
	"errors"
	"testing"
	"time"
)

func stayOfNights(n int) EligibilitySubject {
	arrival := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	departure := arrival.AddDate(0, 0, n)
	return EligibilitySubject{Stay: &StayEvidence{Arrival: &arrival, Departure: &departure}}
}

func approvedPolicy(t *testing.T) DataAllocationPolicy {
	t.Helper()
	max := 20.0
	return DataAllocationPolicy{Mode: AllocPerStayNight, GBPerNight: 1, MinGB: 5, MaxGB: &max}
}

func TestApprovedPerStayNightExample(t *testing.T) {
	p := approvedPolicy(t)
	for _, tc := range []struct{ nights, gb int64 }{
		{2, 5},   // below the floor -> the floor
		{5, 5},   // exactly the floor
		{8, 8},   // between floor and ceiling -> the calculation
		{10, 10}, //
		{12, 12}, //
		{25, 20}, // above the ceiling -> the ceiling
	} {
		got, snap, err := EffectiveDataQuotaBytes(p, stayOfNights(int(tc.nights)))
		if err != nil {
			t.Fatalf("%d nights: %v", tc.nights, err)
		}
		if !snap {
			t.Fatalf("%d nights: PER_STAY_NIGHT must produce a snapshot", tc.nights)
		}
		if want := tc.gb * bytesPerGB; got != want {
			t.Errorf("%d nights: got %d bytes (%.1f GB), want %d (%d GB)",
				tc.nights, got, float64(got)/bytesPerGB, want, tc.gb)
		}
	}
}

func TestFixedIsUnchangedAndSnapshotsNothing(t *testing.T) {
	// A FIXED package must not write a per-entitlement quota at all: the entitlement goes on reading the
	// pinned plan revision, which is what every entitlement granted before this existed does.
	for _, p := range []DataAllocationPolicy{{Mode: AllocFixed}, {}} {
		b, snap, err := EffectiveDataQuotaBytes(p, stayOfNights(9))
		if err != nil || snap || b != 0 {
			t.Fatalf("FIXED produced bytes=%d snapshot=%v err=%v; want 0,false,nil", b, snap, err)
		}
	}
	// ...and an absent policy IS fixed, which is what every existing revision carries.
	got, err := ParseDataAllocationPolicy(nil)
	if err != nil || got.Mode != AllocFixed {
		t.Fatalf("an absent policy must read as FIXED, got %+v (%v)", got, err)
	}
}

func TestPerStayNightFailsClosedWithoutAuthoritativeDates(t *testing.T) {
	p := approvedPolicy(t)
	arrival := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	departure := arrival.AddDate(0, 0, 4)

	cases := map[string]EligibilitySubject{
		"no stay at all":     {},
		"no stay evidence":   {Stay: nil},
		"missing arrival":    {Stay: &StayEvidence{Departure: &departure}},
		"missing departure":  {Stay: &StayEvidence{Arrival: &arrival}},
		"departure precedes": {Stay: &StayEvidence{Arrival: &departure, Departure: &arrival}},
	}
	for name, s := range cases {
		_, snap, err := EffectiveDataQuotaBytes(p, s)
		if err == nil {
			t.Errorf("%s: was granted an allowance instead of refusing", name)
		}
		if !errors.Is(err, ErrStayLengthUnknown) {
			t.Errorf("%s: refused with %v, want ErrStayLengthUnknown", name, err)
		}
		if snap {
			t.Errorf("%s: refused but still claimed a snapshot", name)
		}
	}
}

func TestClampEdges(t *testing.T) {
	max := 20.0
	// A zero-night (same-day) stay is a known length, not a missing one, so it gets the floor rather than a
	// refusal -- and never a zero-byte grant, which would be exhausted the moment it was created.
	b, _, err := EffectiveDataQuotaBytes(
		DataAllocationPolicy{Mode: AllocPerStayNight, GBPerNight: 1, MinGB: 5, MaxGB: &max}, stayOfNights(0))
	if err != nil || b != 5*bytesPerGB {
		t.Fatalf("a same-day stay got %d bytes (%v); want the 5 GB floor", b, err)
	}
	// No ceiling configured: the calculation runs on.
	b, _, err = EffectiveDataQuotaBytes(
		DataAllocationPolicy{Mode: AllocPerStayNight, GBPerNight: 2, MinGB: 5}, stayOfNights(30))
	if err != nil || b != 60*bytesPerGB {
		t.Fatalf("with no ceiling, 30 nights x 2 GB got %d bytes (%v); want 60 GB", b, err)
	}
	// Fractional per-night allowances round once, at the end.
	b, _, err = EffectiveDataQuotaBytes(
		DataAllocationPolicy{Mode: AllocPerStayNight, GBPerNight: 0.1}, stayOfNights(7))
	if err != nil || b != 700_000_000 {
		t.Fatalf("7 nights x 0.1 GB got %d bytes (%v); want 700000000", b, err)
	}
	// An absurd stay length is clamped rather than overflowed into a negative quota.
	b, _, err = EffectiveDataQuotaBytes(
		DataAllocationPolicy{Mode: AllocPerStayNight, GBPerNight: 1000, MaxGB: &max}, stayOfNights(99999))
	if err != nil || b != 20*bytesPerGB {
		t.Fatalf("an absurd stay got %d bytes (%v); want the ceiling", b, err)
	}
}

func TestPolicyParsing(t *testing.T) {
	p, err := ParseDataAllocationPolicy(map[string]any{
		"mode": AllocPerStayNight, "gb_per_night": 1.0, "min_gb": 5.0, "max_gb": 20.0,
	})
	if err != nil {
		t.Fatalf("the approved policy was refused: %v", err)
	}
	if p.GBPerNight != 1 || p.MinGB != 5 || p.MaxGB == nil || *p.MaxGB != 20 {
		t.Fatalf("parsed as %+v", p)
	}

	bad := map[string]map[string]any{
		"unknown mode":          {"mode": "PER_GUEST_MOOD"},
		"no per-night rate":     {"mode": AllocPerStayNight},
		"zero per-night rate":   {"mode": AllocPerStayNight, "gb_per_night": 0.0},
		"negative rate":         {"mode": AllocPerStayNight, "gb_per_night": -1.0},
		"ceiling below floor":   {"mode": AllocPerStayNight, "gb_per_night": 1.0, "min_gb": 10.0, "max_gb": 5.0},
		"zero ceiling":          {"mode": AllocPerStayNight, "gb_per_night": 1.0, "max_gb": 0.0},
		"non-numeric per-night": {"mode": AllocPerStayNight, "gb_per_night": "lots"},
	}
	for name, raw := range bad {
		if _, err := ParseDataAllocationPolicy(raw); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
