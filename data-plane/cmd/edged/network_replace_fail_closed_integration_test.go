//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// WHEN THE DATABASE, THE CONFIRMED REVISION AND THE LIVE NETWORK COULD HAVE DISAGREED.
//
// Each case here is a sequence in which the staged-replacement lifecycle used to leave one of those three
// describing something the other two did not. They are integration tests because every one of them is about a
// state transition under a real constraint, a real foreign key or a real lock.

// seedRevision creates a netd revision row in a given state, retiring any that is mid-flight (at most one may
// be, ncr_single_inflight).
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

// A SUPERSEDED REVISION MEANS A NEWER CONFIGURATION IS LIVE, NOT THAT THIS ONE FAILED.
//
// netd marks the previous active revision superseded whenever ANY later revision is confirmed. A replacement
// whose settle was withheld sits in APPLIED with an active revision BY DESIGN -- so the moment the operator made
// any unrelated network change and confirmed it, the reconcile saw 'superseded', read it as "the apply did not
// survive", and reverted: the successor row deleted and the original re-enabled, while the successor was still
// on the wire, rendered by the very revision that had just been confirmed.
func TestIntegration_NetworkReplace_ASupersededRevisionIsNotTreatedAsAFailedOne(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.160.0.0/24", "10.160.0.1")
	replID, successor := f.stageAndMaterialise(t, netID, parent, 80)

	// The apply was kept, and then a LATER revision was confirmed on top of it.
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

// AN APPLY STILL IN FLIGHT IS NOT AN ABANDONED ONE. netApply commits the materialisation, calls netd, and only
// then records which revision carries it -- it cannot record it sooner. In that window the row reads
// (APPLIED, revision_id NULL), and the reconcile used to read a NULL revision as "the revision is gone" and
// revert, deleting the successor while netd was still building its bridge.
func TestIntegration_NetworkReplace_AnApplyWithNoRevisionYetIsNotReverted(t *testing.T) {
	f := newAPI(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.161.0.0/24", "10.161.0.1")
	replID, successor := f.stageAndMaterialise(t, netID, parent, 81)

	// Exactly the state netApply is in between its commit and its revision update.
	if err := f.app.reconcileStaleReplacements(context.Background(), nil); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if state, succ := f.replacementState(t, replID); state != "APPLIED" || succ == nil {
		t.Fatalf("an in-flight apply was reverted: state=%s successor=%v", state, succ)
	}
	if enabled, _, _, _ := f.networkState(t, successor); !enabled {
		t.Fatal("an in-flight apply's successor was disabled")
	}

	// ...and once the grace has genuinely passed with no revision recorded, it IS put back: that can only mean
	// the apply died, and the database must not go on claiming a successor nothing accounts for.
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

// A SUCCESSOR A CLIENT ALREADY SIGNED IN ON CANNOT BE DELETED, AND MUST NOT FAIL THE REVERT.
//
// The successor is live for the whole confirmation window, so a guest can sign in on it before the operator
// decides. That writes rows naming the network through foreign keys with no cascade -- deliberately, they are
// that guest's history -- so the DELETE fails, and because it failed inside the revert's transaction the WHOLE
// revert used to abort: the database went on claiming the successor was live while netd had already put the
// original back on the wire. One guest connecting was enough.
func TestIntegration_NetworkReplace_ASuccessorWithClientHistoryIsKeptNotFailed(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.162.0.0/24", "10.162.0.1")
	replID, successor := f.stageAndMaterialise(t, netID, parent, 82)

	// A device appears on the successor, exactly as a sign-in records it.
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
	// The wire is back on the original, and the database says so.
	if enabled, _, _, _ := f.networkState(t, netID); !enabled {
		t.Fatal("the original network was not re-enabled")
	}
	if enabled, _, _, _ := f.networkState(t, successor); enabled {
		t.Fatal("the successor is still enabled, so the database and the wire disagree")
	}
	// ...and the guest's history survived, which is why the row could not be deleted in the first place.
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

// A REQUEST WHOSE ORIGINAL IS GONE IS CLOSED, NOT LEFT LOOKING APPLIABLE. The re-enable's RowsAffected was not
// checked, so a request whose original had been deleted went back to PENDING naming a network that no longer
// existed -- and every subsequent apply, by every operator, then died in the materialise.
func TestIntegration_NetworkReplace_ARequestWhoseOriginalIsGoneIsClosed(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.163.0.0/24", "10.163.0.1")
	replID, _ := f.stageAndMaterialise(t, netID, parent, 83)

	// The original is disabled while the replacement is applied, and the delete route only refuses ENABLED
	// networks -- so this is reachable, not hypothetical.
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

	// The proof that matters: a later apply is not wedged by it.
	tx, err := f.app.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := f.app.materialisePendingReplacements(ctx, tx, "operator@test"); err != nil {
		t.Fatalf("a later apply was wedged by the closed request: %v", err)
	}
}

// TWO CHANGES MAY NOT BE STAGED ONTO THE SAME SLOT. The stage-time check only compared the request against
// networks enabled NOW, and the one-live-request index is keyed on the network being replaced -- so two
// replacements of two DIFFERENT networks onto the same VLAN and port both accepted, and the collision surfaced
// at the apply as a uniqueness error that aborted every operator's apply until somebody found the request.
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

// A CATALOGUE THAT CANNOT BE READ WITHHOLDS THE SETTLE; IT DOES NOT CONFIRM ON A GUESS.
//
// forwardPackagesOnto used to return (nil, nil) when the catalogue was unavailable, which the settle read as
// "there was nothing to carry forward" and then marked the replacement CONFIRMED -- and a CONFIRMED replacement
// is excluded from the reconcile and the settle alike, so nothing ever came back for it. One transient error
// permanently left the hotel's packages naming a retired network id.
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

	// With the catalogue back, the same settle finishes -- which is the whole point of withholding.
	f.app.commerce = f.commerceAdmin(t)
	if out = f.app.settleConfirmedReplacements(context.Background(), nil, ""); len(out) != 1 || out[0]["state"] != "CONFIRMED" {
		t.Fatalf("settle after recovery = %v, want CONFIRMED", out)
	}
}

// THE CONFIRM PRE-FLIGHT ANSWERS FOR ITS OWN REVISION. Unscoped, one stuck replacement blocked the confirmation
// of EVERY future revision for good -- each later apply then watchdog-rolled-back, the appliance lost the
// ability to change its networking at all, and the refusal named a package unrelated to the operator's change.
func TestIntegration_NetworkReplace_ThePreflightDoesNotBlockUnrelatedRevisions(t *testing.T) {
	f := newAPI(t)
	f.app.commerce = f.commerceAdmin(t)
	parent := uniqueParent()
	netID, _ := f.seedReplaceable(t, parent, "10.167.0.0/24", "10.167.0.1")
	// A package that cannot be carried forward: its duration policy was valid when published and is not now.
	f.seedNetworkPackage(t, "stuck-"+parent, netID, map[string]string{
		"duration_policy": `'{"end_mode":"FIXED_AT","ends_at":"2020-01-01T00:00:00Z"}'::jsonb`,
	})
	replID, _ := f.stageAndMaterialise(t, netID, parent, 85)
	stuckRev := f.seedRevision(t, "active")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.guest_network_replacements SET revision_id=$2::uuid WHERE id=$1`, replID, stuckRev); err != nil {
		t.Fatal(err)
	}

	// It does block ITS OWN revision...
	if blocking := f.app.replacementsBlockingConfirm(context.Background(), stuckRev); len(blocking) != 1 {
		t.Fatalf("the stuck replacement did not block its own revision: %v", blocking)
	}
	// ...and not somebody else's.
	other := f.seedRevision(t, "pending_confirmation")
	if blocking := f.app.replacementsBlockingConfirm(context.Background(), other); len(blocking) != 0 {
		t.Fatalf("an unrelated revision was blocked by a stuck replacement: %v", blocking)
	}
}
