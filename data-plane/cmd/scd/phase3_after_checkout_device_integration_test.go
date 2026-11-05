//go:build integration

package main

// After Check-out Access admits only the stay's own devices; a new device is refused by the database's guard, and the refusal
// is recorded as itself (0208), never as an internal failure the monitor would count as an outage.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/onegate/onegate/data-plane/internal/metrics"
	"github.com/onegate/onegate/data-plane/internal/signinattempt"
)

func TestIntegration_Phase3_ANewDeviceAfterCheckoutIsRecordedAsSuch(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.met = metrics.New("test", prometheus.Labels{})
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	grant := func(res phase3Response, ip, mac string) phase3Response {
		body := map[string]any{"auth_context_id": res.AuthContextID, "device": map[string]string{"ip": ip, "mac": mac}}
		if res.LiveAccess {
			body["join_live"] = true
		} else {
			body["package_revision_id"] = f.pkgRev
		}
		raw, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
		var r phase3Response
		_ = json.Unmarshal(rec.Body.Bytes(), &r)
		return r
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

	// Device A is the stay's own device.
	a := grant(resolve(f.net.guestIP, f.net.mac, "00000008-0000-4000-8000-00000000000a"), f.net.guestIP, f.net.mac)
	if a.Outcome != outcomeVerified {
		t.Fatalf("device A was not granted: %+v", a)
	}
	// The stay's access becomes After Check-out Access, as the checkout conversion leaves it.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`) // entitlement terms are immutable to ordinary writers
	if tag, err := tx.Exec(ctx, `UPDATE iam_v2.entitlements SET end_mode = 'GRACE_AFTER_CHECKOUT' WHERE stay_id = $1::uuid AND status = 'ACTIVE'`,
		f.stay); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("convert the stay's access: %v (%d rows)", err, tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Device B never used the stay: refused by the guard, told so, and recorded as that refusal.
	macB := "02:00:00:bb:08:0b"
	refused := grant(resolve(f.net.otherIP, macB, "00000008-0000-4000-8000-00000000000b"), f.net.otherIP, macB)
	if refused.Outcome != outcomeNotVerified || refused.FailureClass != string(signinattempt.GuestAfterCheckoutDevice) {
		t.Fatalf("a new device after check-out must be told AFTER_CHECKOUT_DEVICE: %+v", refused)
	}
	if got := f.attemptResult(t, "00000008-0000-4000-8000-00000000000b"); got != string(signinattempt.AfterCheckoutNewDeviceRejected) {
		t.Fatalf("the refusal was recorded as %s, want AFTER_CHECKOUT_NEW_DEVICE_REJECTED", got)
	}
	// The eligibility rule is unchanged: B holds no place and no session on the stay's access.
	var placed, live int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_devices ed JOIN iam_v2.devices d ON d.id = ed.device_id
	     WHERE d.mac = $1::macaddr`, macB).Scan(&placed)
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.sessions WHERE mac = $1::macaddr AND ended IS NULL`, macB).Scan(&live)
	if placed != 0 || live != 0 {
		t.Fatalf("the refused device holds %d places and %d live sessions", placed, live)
	}
}
