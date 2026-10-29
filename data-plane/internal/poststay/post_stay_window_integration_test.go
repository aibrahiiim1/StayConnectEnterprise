//go:build integration && phase5

package poststay

// Post-stay sign-in is bound to the stay that was left (AD-36): it stops at the site's window after check-out, and the
// next occupant of the room, or the same room and reservation on another PMS interface, reaches nothing of it.

import (
	"context"
	"errors"
	"testing"
	"time"
)

func setCheckout(t *testing.T, f fx, days int) {
	t.Helper()
	ctx := context.Background()
	p := pool(t)
	defer p.Close()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatal(err)
	}
	// The check-out time is immutable inside an episode, so the stay is reinstated (a new episode) and checked out again
	// at the time wanted, as internal/checkout does it.
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET status='IN_HOUSE', effective_checkout_at=NULL, lifecycle_version=lifecycle_version+1
		WHERE id=$1`, f.stay); err != nil {
		t.Fatalf("reinstate: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false,
		effective_checkout_at = now() - make_interval(days => $2) WHERE id=$1`, f.stay, days); err != nil {
		t.Fatalf("check out in the past: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// addStay puts another stay of the same site on the given PMS interface.
func addStay(t *testing.T, f fx, iface, reservation, status, room, last string) string {
	t.Helper()
	ctx := context.Background()
	p := pool(t)
	defer p.Close()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatal(err)
	}
	var id string
	checkout := "now()"
	if status == "IN_HOUSE" {
		checkout = "NULL"
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	      external_stay_identity,status,lifecycle_version,effective_checkout_at,normalized_room_number)
	    VALUES (gen_random_uuid(),$1,$2,$3,$4,$4,$5,1,`+checkout+`,$6) RETURNING id::text`,
		f.tenant, f.site, iface, reservation, status, room).Scan(&id); err != nil {
		t.Fatalf("add stay: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,first_name_norm,last_name_norm,is_primary)
		VALUES ($1,$2,$3,$4,'GRACE',$5,true)`, f.tenant, f.site, iface, id, last); err != nil {
		t.Fatalf("add guest: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

// A stay left longer ago than the window opens nothing, from the device that was on it and from a new one; the same
// stay inside a wider window opens again, so the window is the setting and not a constant.
func TestIntegration_AD36_AStayPastTheWindowOpensNothing(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	fresh := newDevice(t, p, f, "02:00:00:00:00:91")
	setCheckout(t, f, 10)
	s := storeAtFixedTime(t, p, time.Now().Add(-200*time.Minute))
	ctx := context.Background()

	for name, r := range map[string]VerifyRequest{
		"lineage device, default window": req(f, f.device, "101", "R1", ""),
		"new device, default window":     req(f, fresh, "101", "R1", "LOVELACE"),
	} {
		if _, err := s.Verify(ctx, r); !errors.Is(err, ErrNotAuthenticable) {
			t.Errorf("%s: a stay left 10 days ago must open nothing in the 7-day default window, got %v", name, err)
		}
	}
	wide := req(f, f.device, "101", "R1", "")
	wide.WindowDays = 30
	if _, err := s.Verify(ctx, wide); err != nil {
		t.Fatalf("the same stay inside a 30-day window: %v", err)
	}
	narrow := req(f, fresh, "101", "R1", "LOVELACE")
	narrow.WindowDays = 7
	if _, err := s.Verify(ctx, narrow); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a 7-day window must still refuse the new device: %v", err)
	}
}

// The room's next occupant: the leaving guest's room and reservation open the leaving guest's stay and nothing of the
// new one; the new occupant's reservation opens nothing while they are in house.
func TestIntegration_AD36_TheNextOccupantOfTheRoomIsAnotherStay(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	next := addStay(t, f, f.iface, "R2", "IN_HOUSE", "101", "BABBAGE")
	newcomer := newDevice(t, p, f, "02:00:00:00:00:92")
	s := storeAtFixedTime(t, p, time.Now().Add(-300*time.Minute))
	ctx := context.Background()

	profile, err := s.Verify(ctx, req(f, f.device, "101", "R1", ""))
	if err != nil {
		t.Fatalf("the leaving guest: %v", err)
	}
	var origin string
	if err := p.QueryRow(ctx, `SELECT origin_stay_id::text FROM iam_v2.post_stay_profiles WHERE id=$1`, profile).Scan(&origin); err != nil || origin != f.stay {
		t.Fatalf("the profile is bound to stay %q (err %v), want the stay that was left %q", origin, err, f.stay)
	}
	if origin == next {
		t.Fatal("the profile was bound to the next occupant's stay")
	}
	if _, err := s.Verify(ctx, req(f, f.device, "101", "R2", "")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("the leaving guest typing the next occupant's reservation: %v", err)
	}
	if _, err := s.Verify(ctx, req(f, newcomer, "101", "R2", "BABBAGE")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("the next occupant, still in house, signing in as post-stay: %v", err)
	}
}

// The same room, reservation and last name on a second PMS interface is a different stay: a new device cannot tell the
// two apart and is refused rather than guessed at, and the device that was on the first stay reaches only the first.
func TestIntegration_AD36_AnotherPMSInterfaceIsNotTheSameStay(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	ctx := context.Background()
	var other string
	if err := p.QueryRow(ctx, `WITH pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	       VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	     pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	       SELECT gen_random_uuid(),$1,$2,pi.id,1,'UTC','{}'::jsonb FROM pi RETURNING id)
	     SELECT id::text FROM pi`, f.tenant, f.site).Scan(&other); err != nil {
		t.Fatalf("second interface: %v", err)
	}
	second := addStay(t, f, other, "R1", "CHECKED_OUT", "101", "LOVELACE")
	fresh := newDevice(t, p, f, "02:00:00:00:00:93")
	s := storeAtFixedTime(t, p, time.Now().Add(-400*time.Minute))

	if _, err := s.Verify(ctx, req(f, fresh, "101", "R1", "LOVELACE")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("a new device matching two stays on two interfaces must be refused: %v", err)
	}
	profile, err := s.Verify(ctx, req(f, f.device, "101", "R1", ""))
	if err != nil {
		t.Fatalf("the device that was on the first stay: %v", err)
	}
	var origin string
	if err := p.QueryRow(ctx, `SELECT origin_stay_id::text FROM iam_v2.post_stay_profiles WHERE id=$1`, profile).Scan(&origin); err != nil {
		t.Fatal(err)
	}
	if origin != f.stay || origin == second {
		t.Fatalf("the profile is bound to %q, want the first interface's stay %q", origin, f.stay)
	}
}
