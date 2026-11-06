//go:build integration

package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Room sign-in records when a device was first and last seen; a device created before first_seen was recorded keeps NULL
// (never back-filled), and later sign-ins move only last_seen and last_ip.
func TestIntegration_Phase3Auth_RoomSignInRecordsWhenADeviceWasFirstAndLastSeen(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()
	read := func(id string) (first, last *time.Time, ip *string) {
		t.Helper()
		if err := f.pool.QueryRow(ctx, `SELECT first_seen, last_seen, host(last_ip) FROM iam_v2.devices WHERE id = $1`, id).
			Scan(&first, &last, &ip); err != nil {
			t.Fatal(err)
		}
		return
	}
	dev, err := f.p3.device(ctx, wireDevice{IP: f.net.guestIP, MAC: f.net.mac})
	if err != nil {
		t.Fatal(err)
	}
	first, last, ip := read(dev.DeviceID)
	if first == nil || last == nil || ip == nil || *ip != f.net.guestIP {
		t.Fatalf("a new device was recorded without its first sighting: first=%v last=%v ip=%v", first, last, ip)
	}
	time.Sleep(20 * time.Millisecond)
	again, err := f.p3.device(ctx, wireDevice{IP: f.net.guestIP, MAC: f.net.mac})
	if err != nil || again.DeviceID != dev.DeviceID {
		t.Fatalf("the same device resolved to another identity: %v %v", again.DeviceID, err)
	}
	first2, last2, _ := read(dev.DeviceID)
	if !first2.Equal(*first) || !last2.After(*last) {
		t.Fatalf("a later sign-in moved first_seen (%v -> %v) or did not advance last_seen (%v -> %v)", first, first2, last, last2)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.devices SET first_seen = NULL, last_seen = NULL WHERE id = $1`, dev.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.p3.device(ctx, wireDevice{IP: f.net.guestIP, MAC: f.net.mac}); err != nil {
		t.Fatal(err)
	}
	first3, last3, _ := read(dev.DeviceID)
	if first3 != nil || last3 == nil {
		t.Fatalf("a device created before first_seen was recorded got a fabricated first_seen %v (last_seen %v)", first3, last3)
	}
}

// The upsert runs as the production role: INSERT on the table and UPDATE on exactly last_seen and last_ip.
func TestIntegration_Phase3Auth_DeviceUpsertRunsAsTheProductionRole(t *testing.T) {
	dsn := os.Getenv("SCD_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("SCD_ROLE_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var tenant, site, appliance string
	if err := tx.QueryRow(ctx, `SELECT tenant_id::text, site_id::text, appliance_id::text FROM iam_v2.devices LIMIT 1`).
		Scan(&tenant, &site, &appliance); err != nil {
		t.Skipf("no device to take a scope from: %v", err)
	}
	var id string
	for i := 0; i < 2; i++ {
		if err := tx.QueryRow(ctx, deviceUpsertSQL, tenant, site, appliance, "02:00:00:00:5e:01", "10.3.0.9").Scan(&id); err != nil {
			t.Fatalf("pass %d: the device upsert does not run as svc_scd: %v", i, err)
		}
	}
}
