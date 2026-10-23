package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

func (f *fixture) fetchLicence(appID string) []string {
	f.t.Helper()
	lb := &LicensesBase{Base: f.base, Svc: f.base.Lic}
	req := httptest.NewRequest(http.MethodGet, "/v1/appliance/license", nil)
	req = req.WithContext(auth.ContextWithAppliance(req.Context(), &auth.ApplianceIdent{ApplianceID: appID}))
	rec := httptest.NewRecorder()
	lb.ApplianceLicenseHandler(rec, req)
	if rec.Code != http.StatusOK {
		f.t.Fatalf("licence fetch: %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Revoked []string `json:"revoked"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Revoked
}

// Deleting a site must not erase a revocation the appliance has not received; it is refused until a licence fetch carries it.
func TestSiteDeleteWaitsForRevocationDelivery(t *testing.T) {
	f := newFixture(t)
	cust, siteA := f.customerSite("Two Sites")
	siteB, err := createSite(f.ctx, f.pool, cust, "Second", "", "UTC", "", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := f.register(newBox("SC-TEST-0005", "hwf-5"))
	app := decode(t, rec)["appliance_id"].(string)
	f.activate(app, cust, siteA, 20)
	var licID string
	_ = f.pool.QueryRow(f.ctx, `SELECT id::text FROM licenses WHERE $1::uuid = ANY(appliance_ids) AND status='active'`, app).Scan(&licID)

	rec = f.operatorCallAs(f.session, f.base.licenseAction("revoke"), http.MethodPost, map[string]string{"id": licID},
		map[string]any{"reason": "unpaid"})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	rec = f.operatorCall(f.base.move, http.MethodPost, app, map[string]any{"customer_id": cust, "site_id": siteB, "reason": "moved"})
	if rec.Code != http.StatusOK {
		t.Fatalf("move: %d %s", rec.Code, rec.Body.String())
	}

	del := func() *httptest.ResponseRecorder {
		return f.operatorCallAs(f.session, f.base.deleteSite, http.MethodDelete, map[string]string{"id": siteA},
			map[string]any{"confirm": "Two Sites site", "reason": "closed"})
	}
	rec = del()
	if rec.Code != http.StatusConflict {
		t.Fatalf("site delete must wait for the revocation to be delivered, got %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM licenses WHERE id=$1 AND status='revoked'`, licID); n != 1 {
		t.Fatal("the revoked licence row must survive the refused delete")
	}

	got := f.fetchLicence(app)
	if len(got) != 1 || got[0] != licID {
		t.Fatalf("the licence fetch must carry the revocation, got %v", got)
	}
	if n := f.count(`SELECT count(*) FROM licenses WHERE id=$1 AND revocation_delivered_at IS NOT NULL`, licID); n != 1 {
		t.Fatal("the delivered revocation must be recorded")
	}
	if rec = del(); rec.Code != http.StatusNoContent {
		t.Fatalf("after delivery the site can be deleted, got %d %s", rec.Code, rec.Body.String())
	}
}
