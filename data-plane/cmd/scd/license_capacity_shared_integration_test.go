//go:build integration

package main

// The shared admission path (reserveLicensedSlotShared / admitLicensed): sign-ins no longer queue behind one exclusive lock while the appliance is far from its
// licensed limit, and the limit is still never exceeded and always reachable. Real PostgreSQL, real gate, concurrent callers.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func (f *capFixture) admitShared(sl slot, limit int64, appliance string, maxInFlight int) error {
	ctx := context.Background()
	return admitLicensed(ctx, f.pool, appliance, limit, maxInFlight, func(tx pgx.Tx, reserve func() error) error {
		if err := reserve(); err != nil {
			return err
		}
		for _, family := range []string{"device_auth", "session_binding"} {
			if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, family); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `SELECT iam_v2.authorize_entitlement_device($1::uuid,$2::uuid,now())`, sl.entitlement, sl.device); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
		    INSERT INTO iam_v2.sessions
		      (tenant_id, site_id, entitlement_id, device_id, credential_method, state, started)
		    VALUES ($1::uuid,$2::uuid,$3::uuid,$4::uuid,'VOUCHER','PENDING_ENFORCEMENT',now())`,
			f.tenant, f.site, sl.entitlement, sl.device)
		return err
	})
}

func (f *capFixture) newAppliance(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Two hundred sign-ins race for 60 slots with at most 8 in flight: exactly 60 are admitted and every other one is told the licence is full.
func TestSharedAdmissionNeverExceedsAndAlwaysReachesTheLimit(t *testing.T) {
	f := newCapFixture(t)
	appliance := f.newAppliance(t)
	const limit, contenders, inFlight = 60, 200, 8
	slots := make([]slot, contenders)
	for i := range slots {
		slots[i] = f.prepare(t, appliance, i)
	}
	var ok, full, other atomic.Int64
	var wg sync.WaitGroup
	gate := make(chan struct{})
	sem := make(chan struct{}, inFlight) // the pool: at most inFlight admissions in flight, as in scd
	for i := range slots {
		wg.Add(1)
		go func(sl slot) {
			defer wg.Done()
			<-gate
			sem <- struct{}{}
			defer func() { <-sem }()
			err := f.admitShared(sl, limit, appliance, inFlight)
			var cap *licenseCapacityError
			switch {
			case err == nil:
				ok.Add(1)
			case errors.As(err, &cap):
				full.Add(1)
			default:
				other.Add(1)
				t.Errorf("admission: %v", err)
			}
		}(slots[i])
	}
	close(gate)
	wg.Wait()
	if got := f.activeOn(t, appliance); got != limit {
		t.Fatalf("%d live sessions, want exactly the limit %d", got, limit)
	}
	if ok.Load() != limit || full.Load() != contenders-limit || other.Load() != 0 {
		t.Fatalf("admitted %d, refused %d, errors %d; want %d / %d / 0", ok.Load(), full.Load(), other.Load(), limit, contenders-limit)
	}
}

// Shared-path and exclusive-path callers (the PMS room sign-in and the post-stay path still use reserveLicensedSlot) race on the same appliance: still never over.
func TestSharedAndExclusiveAdmissionsTogetherNeverExceedTheLimit(t *testing.T) {
	f := newCapFixture(t)
	appliance := f.newAppliance(t)
	const limit, contenders, inFlight = 30, 120, 6
	slots := make([]slot, contenders)
	for i := range slots {
		slots[i] = f.prepare(t, appliance, i)
	}
	var ok atomic.Int64
	var wg sync.WaitGroup
	gate := make(chan struct{})
	sem := make(chan struct{}, inFlight)
	for i := range slots {
		wg.Add(1)
		go func(i int, sl slot) {
			defer wg.Done()
			<-gate
			sem <- struct{}{}
			defer func() { <-sem }()
			var err error
			if i%3 == 0 {
				err = f.admitSlot(sl, limit, appliance) // the exclusive path, as the room sign-in takes it
			} else {
				err = f.admitShared(sl, limit, appliance, inFlight)
			}
			var cap *licenseCapacityError
			if err == nil {
				ok.Add(1)
			} else if !errors.As(err, &cap) {
				t.Errorf("admission: %v", err)
			}
		}(i, slots[i])
	}
	close(gate)
	wg.Wait()
	if got := f.activeOn(t, appliance); got != limit || ok.Load() != limit {
		t.Fatalf("%d live sessions, %d admitted; want exactly %d", got, ok.Load(), limit)
	}
}

// Far from the limit two admissions are in flight together (the shared lock does not serialise them); an exclusive caller waits for both.
func TestSharedAdmissionsDoNotQueueBehindEachOtherButAnExclusiveOneWaits(t *testing.T) {
	f := newCapFixture(t)
	appliance := f.newAppliance(t)
	ctx := context.Background()
	const limit, inFlight = 1000, 8

	open := func() (pgx.Tx, error) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			return nil, err
		}
		if err := reserveLicensedSlotShared(ctx, tx, appliance, limit, inFlight); err != nil {
			_ = tx.Rollback(ctx)
			return nil, err
		}
		return tx, nil
	}
	a, err := open()
	if err != nil {
		t.Fatal(err)
	}
	defer a.Rollback(ctx)
	done := make(chan error, 1)
	go func() {
		b, err := open() // must not wait for a
		if err == nil {
			err = b.Rollback(ctx)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a second shared admission waited for the first: the shared path serialises")
	}
	// An exclusive admission must wait for the open shared one.
	excl := make(chan error, 1)
	go func() {
		err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error { return reserveLicensedSlot(ctx, tx, appliance, limit) })
		excl <- err
	}()
	select {
	case <-excl:
		t.Fatal("the exclusive admission did not wait for an open shared one: its count could miss that admission")
	case <-time.After(400 * time.Millisecond):
	}
	_ = a.Rollback(ctx)
	select {
	case err := <-excl:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the exclusive admission never ran after the shared one ended")
	}
}

// Within inFlight of the limit the shared path refuses to decide and the exact path is used; a limit no larger than inFlight is always exact.
func TestSharedAdmissionHandsOverToTheExactPathNearTheLimit(t *testing.T) {
	f := newCapFixture(t)
	ctx := context.Background()

	appliance := f.newAppliance(t)
	const limit, inFlight = 20, 5
	for i := 0; i < limit-inFlight; i++ { // 15 live: exactly at limit-inFlight
		if err := f.admit(t, limit, appliance, i); err != nil {
			t.Fatalf("filling: %v", err)
		}
	}
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error { return reserveLicensedSlotShared(ctx, tx, appliance, limit, inFlight) })
	if !errors.Is(err, errLicenceNearLimit) {
		t.Fatalf("at limit-inFlight the shared path must hand over, got %v", err)
	}
	// admitLicensed does hand over, and the last five are then admitted one by one, the sixth refused.
	for i := 0; i < inFlight; i++ {
		if err := f.admitShared(f.prepare(t, appliance, i), limit, appliance, inFlight); err != nil {
			t.Fatalf("slot %d of the last %d: %v", i+1, inFlight, err)
		}
	}
	var cap *licenseCapacityError
	if err := f.admitShared(f.prepare(t, appliance, 99), limit, appliance, inFlight); !errors.As(err, &cap) || cap.Current != limit {
		t.Fatalf("over the limit: %v", err)
	}

	small := f.newAppliance(t) // a limit that is not above inFlight is never decided on the shared path
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error { return reserveLicensedSlotShared(ctx, tx, small, 4, 8) }); !errors.Is(err, errLicenceNearLimit) {
		t.Fatalf("small limit: %v", err)
	}
	// Unlimited stays unlimited.
	if err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error { return reserveLicensedSlotShared(ctx, tx, small, 0, 8) }); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
}
