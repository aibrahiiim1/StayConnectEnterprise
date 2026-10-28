package otp

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// P2 M11: parallel requests for one destination cannot all pass the cooldown; exactly one code is issued. Needs OTP_TEST_DSN.
func TestParallelIssueForOneDestinationIssuesOneCode(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()
	const n = 10
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "email", Destination: "race@example.com",
				IP: "10.0.0.7", DeviceMAC: "aa:bb:cc:dd:ee:07"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	issued := 0
	for err := range errs {
		switch {
		case err == nil:
			issued++
		case errors.Is(err, ErrCooldown):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM auth_otps WHERE destination = 'race@example.com'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if issued != 1 || rows != 1 {
		t.Fatalf("parallel issue produced %d codes (%d rows), want exactly 1", issued, rows)
	}
}

// P2 M11: the per-address hourly cap holds under parallel requests to many destinations.
func TestParallelIssueRespectsAddressCap(t *testing.T) {
	db := otpDB(t)
	ring := testRing(t)
	ctx := context.Background()
	const n = IPHourlyCap + 15
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = Issue(ctx, db, ring, IssueParams{TenantID: otpTenant, Channel: "email",
				Destination: "dest" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "@example.com", IP: "10.0.0.8"})
		}(i)
	}
	wg.Wait()
	var rows int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM auth_otps WHERE ip = '10.0.0.8'::inet`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows > IPHourlyCap {
		t.Fatalf("address cap %d exceeded under parallel requests: %d codes", IPHourlyCap, rows)
	}
}
