package main

// PACKAGE ACTIVITY, CODE-EXISTS AND DELETABILITY — the pieces that can be proved without a database.
//
// The SQL itself is checked against the PRODUCTION BASELINE and the Gate-P grant files: every iam_v2 table the
// activity queries name must be one svc_edged may SELECT, and every alias.column they read must exist on that
// table. A typo or an ungranted table is otherwise a 500 on the appliance while every unit test passes, which is
// exactly how the package list failed in PRE-LIVE once before.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// ------------------------------------------------------------------------------------ filter parsing ----

func mustFilter(t *testing.T, raw string) activityFilter {
	t.Helper()
	q, _ := url.ParseQuery(raw)
	f, err := parseActivityFilter(q, time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return f
}

func TestActivityFilterDefaultsAndPagingBounds(t *testing.T) {
	f := mustFilter(t, "")
	if f.Limit != activityDefaultLimit || f.Offset != 0 {
		t.Fatalf("defaults: limit=%d offset=%d", f.Limit, f.Offset)
	}
	if got := f.To.Sub(f.From); got != 7*24*time.Hour {
		t.Fatalf("default range is 7 days, got %v", got)
	}
	if f := mustFilter(t, "limit=100000"); f.Limit != activityMaxLimit {
		t.Fatalf("limit must be capped at %d, got %d", activityMaxLimit, f.Limit)
	}
	if f := mustFilter(t, "offset=99999999"); f.Offset != activityMaxOffset {
		t.Fatalf("offset must be capped, got %d", f.Offset)
	}
	if f := mustFilter(t, "range=24h"); f.To.Sub(f.From) != 24*time.Hour {
		t.Fatal("24h range")
	}
	if f := mustFilter(t, "range=30d"); f.To.Sub(f.From) != 30*24*time.Hour {
		t.Fatal("30d range")
	}
	f = mustFilter(t, "range=custom&from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z")
	if f.From.Day() != 1 || f.To.Day() != 2 {
		t.Fatalf("custom range: %v..%v", f.From, f.To)
	}
}

func TestActivityFilterRefusesWhatItCannotUse(t *testing.T) {
	now := time.Now()
	for _, raw := range []string{
		"limit=0", "limit=-3", "limit=abc", "offset=-1",
		"range=1y", "range=custom", "range=custom&from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z",
		"range=custom&from=2020-01-01T00:00:00Z&to=2026-01-01T00:00:00Z",
		"package_id=not-a-uuid", "package_id=1'--DROP", "status=deleted", "source=HACKED",
	} {
		q, _ := url.ParseQuery(raw)
		if _, err := parseActivityFilter(q, now); err == nil {
			t.Errorf("%q was accepted; a value the query cannot use must be refused, not reinterpreted", raw)
		}
	}
}

// THE SCOPE IS THE APPLIANCE'S, AND OPERATOR TEXT IS NEVER SPLICED INTO SQL.
func TestActivitySQLIsScopedAndParameterised(t *testing.T) {
	q, _ := url.ParseQuery("q=4'; DROP TABLE x --&source=VOUCHER_REDEMPTION&status=active" +
		"&package_id=11111111-2222-3333-4444-555555555555&tenant_id=evil&site_id=evil")
	f, err := parseActivityFilter(q, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	sql, args := activityListSQL(f, "tenant-A", "site-A")
	if args[0] != "tenant-A" || args[1] != "site-A" {
		t.Fatalf("$1/$2 must be the appliance's tenant and site, got %v %v", args[0], args[1])
	}
	for _, a := range args {
		if a == "evil" {
			t.Fatal("a tenant or site from the query string reached the SQL arguments")
		}
	}
	if strings.Contains(sql, "DROP TABLE") || strings.Contains(sql, "VOUCHER_REDEMPTION") || strings.Contains(sql, "11111111") {
		t.Fatal("a filter value was interpolated into the SQL text instead of passed as a parameter")
	}
	for _, want := range []string{"p.tenant_id = $1", "p.site_id = $2", "s.tenant_id = $1", "s.site_id = $2"} {
		if !strings.Contains(sql, want) {
			t.Errorf("activity SQL lacks %q", want)
		}
	}
	// Paging is by parameter, newest first.
	if !strings.Contains(sql, "ORDER BY f.occurred_at DESC NULLS LAST") {
		t.Error("activity is not sorted most recent first")
	}
	last2 := args[len(args)-2:]
	if last2[0] != f.Limit || last2[1] != f.Offset {
		t.Errorf("limit/offset are not the last two parameters: %v", last2)
	}
	if likeEscape(`4_2%\`) != `4\_2\%\\` {
		t.Errorf("likeEscape: %q", likeEscape(`4_2%\`))
	}
}

// THE OFFER IS SAID ONLY WHEN IT HAPPENED, AND NO OFFER TIME IS INVENTED.
func TestActivityRowNeverFabricatesAnOffer(t *testing.T) {
	exp := time.Now()
	x := activityRow{Source: "VOUCHER_REDEMPTION", PackageCode: "GOLD", HadOffer: false,
		OfferExpiresAt: &exp, OfferTakenAt: &exp}
	finishActivityRow(&x, "")
	if x.OfferExpiresAt != nil || x.OfferTakenAt != nil {
		t.Fatal("a grant with no quote carries offer times")
	}
	if x.Status != "NOT_GRANTED" {
		t.Fatalf("a purchase with no entitlement must read NOT_GRANTED, got %q", x.Status)
	}
	if x.SourceLabel != "Voucher" || x.PackageName != "GOLD" {
		t.Fatalf("labels: %q %q", x.SourceLabel, x.PackageName)
	}

	y := activityRow{Source: "CHECKOUT_GRACE", EntitlementID: "e", StayID: "st 1", SystemPackage: true,
		PackageCode: "__system_checkout_grace"}
	finishActivityRow(&y, "ACTIVE")
	if y.Status != "ACTIVE" || y.PackageName != "After check-out grace" {
		t.Fatalf("system grace row: %+v", y)
	}
	if y.UsageHref != "/usage?stay=st+1" {
		t.Fatalf("the usage link must point at THIS stay, got %q", y.UsageHref)
	}

	src, err := os.ReadFile("resources_commerce.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "OfferedAt = x.ExpiresAt") {
		t.Fatal("guest-activity copies the expiry into the offer time again")
	}
}

// --------------------------------------------------------------------- schema and privilege checks ----

// baselineColumns reads every CREATE TABLE iam_v2.<t> in the production baseline into table -> columns.
func baselineColumns(t *testing.T) map[string]map[string]bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "migrations", "baseline", "0000_production_baseline.sql"))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	out := map[string]map[string]bool{}
	var cur map[string]bool
	head := regexp.MustCompile(`^CREATE (?:TABLE|VIEW) iam_v2\.(\w+) \($`)
	col := regexp.MustCompile(`^    ([a-z_][a-z0-9_]*) `)
	for _, line := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if m := head.FindStringSubmatch(line); m != nil {
			cur = map[string]bool{}
			out[m[1]] = cur
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, ")") {
			cur = nil
			continue
		}
		if m := col.FindStringSubmatch(line); m != nil && m[1] != "constraint" {
			cur[m[1]] = true
		}
	}
	return out
}

// edgedSelectGrants reads the Gate-P allowlist: the tables svc_edged may SELECT after a reconcile.
func edgedSelectGrants(t *testing.T) map[string]bool {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("..", "..", "..", "deploy", "gatep", "*.sql"))
	if len(files) == 0 {
		t.Fatal("no Gate-P grant files found")
	}
	re := regexp.MustCompile(`(?i)GRANT\s+([A-Z, ]+?)\s+ON\s+(?:TABLE\s+)?iam_v2\.(\w+)\s+TO\s+svc_edged`)
	out := map[string]bool{}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			if strings.Contains(strings.ToUpper(m[1]), "SELECT") {
				out[m[2]] = true
			}
		}
	}
	return out
}

// checkSQLAgainstSchema asserts every iam_v2 table is SELECT-granted and every alias.column exists.
func checkSQLAgainstSchema(t *testing.T, report func(string, ...any), name, sql string, cols map[string]map[string]bool, grants map[string]bool) {
	t.Helper()
	aliases := map[string]string{}
	for _, m := range regexp.MustCompile(`iam_v2\.(\w+)\s+(?:AS\s+)?([a-z][a-z0-9_]*)\b`).FindAllStringSubmatch(sql, -1) {
		table, alias := m[1], m[2]
		if _, ok := cols[table]; !ok {
			report("%s: iam_v2.%s is not a table in the production baseline", name, table)
			continue
		}
		if !grants[table] {
			report("%s: svc_edged holds no SELECT on iam_v2.%s in the Gate-P allowlist", name, table)
		}
		switch alias {
		case "on", "where", "join", "left", "group", "order", "limit":
			continue
		}
		if prev, ok := aliases[alias]; ok && prev != table {
			report("%s: alias %s is used for both %s and %s; the column check would be ambiguous", name, alias, prev, table)
		}
		aliases[alias] = table
	}
	for _, m := range regexp.MustCompile(`\b([a-z][a-z0-9_]*)\.([a-z_][a-z0-9_]*)\b`).FindAllStringSubmatch(sql, -1) {
		table, ok := aliases[m[1]]
		if !ok {
			continue // a CTE alias (act, f, page, ss) or the schema name itself
		}
		if !cols[table][m[2]] {
			report("%s: %s.%s -- iam_v2.%s has no column %q in the production baseline", name, m[1], m[2], table, m[2])
		}
	}
}

func TestActivitySQLReadsOnlyGrantedTablesAndRealColumns(t *testing.T) {
	cols := baselineColumns(t)
	grants := edgedSelectGrants(t)
	if len(cols["purchases"]) == 0 || !cols["internet_package_revisions"]["currency_exponent"] {
		t.Fatal("the baseline parser found no purchases table or no currency_exponent; the check would pass vacuously")
	}
	f := mustFilter(t, "q=4202&source=GUEST_SELECTION&status=other&package_id=11111111-2222-3333-4444-555555555555")
	list, _ := activityListSQL(f, "t", "s")
	sum, _ := activitySummarySQL(f, "t", "s")
	// The checker must be able to fail: a wrong column and an ungranted table are both caught.
	var caught []string
	collect := func(format string, a ...any) { caught = append(caught, format) }
	checkSQLAgainstSchema(t, collect, "self-check",
		`SELECT p.created_at FROM iam_v2.purchases p JOIN iam_v2.vouchers v ON v.id = p.id`, cols, grants)
	if len(caught) < 2 {
		t.Fatalf("the schema checker missed a bogus column or an ungranted table (%d findings)", len(caught))
	}
	checkSQLAgainstSchema(t, t.Errorf, "activity list", list, cols, grants)
	checkSQLAgainstSchema(t, t.Errorf, "activity summary", sum, cols, grants)

	// The view must never read the tables that would identify a guest or a credential.
	for _, s := range []string{list, sum} {
		for _, forbidden := range []string{"stay_guests", "guest_access_accounts", "iam_v2.vouchers", "auth_contexts",
			"guest_principals", "iam_v2.devices", ".mac", "username", "display_name"} {
			if strings.Contains(s, forbidden) {
				t.Errorf("activity SQL reads %q, which no existing guest-activity or usage view exposes", forbidden)
			}
		}
	}
}

// ------------------------------------------------------------------------------- handler behaviour ----

type fakeCommerceRepo struct {
	iamv2.CommerceAdminRepository
	tx    *fakeCommerceTx
	found bool
	refs  []iamv2.DeletabilityReason
}

func (f *fakeCommerceRepo) WithTx(ctx context.Context, fn func(iamv2.CommerceAdminTx) error) error {
	return fn(f.tx)
}
func (f *fakeCommerceRepo) PackageReferences(context.Context, string, string, string) ([]iamv2.DeletabilityReason, bool, error) {
	return f.refs, f.found, nil
}
func (f *fakeCommerceRepo) PlanReferences(context.Context, string, string, string) ([]iamv2.DeletabilityReason, bool, error) {
	return f.refs, f.found, nil
}

type fakeCommerceTx struct {
	iamv2.CommerceAdminTx
}

func (fakeCommerceTx) PlanRevisionBelongs(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (fakeCommerceTx) CreatePackage(context.Context, string, string, string) (string, error) {
	return "", iamv2.ErrCodeExists
}
func (fakeCommerceTx) CreatePlan(context.Context, string, string, string) (string, error) {
	return "", iamv2.ErrCodeExists
}

func commerceTestServer(t *testing.T, repo *fakeCommerceRepo) *server {
	t.Helper()
	cfg := iamv2.CommerceConfig{MasterEnabled: true, AdminEnabled: true}
	a, err := iamv2.NewCommerceAdmin(cfg, repo, iamv2.NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	return &server{commerce: a, commerceCfg: cfg, tenantID: "tenant-A", siteID: "site-A"}
}

func TestAddWithATakenCodeAnswers409CodeExists(t *testing.T) {
	s := commerceTestServer(t, &fakeCommerceRepo{tx: &fakeCommerceTx{}})
	for _, tc := range []struct {
		path string
		h    http.HandlerFunc
		body string
	}{
		{"/commercial-packages", s.publishCommercialPackage,
			`{"code":"GOLD","service_plan_revision_id":"r","duration_policy":{"end_mode":"MANUAL_END"},"grant_tiers":[{"order":10,"grant":{}}],"create_only":true}`},
		{"/commercial-packages/plans", s.publishServicePlan, `{"code":"FAST","create_only":true}`},
	} {
		w := httptest.NewRecorder()
		tc.h(w, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: want 409, got %d %s", tc.path, w.Code, w.Body.String())
		}
		var body map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if body["error"] != "code_exists" || !strings.Contains(body["message"], "Edit") {
			t.Fatalf("%s: the refusal must be code_exists and say how to revise instead: %v", tc.path, body)
		}
	}
}

func TestDeletabilityRoutesNeverSayYes(t *testing.T) {
	n := int64(2)
	repo := &fakeCommerceRepo{found: true, refs: []iamv2.DeletabilityReason{{Code: "ENTITLEMENTS", Message: "2 grants", Count: &n}}}
	s := commerceTestServer(t, repo)
	r := chi.NewRouter()
	r.Mount("/commercial-packages", s.commercialPackagesRoutes())

	for _, path := range []string{
		"/commercial-packages/11111111-2222-3333-4444-555555555555/deletability",
		"/commercial-packages/plans/11111111-2222-3333-4444-555555555555/deletability",
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var d iamv2.Deletability
		if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		if d.Deletable || len(d.Reasons) != 2 || d.Reasons[1].Code != iamv2.ReasonDeleteRequiresSchemaChange {
			t.Fatalf("%s: %+v", path, d)
		}
	}

	// A malformed id is not a database error, and an unknown one is not found.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/commercial-packages/zzz/deletability", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("malformed id: want 404, got %d", w.Code)
	}
	repo.found = false
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/commercial-packages/11111111-2222-3333-4444-555555555555/deletability", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: want 404, got %d", w.Code)
	}
}

func TestActivityRefusesBadParamsBeforeTouchingTheDatabase(t *testing.T) {
	s := commerceTestServer(t, &fakeCommerceRepo{}) // s.db is nil: reaching it would panic
	w := httptest.NewRecorder()
	s.listPackageActivity(w, httptest.NewRequest(http.MethodGet, "/commercial-packages/activity?limit=0", nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", w.Code)
	}
	s.commerceCfg = iamv2.CommerceConfig{}
	w = httptest.NewRecorder()
	s.listPackageActivity(w, httptest.NewRequest(http.MethodGet, "/commercial-packages/activity", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin surface off: want 503, got %d", w.Code)
	}
}
