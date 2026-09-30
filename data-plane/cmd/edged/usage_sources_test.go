package main

// USAGE BY ACCESS SOURCE — what can be proved without a database.
//
// Every query the usage explorer runs is checked against the PRODUCTION BASELINE and the Gate-P grant files
// with the same checker the package-activity view uses (commerce_activity_test.go): each iam_v2 table must be
// one svc_edged may SELECT, and each alias.column must exist. An ungranted table is otherwise a 500 on the
// appliance while every unit test passes.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// usageSQLUnderTest is every statement the usage explorer's access-source views run.
func usageSQLUnderTest() map[string]string {
	out := map[string]string{
		"source list":    usageSourceListSQL(50),
		"stay header":    stayHeaderSQL,
		"account header": accountHeaderSQL,
		"voucher header": voucherHeaderSQL,
	}
	for kind, col := range usageSubjectColumn {
		out[kind+" devices"] = subjectDevicesSQL(col)
		out[kind+" sessions"] = subjectSessionsSQL(col)
	}
	return out
}

func TestUsageSourceSQLReadsOnlyGrantedTablesAndRealColumns(t *testing.T) {
	cols := baselineColumns(t)
	grants := edgedSelectGrants(t)
	for _, needed := range []string{"entitlements", "sessions", "devices", "stays", "pms_interfaces",
		"guest_access_accounts", "service_plan_revisions"} {
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

// THE SOURCES WHOSE IDENTIFYING DATA svc_edged MAY NOT READ ARE NOT OFFERED. If one of these ever becomes
// granted, this test is where the decision to add the source type gets made on purpose.
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
		"source list":    {"e.tenant_id = $1 AND e.site_id = $2", "e.guest_principal_id IS NULL", "st.tenant_id = $1", "ga.tenant_id = $1 AND ga.site_id = $2"},
		"stay header":    {"st.tenant_id = $2 AND st.site_id = $3"},
		"account header": {"ga.tenant_id = $2 AND ga.site_id = $3", "x.tenant_id = ga.tenant_id AND x.site_id = ga.site_id"},
		"voucher header": {"x.tenant_id = $2 AND x.site_id = $3"},
	}
	sqls := usageSQLUnderTest()
	for kind := range usageSubjectColumn {
		want[kind+" devices"] = []string{"se.tenant_id = $2 AND se.site_id = $3"}
		want[kind+" sessions"] = []string{"se.tenant_id = $2 AND se.site_id = $3"}
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
