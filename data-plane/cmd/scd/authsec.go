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

const authWindow = time.Minute

// Abuse dampers behind the operator-visible sign-in protection settings; deliberately not settings themselves.
const (
	throttleEndpointPerWindow = signinCeilingFloor // the floor of the appliance-wide ceiling; the effective value is signinCeiling (AD-51)
	throttleClientPerWindow   = 30               // one source IP, or one device, for one method
	throttleIdentityPerWindow = 10               // one identity from one client, for one method
	throttleBlock             = 15 * time.Minute // a client-scoped cap, once exceeded, blocks this long
)

// identityPerClient scopes the identity throttle to the identity plus the client making the attempt; the store HMACs it.
func identityPerClient(identity string, ip net.IP, mac net.HardwareAddr) string {
	client := ""
	switch {
	case len(mac) != 0:
		client = "mac=" + mac.String()
	case ip != nil:
		client = "ip=" + ip.String()
	}
	return identity + "\x00" + client
}

// initAuthSecurity fails closed for anything enabled: missing or inconsistent key material aborts startup.
// IAM-v2 is always constructed with a nil repository while its master flag is off, so it issues no SQL.
func (s *server) initAuthSecurity(ctx context.Context, pool *pgxpool.Pool, c cfg, log *slog.Logger) error {
	iamCfg, err := iamv2.LoadConfigFromEnv(os.Getenv, true)
	if err != nil {
		return fmt.Errorf("iamv2 config: %w", err)
	}
	// The repository is wired only when the master flag is on; while off the authenticator has none and
	// short-circuits to DecisionDisabled, never serving from legacy tables.
	var iamRepo iamv2.Repository // nil: prefer NO repository while the master flag is OFF
	if iamCfg.MasterEnabled {
		if pool == nil {
			return fmt.Errorf("iamv2 master flag is enabled but no database pool is available")
		}
		iamRepo = iamv2.NewPgRepository(pool)
	}
	// Vouchers store only a blind-index HMAC. Its key is resolved per generation and opened with the DEK
	// (not read from a raw file) so rotation and supersession work.
	var authOpts []iamv2.Option
	if iamCfg.Enabled(iamv2.MethodVoucher) {
		dekPath := filepath.Join(c.SecretsDir, voucherDEKFile)
		dek, err := localkeys.LoadExistingKey(dekPath)
		if err != nil {
			return fmt.Errorf("iamv2 VOUCHER is enabled but its code-encryption key is unavailable at %s "+
				"(run keybootstrap at deploy): %w", dekPath, err)
		}
		kr := iamv2.MapVoucherKeyring{voucherDEKID: dek}
		// Candidates: after a rotation, a voucher issued under the previous generation is found only by trying that key too.
		authOpts = append(authOpts, iamv2.WithVoucherHMACCandidates(
			newGenerationVoucherHMAC(pool, kr)))
	}
	auth, err := iamv2.New(iamCfg, iamRepo, iamv2.NopObserver{}, authOpts...)
	if err != nil {
		return fmt.Errorf("iamv2 new: %w", err)
	}
	s.iamv2Auth = auth
	// Keep the config: guest entry points must decide authority from the value the Authenticator was gated on.
	s.iamv2Cfg = iamCfg
	log.Info("iamv2 authenticator constructed", "flags", iamCfg.SafeFlagSummary())

	commCfg, err := iamv2.LoadCommerceConfigFromEnv(os.Getenv)
	if err != nil {
		return fmt.Errorf("phase2 commerce config: %w", err)
	}
	var commRepo iamv2.CommerceRepository // nil: NO repository while the master flag is OFF
	if commCfg.MasterEnabled {
		if pool == nil {
			return fmt.Errorf("phase2 commerce master flag is enabled but no database pool is available")
		}
		commRepo = iamv2.NewPgCommerceRepository(pool)
	}
	// Same flag as acctd's accrual tick: the aggregate acquisition capability and the accrual tick must never be configured independently.
	comm, err := iamv2.NewCommerceEngine(commCfg, commRepo, iamv2.NopObserver{},
		iamv2.WithAggregateOnlineTime(phase6AggregateOn()),
		// Effective acquisition methods come from the module resolver, read on every call and never cached.
		iamv2.WithMethodGate(s.siteAcquisitionMethods))
	if err != nil {
		return fmt.Errorf("phase2 commerce new: %w", err)
	}
	s.commerce = comm
	s.commerceCfg = commCfg
	log.Info("phase2 dark commerce engine constructed", "flags", commCfg.SafeFlagSummary())

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

// loadOTPRing validates the key files against the database lifecycle metadata; any disagreement fails closed.
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

// throttleGuard charges the durable throttle for one auth attempt and fails closed (deny) on store error; a no-op allow when disabled.
// method must be a known throttle method; identity is an already-non-sensitive selector ("" skips it).
func (s *server) throttleGuard(ctx context.Context, method string, ip net.IP, mac net.HardwareAddr, identity string) (bool, time.Duration) {
	return s.throttleCharge(ctx, method, ip, mac, identity, true)
}

// throttleGuardNoCeiling is the second step of a two-step sign-in (the one-time code's verify): the client's own limits
// still apply, but the appliance-wide ceiling was charged at issue, so one guest is not counted twice against it (AD-51).
func (s *server) throttleGuardNoCeiling(ctx context.Context, method string, ip net.IP, mac net.HardwareAddr, identity string) (bool, time.Duration) {
	return s.throttleCharge(ctx, method, ip, mac, identity, false)
}

func (s *server) throttleCharge(ctx context.Context, method string, ip net.IP, mac net.HardwareAddr, identity string, ceiling bool) (bool, time.Duration) {
	if s.authThrottle == nil {
		return true, 0
	}
	var rules []throttle.Rule
	if ceiling {
		// Endpoint-wide damper, charged only when the client's own rules allowed the attempt, so one blocked client cannot use up the shared budget.
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeEndpoint, Value: "auth", Method: throttle.MethodAny, Limit: s.signinCeiling(ctx), Global: true})
	}
	if ip != nil {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIP, Value: ip.String(), Method: method, Limit: throttleClientPerWindow, Block: throttleBlock})
	}
	if len(mac) != 0 {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeDevice, Value: mac.String(), Method: method, Limit: throttleClientPerWindow, Block: throttleBlock})
	}
	if identity != "" {
		// Count the identity per client: keyed on identity alone, anyone could lock a person out by guessing wrong at their address.
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity, Value: identityPerClient(identity, ip, mac),
			Method: method, Limit: throttleIdentityPerWindow, Block: throttleBlock})
	}
	d, err := s.authThrottle.Allow(ctx, rules...)
	if err != nil {
		// FAIL CLOSED: a broken throttle store must never silently permit unlimited attempts.
		return false, authWindow
	}
	return d.Allowed, d.RetryAfter
}
