package iamv2

// HOW MANY BYTES A PACKAGE GRANTS.
//
// Until now the answer was always "whatever the pinned Service Plan revision says", which is a fine answer for
// a package sold as a fixed allowance and a poor one for a hotel: a guest staying two nights and a guest
// staying three weeks are handed the same 5 GB, so the short stay is generous and the long one runs dry on day
// four. PER_STAY_NIGHT scales the allowance with the length of the stay, between a floor and a ceiling.
//
// THE PART THAT MATTERS MOST IS NOT THE ARITHMETIC. It is that the result is computed ONCE, at grant time,
// from Stay evidence that was authoritative at that moment, and then frozen. A Stay that is later extended,
// shortened, corrected or re-synced from the PMS does not reach back and change what a guest was already
// given. That is the same immutability the package and plan revisions already have, applied to a number that
// is now derived rather than stored.
//
// And when the stay length cannot be established, this refuses. A missing arrival or departure is not "assume
// one night" and not "assume the maximum": it is an unanswerable question, and the answer to an unanswerable
// question about how much to grant is that the package is not grantable.

import (
	"errors"
	"fmt"
	"math"
)

// The data-allocation modes a package revision may carry.
const (
	// AllocFixed uses the pinned Service Plan revision's quota unchanged. It is what every package published
	// before this existed does, and it is what an absent policy means -- so nothing already granted or already
	// published changes meaning.
	AllocFixed = "FIXED"
	// AllocPerStayNight derives the quota from the number of nights, clamped to a floor and optional ceiling.
	AllocPerStayNight = "PER_STAY_NIGHT"
)

// bytesPerGB is decimal GB, the unit a data allowance is sold in and the same one the operator UI converts
// with. Using 2^30 here and 10^9 on the screen would quietly hand out 7% more than the operator typed.
const bytesPerGB = 1_000_000_000

// maxAllocNights bounds the multiplication rather than the stay. A stay of a few thousand nights is a data
// error somewhere upstream, and multiplying it by a per-night allowance is how an int64 overflow becomes a
// negative quota -- a negative quota compares as "already exceeded" or as "unlimited" depending on which
// predicate reads it, and both are catastrophic. The clamp below makes the ceiling the answer instead.
const maxAllocNights = 3650

// DataAllocationPolicy is a package revision's rule for turning a stay into a byte allowance.
type DataAllocationPolicy struct {
	Mode string `json:"mode"`
	// GBPerNight may be fractional (0.5 GB a night is a real product), so it is a float on the way in and
	// becomes an exact integer number of bytes on the way out.
	GBPerNight float64  `json:"gb_per_night,omitempty"`
	MinGB      float64  `json:"min_gb,omitempty"`
	MaxGB      *float64 `json:"max_gb,omitempty"`
}

// ErrStayLengthUnknown is the fail-closed outcome: a PER_STAY_NIGHT package cannot be granted to a subject
// whose stay length is not authoritatively known.
var ErrStayLengthUnknown = errors.New("stay length is not authoritatively known")

// ParseDataAllocationPolicy reads the jsonb a package revision carries.
//
// An ABSENT policy is FIXED. That is the whole compatibility story: every revision published before this
// column existed carries NULL, reads as FIXED, and grants exactly what it granted yesterday.
func ParseDataAllocationPolicy(raw map[string]any) (DataAllocationPolicy, error) {
	if len(raw) == 0 {
		return DataAllocationPolicy{Mode: AllocFixed}, nil
	}
	mode, _ := raw["mode"].(string)
	switch mode {
	case "", AllocFixed:
		return DataAllocationPolicy{Mode: AllocFixed}, nil
	case AllocPerStayNight:
	default:
		return DataAllocationPolicy{}, fmt.Errorf("unknown data allocation mode %q", mode)
	}
	p := DataAllocationPolicy{Mode: AllocPerStayNight}
	num := func(key string) (float64, bool, error) {
		v, ok := raw[key]
		if !ok || v == nil {
			return 0, false, nil
		}
		f, ok := asFloat64(v)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
			return 0, false, fmt.Errorf("%s must be a non-negative number", key)
		}
		return f, true, nil
	}
	var err error
	var ok bool
	if p.GBPerNight, ok, err = num("gb_per_night"); err != nil {
		return DataAllocationPolicy{}, err
	} else if !ok || p.GBPerNight <= 0 {
		return DataAllocationPolicy{}, errors.New("gb_per_night must be greater than zero for PER_STAY_NIGHT")
	}
	if p.MinGB, _, err = num("min_gb"); err != nil {
		return DataAllocationPolicy{}, err
	}
	if mx, has, mErr := num("max_gb"); mErr != nil {
		return DataAllocationPolicy{}, mErr
	} else if has {
		if mx <= 0 {
			return DataAllocationPolicy{}, errors.New("max_gb must be greater than zero when set")
		}
		if mx < p.MinGB {
			return DataAllocationPolicy{}, errors.New("max_gb is below min_gb, so no allowance could satisfy both")
		}
		p.MaxGB = &mx
	}
	return p, nil
}

// EffectiveDataQuotaBytes resolves the allowance this grant should freeze.
//
// FIXED returns nothing to snapshot: the entitlement keeps pointing at the plan revision, exactly as every
// entitlement does today, and no new column is populated. Only PER_STAY_NIGHT produces a snapshot.
//
//	calculated = nights x gb_per_night
//	effective  = max(min_gb, calculated), then min(max_gb, ...) when a ceiling is set
//
// The clamp order is the approved one and it is not symmetric: the floor is applied first so a short stay
// receives the minimum, and the ceiling second so it wins outright over everything -- including a floor an
// operator set above it, which is refused at publication rather than silently reordered here.
func EffectiveDataQuotaBytes(p DataAllocationPolicy, s EligibilitySubject) (bytes int64, snapshot bool, err error) {
	if p.Mode != AllocPerStayNight {
		return 0, false, nil
	}
	nights, known := s.Stay.Nights()
	if !known {
		return 0, false, ErrStayLengthUnknown
	}
	// A same-day stay is zero nights, which is a real answer rather than a missing one: the floor is what it
	// gets. Only an unknown arrival or departure refuses.
	if nights < 0 {
		return 0, false, ErrStayLengthUnknown
	}
	if nights > maxAllocNights {
		nights = maxAllocNights
	}
	gb := float64(nights) * p.GBPerNight
	if gb < p.MinGB {
		gb = p.MinGB
	}
	if p.MaxGB != nil && gb > *p.MaxGB {
		gb = *p.MaxGB
	}
	// Round to whole bytes at the very end, once, so 0.1 GB a night over seven nights is 700 MB rather than
	// seven accumulated rounding errors.
	b := int64(math.Round(gb * bytesPerGB))
	if b < 0 || b > maxDataQuotaBytes {
		return 0, false, fmt.Errorf("computed allowance is outside the supported range")
	}
	// A ZERO ALLOWANCE IS NOT A GRANT. It would produce an entitlement that is exhausted the instant it is
	// created, which reads to a guest as a package that never worked and to an operator as an enforcement bug.
	if b == 0 {
		return 0, false, errors.New("computed allowance is zero bytes")
	}
	return b, true, nil
}

// asFloat64 accepts the numeric shapes jsonb decoding produces. json.Number is handled through its string
// form so an integer that does not fit a float64 mantissa is still refused rather than silently rounded.
func asFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		if i, ok := asInt64(v); ok {
			return float64(i), true
		}
		if jn, ok := v.(interface{ Float64() (float64, error) }); ok {
			f, err := jn.Float64()
			return f, err == nil
		}
		return 0, false
	}
}
