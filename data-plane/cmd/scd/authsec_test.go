package main

import (
	"context"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// panicRepo fails the test on any call: the dark authenticator must never reach the repository.
type panicRepo struct{ t *testing.T }

func (r panicRepo) WithTx(ctx context.Context, fn func(iamv2.Tx) error) error {
	r.t.Fatalf("PRODUCTION-SAFETY VIOLATION: dark IAM-v2 entered its repository (would issue SQL)")
	return nil
}

// Drives every auth method through the dark authenticator; it must short-circuit without touching the repository.
func TestDarkIAMv2WiringIssuesZeroSQL(t *testing.T) {
	emptyEnv := func(string) string { return "" }
	cfg, err := iamv2.LoadConfigFromEnv(emptyEnv, true)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.MasterEnabled {
		t.Fatal("empty environment must yield master OFF")
	}
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

func TestThrottleGuardDisabledIsNoop(t *testing.T) {
	s := &server{}
	ok, retry := s.throttleGuard(context.Background(), "account", nil, nil, "someone")
	if !ok || retry != 0 {
		t.Fatalf("nil throttle store must allow with no retry, got ok=%v retry=%v", ok, retry)
	}
	_ = time.Second
}
