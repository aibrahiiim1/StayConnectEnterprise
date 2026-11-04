package main

import (
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func clientFilter(t *testing.T, raw string) clientActivityFilter {
	t.Helper()
	q, _ := url.ParseQuery(raw)
	f, err := parseClientActivityFilter(q, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestClientActivityFilterBounds(t *testing.T) {
	f := clientFilter(t, "")
	if f.To.Sub(f.From) != 24*time.Hour || f.Limit != pageSizeDefault || f.Offset != 0 {
		t.Fatalf("default filter: %+v", f)
	}
	for _, bad := range []string{"range=1y", "category=billing", "page=0", "page_size=0", "page_size=1000",
		"range=custom&from=2026-01-01T00:00:00Z&to=2026-06-01T00:00:00Z"} {
		q, _ := url.ParseQuery(bad)
		if _, err := parseClientActivityFilter(q, time.Now()); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if f := clientFilter(t, "page=3&page_size=20"); f.Limit != 20 || f.Offset != 40 {
		t.Fatalf("paging: %+v", f)
	}
	// The page's anchor fixes the window, so a second page asks the same question as the first.
	anchor := time.Date(2026, 11, 4, 11, 30, 0, 0, time.UTC)
	if f := clientFilter(t, "range=1h&at="+strconv.FormatInt(anchor.UnixMilli(), 10)); !f.To.Equal(anchor) || !f.From.Equal(anchor.Add(-time.Hour)) {
		t.Fatalf("anchored window: %v .. %v", f.From, f.To)
	}
	for _, bad := range []string{"at=abc", "at=" + strconv.FormatInt(time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC).UnixMilli(), 10),
		"at=" + strconv.FormatInt(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), 10)} {
		q, _ := url.ParseQuery(bad)
		if _, err := parseClientActivityFilter(q, time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, err := boundedSearch(strings.Repeat("x", clientActivityMaxQ+1)); err == nil {
		t.Fatal("an over-long search was accepted")
	}
}

func TestClientActivityHidesHotelSourcesOffHotelSites(t *testing.T) {
	sc := clientActivityScopeFor([]string{"site_admin"}, false)
	if sc.SignIns || sc.Stays {
		t.Fatalf("hotel sources offered on a site without the Hotel module: %+v", sc)
	}
	for _, a := range sc.StaffActions {
		if strings.HasPrefix(a, "guest_signin") {
			t.Errorf("hotel staff action %s offered on a site without the Hotel module", a)
		}
	}
	var args []any
	sql := clientActivitySources(sc, &args)
	for _, hotelOnly := range []string{"sign_in_attempts", "guest_signin_restrictions", "stay_events", "effective_checkout_at"} {
		if strings.Contains(sql, hotelOnly) {
			t.Errorf("non-hotel SQL reads %s", hotelOnly)
		}
	}
	if !clientActivityScopeFor([]string{"site_admin"}, true).SignIns {
		t.Fatal("a hotel site admin is not shown room sign-ins")
	}
}

func TestClientActivityAddsOnlyWhatTheReaderMayRead(t *testing.T) {
	// A role without audit, account or sign-in-attempt access sees connections, packages and devices only.
	sc := clientActivityScopeFor([]string{"custom:none"}, true)
	if sc.Accounts || sc.Staff || sc.SignIns || sc.Stays || len(sc.StaffActions) != 0 {
		t.Fatalf("scope wider than the role: %+v", sc)
	}
	var args []any
	sql := clientActivitySources(sc, &args)
	for _, s := range []string{"audit_log", "sign_in_attempts", "ACCOUNT_CREATED"} {
		if strings.Contains(sql, s) {
			t.Errorf("SQL for a narrow role reads %s", s)
		}
	}
}

func TestClientActivitySQLReadsOnlyGrantedTablesAndRealColumns(t *testing.T) {
	cols := baselineColumns(t)
	grants := edgedSelectGrants(t)
	f := clientFilter(t, "category=connection&problems=1")
	f.Q = "4202"
	sc := clientActivityScopeFor([]string{"site_admin"}, true)
	list, args := clientActivitySQL(f, sc, "t", "s", true)
	checkSQLAgainstSchema(t, t.Errorf, "client activity", list, cols, grants)
	if strings.Contains(list, "4202") {
		t.Fatal("the search text is in the SQL instead of a parameter")
	}
	if args[0] != "t" || args[1] != "s" {
		t.Fatalf("tenant and site are not the first parameters: %v", args[:2])
	}
	for _, forbidden := range []string{"stay_guests", "iam_v2.vouchers", "auth_contexts", "guest_principal_identities",
		"password_hash", "sensitive_ciphertext"} {
		if strings.Contains(list, forbidden) {
			t.Errorf("client activity reads %q", forbidden)
		}
	}
	// The restriction row is overwritten by each new pause, so it is never history: pauses come from the attempts and staff releases.
	if strings.Contains(list, "guest_signin_restrictions") {
		t.Fatal("the timeline reads the mutable restriction row as if it were history")
	}
	// A staff action on a client account names that account, so it can be read and searched like any other client.
	if !strings.Contains(list, "gat.id::text = al.target_id") || !strings.Contains(list, "COALESCE(gat.username,") {
		t.Fatal("staff actions on client accounts are not resolved to the account")
	}
	// Every branch yields the same number of columns as the first.
	want := len(strings.Split(clientActivityCols, ","))
	if want != 16 {
		t.Fatalf("clientActivityCols has %d columns", want)
	}
}
