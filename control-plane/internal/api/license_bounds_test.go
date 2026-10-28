package api

import (
	"testing"
	"time"
)

// Audit Phase 1 L10: licence terms are bounded; values at the bound pass.
func TestLicenseTermsBounds(t *testing.T) {
	grace := func(n int) *int { return &n }
	far := time.Now().AddDate(11, 0, 0)
	near := time.Now().AddDate(9, 0, 0)
	for name, c := range map[string]struct {
		t  licenseTerms
		ok bool
	}{
		"valid_days at the bound":     {licenseTerms{MaxConcurrentOnlineGuests: 1, ValidDays: 3650}, true},
		"valid_days over the bound":   {licenseTerms{MaxConcurrentOnlineGuests: 1, ValidDays: 3651}, false},
		"valid_days overflow":         {licenseTerms{MaxConcurrentOnlineGuests: 1, ValidDays: 200000}, false},
		"valid_until in nine years":   {licenseTerms{MaxConcurrentOnlineGuests: 1, ValidUntil: &near}, true},
		"valid_until in eleven years": {licenseTerms{MaxConcurrentOnlineGuests: 1, ValidUntil: &far}, false},
		"grace at the bound":          {licenseTerms{MaxConcurrentOnlineGuests: 1, GracePeriodDays: grace(365)}, true},
		"grace over the bound":        {licenseTerms{MaxConcurrentOnlineGuests: 1, GracePeriodDays: grace(366)}, false},
		"negative grace":              {licenseTerms{MaxConcurrentOnlineGuests: 1, GracePeriodDays: grace(-1)}, false},
		"guest cap over the bound":    {licenseTerms{MaxConcurrentOnlineGuests: 1_000_001}, false},
	} {
		_, err := c.t.params("a", "op")
		if (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", name, err, c.ok)
		}
	}
}
