//go:build integration

package main

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// Many wrong room sign-ins on one network from many devices pause room sign-in there; staff can confirm a
// genuine guest's device or end the pause, and the burst a pause answered does not start the next one.
func TestIntegration_NetworkPause_StaffConfirmAndEnd(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	gn := f.seedNet(t, "Guest Wi-Fi pause")

	// Low numbers so the test is short. The settings themselves are bounded and audited.
	if st, _ := f.do(t, http.MethodPut, "/guest-signin-protection/network",
		map[string]any{"network_max_failures": 5, "network_window_seconds": 600, "network_pause_seconds": 900,
			"reason": "too low", "password": f.password}); st != http.StatusBadRequest {
		t.Fatalf("a threshold below 10 was accepted: %d", st)
	}
	if st, _ := f.do(t, http.MethodPut, "/guest-signin-protection/network",
		map[string]any{"network_max_failures": 10, "network_window_seconds": 600, "network_pause_seconds": 900,
			"reason": "test threshold", "password": "wrong"}); st != http.StatusUnauthorized {
		t.Fatalf("a wrong password changed the settings: %d", st)
	}
	st, body := f.do(t, http.MethodPut, "/guest-signin-protection/network",
		map[string]any{"network_max_failures": 10, "network_window_seconds": 600, "network_pause_seconds": 900,
			"reason": "test threshold", "password": f.password})
	if st != http.StatusOK || body["network_max_failures"] != float64(10) || body["is_default"] != false {
		t.Fatalf("save = %d %v", st, body)
	}

	gate := func(mac string) bool {
		t.Helper()
		var paused bool
		if err := f.pool.QueryRow(ctx, `SELECT paused FROM iam_v2.guest_signin_network_gate($1::uuid,$2::uuid,$3::uuid,$4::macaddr)`,
			f.tenant, f.site, gn, mac).Scan(&paused); err != nil {
			t.Fatal(err)
		}
		return paused
	}
	wrong := func(i int) (pausedNow bool) {
		t.Helper()
		mac := fmt.Sprintf("02:00:00:00:%02x:%02x", i/256, i%256)
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.sign_in_attempts (tenant_id,site_id,result,verifier_kind,
			guest_network_id,guest_network_name,submitted_room,device_mac) VALUES ($1,$2,'ROOM_NOT_IN_MIRROR','UNKNOWN',$3::uuid,'Guest Wi-Fi pause',$4,$5::macaddr)`,
			f.tenant, f.site, gn, fmt.Sprint(100+i), mac); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(ctx, `SELECT paused_now FROM iam_v2.guest_signin_note_network_failure($1::uuid,$2::uuid,$3::uuid,'Guest Wi-Fi pause')`,
			f.tenant, f.site, gn).Scan(&pausedNow); err != nil {
			t.Fatal(err)
		}
		return pausedNow
	}

	// Nine wrong room sign-ins from nine devices: no pause. The tenth pauses the network.
	for i := 1; i <= 9; i++ {
		if wrong(i) {
			t.Fatalf("paused after %d", i)
		}
	}
	if gate("02:00:00:00:ff:01") {
		t.Fatal("paused before the threshold")
	}
	if !wrong(10) {
		t.Fatal("the tenth wrong room sign-in did not pause the network")
	}
	guest, stranger := "02:00:00:00:00:03", "02:00:00:00:ff:02"
	if !gate(guest) || !gate(stranger) {
		t.Fatal("the pause does not hold for every device")
	}

	// Staff see it.
	st, body = f.do(t, http.MethodGet, "/guest-signin-restrictions/network-pauses", nil)
	pauses, _ := body["pauses"].([]any)
	if st != http.StatusOK || len(pauses) != 1 {
		t.Fatalf("pauses = %d %v", st, body)
	}
	pause := pauses[0].(map[string]any)
	id := pause["id"].(string)
	if pause["failure_count"] != float64(10) || pause["guest_network"] != "Guest Wi-Fi pause" {
		t.Fatalf("pause = %v", pause)
	}
	st, body = f.do(t, http.MethodGet, "/guest-signin-restrictions/network-pauses/"+id+"/devices", nil)
	devices, _ := body["devices"].([]any)
	if st != http.StatusOK || len(devices) != 10 {
		t.Fatalf("devices = %d %v", st, body)
	}

	// A genuine guest at the desk: confirmed, they may try again; nobody else may. (Audit rows are counted as a
	// difference: the test database keeps earlier runs' rows for the same device address.)
	confirmedBefore := f.auditTargetCount(t, "guest_signin_network_pause.confirm_device", guest)
	if st, _ := f.do(t, http.MethodPost, "/guest-signin-restrictions/network-pauses/"+id+"/confirm",
		map[string]any{"device_mac": guest}); st != http.StatusBadRequest {
		t.Fatalf("confirmed without a reason: %d", st)
	}
	st, body = f.do(t, http.MethodPost, "/guest-signin-restrictions/network-pauses/"+id+"/confirm",
		map[string]any{"device_mac": guest, "reason": "guest at reception, room 103, passport seen"})
	if st != http.StatusOK || body["confirmed"] != true {
		t.Fatalf("confirm = %d %v", st, body)
	}
	if gate(guest) {
		t.Fatal("the confirmed guest is still paused")
	}
	if !gate(stranger) {
		t.Fatal("confirming one guest lifted the pause for everyone")
	}
	if f.auditTargetCount(t, "guest_signin_network_pause.confirm_device", guest) != confirmedBefore+1 {
		t.Fatal("the confirmation is not audited")
	}
	var access int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&access)
	if access != 0 {
		t.Fatal("confirming a device granted access")
	}

	// A false alarm: staff end the pause; then the same burst does not pause it again.
	if st, _ := f.do(t, http.MethodPost, "/guest-signin-restrictions/network-pauses/"+id+"/end",
		map[string]any{"reason": "tour group mistyping their surname"}); st != http.StatusOK {
		t.Fatalf("end = %d", st)
	}
	if gate(stranger) {
		t.Fatal("the ended pause still holds")
	}
	if f.auditTargetCount(t, "guest_signin_network_pause.end", id) != 1 {
		t.Fatal("ending the pause is not audited")
	}
	if wrong(11) {
		t.Fatal("the failures the ended pause already answered started a new one")
	}
	if st, _ := f.do(t, http.MethodPost, "/guest-signin-restrictions/network-pauses/"+id+"/end",
		map[string]any{"reason": "again"}); st != http.StatusNotFound {
		t.Fatalf("an ended pause was ended again: %d", st)
	}

	// The desk may act on a pause but not change the numbers.
	desk := newAPIIn(t, f.tenant, "front_office_operator")
	if st, _ := desk.do(t, http.MethodGet, "/guest-signin-restrictions/network-pauses", nil); st != http.StatusOK {
		t.Fatalf("the desk cannot see pauses: %d", st)
	}
	if st, _ := desk.do(t, http.MethodPut, "/guest-signin-protection/network",
		map[string]any{"network_max_failures": 20, "network_window_seconds": 600, "network_pause_seconds": 900,
			"reason": "desk try", "password": desk.password}); st != http.StatusForbidden {
		t.Fatalf("the desk changed the pause settings: %d", st)
	}

	// Confirmations go a day after their pause.
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.guest_signin_network_pauses SET started_at = now() - interval '3 days',
		expires_at = now() - interval '2 days', ended_at = now() - interval '2 days' WHERE id = $1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.guest_signin_network_prune()`); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_signin_device_passes WHERE pause_id = $1::uuid`, id).Scan(&left)
	if left != 0 {
		t.Fatal("a confirmation outlived its pause by more than a day")
	}
}
