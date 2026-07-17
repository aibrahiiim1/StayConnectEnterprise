package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/keybootstrap"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/otpkey"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

// authWindow is the durable-throttle fixed window length.
const authWindow = time.Minute

// initAuthSecurity constructs the Phase 1B dark-auth machinery and attaches it to the server. It is
// FAIL-CLOSED for anything explicitly enabled: an enabled feature whose key material is missing or
// disagrees with the database aborts startup rather than running with weakened enforcement. When a
// feature flag is off the corresponding field stays nil and the legacy path runs unchanged.
//
// IAM-v2 is ALWAYS constructed here, but from LoadConfigFromEnv(production=true) with every method
// flag defaulting OFF, so the authenticator short-circuits to DecisionDisabled without ever touching a
// repository — and we pass a nil repository precisely because the master flag is off. Nothing in this
// function issues a single IAM-v2 SQL statement.
func (s *server) initAuthSecurity(ctx context.Context, pool *pgxpool.Pool, c cfg, log *slog.Logger) error {
	// --- dark IAM-v2 (always constructed, always inert unless env flags are set) ---
	iamCfg, err := iamv2.LoadConfigFromEnv(os.Getenv, true)
	if err != nil {
		return fmt.Errorf("iamv2 config: %w", err)
	}
	var iamRepo iamv2.Repository // nil: prefer NO repository while the master flag is OFF
	if iamCfg.MasterEnabled {
		// Not reachable in Phase 1B (flags OFF); wiring a real repo here is a Phase 2 cutover step.
		return fmt.Errorf("iamv2 master flag is enabled but no production repository is wired (Phase 2 only)")
	}
	auth, err := iamv2.New(iamCfg, iamRepo, iamv2.NopObserver{})
	if err != nil {
		return fmt.Errorf("iamv2 new: %w", err)
	}
	s.iamv2Auth = auth
	log.Info("iamv2 dark authenticator constructed", "flags", iamCfg.SafeFlagSummary())

	// --- durable throttle (D4) ---
	if c.DurableThrottle {
		keyPath := filepath.Join(c.SecretsDir, "throttle.key")
		key, err := localkeys.LoadExistingKey(keyPath) // runtime is load-only; absent => fail closed
		if err != nil {
			return fmt.Errorf("durable throttle enabled but key unavailable (run keybootstrap at deploy): %w", err)
		}
		st, err := throttle.New(pool, key, authWindow)
		if err != nil {
			return fmt.Errorf("throttle store: %w", err)
		}
		s.authThrottle = st
		log.Info("durable auth throttle enabled")
	}

	// --- keyed-HMAC OTP ring (D7) ---
	if c.OTPHMAC {
		ring, err := loadOTPRing(ctx, pool, c.SecretsDir)
		if err != nil {
			return fmt.Errorf("OTP keyed-HMAC enabled but ring unavailable: %w", err)
		}
		s.otpRing = ring
		log.Info("keyed-HMAC OTP ring enabled", "active_generation", ring.Active(), "generations", ring.Generations())
	}
	return nil
}

// loadOTPRing loads the OTP generation key files (runtime load-only) and validates them against the
// database lifecycle metadata: exactly one active generation, and key material present for the active
// generation and every generation still referenced by an unexpired OTP. Any disagreement fails closed.
func loadOTPRing(ctx context.Context, pool *pgxpool.Pool, secretsDir string) (*otpkey.Ring, error) {
	keys, err := localkeys.LoadGenerationKeys(secretsDir, keybootstrap.OTPPrefix)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no OTP key generations present in %s (run keybootstrap at deploy)", secretsDir)
	}
	var active int
	if err := pool.QueryRow(ctx,
		`SELECT generation FROM public.otp_hmac_key_generations WHERE active`).Scan(&active); err != nil {
		return nil, fmt.Errorf("read active OTP generation: %w", err)
	}
	referenced, err := referencedOTPGenerations(ctx, pool)
	if err != nil {
		return nil, err
	}
	if err := localkeys.ValidateOTPGenerations(keys, active, referenced); err != nil {
		return nil, err
	}
	return otpkey.NewRing(keys, active)
}

func referencedOTPGenerations(ctx context.Context, pool *pgxpool.Pool) ([]int, error) {
	rows, err := pool.Query(ctx,
		`SELECT DISTINCT otp_key_generation FROM public.auth_otps
		  WHERE otp_key_generation IS NOT NULL AND expires_at > now()`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var g int
		if err := rows.Scan(&g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// throttleGuard charges the durable throttle for one auth attempt with an EXPLICIT method and returns
// whether the attempt may proceed. It fails closed (deny) on any store error. Scope values are HMAC'd
// inside the store, so no raw IP/MAC/identity is persisted or logged here. When the durable throttle
// is disabled this is a no-op that always allows (legacy in-memory limiter still applies where wired).
//
// method must be one of the throttle package's known methods (account|otp|voucher|social|pms). The
// identity value is an already-non-sensitive selector (e.g. lowercased username or destination); pass
// "" to skip that dimension.
func (s *server) throttleGuard(ctx context.Context, method string, ip net.IP, mac net.HardwareAddr, identity string) (bool, time.Duration) {
	if s.authThrottle == nil {
		return true, 0
	}
	rules := []throttle.Rule{
		// endpoint-wide damper shared across methods (deliberate MethodAny)
		{Kind: throttle.ScopeEndpoint, Value: "auth", Method: throttle.MethodAny, Limit: 600},
	}
	if ip != nil {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIP, Value: ip.String(), Method: method, Limit: 30, Block: 15 * time.Minute})
	}
	if len(mac) != 0 {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeDevice, Value: mac.String(), Method: method, Limit: 30, Block: 15 * time.Minute})
	}
	if identity != "" {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity, Value: identity, Method: method, Limit: 10, Block: 15 * time.Minute})
	}
	d, err := s.authThrottle.Allow(ctx, rules...)
	if err != nil {
		// FAIL CLOSED: a broken throttle store must never silently permit unlimited attempts.
		return false, authWindow
	}
	return d.Allowed, d.RetryAfter
}
