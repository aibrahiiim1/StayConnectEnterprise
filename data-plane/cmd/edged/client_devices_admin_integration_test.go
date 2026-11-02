//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func (f *apiFixture) getDevices(t *testing.T, search string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", f.srv.URL+"/edge/v1/client-devices", bytes.NewReader(nil))
	if search != "" {
		req.Header.Set(deviceSearchHeader, search)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.sessTok})
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func deviceOfSession(t *testing.T, f *apiFixture, sessionID string) string {
	t.Helper()
	var d string
	if err := f.pool.QueryRow(context.Background(), `SELECT device_id::text FROM iam_v2.sessions WHERE id=$1::uuid`, sessionID).Scan(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestIntegration_ClientDevices_FindInspectReleaseAndPrepare(t *testing.T) {
	f := newAPI(t)
	_, client := newFakeSCD(t, f)
	f.app.scd = client
	ctx := context.Background()

	_, ent, sessions := seedAccountWithSessions(t, f, "dev-owner", 9000, "10.92.0.11")
	dev := deviceOfSession(t, f, sessions[0].id)
	before := readEntState(t, f, ent)

	st, body := f.getDevices(t, "dev-owner")
	data, _ := body["data"].([]any)
	if st != 200 || len(data) != 1 {
		t.Fatalf("search by account = %d %v", st, body)
	}
	row := data[0].(map[string]any)
	cur, _ := row["current"].(map[string]any)
	if row["id"] != dev || row["online"] != true || cur["entitlement_id"] != ent || cur["slot"] != "AUTHORIZED" ||
		cur["subject_kind"] != "ACCOUNT" || cur["subject_label"] != "dev-owner" {
		t.Fatalf("device row = %v", row)
	}
	if _, none := f.getDevices(t, "nobody-matches-this"); len(none["data"].([]any)) != 0 {
		t.Fatal("an unmatched search returns nothing")
	}

	st, body = f.do(t, "GET", "/client-devices/"+dev, nil)
	pkgs, _ := body["packages"].([]any)
	sess, _ := body["sessions"].([]any)
	if st != 200 || len(pkgs) != 1 || len(sess) != 1 || body["remembered_signins"] != float64(0) {
		t.Fatalf("device detail = %d %v", st, body)
	}

	if st, _ := f.do(t, "POST", "/client-devices/"+dev+"/release", map[string]any{"entitlement_id": ent}); st != 400 {
		t.Fatal("a release without a reason must be refused")
	}
	st, body = f.do(t, "POST", "/client-devices/"+dev+"/release", map[string]any{"entitlement_id": ent, "reason": "guest moved to a new phone"})
	if st != 200 || body["status"] != "released" {
		t.Fatalf("release = %d %v", st, body)
	}
	if got := sessionStates(t, f, sessions)["10.92.0.11"]; got[0] != "ended" {
		t.Fatalf("the device must be disconnected before its place is released: %v", got)
	}
	var slot string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM iam_v2.entitlement_devices WHERE entitlement_id=$1::uuid AND device_id=$2::uuid`, ent, dev).Scan(&slot)
	if slot != "DISCONNECTED" {
		t.Fatalf("the place is still held: %s", slot)
	}
	after := readEntState(t, f, ent)
	if after.status != before.status || after.window != before.window || after.dataUsed != before.dataUsed {
		t.Fatalf("releasing a place changed the package: %+v -> %+v", before, after)
	}
	var audited int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.device_slot_actions WHERE device_id=$1::uuid AND actor='STAFF'
	     AND action='RELEASE' AND outcome='OK' AND operator_id=$2::uuid AND reason='guest moved to a new phone'`, dev, f.operator).Scan(&audited)
	if audited != 1 || auditCount(t, f, "client_device.place_released", dev) != 1 {
		t.Fatal("the release is recorded in the database and in the operator audit")
	}
	if st, body := f.do(t, "POST", "/client-devices/"+dev+"/release", map[string]any{"entitlement_id": ent, "reason": "again please"}); st != 409 || body["error"] != "refused_already_released" {
		t.Fatalf("a second release = %d %v", st, body)
	}

	_, ent2, s2 := seedAccountWithSessions(t, f, "dev-tester", 1, "10.92.0.21")
	dev2 := deviceOfSession(t, f, s2[0].id)
	st, body = f.do(t, "POST", "/client-devices/"+dev2+"/prepare-test", map[string]any{"reason": "PRE-LIVE sign-in test"})
	if st != 200 || body["status"] != "prepared" || body["disconnected_sessions"] != float64(1) || body["places_released"] != float64(1) {
		t.Fatalf("prepare = %d %v", st, body)
	}
	_ = f.pool.QueryRow(ctx, `SELECT status FROM iam_v2.entitlement_devices WHERE entitlement_id=$1::uuid AND device_id=$2::uuid`, ent2, dev2).Scan(&slot)
	var signins int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.device_slot_actions WHERE device_id=$1::uuid AND action='REQUIRE_SIGNIN'`, dev2).Scan(&signins)
	if slot != "DISCONNECTED" || signins != 1 {
		t.Fatalf("prepare: slot %s, sign-in actions %d", slot, signins)
	}
	var stillThere int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE id=$1::uuid`, ent2).Scan(&stillThere)
	if stillThere != 1 {
		t.Fatal("preparing a device deletes nothing")
	}
}

func TestIntegration_ClientDevices_TheDailyChangeLimitSetting(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "GET", "/device-settings/", nil)
	set, _ := body["settings"].(map[string]any)
	if st != 200 || set["client_device_changes_per_day"] != float64(3) || body["config_version"] != float64(0) {
		t.Fatalf("defaults = %d %v", st, body)
	}
	if st, _ := f.do(t, "PUT", "/device-settings/", map[string]any{"client_device_changes_per_day": 11, "reason": "too many", "password": f.password}); st != 400 {
		t.Fatal("11 is above the hard maximum")
	}
	if st, _ := f.do(t, "PUT", "/device-settings/", map[string]any{"client_device_changes_per_day": 5, "reason": "conference", "password": "wrong"}); st != 401 {
		t.Fatal("a wrong step-up password saved the setting")
	}
	st, body = f.do(t, "PUT", "/device-settings/", map[string]any{"client_device_changes_per_day": 5, "reason": "conference week", "password": f.password})
	if st != 200 || body["config_version"] != float64(1) {
		t.Fatalf("save = %d %v", st, body)
	}
	st, body = f.do(t, "GET", "/device-settings/", nil)
	ch, _ := body["changes"].([]any)
	if set, _ := body["settings"].(map[string]any); set["client_device_changes_per_day"] != float64(5) || len(ch) != 1 {
		t.Fatalf("after save = %v", body)
	}
	desk := newAPIIn(t, f.tenant, "front_office_operator")
	if st, _ := desk.do(t, "PUT", "/device-settings/", map[string]any{"client_device_changes_per_day": 2, "reason": "desk try", "password": desk.password}); st != 403 {
		t.Fatalf("the desk changed the setting: %d", st)
	}
	if st, _ := desk.do(t, "PUT", "/device-settings/", map[string]any{"client_device_changes_per_day": 2}); st != 403 {
		t.Fatalf("the desk changed the setting by confirming: %d", st)
	}

	// Confirm-only (PO, 2026-11-02): the console's warning is the confirmation; no reason or password is asked. The
	// change is still attributed and recorded with the old and new values.
	st, body = f.do(t, "PUT", "/device-settings/", map[string]any{"client_device_changes_per_day": 4})
	if st != 200 || body["config_version"] != float64(2) {
		t.Fatalf("a confirmed change without reason or password = %d %v", st, body)
	}
	var by, why string
	var oldN, newN int
	if err := f.pool.QueryRow(context.Background(), `SELECT changed_by, change_reason, old_client_device_changes_per_day,
		new_client_device_changes_per_day FROM iam_v2.device_settings_changes WHERE site_id=$1 ORDER BY changed_at DESC LIMIT 1`,
		f.site).Scan(&by, &why, &oldN, &newN); err != nil {
		t.Fatal(err)
	}
	if by == "" || why != defaultSettingReason || oldN != 5 || newN != 4 {
		t.Fatalf("the confirmed change was recorded as by=%q reason=%q %d->%d", by, why, oldN, newN)
	}
}
