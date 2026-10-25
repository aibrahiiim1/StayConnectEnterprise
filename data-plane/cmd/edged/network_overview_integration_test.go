//go:build integration

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The status an operator sees follows the revisions the appliance confirmed, not what was saved.

func (f *apiFixture) overviewOf(t *testing.T, id string) overviewNetwork {
	t.Helper()
	st, body := f.do(t, "GET", "/network/overview", nil)
	if st != 200 {
		t.Fatalf("overview = %d %v", st, body)
	}
	raw, _ := json.Marshal(body["networks"])
	var nets []overviewNetwork
	if err := json.Unmarshal(raw, &nets); err != nil {
		t.Fatal(err)
	}
	for _, n := range nets {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("network %s is not in the overview: %s", id, raw)
	return overviewNetwork{}
}

// seedRevisionWith records a revision whose intent is the site's current rows, in the given state.
func (f *apiFixture) seedRevisionWith(t *testing.T, state, reason string) string {
	t.Helper()
	ctx := context.Background()
	nets, err := f.app.loadSavedIntents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(nets)
	if state == "active" {
		if _, err := f.pool.Exec(ctx, `UPDATE network_config_revisions SET state='superseded' WHERE state='active'`); err != nil {
			t.Fatal(err)
		}
	}
	if state == "applying" || state == "pending_confirmation" {
		if _, err := f.pool.Exec(ctx, `UPDATE network_config_revisions SET state='superseded' WHERE state IN ('applying','pending_confirmation')`); err != nil {
			t.Fatal(err)
		}
	}
	var id string
	if err := f.pool.QueryRow(ctx, `INSERT INTO network_config_revisions (state, summary, intent, failure_reason, confirmed_at)
		VALUES ($1,'overview test',$2::jsonb,NULLIF($3,''), CASE WHEN $1='active' THEN now() END) RETURNING id::text`, state, string(raw), reason).Scan(&id); err != nil {
		t.Fatalf("seed %s revision: %v", state, err)
	}
	return id
}

func TestIntegration_NetworkOverview_StatusFollowsConfirmedRevisionsNotWhatWasSaved(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	st, body := f.do(t, "POST", "/network/guest-networks",
		netBody("Lobby", "vlan", parent, 140, "10.140.0.0/24", "10.140.0.1", "10.140.0.100", "10.140.0.200"))
	if st != 201 {
		t.Fatalf("create = %d %v", st, body)
	}
	id := body["id"].(string)
	// Clear the slate: nothing in flight, nothing active, from earlier tests in this database.
	for _, s := range []string{"active", "applying", "pending_confirmation"} {
		_, _ = f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='superseded' WHERE state=$1`, s)
	}

	// 1. Created and saved as enabled: the API succeeded, nothing runs.
	if n := f.overviewOf(t, id); n.Status != "change_pending" || n.Running || n.EverActivated {
		t.Fatalf("saved but never applied: %+v", n)
	}

	// 2. Switched off before it was ever confirmed: it was never activated.
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+id+"/disable", nil); st != 200 {
		t.Fatal("disable")
	}
	if n := f.overviewOf(t, id); n.Status != "not_activated" {
		t.Fatalf("never confirmed, switched off: %+v", n)
	}

	// 3. Being applied.
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+id+"/enable", nil); st != 200 {
		t.Fatal("enable")
	}
	pending := f.seedRevisionWith(t, "pending_confirmation", "")
	if n := f.overviewOf(t, id); n.Status != "activating" {
		t.Fatalf("applied, awaiting confirmation: %+v", n)
	}

	// 4. Confirmed.
	_, _ = f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='superseded' WHERE id=$1`, pending)
	f.seedRevisionWith(t, "active", "")
	if n := f.overviewOf(t, id); n.Status != "active" || !n.Running || !n.EverActivated || n.Reason == "" {
		t.Fatalf("confirmed and running: %+v", n)
	}

	// 5. An edit that is saved but not applied.
	if st, body := f.do(t, "PUT", "/network/guest-networks/"+id, map[string]any{"name": "Lobby 2", "gateway_ip": "10.140.0.1", "subnet_cidr": "10.140.0.0/24"}); st != 200 {
		t.Fatalf("edit = %d %v", st, body)
	}
	n := f.overviewOf(t, id)
	if n.Status != "change_pending" || len(n.Changes) != 1 || n.Changes[0].Field != "name" || n.Changes[0].Running != "Lobby" {
		t.Fatalf("an edit not yet applied: %+v", n)
	}

	// 6. A change that was applied and then failed.
	f.seedRevisionWith(t, "rolled_back", "health check failed: gateway_up")
	if n := f.overviewOf(t, id); n.Status != "failed" || !strings.Contains(n.Reason, "gateway_up") {
		t.Fatalf("the last change was rolled back: %+v", n)
	}

	// 7. Switched off after having been confirmed: disabled, and it keeps its settings.
	_, _ = f.pool.Exec(context.Background(), `UPDATE network_config_revisions SET state='superseded' WHERE state IN ('rolled_back','failed') AND summary='overview test'`)
	if st, _ := f.do(t, "POST", "/network/guest-networks/"+id+"/disable", nil); st != 200 {
		t.Fatal("disable")
	}
	f.seedRevisionWith(t, "active", "")
	n = f.overviewOf(t, id)
	if n.Status != "disabled" || !n.EverActivated || n.Running {
		t.Fatalf("disabled after confirmation: %+v", n)
	}
}

func TestIntegration_NetworkOverview_AwaitingAssignmentDescribesNothing(t *testing.T) {
	f := newAPI(t)
	saved := f.app.tenantID
	f.app.tenantID = ""
	defer func() { f.app.tenantID = saved }()
	st, body := f.do(t, "GET", "/network/overview", nil)
	if st != 200 {
		t.Fatalf("%d %v", st, body)
	}
	if nets, _ := body["networks"].([]any); len(nets) != 0 {
		t.Fatalf("an appliance with no assignment described networks: %v", nets)
	}
}

func TestIntegration_NetworkDiagnostics_AnUnreachableNetworkServiceIsSaidNotInvented(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "POST", "/network/guest-networks",
		netBody("Lobby", "vlan", uniqueParent(), 141, "10.141.0.0/24", "10.141.0.1", "10.141.0.100", "10.141.0.200"))
	if st != 201 {
		t.Fatal(body)
	}
	id := body["id"].(string)
	st, body = f.do(t, "GET", "/network/guest-networks/"+id+"/diagnostics", nil)
	if st != 502 || body["error"] != "netd_unreachable" {
		t.Fatalf("with no network service the diagnostics should say so, got %d %v", st, body)
	}
	if st, _ := f.do(t, "GET", "/network/guest-networks/00000000-0000-0000-0000-00000000dead/diagnostics", nil); st != 404 {
		t.Fatalf("unknown network = %d", st)
	}
}
