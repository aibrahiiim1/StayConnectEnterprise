package api

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

func TestMoveToAnotherCustomerIsRefused(t *testing.T) {
	ref := checkMoveCustomer("cust-a", "cust-b")
	if ref == nil || ref.Status != http.StatusConflict || ref.Code != "cross_customer_move" {
		t.Fatalf("cross-customer move not refused with 409: %+v", ref)
	}
	if ref.Message != msgMoveCrossCustomer {
		t.Fatalf("operator message changed: %q", ref.Message)
	}
	if checkMoveCustomer("cust-a", "cust-a") != nil {
		t.Fatal("a same-customer move was refused")
	}
}

func TestMoveFailsClosedOnLicensing(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	valid := licensing.IssueParams{ValidUntil: now.Add(90 * 24 * time.Hour)}

	cases := []struct {
		name       string
		available  bool
		terms      licensing.IssueParams
		err        error
		wantCarry  bool
		wantStatus int // 0 = allowed
		wantCode   string
	}{
		{"licensing unavailable (no vendor signer)", false, valid, nil, false, 503, "licensing_unavailable"},
		{"licensing unavailable even with no licence", false, licensing.IssueParams{}, licensing.ErrNoLicense, false, 503, "licensing_unavailable"},
		{"licence query failed", true, licensing.IssueParams{}, errors.New("conn reset"), false, 503, "licensing_unavailable"},
		{"wrapped query failure is still a failure", true, licensing.IssueParams{}, errors.New("timeout"), false, 503, "licensing_unavailable"},
		{"verified no licence: move, nothing to carry", true, licensing.IssueParams{}, licensing.ErrNoLicense, false, 0, ""},
		{"current licence: carried", true, valid, nil, true, 0, ""},
		{"licence past valid_until (grace): refused", true, licensing.IssueParams{ValidUntil: now.Add(-time.Hour)}, nil, false, 409, "license_expired"},
	}
	for _, c := range cases {
		carry, ref := decideMoveLicence(c.available, c.terms, c.err, now)
		if carry != c.wantCarry {
			t.Errorf("%s: carry=%v want %v", c.name, carry, c.wantCarry)
		}
		switch {
		case c.wantStatus == 0 && ref != nil:
			t.Errorf("%s: refused %+v", c.name, ref)
		case c.wantStatus != 0 && (ref == nil || ref.Status != c.wantStatus || ref.Code != c.wantCode):
			t.Errorf("%s: got %+v want %d %s", c.name, ref, c.wantStatus, c.wantCode)
		}
	}
	if _, ref := decideMoveLicence(false, valid, nil, now); ref.Message != msgMoveLicensingUnavailable {
		t.Fatalf("operator message changed: %q", ref.Message)
	}
}

// A carried licence keeps every term: PreservedTerms changes only the actor and never re-derives validity.
func TestSameCustomerMoveKeepsLicenceTerms(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cur := licensing.IssueParams{TenantID: "c", SiteID: "old", ApplianceID: "a", CreatedBy: "someone",
		MaxConcurrentOnlineGuests: 150, ValidFrom: from, ValidUntil: from.AddDate(1, 0, 0),
		GracePeriodDays: 10, OfflineGraceDays: 30, Status: "suspended"}
	p := licensing.PreservedTerms(cur, "operator")
	p.SiteID = "new"
	if p.MaxConcurrentOnlineGuests != 150 || !p.ValidUntil.Equal(cur.ValidUntil) || !p.ValidFrom.Equal(from) ||
		p.GracePeriodDays != 10 || p.OfflineGraceDays != 30 || p.Status != "suspended" || p.ValidFor != 0 {
		t.Fatalf("terms changed on move: %+v", p)
	}
	if p.CreatedBy != "operator" || p.SiteID != "new" || p.TenantID != "c" {
		t.Fatalf("binding/actor wrong: %+v", p)
	}
}
