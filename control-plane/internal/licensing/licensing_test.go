package licensing

import (
	"testing"
	"time"

	lic "github.com/stayconnect/enterprise/license"
)

// F8: a WAN-MAC rebind (and suspend/resume) re-signs the licence with the SAME terms. It used to re-issue
// with cap 0 (unlimited) and a fresh 365-day term.
func TestPreservedTermsKeepEveryTerm(t *testing.T) {
	until := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	cur := IssueParams{
		TenantID: "t", SiteID: "s", ApplianceID: "a", CreatedBy: "original",
		MaxConcurrentOnlineGuests: 250, ValidFrom: from, ValidUntil: until,
		GracePeriodDays: 14, OfflineGraceDays: 21, Status: lic.DocSuspended,
		ValidFor: 365 * 24 * time.Hour,
	}
	got := PreservedTerms(cur, "operator-2")
	if got.MaxConcurrentOnlineGuests != 250 {
		t.Errorf("cap changed: %d", got.MaxConcurrentOnlineGuests)
	}
	if !got.ValidUntil.Equal(until) || !got.ValidFrom.Equal(from) {
		t.Errorf("validity changed: %v..%v", got.ValidFrom, got.ValidUntil)
	}
	if got.GracePeriodDays != 14 || got.OfflineGraceDays != 21 {
		t.Errorf("grace changed: %d/%d", got.GracePeriodDays, got.OfflineGraceDays)
	}
	if got.Status != lic.DocSuspended {
		t.Errorf("status changed: %s", got.Status)
	}
	if got.ValidFor != 0 {
		t.Errorf("valid_for must not survive: a re-issue would otherwise re-derive valid_until from now")
	}
	if got.CreatedBy != "operator-2" || got.ApplianceID != "a" || got.TenantID != "t" || got.SiteID != "s" {
		t.Errorf("identity/actor wrong: %+v", got)
	}
	if cur.CreatedBy != "original" {
		t.Errorf("input mutated")
	}
}
