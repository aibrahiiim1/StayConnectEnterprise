package api

import (
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
)

// Retention never takes what licensing needs: a still-valid revoked licence stays on the revocation list, and an
// appliance's newest licence is kept so the next version is numbered above any it has seen (it refuses a lower one).
func TestRetentionKeepsWhatLicensingNeeds(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Licence Hotel")
	app := decode(t, f.register(newBox("SC-TEST-0056", "hwf-56")))["appliance_id"].(string)
	f.activate(app, cust, site, 10)
	var v1 string
	_ = f.pool.QueryRow(f.ctx, `SELECT id::text FROM licenses WHERE $1::uuid = ANY(appliance_ids) AND status='active'`, app).Scan(&v1)
	if rec := f.operatorCallAs(f.session, f.base.licenseAction("revoke"), http.MethodPost, map[string]string{"id": v1},
		map[string]any{"reason": "unpaid"}); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	issue := func() (string, int64) {
		t.Helper()
		doc, _, err := f.base.Lic.Issue(f.ctx, licensing.IssueParams{ApplianceID: app, ValidFor: 30 * 24 * time.Hour,
			MaxConcurrentOnlineGuests: 10, CreatedBy: f.session.OperatorID})
		if err != nil {
			t.Fatal(err)
		}
		return doc.LicenseID, doc.LicenseVersion
	}
	v2, v2ver := issue()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(f.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	retain := func() {
		t.Helper()
		if _, err := ApplyRetention(f.ctx, f.base); err != nil {
			t.Fatal(err)
		}
	}
	exists := func(id string) bool { return f.count(`SELECT count(*) FROM licenses WHERE id=$1`, id) == 1 }

	exec(`UPDATE licenses SET revoked_at = now() - interval '7 years', created_at = now() - interval '8 years',
	      revocation_delivered_at = NULL WHERE id = $1`, v1)
	retain()
	if !exists(v1) {
		t.Fatal("a revoked licence that is still valid was deleted: an offline appliance would never learn of the revocation")
	}
	if !slices.Contains(f.fetchLicence(app), v1) {
		t.Fatal("the kept revocation is not on the appliance's revocation list")
	}
	exec(`UPDATE licenses SET valid_until = now() - interval '7 years', revocation_delivered_at = NULL WHERE id = $1`, v1)
	retain()
	if exists(v1) {
		t.Fatal("a revoked licence past its validity and the history period was kept")
	}

	exec(`UPDATE licenses SET valid_until = now() - interval '7 years', created_at = now() - interval '8 years',
	      issued_at = now() - interval '8 years' WHERE id = $1`, v2)
	retain()
	if !exists(v2) {
		t.Fatal("the appliance's newest licence was deleted by retention")
	}
	if _, v3ver := issue(); v3ver <= v2ver {
		t.Fatalf("the licence issued after retention has version %d, not above the %d the appliance has seen", v3ver, v2ver)
	}
}
