//go:build integration

package main

import (
	"context"
	"testing"
)

// The kernel half of staff End Package, as svc_scd: an ended session's address is withdrawn; a session still live, an address
// another live session now holds, and a session of another site are never touched.
func TestIntegration_PackageWithdrawTouchesOnlyTheEndedPackagesOwnAddresses(t *testing.T) {
	f := newSSFixture(t)
	ctx := context.Background()
	end := func(ent string) {
		t.Helper()
		if _, err := f.owner.Exec(ctx, `SELECT iam_v2.p4_end_access_funded_by($1::uuid, 'ADMIN')`, ent); err != nil {
			t.Fatalf("end: %v", err)
		}
	}
	entA, _, ended := f.seedEntitlementSession(t, f.seedAccount(t, "wa-"+randHex(3)), f.ip(10), "02:aa:00:00:00:10")
	_, _, live := f.seedEntitlementSession(t, f.seedAccount(t, "wb-"+randHex(3)), f.ip(11), "02:aa:00:00:00:11")
	entC, _, handedOver := f.seedEntitlementSession(t, f.seedAccount(t, "wc-"+randHex(3)), f.ip(12), "02:aa:00:00:00:12")
	end(entA)
	end(entC)
	// Another guest now holds the address the ended session had.
	f.seedEntitlementSession(t, f.seedAccount(t, "wd-"+randHex(3)), f.ip(12), "02:aa:00:00:00:13")
	foreign := "0badc0de-0000-4000-8000-" + randHex(6)

	code, out := f.post(t, f.s.withdrawEndedSessions, map[string]any{"sessions": []string{ended, live, handedOver, foreign}})
	if code != 200 {
		t.Fatalf("withdraw = %d %v", code, out)
	}
	got := map[string]string{}
	for _, r := range out["results"].([]any) {
		m := r.(map[string]any)
		got[m["session"].(string)] = m["state"].(string)
	}
	want := map[string]string{ended: "withdrawn", live: "not_ended", handedOver: "address_reused", foreign: "failed"}
	for id, w := range want {
		if got[id] != w {
			t.Fatalf("session %s: %q, want %q (all: %v)", id, got[id], w, got)
		}
	}
	if c, _ := f.post(t, f.s.withdrawEndedSessions, map[string]any{"sessions": []string{}}); c != 400 {
		t.Fatalf("an empty request = %d", c)
	}
}
