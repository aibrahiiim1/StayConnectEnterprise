//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A returning room guest joins the stay's live access even after its package was edited, and is never offered it to buy again.
func TestIntegration_Phase3Auth_RoomSignInJoinsTheStaysLiveAccess(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()
	grant := func(body map[string]any) phase3GrantResp {
		t.Helper()
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		var out phase3GrantResp
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	phone := map[string]string{"ip": f.net.guestIP, "mac": f.net.mac}
	tablet := map[string]string{"ip": f.net.otherIP, "mac": "02:00:00:bb:00:41"}

	_, first := post(t, f.p3.resolveHandler, f.resolveBody("412", "Okonkwo", "", "00000041-0000-4000-8000-000000000000"))
	if first.Outcome != outcomeVerified || first.LiveAccess || len(first.Offers) == 0 {
		t.Fatalf("a stay without access must be offered packages: %+v", first)
	}
	if out := grant(map[string]any{"auth_context_id": first.AuthContextID, "join_live": true, "device": phone}); out.Outcome == outcomeVerified {
		t.Fatalf("a join was granted with no live access to join: %+v", out)
	}
	bought := grant(map[string]any{"auth_context_id": first.AuthContextID, "package_revision_id": f.pkgRev, "device": phone})
	if bought.Outcome != outcomeVerified || bought.EntitlementID == "" {
		t.Fatalf("the refused join consumed the context: %+v", bought)
	}

	// The operator edits the package: the stay's access now sits on a superseded revision.
	if _, err := f.pool.Exec(ctx, `WITH r AS (
		INSERT INTO iam_v2.internet_package_revisions (id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,
		       package_type,price_minor,settlement_methods,duration_policy)
		SELECT gen_random_uuid(),tenant_id,site_id,package_id,2,service_plan_revision_id,package_type,price_minor,
		       settlement_methods,duration_policy FROM iam_v2.internet_package_revisions WHERE id=$1 RETURNING id, package_id)
		UPDATE iam_v2.internet_packages ip SET current_revision_id = r.id FROM r WHERE ip.id = r.package_id`, f.pkgRev); err != nil {
		t.Fatalf("supersede the package revision: %v", err)
	}

	body := f.resolveBody("412", "Okonkwo", "", "00000042-0000-4000-8000-000000000000")
	body["device"] = tablet
	_, again := post(t, f.p3.resolveHandler, body)
	if again.Outcome != outcomeVerified || !again.LiveAccess || len(again.Offers) != 0 {
		t.Fatalf("a room sign-in on a stay with live access must join it, not list packages: %+v", again)
	}
	joined := grant(map[string]any{"auth_context_id": again.AuthContextID, "join_live": true, "device": tablet})
	if joined.Outcome != outcomeVerified || joined.SessionID == "" || joined.EntitlementID != bought.EntitlementID {
		t.Fatalf("the second device did not join the stay's access %s: %+v", bought.EntitlementID, joined)
	}
	for name, sql := range map[string]string{
		"purchases":    `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1`,
		"entitlements": `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`,
	} {
		var n int
		if err := f.pool.QueryRow(ctx, sql, f.stay).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if n != 1 {
			t.Fatalf("joining wrote %d %s, want the one from the first purchase", n, name)
		}
	}
}
