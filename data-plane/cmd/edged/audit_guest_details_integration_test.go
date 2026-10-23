//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// A guest-related entry shows whom it was about only to a role that can read its source screen; a search does not match it.
func TestIntegration_ActivityLogGuestDetailsFollowTheirScreen(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	email := fmt.Sprintf("ann-%d@example.com", time.Now().UnixNano())
	for _, e := range []struct{ action, ttype, target, payload string }{
		{"guest_account.created", "guest_account", email, `{"username":"` + email + `"}`},
		{"network.dhcp.reservation.created", "dhcp_reservation", "aa:bb:cc:dd:ee:01", `{"mac":"aa:bb:cc:dd:ee:01","note":"` + email + `"}`},
		{"operator.created", "operator", "staff-" + email, `{"role":"site_viewer"}`},
	} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(tenant_id,ts,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES ($1,now(),'operator','op-1',$2,$3,$4,$5::jsonb)`, f.tenant, e.action, e.ttype, e.target, e.payload); err != nil {
			t.Fatalf("seed %s: %v", e.action, err)
		}
	}
	read := func(c *apiFixture, search string) []map[string]any {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, c.srv.URL+"/edge/v1/audit", nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.sessTok})
		if search != "" {
			req.Header.Set(auditSearchHeader, search)
		}
		resp, err := c.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Data []map[string]any `json:"data"`
		}
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&body) != nil {
			t.Fatalf("audit read = %d", resp.StatusCode)
		}
		return body.Data
	}
	find := func(rows []map[string]any, action string) map[string]any {
		for _, r := range rows {
			if r["action"] == action {
				return r
			}
		}
		return nil
	}

	all := read(f, "")
	if r := find(all, "guest_account.created"); r == nil || r["target_id"] != email || !strings.Contains(fmt.Sprint(r["payload"]), email) {
		t.Fatalf("the site admin sees the guest: %v", r)
	}

	pay := newAPIIn(t, f.tenant, "payments_operator")
	rows := read(pay, "")
	for _, action := range []string{"guest_account.created", "network.dhcp.reservation.created"} {
		r := find(rows, action)
		if r == nil {
			t.Fatalf("%s must still be listed", action)
		}
		if _, ok := r["target_id"]; ok || strings.Contains(fmt.Sprint(r["payload"]), email) || !strings.Contains(fmt.Sprint(r["payload"]), "details_hidden") {
			t.Fatalf("%s names the guest to a role that cannot see them: %v", action, r)
		}
		if r["actor_id"] != "op-1" {
			t.Fatalf("who did it stays visible: %v", r)
		}
	}
	if r := find(rows, "operator.created"); r == nil || r["target_id"] != "staff-"+email {
		t.Fatalf("staff entries are unchanged: %v", r)
	}
	if got := read(pay, email); find(got, "guest_account.created") != nil || find(got, "network.dhcp.reservation.created") != nil {
		t.Fatalf("a search matched a hidden guest: %v", got)
	}
	if got := read(f, email); find(got, "guest_account.created") == nil {
		t.Fatalf("the admin's search must find the entry: %v", got)
	}
}

// actor=<a guest's MAC> must not confirm, through matches or counts, that the guest was here to a role that cannot read the source screen.
func TestIntegration_ActivityLogActorFilterHidesGuests(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	n := time.Now().UnixNano()
	mac := fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", n&0xff, (n>>8)&0xff, (n>>16)&0xff, (n>>24)&0xff, (n>>32)&0xff)
	for _, e := range []struct{ action, ttype string }{
		{"guest_account.created", "guest_account"},
		{"network.dhcp.reservation.created", "dhcp_reservation"},
	} {
		if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(tenant_id,ts,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES ($1,now(),'operator','op-1',$2,$3,$4,'{}'::jsonb)`, f.tenant, e.action, e.ttype, mac); err != nil {
			t.Fatalf("seed %s: %v", e.action, err)
		}
	}
	read := func(c *apiFixture, actor string) (rows []map[string]any, actions []map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, c.srv.URL+"/edge/v1/audit?actor="+url.QueryEscape(actor), nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.sessTok})
		resp, err := c.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body struct {
			Data    []map[string]any `json:"data"`
			Actions []map[string]any `json:"actions"`
		}
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&body) != nil {
			t.Fatalf("audit read = %d", resp.StatusCode)
		}
		return body.Data, body.Actions
	}
	if rows, actions := read(f, mac); len(rows) != 2 || len(actions) != 2 {
		t.Fatalf("the site admin's actor filter finds the device: %v %v", rows, actions)
	}
	pay := newAPIIn(t, f.tenant, "payments_operator")
	if rows, actions := read(pay, mac); len(rows) != 0 || len(actions) != 0 {
		t.Fatalf("the actor filter confirmed a guest to a role that cannot see them: %v %v", rows, actions)
	}
	if rows, _ := read(pay, "op-1"); len(rows) < 2 {
		t.Fatalf("filtering by the operator who acted must still work: %v", rows)
	}
}
