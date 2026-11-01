//go:build integration

package main

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// A Room sign-in session tells the plan producer at commit (pg_notify onegate_session_pending), as the voucher and account
// path does. Without it enforcement waited for acctd's next period, and on the real CHR appliance it arrived after portald's
// 2.5 s budget: the guest was online and was told the stay could not be verified (2026-11-01).
func TestIntegration_ARoomSignInSessionWakesThePlanProducerAtCommit(t *testing.T) {
	f := newSSFixture(t)
	ctx := context.Background()
	ent, dev, _ := f.seedEntitlementSession(t, f.seedAccount(t, "nt-"+randHex(3)), f.ip(60), "02:44:00:00:00:60")
	// The seeded session holds the address; end it so the new one is the device's only live session.
	if err := pgx.BeginFunc(ctx, f.owner, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET state='ended', ended=now(), end_reason='SEED' WHERE entitlement_id=$1::uuid`, ent)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	lc, err := f.owner.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer lc.Release()
	if _, err := lc.Exec(ctx, `LISTEN onegate_session_pending`); err != nil {
		t.Fatal(err)
	}
	p := &phase3Auth{srv: f.s}
	mac, _ := net.ParseMAC("02:44:00:00:00:61")
	var id string
	if err := pgx.BeginFunc(ctx, f.s.db, func(tx pgx.Tx) error {
		var e error
		id, e = p.openSessionTx(ctx, tx, ent, deviceIdentity{DeviceID: dev, IP: net.ParseIP(f.ip(61)), MAC: mac})
		return e
	}); err != nil {
		t.Fatalf("open the room sign-in session: %v", err)
	}
	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for {
		n, err := lc.Conn().WaitForNotification(wctx)
		if err != nil {
			t.Fatalf("no onegate_session_pending notification for the room sign-in session: %v", err)
		}
		if n.Payload == id {
			return
		}
	}
}
