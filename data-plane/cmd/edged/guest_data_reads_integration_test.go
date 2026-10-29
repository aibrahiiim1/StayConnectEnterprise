//go:build integration

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIntegration_GuestDataReads_AreRecorded(t *testing.T) {
	f := newAPI(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	f.app.reads = newReadRecorder(f.app.writeReadEntry)
	go f.app.reads.run(ctx, 50*time.Millisecond)

	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "grace-" + sfx + "@example.com"
	acct, _, session := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.77.0.10")

	get := func(path string, header map[string]string) int {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, f.srv.URL+"/edge/v1"+path, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.sessTok})
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := f.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, c := range []struct {
		path   string
		header map[string]string
		want   int
	}{
		{"/guest-accounts/" + acct, nil, 200},
		{"/guest-accounts/" + acct, nil, 200}, // opened again: one entry
		{"/sessions/" + session, nil, 200},
		{"/guest-accounts", nil, 200},                                          // browsing: recorded as listed (AD-7)
		{"/guest-accounts/00000000-0000-4000-8000-000000000000", nil, 404},     // failed read: not recorded
		{"/guest-accounts", map[string]string{accountSearchHeader: "gr"}, 200}, // typing ...
		{"/guest-accounts", map[string]string{accountSearchHeader: url.PathEscape(email)}, 200},
	} {
		if st := get(c.path, c.header); st != c.want {
			t.Fatalf("GET %s = %d, want %d", c.path, st, c.want)
		}
	}
	stop()
	<-f.app.reads.done

	type row struct{ action, targetType, targetID, search, ip, actor string }
	read := func() []row {
		rows, err := f.pool.Query(context.Background(), `
			SELECT action, COALESCE(target_type,''), COALESCE(target_id,''), COALESCE(payload->>'search',''),
			       COALESCE(host(ip),''), COALESCE(actor_id,'')
			  FROM audit_log WHERE tenant_id = $1 AND action LIKE 'guest_data.%' ORDER BY ts, action`, f.tenant)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.action, &r.targetType, &r.targetID, &r.search, &r.ip, &r.actor); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	got := read()
	want := []row{
		{"guest_data.viewed", "client_account", acct, "", "127.0.0.1", f.operator},
		{"guest_data.viewed", "session", session, "", "127.0.0.1", f.operator},
		// AD-7: a page of a list with nothing typed is recorded too, as listed, with no search text.
		{"guest_data.listed", "client_accounts", "", "", "127.0.0.1", f.operator},
		{"guest_data.searched", "client_accounts", "", email, "127.0.0.1", f.operator},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("recorded\n  %v\nwant\n  %v", got, want)
	}

	// The searched text is shown only to holders of guest-data-rights; a viewer sees that a search happened, not the text.
	searchPayload := func(c *apiFixture) string {
		t.Helper()
		st, body := c.do(t, http.MethodGet, "/audit?action=guest_data.searched", nil)
		rows, _ := body["data"].([]any)
		if st != http.StatusOK || len(rows) == 0 {
			t.Fatalf("audit = %d %v", st, body)
		}
		return fmt.Sprint(rows[0].(map[string]any)["payload"])
	}
	if p := searchPayload(f); !strings.Contains(p, email) {
		t.Fatalf("the site admin does not see the search text: %s", p)
	}
	viewer := newAPIIn(t, f.tenant, "site_viewer")
	if p := searchPayload(viewer); strings.Contains(p, email) || !strings.Contains(p, "details_hidden") {
		t.Fatalf("a site viewer sees the search text: %s", p)
	}

	st, body := f.do(t, "POST", "/guest-data-rights/erase",
		map[string]any{"kind": "account", "id": acct, "reason": "guest asked", "password": f.password})
	if st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	for _, r := range read() {
		if r.action == "guest_data.erased" || r.action == "guest_data.exported" {
			continue
		}
		if r.targetID == acct || r.search == email {
			t.Fatalf("an entry still names the erased guest: %+v", r)
		}
	}
}
