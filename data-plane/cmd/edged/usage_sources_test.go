package main

// Usage by access source, without a database: every query is checked against the production baseline and Gate-P grants
// (as commerce_activity_test.go does), since an ungranted table is a 500 on the appliance while unit tests pass.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func usageSQLUnderTest() map[string]string {
	out := map[string]string{
		"source list":    usageSourceListSQL(51, 50),
		"stay header":    stayHeaderSQL,
		"account header": accountHeaderSQL,
		"voucher header": voucherHeaderSQL,
	}
	out["session segments"] = sessionSegmentsSQL
	for kind, col := range usageSubjectColumn {
		out[kind+" devices"] = subjectDevicesSQL(col)
		out[kind+" sessions"] = subjectSessionsSQL(col)
		out[kind+" packages"] = subjectPackagesSQL(col)
	}
	return out
}

func TestUsageSourceSQLReadsOnlyGrantedTablesAndRealColumns(t *testing.T) {
	cols := baselineColumns(t)
	grants := edgedSelectGrants(t)
	for _, needed := range []string{"entitlements", "sessions", "devices", "stays", "pms_interfaces",
		"guest_access_accounts", "service_plan_revisions", "internet_packages", "internet_package_revisions",
		"session_entitlement_bindings", "accounting_records"} {
		if !grants[needed] {
			t.Fatalf("svc_edged holds no SELECT on iam_v2.%s; the check below would be meaningless", needed)
		}
	}
	if !cols["guest_access_accounts"]["username"] || !cols["entitlements"]["voucher_id"] {
		t.Fatal("the baseline parser did not find the columns this view reads; the check would pass vacuously")
	}
	for name, sql := range usageSQLUnderTest() {
		checkSQLAgainstSchema(t, t.Errorf, name, sql, cols, grants)
	}
}

// Sources whose identifying data svc_edged may not read are not offered; if one becomes granted, this test is where adding it gets decided.
func TestTheUngrantedSourcesStayUngranted(t *testing.T) {
	grants := edgedSelectGrants(t)
	for _, table := range []string{"vouchers", "guest_principals", "guest_principal_identities"} {
		if grants[table] {
			t.Errorf("svc_edged may now SELECT iam_v2.%s; revisit what the usage explorer shows for that source", table)
		}
	}
	if _, ok := usageSubjectColumn["principal"]; ok {
		t.Error("the email / phone / social source is offered, but nothing svc_edged may read identifies it")
	}
}

// NO CODE, NO PASSWORD, NO GUEST NAME.
func TestUsageSourceSQLNeverReadsCredentialsOrNames(t *testing.T) {
	for name, sql := range usageSQLUnderTest() {
		for _, forbidden := range []string{"display_name", "password", "notes", "iam_v2.voucher",
			"iam_v2.guest_principal", "guest_principal_identities", "stay_guests", "auth_contexts",
			"code_last4", "code_hash"} {
			if strings.Contains(sql, forbidden) {
				t.Errorf("%s reads %q", name, forbidden)
			}
		}
	}
}

// THE SCOPE IS THE APPLIANCE'S, on every statement.
func TestUsageSourceSQLIsScoped(t *testing.T) {
	want := map[string][]string{
		"source list":      {"e.tenant_id = $1 AND e.site_id = $2", "e.guest_principal_id IS NULL", "st.tenant_id = $1", "ga.tenant_id = $1 AND ga.site_id = $2"},
		"stay header":      {"st.tenant_id = $2 AND st.site_id = $3"},
		"account header":   {"ga.tenant_id = $2 AND ga.site_id = $3", "x.tenant_id = ga.tenant_id AND x.site_id = ga.site_id"},
		"voucher header":   {"x.tenant_id = $2 AND x.site_id = $3"},
		"session segments": {"b.tenant_id = $2 AND b.site_id = $3", "ar.tenant_id = b.tenant_id AND ar.site_id = b.site_id"},
	}
	sqls := usageSQLUnderTest()
	for kind := range usageSubjectColumn {
		want[kind+" devices"] = []string{"se.tenant_id = $2 AND se.site_id = $3"}
		want[kind+" sessions"] = []string{"se.tenant_id = $2 AND se.site_id = $3"}
		want[kind+" packages"] = []string{"e.tenant_id = $2 AND e.site_id = $3"}
	}
	for name, needles := range want {
		for _, n := range needles {
			if !strings.Contains(sqls[name], n) {
				t.Errorf("%s lacks %q", name, n)
			}
		}
	}
	// Operator text never reaches the SQL text: the list takes its search and type as parameters.
	list := sqls["source list"]
	for _, p := range []string{"$5", "$6"} {
		if !strings.Contains(list, p) {
			t.Errorf("source list does not take %s as a parameter", p)
		}
	}
}

// THE SUBJECT COLUMNS ARE EXACTLY THE ENTITLEMENT'S SUBJECT COLUMNS, minus the one that cannot be identified.
func TestSubjectColumnsMatchTheEntitlementSubject(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "baseline", "0000_production_baseline.sql"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "num_nonnulls(stay_id, guest_account_id, voucher_id, guest_principal_id, anonymous_subject_id) = 1") {
		t.Fatal("ent_one_subject changed; the access-source types must be revisited")
	}
	var keys []string
	for k := range usageSubjectColumn {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "account,open,stay,voucher" {
		t.Fatalf("source types: %v", keys)
	}
}

// THE ALLOWANCE SHOWN IS THE CURRENT PACKAGE'S: a MAX across packages hid an unlimited one behind an old free package's quota.
func TestUsageListsShowTheMostRecentEntitlementsAllowance(t *testing.T) {
	b, err := os.ReadFile("resources_usage.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, bad := range []string{"MAX(COALESCE(e.data_quota_bytes", "MAX(e.consumed_data_bytes)", "MAX(e.terminal_reason)"} {
		if strings.Contains(src, bad) {
			t.Errorf("a usage list aggregates %q across entitlements", bad)
		}
	}
	if n := strings.Count(src, "currentEntitlementCols+") + strings.Count(src, "currentEntitlementCols +"); n != 2 {
		t.Errorf("the stay list and the source list should both read currentEntitlementCols; found %d uses", n)
	}
	for _, col := range []string{"[1] AS quota", "[1] AS consumed", "[1] AS end_reason", "[1] AS pkg_name", "[1] AS pkg_type", "[1] AS pkg_status"} {
		if !strings.Contains(usageSourceListSQL(51, 50), "ORDER BY e.activated_at DESC NULLS LAST, e.id DESC))"+col) {
			t.Errorf("the source list does not take %q from the most recent entitlement", col)
		}
	}
	// The package list is read in the lists' latest-package order, so a cut list always keeps the latest package.
	if !strings.Contains(subjectPackagesSQL("stay_id"), "ORDER BY e.activated_at DESC NULLS LAST, e.id DESC\n\t\t LIMIT 101") {
		t.Error("the package list is not read newest first, one past its bound")
	}
}

// A SOURCE WITH MORE PACKAGES THAN THE BOUND KEEPS ITS NEWEST: the header's latest package must be in the list.
func TestOldestFirstKeepsTheNewestPackages(t *testing.T) {
	newest := make([]usagePackageRow, packageHistoryMax+1)
	for i := range newest {
		newest[i] = usagePackageRow{EntitlementID: strconv.Itoa(packageHistoryMax + 1 - i)} // 101 (newest) .. 1 (oldest)
	}
	out, more := oldestFirst(newest)
	if !more || len(out) != packageHistoryMax {
		t.Fatalf("more=%v len=%d", more, len(out))
	}
	if out[0].EntitlementID != "2" || out[len(out)-1].EntitlementID != "101" {
		t.Errorf("kept %s..%s, want the newest 100 oldest first (2..101)", out[0].EntitlementID, out[len(out)-1].EntitlementID)
	}
	var name, typ, status string
	out[len(out)-1].Name = "newest"
	withLatestPackage(&name, &typ, &status, out)
	if name != "newest" {
		t.Errorf("latest package = %q", name)
	}
	few, more := oldestFirst([]usagePackageRow{{EntitlementID: "b"}, {EntitlementID: "a"}})
	if more || few[0].EntitlementID != "a" || few[1].EntitlementID != "b" {
		t.Errorf("a short list: more=%v %+v", more, few)
	}
}

func i64(v int64) *int64 { return &v }

// A CARRIED SESSION IS SPLIT ONLY WHEN THE SAMPLES ADD UP: never an estimate, and never counted twice.
func TestApplySegmentsSplitsOnlyWhenExact(t *testing.T) {
	t0 := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	t1, t2 := t0.Add(time.Hour), t0.Add(2*time.Hour)
	seg := func(sess, ent, name string, from time.Time, until *time.Time, down, up int64) rawSegment {
		return rawSegment{sessionID: sess, seg: sessionPackageSegment{EntitlementID: ent, PackageName: name, From: from, Until: until}, down: down, up: up}
	}
	sessions := []staySessionRow{
		{SessionID: "carried", BytesDown: 900, BytesUp: 90},
		{SessionID: "purged", BytesDown: 500, BytesUp: 50},
		{SessionID: "single", BytesDown: 7, BytesUp: 1},
	}
	applySegments(sessions, []rawSegment{
		seg("carried", "free", "Free", t0, &t1, 100, 10),
		seg("carried", "full", "Full stay", t1, &t2, 700, 70),
		seg("carried", "grace", "After check-out", t2, nil, 100, 10),
		// raw samples summarised: the bindings remain, the samples do not add up to the session
		seg("purged", "free", "Free", t0, &t1, 0, 0),
		seg("purged", "full", "Full stay", t1, nil, 20, 2),
		seg("single", "free", "Free", t0, nil, 7, 1),
	})

	c := sessions[0]
	if c.SplitExact == nil || !*c.SplitExact || len(c.Packages) != 3 {
		t.Fatalf("carried session: exact=%v packages=%d", c.SplitExact, len(c.Packages))
	}
	var sum int64
	for i, want := range []int64{110, 770, 110} {
		if c.Packages[i].BytesAll == nil || *c.Packages[i].BytesAll != want {
			t.Errorf("package %d (%s) = %v, want %d", i, c.Packages[i].PackageName, c.Packages[i].BytesAll, want)
		} else {
			sum += *c.Packages[i].BytesAll
		}
	}
	if sum != c.BytesDown+c.BytesUp {
		t.Errorf("the split adds up to %d, the session to %d: it counted something twice or missed it", sum, c.BytesDown+c.BytesUp)
	}

	p := sessions[1]
	if p.SplitExact == nil || *p.SplitExact || len(p.Packages) != 2 {
		t.Fatalf("purged session: exact=%v packages=%d", p.SplitExact, len(p.Packages))
	}
	for _, s := range p.Packages {
		if s.BytesAll != nil || s.BytesDown != nil || s.BytesUp != nil {
			t.Errorf("a split that does not add up must show no bytes, got %+v", s)
		}
	}
	if p.Packages[0].PackageName != "Free" || !p.Packages[1].From.Equal(t1) {
		t.Errorf("the packages and their times are still shown: %+v", p.Packages)
	}

	if s := sessions[2]; s.Packages != nil || s.SplitExact != nil {
		t.Errorf("a session under one package needs no split: %+v", s)
	}
}

func TestWithLatestPackageTakesTheLastOfTheOldestFirstList(t *testing.T) {
	var name, typ, status string
	withLatestPackage(&name, &typ, &status, nil)
	if name != "" {
		t.Fatal("no package must leave the header empty")
	}
	withLatestPackage(&name, &typ, &status, []usagePackageRow{
		{Name: "Free", Type: "GENERAL", Status: "TERMINATED", QuotaBytes: i64(7e9)},
		{Name: "Full stay", Type: "GENERAL", Status: "TERMINATED"},
		{Name: "Checkout grace", Type: "CHECKOUT_GRACE", Status: "ACTIVE", QuotaBytes: i64(2e9)},
	})
	if name != "Checkout grace" || typ != "CHECKOUT_GRACE" || status != "ACTIVE" {
		t.Errorf("latest package = %q %q %q", name, typ, status)
	}
}

func TestParseSourceType(t *testing.T) {
	for in, want := range map[string]string{"": "", "all": "", "stay": "stay", "STAY": "stay", " account ": "account", "voucher": "voucher"} {
		got, ok := parseSourceType(in)
		if !ok || got != want {
			t.Errorf("parseSourceType(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"principal", "guest_principal", "stay_id", "stay; DROP TABLE x", "rooms"} {
		if _, ok := parseSourceType(bad); ok {
			t.Errorf("%q was accepted as a source type", bad)
		}
	}
}

func TestSearchTextIsNormalisedAndLiteral(t *testing.T) {
	if got := normaliseSourceQuery("  3fa85f64… "); got != "3fa85f64" {
		t.Errorf("a pasted short card reference: %q", got)
	}
	if got := likeEscape(normaliseSourceQuery("4_2%")); got != `4\_2\%` {
		t.Errorf("search text is not literal inside ILIKE: %q", got)
	}
}

// A STAY FIELD NEVER RIDES ON ANOTHER SOURCE, and a username never rides on a non-account.
func TestFinishSourceRowKeepsFieldsToTheirType(t *testing.T) {
	x := finishSourceRow(usageSourceRow{Type: "account", Username: "4202smith", Room: "4202", PMSInterface: "Main", Reservation: "BK1", StayStatus: "IN_HOUSE"})
	if x.Room != "" || x.PMSInterface != "" || x.Reservation != "" || x.StayStatus != "" || x.Username != "4202smith" {
		t.Errorf("account row: %+v", x)
	}
	v := finishSourceRow(usageSourceRow{Type: "voucher", Username: "leak", Room: "1"})
	if v.Username != "" || v.Room != "" {
		t.Errorf("voucher row: %+v", v)
	}
	s := finishSourceRow(usageSourceRow{Type: "stay", Username: "leak", Room: "4202", PMSInterface: "Main"})
	if s.Username != "" || s.Room != "4202" || s.PMSInterface != "Main" {
		t.Errorf("stay row: %+v", s)
	}
}

// BAD REQUESTS ARE REFUSED BEFORE THE DATABASE (s.db is nil: reaching it would panic), and the older routes
// that deep links and other screens use are still served.
func TestUsageSourceRoutesRefuseBadParamsAndKeepOldRoutes(t *testing.T) {
	s := &server{tenantID: "tenant-A", siteID: "site-A"}
	r := chi.NewRouter()
	r.Mount("/usage", s.usageRoutes())
	const id = "11111111-2222-3333-4444-555555555555"
	for path, want := range map[string]int{
		"/usage/sources?type=principal":     http.StatusBadRequest,
		"/usage/sources?type=stay_id":       http.StatusBadRequest,
		"/usage/sources/principal/" + id:    http.StatusBadRequest,
		"/usage/sources/all/" + id:          http.StatusBadRequest,
		"/usage/sources/voucher/not-a-uuid": http.StatusNotFound,
		"/usage/sources/account/1'--":       http.StatusNotFound,
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Errorf("GET %s: want %d, got %d %s", path, want, w.Code, w.Body.String())
		}
	}

	routes := map[string]bool{}
	_ = chi.Walk(s.usageRoutes().(chi.Routes), func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes[method+" "+route] = true
		return nil
	})
	for _, rt := range []string{"GET /stays", "GET /stays/{stay_id}", "GET /devices/{mac}",
		"GET /sessions/{session_id}/samples", "GET /sources", "GET /sources/{type}/{id}"} {
		if !routes[rt] {
			t.Errorf("usage no longer serves %s", rt)
		}
	}
}
