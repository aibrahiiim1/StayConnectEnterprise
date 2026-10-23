package main

// List filters and the effective state: nothing writes REDEMPTION_EXPIRED, so a stored-state filter for expired would match nothing. Pins the Go parts:
// the refusals, and the SQL agreement with voucherRedeemable.

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTheListFiltersAreRefusedRatherThanIgnored(t *testing.T) {
	bad := []string{
		"state=SPENT",
		"effective=expired_unused",
		"effective=UNUSED",
		"last4=ABCDE",
		"last4=AB-1",
		"last4=%25",
	}
	for _, q := range bad {
		v, _ := url.ParseQuery(q)
		if _, err := parseVoucherListFilters(v); err == nil {
			t.Errorf("%q was accepted; a filter the server cannot apply must be refused, not dropped", q)
		}
	}
	good := []string{"", "effective=available", "effective=EXPIRED", "effective=not_yet_valid",
		"effective=redeemed", "effective=cancelled", "last4=ab12", "last4=7", "state=unused"}
	for _, q := range good {
		v, _ := url.ParseQuery(q)
		if _, err := parseVoucherListFilters(v); err != nil {
			t.Errorf("%q was refused: %v", q, err)
		}
	}
	v, _ := url.ParseQuery("last4=ab12&effective=Expired")
	f, _ := parseVoucherListFilters(v)
	if f.Last4 != "AB12" || f.Effective != "expired" {
		t.Errorf("normalisation: last4=%q effective=%q", f.Last4, f.Effective)
	}
}

// A bad filter is refused BEFORE the database is touched: the handler has no pool, so reaching a query would panic.
func TestABadFilterNeverReachesTheDatabase(t *testing.T) {
	s := &server{}
	for _, h := range []http.HandlerFunc{s.listVouchers, s.voucherSummary} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/v1/vouchers?effective=bogus", nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.listVoucherBatches(rec, httptest.NewRequest(http.MethodGet, "/v1/vouchers/batches?limit=900", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("batches limit 900: status %d, want 400", rec.Code)
	}
}

// The SQL is the authenticator rule, written once: until is EXCLUSIVE and from INCLUSIVE, as in voucherRedeemable; if one drifts a card shows available that sign-in refuses.
func TestTheEffectiveStateSQLIsTheAuthenticatorsWindowRule(t *testing.T) {
	sql := voucherEffectiveStateSQL
	for _, want := range []string{
		"redemption_valid_until <= now()",
		"redemption_valid_from  >  now()",
		"WHEN state = 'REDEEMED' THEN 'redeemed'",
		"WHEN state = 'REVOKED'  THEN 'cancelled'",
		"WHEN state = 'REDEMPTION_EXPIRED' THEN 'expired'",
		"ELSE 'available'",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("effective-state SQL no longer contains %q", want)
		}
	}
	// Expiry is decided before not-yet-valid: a window that has closed is closed, whatever its start.
	if strings.Index(sql, "redemption_valid_until <=") > strings.Index(sql, "redemption_valid_from  >") {
		t.Error("the not-yet-valid branch is evaluated before the expired branch")
	}
	// The list, its total and the summary share one WHERE, so a filter means the same thing in all three.
	if !strings.Contains(voucherListFilterSQL, voucherEffectiveStateSQL) {
		t.Error("the list filter does not use the shared effective-state expression")
	}
}
