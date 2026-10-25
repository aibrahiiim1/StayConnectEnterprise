//go:build integration

package main

// The SQL half of the pipeline-integrity work, against a real (disposable) full-schema database: the in-flight slot, an interrupted apply at boot,
// and the record of bridges netd applied.

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// clearInFlight frees the single in-flight slot and removes this test's rows; nothing else may hold the slot while a test uses it.
func integrityPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	db := gateTestPool(t)
	ctx := context.Background()
	reset := func() {
		_, _ = db.Exec(ctx, `UPDATE network_config_revisions SET state='superseded' WHERE state IN ('applying','pending_confirmation')`)
		_, _ = db.Exec(ctx, `DELETE FROM network_config_revisions WHERE summary LIKE 'netinteg:%'`)
	}
	reset()
	t.Cleanup(reset)
	return db
}

func newRev(t *testing.T, db *pgxpool.Pool, state, intentJSON string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(context.Background(), `INSERT INTO network_config_revisions (state, summary, intent)
		VALUES ($1,'netinteg: test',$2::jsonb) RETURNING id::text`, state, intentJSON).Scan(&id); err != nil {
		t.Fatalf("insert %s revision: %v", state, err)
	}
	return id
}

func TestIntegration_InFlightRevisionReportsTheSlotHolder(t *testing.T) {
	db := integrityPool(t)
	st := &store{db: db}
	ctx := context.Background()

	if id, state, err := st.InFlightRevision(ctx); err != nil || id != "" || state != "" {
		t.Fatalf("an empty slot reported %q/%q (%v)", id, state, err)
	}
	pending := newRev(t, db, "pending_confirmation", `[]`)
	id, state, err := st.InFlightRevision(ctx)
	if err != nil || id != pending || state != "pending_confirmation" {
		t.Fatalf("got %q/%q (%v), want the pending revision %q", id, state, err, pending)
	}
}

// The failure this work removes: a revision left 'applying' by a dead process holds the unique slot against every later apply, and nothing moved it.
func TestIntegration_AnInterruptedApplyHoldsTheSlotUntilBootReclaimsIt(t *testing.T) {
	db := integrityPool(t)
	st := &store{db: db}
	ctx := context.Background()
	stuck := newRev(t, db, "applying", `[]`)
	next := newRev(t, db, "validated", `[]`)

	if err := st.MarkApplying(ctx, next, "/tmp/x", "", nil); err == nil {
		t.Fatal("setup: the unique index should refuse a second in-flight revision")
	}
	ids, err := st.ApplyingRevisions(ctx)
	if err != nil || len(ids) != 1 || ids[0] != stuck {
		t.Fatalf("ApplyingRevisions = %v (%v), want [%s]", ids, err, stuck)
	}

	// netd starts: the real boot step, against the real store, with the kernel faked.
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.st = st
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{} }
	a.ReclaimInterruptedOnBoot(ctx)

	var state, reason string
	if err := db.QueryRow(ctx, `SELECT state, COALESCE(failure_reason,'') FROM network_config_revisions WHERE id=$1`, stuck).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "rolled_back" || !strings.Contains(reason, "interrupted") {
		t.Fatalf("the interrupted revision is %q (%q)", state, reason)
	}
	if err := st.MarkApplying(ctx, next, "/tmp/x", "", nil); err != nil {
		t.Fatalf("the slot is still held after the reclaim: %v", err)
	}
}

func TestIntegration_RecordedBridgesComeOnlyFromRevisionsNetdApplied(t *testing.T) {
	db := integrityPool(t)
	st := &store{db: db}
	ctx := context.Background()
	mk := func(br, typ, parent string, vlan int) string {
		return `{"id":"id-` + br + `","bridge_name":"` + br + `","network_type":"` + typ + `","parent_interface":"` + parent + `","vlan_id":` + itoa(vlan) + `}`
	}
	newRev(t, db, "superseded", `[`+mk("br-g20", "vlan", "ens192", 20)+`,`+mk("br-g-aaaa0001", "untagged", "ens192", 0)+`]`)
	newRev(t, db, "active", `[`+mk("br-g30", "vlan", "ens192", 30)+`]`)
	newRev(t, db, "rolled_back", `[`+mk("br-g40", "vlan", "ens192", 40)+`]`)
	// Never reached the wire:
	newRev(t, db, "failed", `[`+mk("br-g50", "vlan", "ens192", 50)+`]`)
	newRev(t, db, "validated", `[`+mk("br-g60", "vlan", "ens192", 60)+`]`)
	// The same name recorded on two different members is ambiguous, so no member is claimed:
	newRev(t, db, "superseded", `[`+mk("br-g70", "vlan", "ens192", 70)+`]`)
	newRev(t, db, "superseded", `[`+mk("br-g70", "vlan", "ens160", 70)+`]`)

	got, err := st.RecordedBridges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"br-g20": "ens192.20", "br-g-aaaa0001": "ens192", "br-g30": "ens192.30", "br-g40": "ens192.40", "br-g70": ""}
	for br, member := range want {
		rec, ok := got[br]
		if !ok || rec.Member != member {
			t.Errorf("%s: recorded=%v member=%q, want member %q", br, ok, rec.Member, member)
		}
	}
	if !got["br-g20"].IDs["id-br-g20"] || len(got["br-g20"].IDs) != 1 {
		t.Errorf("br-g20 networks = %v, want only id-br-g20", got["br-g20"].IDs)
	}
	for _, br := range []string{"br-g50", "br-g60"} {
		if _, ok := got[br]; ok {
			t.Errorf("%s never reached the wire but is recorded as netd's", br)
		}
	}
}

// The recorded bridges are what lets a bridge created before the ownership mark existed still be cleaned up by the real code path.
func TestIntegration_RemovalUsesTheRecordFromTheRealStore(t *testing.T) {
	db := integrityPool(t)
	st := &store{db: db}
	newRev(t, db, "superseded", `[{"bridge_name":"br-g81","network_type":"vlan","parent_interface":"ens192","vlan_id":81}]`)

	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	a.st = st
	a.liveBridgesFn = func() map[string]bool { return map[string]bool{"br-g81": true, "br-gstranger": true} }
	a.memberFn = func(string) string { return "ens192.81" }

	removed, _, unmanaged, err := a.removeStrayBridges(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(removed, ",") != "br-g81" || strings.Join(unmanaged, ",") != "br-gstranger" {
		t.Fatalf("removed=%v unmanaged=%v", removed, unmanaged)
	}
	var members []string
	for _, c := range k.cmds {
		if strings.HasPrefix(c, "ip link del ") {
			members = append(members, strings.TrimPrefix(c, "ip link del "))
		}
	}
	if strings.Join(members, ",") != "br-g81,ens192.81" {
		t.Fatalf("deleted %v, want the recorded bridge and its recorded VLAN interface", members)
	}
	_ = netcfg.DefaultTopology
}

func TestIntegration_AnIncompleteRollbackIsFoundUntilResolvedOrSuperseded(t *testing.T) {
	db := integrityPool(t)
	st := &store{db: db}
	ctx := context.Background()
	failed := newRev(t, db, "rolled_back", `[]`)
	if _, err := db.Exec(ctx, `UPDATE network_config_revisions SET failure_reason='health check failed -- ROLLBACK INCOMPLETE, the appliance may not be on its confirmed configuration: x' WHERE id=$1`, failed); err != nil {
		t.Fatal(err)
	}
	if id, err := st.IncompleteRollback(ctx); err != nil || id != failed {
		t.Fatalf("IncompleteRollback = %q (%v), want %q", id, err, failed)
	}

	// A revision applied after it means the block was dealt with.
	later := newRev(t, db, "active", `[]`)
	if id, _ := st.IncompleteRollback(ctx); id != "" {
		t.Fatalf("a later confirmed revision %s still leaves %q blocking", later, id)
	}
	if _, err := db.Exec(ctx, `DELETE FROM network_config_revisions WHERE id=$1`, later); err != nil {
		t.Fatal(err)
	}

	if err := st.ResolveIncompleteRollbacks(ctx); err != nil {
		t.Fatal(err)
	}
	if id, _ := st.IncompleteRollback(ctx); id != "" {
		t.Fatalf("a resolved rollback still blocks: %q", id)
	}
	var reason string
	_ = db.QueryRow(ctx, `SELECT failure_reason FROM network_config_revisions WHERE id=$1`, failed).Scan(&reason)
	if !strings.Contains(reason, "since resolved") || !strings.Contains(reason, "health check failed") {
		t.Fatalf("the original reason was lost: %q", reason)
	}
}

// What the database returns for a revision is comparable to what Apply put in force, so a revision that IS running can be confirmed.
func TestIntegration_ARevisionIntentRoundTripsAndIsRecognisedAsRunning(t *testing.T) {
	db := integrityPool(t)
	st := &store{db: db}
	ctx := context.Background()
	intent := []netcfg.GuestNetwork{{
		ID: "11111111-1111-1111-1111-111111111111", Name: "Guests", Enabled: true, NetworkType: "vlan", ParentInterface: "ens192", VLANID: 20,
		BridgeName: "br-g20", GatewayIP: "10.20.0.1", SubnetCIDR: "10.20.0.0/22", PrefixLen: 22, DHCPMode: netcfg.DHCPLocal,
		Pools:        []netcfg.Pool{{StartIP: "10.20.0.100", EndIP: "10.20.3.200"}},
		Reservations: []netcfg.Reservation{}, DNSServers: nil,
	}}
	id, _, err := st.CreateRevision(ctx, "netinteg: roundtrip", intent, "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.RevisionIntent(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !sameIntent(intent, got) {
		t.Fatalf("the stored intent differs from the one applied: %+v vs %+v", intent, got)
	}
}
