package iamv2

// The byte allowance is computed once at grant time from Stay evidence authoritative at that moment, then
// frozen; a later stay change never alters what a guest was given.
// When the stay length cannot be established the package is not grantable: no "assume one night", no "assume
// the maximum".

import (
	"errors"
	"fmt"
	"math"
)

const (
	// AllocFixed uses the plan revision's quota unchanged; an absent policy means FIXED, so existing grants
	// and packages keep their meaning.
	AllocFixed        = "FIXED"
	AllocPerStayNight = "PER_STAY_NIGHT"
)

// Decimal GB, matching the operator UI; 2^30 here would hand out about 7% more than the operator typed.
const bytesPerGB = 1_000_000_000

// Bounds the multiplication: a huge stay is an upstream data error, and overflow would yield a negative quota
// that reads as exceeded or unlimited depending on the predicate.
const maxAllocNights = 3650

type DataAllocationPolicy struct {
	Mode string `json:"mode"`
	// May be fractional (0.5 GB a night); converted to an exact integer byte count.
	GBPerNight float64  `json:"gb_per_night,omitempty"`
	MinGB      float64  `json:"min_gb,omitempty"`
	MaxGB      *float64 `json:"max_gb,omitempty"`
}

// Fail closed: a PER_STAY_NIGHT package cannot be granted when the stay length is not authoritatively known.
var ErrStayLengthUnknown = errors.New("stay length is not authoritatively known")

// An absent policy is FIXED: revisions published before the column existed carry NULL and grant what they
// always did.
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

// FIXED returns nothing to snapshot; only PER_STAY_NIGHT freezes a quota.
//
//	calculated = nights x gb_per_night
//	effective  = max(min_gb, calculated), then min(max_gb, ...) when a ceiling is set
//
// The floor applies first and the ceiling second, so the ceiling wins even over a floor set above it (refused
// at publication, not reordered here).
func EffectiveDataQuotaBytes(p DataAllocationPolicy, s EligibilitySubject) (bytes int64, snapshot bool, err error) {
	if p.Mode != AllocPerStayNight {
		return 0, false, nil
	}
	nights, known := s.Stay.Nights()
	if !known {
		return 0, false, ErrStayLengthUnknown
	}
	// A same-day stay is zero nights, a real answer that gets the floor; only an unknown arrival or departure
	// refuses.
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
	// Round once at the end so 0.1 GB x 7 nights is 700 MB, not seven accumulated rounding errors.
	b := int64(math.Round(gb * bytesPerGB))
	if b < 0 || b > maxDataQuotaBytes {
		return 0, false, fmt.Errorf("computed allowance is outside the supported range")
	}
	// A zero allowance is not a grant: it would be exhausted on creation.
	if b == 0 {
		return 0, false, errors.New("computed allowance is zero bytes")
	}
	return b, true, nil
}

// json.Number goes through its string form so an integer beyond the float64 mantissa is refused rather than
// silently rounded.
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
