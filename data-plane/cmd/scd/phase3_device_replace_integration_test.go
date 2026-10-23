//go:build integration

package main

// A room guest at the stay's device limit can replace a device (contract ONEGATE_DEVICE_MANAGEMENT §3).

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
)

func TestIntegration_Phase3_ARoomGuestAtTheLimitReplacesADevice(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.met = metrics.New("test", prometheus.Labels{})
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`) // revisions are immutable
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.service_plan_revisions SET max_concurrent_devices = 1 WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	grant := func(ac, ip, mac string) (phase3GrantResp, phase3Response) {
		raw, _ := json.Marshal(map[string]any{"auth_context_id": ac, "package_revision_id": f.pkgRev,
			"device": map[string]string{"ip": ip, "mac": mac}})
		rec := httptest.NewRecorder()
		f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		var g phase3GrantResp
		var r phase3Response
		_ = json.Unmarshal(rec.Body.Bytes(), &g)
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return g, r
	}
	resolve := func(ip, mac, reqID string) phase3Response {
		body := f.resolveBody("412", "Okonkwo", "", reqID)
		body["device"] = map[string]string{"ip": ip, "mac": mac}
		_, res := post(t, f.p3.resolveHandler, body)
		if res.Outcome != outcomeVerified || res.AuthContextID == "" {
			t.Fatalf("the stay did not verify: %+v", res)
		}
		return res
	}
	call := func(h http.HandlerFunc, body map[string]any) map[string]any {
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		out["_status"] = float64(rec.Code)
		return out
	}

	// Device A takes the stay's only place.
	resA := resolve(f.net.guestIP, f.net.mac, "00000006-0000-4000-8000-00000000000a")
	gA, _ := grant(resA.AuthContextID, f.net.guestIP, f.net.mac)
	if gA.Outcome != outcomeVerified || gA.EntitlementID == "" {
		t.Fatalf("device A was not granted: %+v", gA)
	}
	var deviceA string
	if err := f.pool.QueryRow(ctx, `SELECT device_id::text FROM iam_v2.sessions WHERE id=$1`, gA.SessionID).Scan(&deviceA); err != nil {
		t.Fatal(err)
	}

	// Device B, same stay: refused at the limit, and the refusal names B's device and network for portald.
	macB := "02:00:00:bb:06:0b"
	resB := resolve(f.net.otherIP, macB, "00000006-0000-4000-8000-00000000000b")
	_, refused := grant(resB.AuthContextID, f.net.otherIP, macB)
	if refused.Outcome != outcomeNotVerified || refused.FailureClass != "DEVICE_LIMIT" || refused.DeviceID == "" || refused.GuestNetworkID != f.network {
		t.Fatalf("the device-limit refusal must name the placed device and network: %+v", refused)
	}

	// The replacement page lists the stay's devices for B's verified sign-in...
	list := call(f.srv.clientDevicesList, map[string]any{"ip": f.net.otherIP, "mac": macB, "auth_context_id": resB.AuthContextID})
	if list["outcome"] != "LISTED" || list["limit"] != float64(1) {
		t.Fatalf("list for the room guest's new device: %v", list)
	}
	// ...and B replaces A.
	rep := call(f.srv.clientDevicesReplace, map[string]any{"ip": f.net.otherIP, "mac": macB,
		"auth_context_id": resB.AuthContextID, "device_id": deviceA})
	if rep["outcome"] != "REPLACED" || rep["entitlement_id"] != gA.EntitlementID || rep["device_id"] != refused.DeviceID {
		t.Fatalf("replace: %v", rep)
	}

	// Activation admits B to the stay's entitlement on the strength of its own room sign-in.
	act := call(f.srv.activateIAMv2Session, map[string]any{"entitlement_id": gA.EntitlementID, "device_id": refused.DeviceID,
		"ip": f.net.otherIP, "mac": macB, "auth_context_id": resB.AuthContextID})
	if act["_status"] != float64(200) || act["session_id"] == nil || act["session_id"] == "" {
		t.Fatalf("activation of the replacing device: %v", act)
	}
	var stayOf string
	if err := f.pool.QueryRow(ctx, `SELECT e.stay_id::text FROM iam_v2.sessions s JOIN iam_v2.entitlements e ON e.id=s.entitlement_id
	     WHERE s.id=$1::uuid`, act["session_id"]).Scan(&stayOf); err != nil || stayOf != f.stay {
		t.Fatalf("the new session is not on the stay's entitlement: %q %v", stayOf, err)
	}
	// One entitlement for the stay: replacing a device never creates another.
	var n int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay).Scan(&n)
	if n != 1 {
		t.Fatalf("the stay has %d entitlements, want 1", n)
	}
}
