//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// seedRevision retires any mid-flight revision first (at most one may exist, ncr_single_inflight).
func (f *apiFixture) seedRevision(t *testing.T, state string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `UPDATE network_config_revisions SET state='superseded'
		 WHERE state IN ('applying','pending_confirmation')`); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.pool.QueryRow(ctx, `INSERT INTO network_config_revisions (state, summary)
		VALUES ($1,'fail-closed test') RETURNING id::text`, state).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *apiFixture) stageAndMaterialise(t *testing.T, netID, parent string, vlan int) (replID, successor string) {
	t.Helper()
	ctx := context.Background()
	st, body := f.do(t, "POST", "/network/guest-networks/"+netID+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": vlan, "reason": "fail-closed test"})
	if st != 201 {
		t.Fatalf("stage = %d %v", st, body)
	}
	replID = body["replacement_id"].(string)
	tx, err := f.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.materialisePendingReplacements(ctx, tx, "operator@test"); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("materialise: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT successor_network_id::text
		  FROM iam_v2.guest_network_replacements WHERE id=$1`, replID).Scan(&successor); err != nil {
		t.Fatal(err)
	}
	return replID, successor
}

// A superseded revision means a newer configuration is live, not that this one failed: netd supersedes the previous
// revision whenever any later one is confirmed, so reverting would tear down a successor still on the wire.
func TestIntegration_NetworkReplace_ASupersededRevisionIsNotTreatedAsAFailedOne(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.160.0.0/24", "10.160.0.1")
	replID, successor := f.stageAndMaterialise(t, netID, parent, 80)

	rev := f.seedRevision(t, "superseded")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.guest_network_replacements SET revision_id=$2::uuid WHERE id=$1`, replID, rev); err != nil {
		t.Fatal(err)
	}

	if err := f.app.reconcileStaleReplacements(context.Background(), nil); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	state, succ := f.replacementState(t, replID)
	if state == "PENDING" || succ == nil {
		t.Fatalf("a superseded revision reverted a live replacement: state=%s successor=%v", state, succ)
	}
	if enabled, _, _, _ := f.networkState(t, successor); !enabled {
		t.Fatal("the successor network was disabled although it is the one on the wire")
	}
	if enabled, _, _, _ := f.networkState(t, netID); enabled {
		t.Fatal("the retired network was re-enabled although the successor is the one on the wire")
	}
}

// An apply in flight is not an abandoned one: netApply records the revision only after calling netd, so the row
// reads (APPLIED, revision_id NULL) meanwhile and must not be reverted.
func TestIntegration_NetworkReplace_AnApplyWithNoRevisionYetIsNotReverted(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.161.0.0/24", "10.161.0.1")
	replID, successor := f.stageAndMaterialise(t, netID, parent, 81)

	if err := f.app.reconcileStaleReplacements(context.Background(), nil); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if state, succ := f.replacementState(t, replID); state != "APPLIED" || succ == nil {
		t.Fatalf("an in-flight apply was reverted: state=%s successor=%v", state, succ)
	}
	if enabled, _, _, _ := f.networkState(t, successor); !enabled {
		t.Fatal("an in-flight apply's successor was disabled")
	}

	// Once the grace has passed with no revision recorded the apply died, so it is put back.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.guest_network_replacements SET applied_at = now() - interval '1 hour' WHERE id=$1`, replID); err != nil {
		t.Fatal(err)
	}
	if err := f.app.reconcileStaleReplacements(context.Background(), nil); err != nil {
		t.Fatalf("reconcile after the grace: %v", err)
	}
	if state, _ := f.replacementState(t, replID); state != "PENDING" {
		t.Fatalf("an abandoned apply was not put back: state=%s", state)
	}
	if enabled, _, _, _ := f.networkState(t, netID); !enabled {
		t.Fatal("the original network was not re-enabled")
	}
}

// A successor a client already signed in on cannot be deleted (history keeps non-cascading foreign keys), and that
// must not fail the revert: otherwise the database claims the successor is live after netd restored the original.
func TestIntegration_NetworkReplace_ASuccessorWithClientHistoryIsKeptNotFailed(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.162.0.0/24", "10.162.0.1")
	replID, successor := f.stageAndMaterialise(t, netID, parent, 82)

	var deviceID string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,gen_random_uuid(),'02:00:00:ab:cd:01') RETURNING id::text`, f.tenant, f.site).Scan(&deviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.device_network_appearances
		  (tenant_id, site_id, device_id, guest_network_id, first_seen, last_seen)
		VALUES ($1,$2,$3,$4,now(),now())`, f.tenant, f.site, deviceID, successor); err != nil {
		t.Fatal(err)
	}

	if err := f.app.revertMaterialised(ctx, []string{replID}, "the apply was rolled back"); err != nil {
		t.Fatalf("the revert failed because a client had used the successor: %v", err)
	}
	if enabled, _, _, _ := f.networkState(t, netID); !enabled {
		t.Fatal("the original network was not re-enabled")
	}
	if enabled, _, _, _ := f.networkState(t, successor); enabled {
		t.Fatal("the successor is still enabled, so the database and the wire disagree")
	}
	var appearances int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.device_network_appearances
		 WHERE guest_network_id=$1`, successor).Scan(&appearances); err != nil {
		t.Fatal(err)
	}
	if appearances != 1 {
		t.Fatalf("the client's history on the successor was destroyed (%d rows)", appearances)
	}
	if state, _ := f.replacementState(t, replID); state != "PENDING" {
		t.Fatalf("the request is not waiting to be applied again: %s", state)
	}
}

// A request whose original is gone is closed, not left PENDING against a missing network, which would wedge every later apply.
func TestIntegration_NetworkReplace_ARequestWhoseOriginalIsGoneIsClosed(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.163.0.0/24", "10.163.0.1")
	replID, _ := f.stageAndMaterialise(t, netID, parent, 83)

	// The original is disabled while the replacement is applied, and the delete route refuses only enabled networks.
	if _, err := f.pool.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map WHERE guest_network_id=$1`, netID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM guest_networks WHERE id=$1`, netID); err != nil {
		t.Fatalf("seed: the original could not be deleted: %v", err)
	}

	if err := f.app.revertMaterialised(ctx, []string{replID}, "the apply was rolled back"); err != nil {
		t.Fatalf("revert: %v", err)
	}
	state, succ := f.replacementState(t, replID)
	if state != "REVERTED" || succ != nil {
		t.Fatalf("the request was left appliable against a network that is gone: state=%s successor=%v", state, succ)
	}

	tx, err := f.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := f.app.materialisePendingReplacements(ctx, tx, "operator@test"); err != nil {
		t.Fatalf("a later apply was wedged by the closed request: %v", err)
	}
}

// Two changes may not be staged onto the same slot: the live-request index is keyed on the replaced network, so the
// collision must be refused at stage time rather than abort every apply later.
func TestIntegration_NetworkReplace_TwoRequestsCannotTakeTheSameSlot(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	first, _ := f.seedReplaceable(t, parent, "10.164.0.0/24", "10.164.0.1")
	st, body := f.do(t, "POST", "/network/guest-networks",
		netBody("Second", "vlan", parent, 95, "10.165.0.0/24", "10.165.0.1", "10.165.0.100", "10.165.0.200"))
	if st != 201 {
		t.Fatalf("seed second = %d %v", st, body)
	}
	second := body["id"].(string)

	if st, b := f.do(t, "POST", "/network/guest-networks/"+first+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 90, "reason": "first onto VLAN 90"}); st != 201 {
		t.Fatalf("first stage = %d %v", st, b)
	}
	st, b := f.do(t, "POST", "/network/guest-networks/"+second+"/replace", map[string]any{
		"network_type": "vlan", "parent_interface": parent, "vlan_id": 90, "reason": "second onto the same VLAN 90"})
	if st != 409 || b["error"] != "topology_conflict" {
		t.Fatalf("the second request onto the same slot = %d %v, want 409 topology_conflict", st, b)
	}
	if !strings.Contains(strings.ToLower(fmt.Sprint(b["message"])), "already waiting to be applied") {
		t.Errorf("the refusal does not say a staged change holds the slot: %v", b["message"])
	}
}

// An unreadable catalogue withholds the settle rather than confirming on a guess: a CONFIRMED replacement is excluded
// from reconcile and settle, so packages would permanently name a retired network.
func TestIntegration_NetworkReplace_AnUnreadableCatalogueWithholdsTheSettle(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.166.0.0/24", "10.166.0.1")
	replID, _ := f.stageAndMaterialise(t, netID, parent, 84)
	rev := f.seedRevision(t, "active")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.guest_network_replacements SET revision_id=$2::uuid WHERE id=$1`, replID, rev); err != nil {
		t.Fatal(err)
	}

	f.app.commerce = nil // the catalogue is not answering
	out := f.app.settleConfirmedReplacements(context.Background(), nil, "")
	if len(out) != 1 || out[0]["state"] != "APPLIED" {
		t.Fatalf("settle = %v, want the request left APPLIED", out)
	}
	if state, _ := f.replacementState(t, replID); state != "APPLIED" {
		t.Fatalf("the replacement was confirmed although no package could be read: %s", state)
	}

	f.app.commerce = f.commerceAdmin(t)
	if out = f.app.settleConfirmedReplacements(context.Background(), nil, ""); len(out) != 1 || out[0]["state"] != "CONFIRMED" {
		t.Fatalf("settle after recovery = %v, want CONFIRMED", out)
	}
}

// The confirm pre-flight is scoped to its own revision; otherwise one stuck replacement would block every future revision.
func TestIntegration_NetworkReplace_ThePreflightDoesNotBlockUnrelatedRevisions(t *testing.T) {
	f := newAPI(t)
	f.app.commerce = f.commerceAdmin(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.167.0.0/24", "10.167.0.1")
	// A package that cannot be carried forward: its stored duration policy is one this build refuses to publish.
	f.seedNetworkPackage(t, "stuck-"+parent, netID, map[string]string{
		"duration_policy": `'{"end_mode":"AT_CHECKOUT"}'::jsonb`,
	})
	replID, _ := f.stageAndMaterialise(t, netID, parent, 85)
	stuckRev := f.seedRevision(t, "active")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.guest_network_replacements SET revision_id=$2::uuid WHERE id=$1`, replID, stuckRev); err != nil {
		t.Fatal(err)
	}

	if blocking := f.app.replacementsBlockingConfirm(context.Background(), stuckRev); len(blocking) != 1 {
		t.Fatalf("the stuck replacement did not block its own revision: %v", blocking)
	}
	other := f.seedRevision(t, "pending_confirmation")
	if blocking := f.app.replacementsBlockingConfirm(context.Background(), other); len(blocking) != 0 {
		t.Fatalf("an unrelated revision was blocked by a stuck replacement: %v", blocking)
	}
}
