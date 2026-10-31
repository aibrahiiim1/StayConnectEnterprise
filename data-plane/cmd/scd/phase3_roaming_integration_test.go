//go:build integration

package main

// Room sign-in across networks: a device that moves to another network gets a session at its new address (the firewall admits an address), and a network
// no PMS interface is mapped to offers no room sign-in at all.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// addNetwork creates another enabled client network on the fixture's site, optionally mapped to its PMS interface.
func (f *authFixture) addNetwork(t *testing.T, mapped bool) (fixtureNet, string, string) {
	t.Helper()
	ctx := context.Background()
	n := claimFixtureNet(t, f.pool)
	var id, bridge string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO public.guest_networks
		  (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
		VALUES (gen_random_uuid(),$1,$2,'second','q'||substr(md5(gen_random_uuid()::text),1,12),'c'||substr(md5(gen_random_uuid()::text),1,12),
		        ($3::text)::inet,($4::text)::inet,($5::text)::cidr,true)
		RETURNING id::text, bridge_name`, f.tenant, f.site, n.gateway+"/24", n.gateway, n.subnet).Scan(&id, &bridge); err != nil {
		t.Fatalf("second network: %v", err)
	}
	if mapped {
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id,is_default)
			VALUES ($1,$2,$3,$4,true)`, f.tenant, f.site, id, f.iface); err != nil {
			t.Fatalf("map the second network: %v", err)
		}
	}
	return n, id, bridge
}

func (f *authFixture) signInAt(t *testing.T, ip, reqID string) (phase3Response, phase3GrantResp) {
	t.Helper()
	body := map[string]any{"room": "412", "last_name": "Okonkwo", "request_id": reqID, "device": map[string]string{"ip": ip, "mac": f.net.mac}}
	_, res := post(t, f.p3.resolveHandler, body)
	var granted phase3GrantResp
	if res.Outcome != outcomeVerified {
		return res, granted
	}
	raw, _ := json.Marshal(map[string]any{
		"auth_context_id": res.AuthContextID, "package_revision_id": f.pkgRev,
		"device": map[string]string{"ip": ip, "mac": f.net.mac},
	})
	rec := httptest.NewRecorder()
	f.p3.grantHandler(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(raw)))
	if err := json.Unmarshal(rec.Body.Bytes(), &granted); err != nil {
		t.Fatalf("undecodable grant %q: %v", rec.Body.String(), err)
	}
	return res, granted
}

type sessionRow struct {
	id, ip, iface, endReason string
	ended                    bool
}

func (f *authFixture) sessionsOf(t *testing.T, entitlement string) []sessionRow {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT id::text, COALESCE(host(ip),''), COALESCE(ingress_interface,''), COALESCE(end_reason,''), ended IS NOT NULL
		  FROM iam_v2.sessions WHERE entitlement_id=$1 ORDER BY started, id`, entitlement)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []sessionRow
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.id, &r.ip, &r.iface, &r.endReason, &r.ended); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// The bug this pins: the grant found the device's open session by entitlement and device alone, so a device that had moved was told "verified" and handed
// its old session, which the firewall authorizes at the OLD address.
func TestIntegration_Phase3Auth_ADeviceThatMovedNetworkGetsASessionAtItsNewAddress(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	res1, first := f.signInAt(t, f.net.guestIP, "00000a01-0000-4000-8000-000000000000")
	if res1.Outcome != outcomeVerified || first.SessionID == "" {
		t.Fatalf("setup: %+v %+v", res1, first)
	}
	second, secondNet, secondBridge := f.addNetwork(t, true)

	res2, moved := f.signInAt(t, second.guestIP, "00000a02-0000-4000-8000-000000000000")
	if res2.Outcome != outcomeVerified || moved.Outcome != outcomeVerified {
		t.Fatalf("the device was not signed in on its new network: %+v %+v", res2, moved)
	}
	if moved.SessionID == first.SessionID {
		t.Fatal("the device was handed its old session, which is authorized at the old address")
	}
	if moved.EntitlementID != first.EntitlementID {
		t.Fatalf("the allowance did not follow the device: %s vs %s", moved.EntitlementID, first.EntitlementID)
	}
	byID := map[string]sessionRow{}
	for _, r := range f.sessionsOf(t, first.EntitlementID) {
		byID[r.id] = r
	}
	old, now := byID[first.SessionID], byID[moved.SessionID]
	if !old.ended || old.endReason != "ADDRESS_CHANGED" {
		t.Fatalf("the old session is %+v, want ended with ADDRESS_CHANGED", old)
	}
	if now.ended || now.ip != second.guestIP || now.iface != secondBridge {
		t.Fatalf("the new session is %+v, want live at %s on %s (network %s)", now, second.guestIP, secondBridge, secondNet)
	}
}

// A retry from the same address on the same network still returns the one session.
func TestIntegration_Phase3Auth_ASignInRetriedAtTheSameAddressKeepsItsSession(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	_, first := f.signInAt(t, f.net.guestIP, "00000b01-0000-4000-8000-000000000000")
	_, again := f.signInAt(t, f.net.guestIP, "00000b02-0000-4000-8000-000000000000")
	if first.SessionID == "" || again.SessionID != first.SessionID {
		t.Fatalf("a repeated sign-in at the same address opened another session: %q then %q", first.SessionID, again.SessionID)
	}
	live := 0
	for _, r := range f.sessionsOf(t, first.EntitlementID) {
		if !r.ended {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("%d live sessions, want 1", live)
	}
}

// A network with no PMS interface mapped is not a stale feed: it offers no room sign-in, and says so in its own terms.
func TestIntegration_Phase3Auth_ANetworkWithNoPMSMappingRefusesRoomSignInAsNotAvailable(t *testing.T) {
	f := newAuthFixture(t)
	unmapped, _, _ := f.addNetwork(t, false)
	body := f.resolveBody("412", "Okonkwo", "", "00000c01-0000-4000-8000-000000000000")
	body["device"] = map[string]string{"ip": unmapped.guestIP, "mac": f.net.mac}
	rec, res := post(t, f.p3.resolveHandler, body)
	if res.Outcome == outcomeVerified {
		t.Fatalf("room sign-in worked on a network no PMS is mapped to: %s", rec.Body.String())
	}
	var result string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT result FROM iam_v2.sign_in_attempts WHERE tenant_id=$1 AND site_id=$2 AND request_id=$3`,
		f.tenant, f.site, "00000c01-0000-4000-8000-000000000000").Scan(&result); err != nil {
		t.Fatalf("no attempt was recorded: %v", err)
	}
	if result != "ROUTING_OR_INTERFACE_FAILURE" {
		t.Fatalf("recorded as %q; an unmapped network is a routing fact, not a stale mirror", result)
	}
}
