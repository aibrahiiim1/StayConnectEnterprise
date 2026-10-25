//go:build integration

package main

import (
	"context"
	"testing"
)

func TestIntegration_NetworkSignin_StoresOnlyANarrowing(t *testing.T) {
	f := newAPI(t)
	net := f.seedNet(t, "Conference")
	path := "/network/guest-networks/" + net + "/sign-in"

	st, got := f.do(t, "GET", path, nil)
	if st != 200 || len(got["disabled_methods"].([]any)) != 0 || got["primary_method"] != "" {
		t.Fatalf("a network with no override = %d %v", st, got)
	}
	for name, body := range map[string]map[string]any{
		"unknown method":       {"disabled_methods": []string{"telepathy"}},
		"lead is switched off": {"disabled_methods": []string{"sms"}, "primary_method": "sms"},
		"unknown lead":         {"primary_method": "telepathy"},
		"remember too long":    {"remember_days": 400},
		"remember negative":    {"remember_days": -1},
	} {
		if st, _ := f.do(t, "PUT", path, body); st != 400 {
			t.Fatalf("%s = %d", name, st)
		}
	}

	days := 7
	st, saved := f.do(t, "PUT", path, map[string]any{"disabled_methods": []string{"social", "sms", "sms"}, "primary_method": "voucher", "remember_days": days})
	if st != 200 {
		t.Fatalf("save = %d %v", st, saved)
	}
	_, got = f.do(t, "GET", path, nil)
	off := got["disabled_methods"].([]any)
	if len(off) != 2 || off[0] != "sms" || off[1] != "social" || got["primary_method"] != "voucher" || got["remember_days"] != float64(7) {
		t.Fatalf("stored %v", got)
	}
	if f.auditTargetCount(t, "network.signin_changed", net) != 1 {
		t.Fatal("the change was not audited")
	}

	// Setting everything back to inheriting removes the override, so "never configured" and "inherit all" are one state.
	if st, _ := f.do(t, "PUT", path, map[string]any{"disabled_methods": []string{}}); st != 200 {
		t.Fatal("clear")
	}
	var n int
	_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.network_signin_overrides WHERE network_id=$1::uuid`, net).Scan(&n)
	if n != 0 {
		t.Fatal("an empty override was kept")
	}
	if st, _ := f.do(t, "GET", "/network/guest-networks/00000000-0000-0000-0000-000000000000/sign-in", nil); st != 404 {
		t.Fatalf("a network that does not exist = %d", st)
	}
}
