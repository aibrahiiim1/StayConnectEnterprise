package main

import (
	"context"
	"crypto/rand"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// TEMPORARY VERIFICATION ACCESS AGAINST THE DATABASE, with a recording kernel (verify_access.go). Needs
// PHASE3_TEST_DSN on a database with migration 0111; skipped otherwise. The real kernel is proven on PRE-LIVE.

type fakeVerifyKernel struct {
	mu       sync.Mutex
	admitted map[string]nft.VerifyGrant // key -> grant
	used     map[string]int64
	applies  int
}

func newFakeVerifyKernel() *fakeVerifyKernel {
	return &fakeVerifyKernel{admitted: map[string]nft.VerifyGrant{}, used: map[string]int64{}}
}
func (f *fakeVerifyKernel) VerifyApply(_ context.Context, g nft.VerifyGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.admitted[g.Key] = g
	f.used[g.Key] = g.UsedBytes
	f.applies++
	return nil
}
func (f *fakeVerifyKernel) VerifyRemove(_ context.Context, key, _ string, _ net.IP) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.admitted, key)
	delete(f.used, key)
}
func (f *fakeVerifyKernel) VerifyRead(context.Context) (nft.VerifyState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := nft.VerifyState{Admitted: map[string]bool{}, Used: map[string]int64{}, Objects: map[string]bool{}}
	for k, g := range f.admitted {
		st.Admitted[g.Bridge+"|"+g.IP.String()] = true
		st.Used[k] = f.used[k]
		st.Objects[k] = true
	}
	return st, nil
}

func verifyFixture(t *testing.T, ta *tenantcfg.TemporaryAccess) (*server, *fakeVerifyKernel, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	const tenant, site = "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"
	if _, err := pool.Exec(ctx, `DELETE FROM iam_v2.verification_access_grants WHERE tenant_id=$1`, tenant); err != nil {
		t.Skipf("no verification_access_grants (apply 0111): %v", err)
	}
	_, _ = pool.Exec(ctx, `DELETE FROM public.guest_networks WHERE tenant_id=$1 AND subnet_cidr='10.91.0.0/24'`, tenant)
	if _, err := pool.Exec(ctx, `INSERT INTO public.guest_networks
		  (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
		SELECT gen_random_uuid(), $1, $2, 'verify-test', 'p'||substr(md5(g::text),1,12), 'bverify0', '10.91.0.1/24', '10.91.0.1', '10.91.0.0/24', true
		  FROM gen_random_uuid() g`, tenant, site); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM iam_v2.verification_access_grants WHERE tenant_id=$1`, tenant)
		_, _ = pool.Exec(context.Background(), `DELETE FROM public.guest_networks WHERE tenant_id=$1 AND subnet_cidr='10.91.0.0/24'`, tenant)
	})
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	k := newFakeVerifyKernel()
	s := &server{db: pool, tenID: tenant, siteID: site, resumeKey: key, verifyK: k,
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			return &tenantcfg.AuthMethods{Email: &tenantcfg.AuthMethod{Enabled: true, TemporaryAccess: ta}}, nil
		}}
	return s, k, pool
}

func TestIntegration_AnIssuedCodeOpensOneMeteredWindowThatIsNeverReset(t *testing.T) {
	s, k, pool := verifyFixture(t, nil) // defaults: 10 min, 2000/1000 kbps, 25 MB, 3/device, 3/destination
	ctx := context.Background()
	ip := net.ParseIP("10.91.0.40")
	v, ok := s.grantVerifyAccess(ctx, "02:00:00:91:00:01", ip, "", "ann@example.com")
	if !ok || v.Seconds < 590 || v.DataMB != 25 || v.DownKbps != 2000 || v.UpKbps != 1000 {
		t.Fatalf("default window: %+v %v", v, ok)
	}
	if k.applies != 1 || len(k.admitted) != 1 {
		t.Fatalf("one kernel install: %d", k.applies)
	}
	for _, g := range k.admitted {
		if g.Bridge != "bverify0" || g.CapBytes != 25*1024*1024 || g.UsedBytes != 0 {
			t.Fatalf("kernel grant: %+v", g)
		}
		k.used[g.Key] = 3 * 1024 * 1024 // the device has used 3 MB
	}
	s.reconcileVerifyAccess(ctx)
	// A SECOND REQUEST (another address, same device) REUSES THE WINDOW: no new install, nothing reset.
	v2, ok := s.grantVerifyAccess(ctx, "02:00:00:91:00:01", ip, "", "other@example.com")
	if !ok || k.applies != 1 || v2.DataMB != 22 || v2.Seconds > v.Seconds {
		t.Fatalf("a second request must reuse the window: %+v applies=%d", v2, k.applies)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.verification_access_grants WHERE tenant_id=$1 AND ended_at IS NULL`, s.tenID).Scan(&n)
	if n != 1 {
		t.Fatalf("one live window per device, got %d", n)
	}
	// DATA EXHAUSTION ends it within one reconcile and removes it from the kernel.
	for key := range k.admitted {
		k.used[key] = 25 * 1024 * 1024
	}
	s.reconcileVerifyAccess(ctx)
	var reason string
	var used int64
	_ = pool.QueryRow(ctx, `SELECT end_reason, used_bytes FROM iam_v2.verification_access_grants WHERE tenant_id=$1`, s.tenID).Scan(&reason, &used)
	if reason != "DATA_EXHAUSTED" || used != 25*1024*1024 || len(k.admitted) != 0 {
		t.Fatalf("exhaustion: reason=%s used=%d kernel=%d", reason, used, len(k.admitted))
	}
}

func TestIntegration_TheWindowIsRestoredWithWhatIsLeftAndEndsOnSignIn(t *testing.T) {
	s, k, pool := verifyFixture(t, nil)
	ctx := context.Background()
	ip := net.ParseIP("10.91.0.41")
	if _, ok := s.grantVerifyAccess(ctx, "02:00:00:91:00:02", ip, "", "bob@example.com"); !ok {
		t.Fatal("grant")
	}
	for key := range k.admitted {
		k.used[key] = 5 * 1024 * 1024
	}
	s.reconcileVerifyAccess(ctx) // persists the 5 MB
	// A RESTART: the kernel lost everything (a netd re-render, an appliance reboot).
	k.admitted, k.used = map[string]nft.VerifyGrant{}, map[string]int64{}
	_, _ = pool.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET expires_at = now() + interval '90 seconds' WHERE tenant_id=$1 AND ended_at IS NULL`, s.tenID)
	s.reconcileVerifyAccess(ctx)
	if len(k.admitted) != 1 {
		t.Fatal("a live window must be restored")
	}
	for _, g := range k.admitted {
		if g.UsedBytes != 5*1024*1024 || g.Remaining > 91*time.Second || g.Remaining < 80*time.Second {
			t.Fatalf("restored with what is LEFT, never a fresh window: %+v", g)
		}
	}
	// SIGN-IN (verification) ends it at once.
	s.endVerifyAccess(ctx, "02:00:00:91:00:02", "VERIFIED")
	var reason string
	_ = pool.QueryRow(ctx, `SELECT end_reason FROM iam_v2.verification_access_grants WHERE tenant_id=$1`, s.tenID).Scan(&reason)
	if reason != "VERIFIED" || len(k.admitted) != 0 {
		t.Fatalf("verification must end the window: %s kernel=%d", reason, len(k.admitted))
	}
}

func TestIntegration_RepeatedRequestsCannotFarmWindows(t *testing.T) {
	two := 2
	s, _, pool := verifyFixture(t, &tenantcfg.TemporaryAccess{PerDeviceDaily: &two, PerDestinationDaily: &two})
	ctx := context.Background()
	end := func() {
		_, _ = pool.Exec(ctx, `UPDATE iam_v2.verification_access_grants SET ended_at=now(), end_reason='EXPIRED' WHERE tenant_id=$1 AND ended_at IS NULL`, s.tenID)
	}
	// Per device: two windows in 24 h, then none -- whatever address is asked for.
	for i, dest := range []string{"a@example.com", "b@example.com"} {
		if _, ok := s.grantVerifyAccess(ctx, "02:00:00:91:00:03", net.ParseIP("10.91.0.42"), "", dest); !ok {
			t.Fatalf("window %d refused", i)
		}
		end()
	}
	if _, ok := s.grantVerifyAccess(ctx, "02:00:00:91:00:03", net.ParseIP("10.91.0.42"), "", "c@example.com"); ok {
		t.Fatal("a third window for the same device in 24 h must be refused")
	}
	// Per destination: a new hardware address does not reset the address's count.
	if _, ok := s.grantVerifyAccess(ctx, "02:00:00:91:00:04", net.ParseIP("10.91.0.43"), "", "A@Example.com"); !ok {
		t.Fatal("a new device's first window for a once-used address is allowed")
	}
	end()
	if _, ok := s.grantVerifyAccess(ctx, "02:00:00:91:00:05", net.ParseIP("10.91.0.44"), "", "a@example.com"); ok {
		t.Fatal("a third window for the same destination in 24 h must be refused, whatever the device")
	}
	// The destination is never stored, only its keyed digest.
	var leaked int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.verification_access_grants WHERE tenant_id=$1 AND destination_digest LIKE '%example%'`, s.tenID).Scan(&leaked)
	if leaked != 0 {
		t.Fatal("the destination must never be stored")
	}
}

func TestIntegration_TheWindowIsOffWhenEmailCodeOrTheSettingIsOff(t *testing.T) {
	off := false
	s, k, _ := verifyFixture(t, &tenantcfg.TemporaryAccess{Enabled: &off})
	if _, ok := s.grantVerifyAccess(context.Background(), "02:00:00:91:00:06", net.ParseIP("10.91.0.45"), "", "d@example.com"); ok || k.applies != 0 {
		t.Fatal("a switched-off window must not open")
	}
	s.methodSwitches = func(context.Context) (*tenantcfg.AuthMethods, error) {
		return &tenantcfg.AuthMethods{Email: &tenantcfg.AuthMethod{Enabled: false}}, nil
	}
	if _, ok := s.grantVerifyAccess(context.Background(), "02:00:00:91:00:06", net.ParseIP("10.91.0.45"), "", "d@example.com"); ok {
		t.Fatal("no email code, no window")
	}
	if !strings.HasPrefix(s.verifyDestDigest("x@example.com"), "") {
		t.Fatal("unreachable")
	}
}
