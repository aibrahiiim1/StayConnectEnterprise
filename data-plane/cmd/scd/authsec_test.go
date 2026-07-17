package main

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// panicRepo is an iamv2.Repository that fails the test if ANY method is invoked. Because scd wires the
// dark authenticator with all flags OFF, no auth path may ever enter the repository — so a single call
// (which would be one IAM-v2 SQL transaction) trips this and fails the test.
type panicRepo struct{ t *testing.T }

func (r panicRepo) WithTx(ctx context.Context, fn func(iamv2.Tx) error) error {
	r.t.Fatalf("PRODUCTION-SAFETY VIOLATION: dark IAM-v2 entered its repository (would issue SQL)")
	return nil
}

// TestDarkIAMv2WiringIssuesZeroSQL constructs the dark authenticator exactly as initAuthSecurity does
// — LoadConfigFromEnv with an all-empty environment (every flag OFF) — and drives every auth method
// through it. The authenticator must short-circuit to DecisionDisabled without ever touching the
// repository, proving the scd wiring issues zero IAM-v2 SQL while dark.
func TestDarkIAMv2WiringIssuesZeroSQL(t *testing.T) {
	emptyEnv := func(string) string { return "" }
	cfg, err := iamv2.LoadConfigFromEnv(emptyEnv, true)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.MasterEnabled {
		t.Fatal("empty environment must yield master OFF")
	}
	// Wire with a repository that panics on any use — the exact opposite of scd's nil repo, but a
	// stronger assertion: if the dark path ever reached SQL, this trips.
	auth, err := iamv2.New(cfg, panicRepo{t}, iamv2.NopObserver{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	for _, m := range []iamv2.Method{iamv2.MethodVoucher, iamv2.MethodAccount, iamv2.MethodOTP, iamv2.MethodSocial} {
		res, err := auth.Authenticate(context.Background(), iamv2.Request{
			Method: m, TenantID: "t", SiteID: "s", Username: "u", Secret: "p",
			FactorType: "EMAIL", FactorValue: "a@example.com", Provider: "google", FactorIssuer: "google",
			Device: iamv2.DeviceContext{MAC: "aa:bb:cc:dd:ee:ff", GuestNetworkID: "g"},
		})
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		if res.Decision != iamv2.DecisionDisabled {
			t.Fatalf("%s: want DecisionDisabled while dark, got %s", m, res.Decision)
		}
	}
}

// TestThrottleGuardDisabledIsNoop proves the durable-throttle guard is a pure allow when the store is
// nil (feature off) — legacy behavior is unchanged and no DB is touched.
func TestThrottleGuardDisabledIsNoop(t *testing.T) {
	s := &server{} // authThrottle nil
	ok, retry := s.throttleGuard(context.Background(), "account", nil, nil, "someone")
	if !ok || retry != 0 {
		t.Fatalf("nil throttle store must allow with no retry, got ok=%v retry=%v", ok, retry)
	}
	_ = time.Second
}
