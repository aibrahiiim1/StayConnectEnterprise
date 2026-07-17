// Package throttle is a durable, local-first authentication throttle backed by the
// public.auth_throttle_buckets table (decision D4). It replaces the process-only counters as the
// AUTHORITATIVE attempt state — it survives service restart and appliance reboot, is safe across
// multiple worker processes (atomic INSERT ... ON CONFLICT DO UPDATE), and stores only an
// irreversible HMAC of each scope value (no raw identity / IP / MAC / username / OTP / credential).
//
// Every bucket carries an explicit normalized auth METHOD so unrelated methods never share a counter
// for the same identity/IP/device. A hard block (blocked_until) applies across every later window for
// that throttle identity until it expires. It has no Central/Redis dependency and uses only the local
// site database. On database failure it FAILS CLOSED (denies the attempt) so a broken store can never
// silently permit unlimited attempts.
package throttle

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScopeKind enumerates the throttle dimensions (must match the DB CHECK constraint).
type ScopeKind string

const (
	ScopeEndpoint ScopeKind = "endpoint"
	ScopeIdentity ScopeKind = "identity"
	ScopeIP       ScopeKind = "ip"
	ScopeDevice   ScopeKind = "device"
	ScopeMethod   ScopeKind = "method"
)

func (s ScopeKind) valid() bool {
	switch s {
	case ScopeEndpoint, ScopeIdentity, ScopeIP, ScopeDevice, ScopeMethod:
		return true
	}
	return false
}

// MethodAny is the deliberate global method value (a policy shared across all auth methods).
const MethodAny = "*"

func validMethod(m string) bool {
	switch m {
	case "account", "otp", "voucher", "social", "pms", MethodAny:
		return true
	}
	return false
}

// Rule is one scope's limit for the current attempt. Method isolates the auth method (use MethodAny
// only for policies intentionally shared across methods, e.g. an endpoint-wide damper).
type Rule struct {
	Kind   ScopeKind
	Value  string // raw value (hmac'd before storage; "" allowed only for endpoint)
	Method string // account|otp|voucher|social|pms|* (defaults to "*")
	Limit  int
	Block  time.Duration // if >0, once the cap is exceeded, hard-block for this long (across windows)
}

// Decision is the result of charging an attempt.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	Reason     ScopeKind
}

// Store is the durable throttle repository.
type Store struct {
	db     *pgxpool.Pool
	key    []byte
	window time.Duration
	now    func() time.Time
}

// New builds a Store. key must be non-empty (>=16B; production loaders require >=32B). window is the
// fixed-window length.
func New(db *pgxpool.Pool, key []byte, window time.Duration) (*Store, error) {
	if db == nil {
		return nil, errors.New("throttle: nil db")
	}
	if len(key) < 16 {
		return nil, errors.New("throttle: HMAC key too short")
	}
	if window <= 0 {
		window = time.Minute
	}
	k := make([]byte, len(key))
	copy(k, key)
	return &Store{db: db, key: k, window: window, now: time.Now}, nil
}

// SetClock overrides the time source (tests only).
func (s *Store) SetClock(f func() time.Time) { s.now = f }

func (s *Store) scopeKey(kind ScopeKind, value string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(string(kind)))
	m.Write([]byte{0})
	m.Write([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Store) windowStart(now time.Time) time.Time { return now.Truncate(s.window).UTC() }

// Allow charges one attempt against every rule and returns whether the attempt may proceed. FAILS
// CLOSED: any database error denies the attempt. A hard block is honored across window boundaries.
func (s *Store) Allow(ctx context.Context, rules ...Rule) (Decision, error) {
	now := s.now().UTC()
	ws := s.windowStart(now)
	wl := int(s.window / time.Second)
	allowed := true
	var reason ScopeKind
	var worstRetry time.Duration

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Decision{Reason: ScopeEndpoint}, fmt.Errorf("throttle: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, r := range rules {
		if !r.Kind.valid() {
			return Decision{}, fmt.Errorf("throttle: invalid scope %q", r.Kind)
		}
		method := strings.ToLower(strings.TrimSpace(r.Method))
		if method == "" {
			// Explicit method is required; use MethodAny deliberately for a global policy.
			return Decision{}, errors.New("throttle: rule is missing an explicit method")
		}
		if !validMethod(method) {
			return Decision{}, fmt.Errorf("throttle: invalid method %q", method)
		}
		if r.Value == "" && r.Kind != ScopeEndpoint {
			continue
		}
		if r.Limit <= 0 {
			continue
		}
		sk := s.scopeKey(r.Kind, r.Value)

		// Cross-window active block: the greatest blocked_until for this throttle identity across ALL
		// windows. A long block therefore stays effective after the fixed window rolls over.
		var maxBlock *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT max(blocked_until) FROM public.auth_throttle_buckets
			   WHERE scope_kind=$1 AND scope_key=$2 AND method=$3 AND blocked_until > $4`,
			string(r.Kind), sk, method, now).Scan(&maxBlock); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Decision{Reason: r.Kind}, fmt.Errorf("throttle: block-read: %w", err)
		}
		if maxBlock != nil && maxBlock.After(now) {
			allowed = false
			reason = r.Kind
			if d := maxBlock.Sub(now); d > worstRetry {
				worstRetry = d
			}
			continue // do not charge a new attempt while hard-blocked
		}

		var count int
		if err := tx.QueryRow(ctx,
			`INSERT INTO public.auth_throttle_buckets
			     (scope_kind, scope_key, method, window_start, window_len_s, attempt_count, updated_at)
			 VALUES ($1,$2,$3,$4,$5,1, now())
			 ON CONFLICT (scope_kind, scope_key, method, window_start)
			 DO UPDATE SET attempt_count = public.auth_throttle_buckets.attempt_count + 1,
			               updated_at = now()
			 RETURNING attempt_count`,
			string(r.Kind), sk, method, ws, wl).Scan(&count); err != nil {
			return Decision{Reason: r.Kind}, fmt.Errorf("throttle: incr: %w", err)
		}

		if count > r.Limit {
			allowed = false
			reason = r.Kind
			retry := ws.Add(s.window).Sub(now)
			if r.Block > 0 {
				bu := now.Add(r.Block)
				if _, err := tx.Exec(ctx,
					`UPDATE public.auth_throttle_buckets SET blocked_until=$5, updated_at=now()
					   WHERE scope_kind=$1 AND scope_key=$2 AND method=$3 AND window_start=$4`,
					string(r.Kind), sk, method, ws, bu); err != nil {
					return Decision{Reason: r.Kind}, fmt.Errorf("throttle: block: %w", err)
				}
				retry = r.Block
			}
			if retry > worstRetry {
				worstRetry = retry
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Decision{Reason: ScopeEndpoint}, fmt.Errorf("throttle: commit: %w", err)
	}
	return Decision{Allowed: allowed, RetryAfter: worstRetry, Reason: reason}, nil
}

// Cleanup deletes buckets whose window (plus grace) has fully expired and that hold no active block.
// Bounded retention; never removes a bucket with an unexpired blocked_until.
func (s *Store) Cleanup(ctx context.Context, grace time.Duration) (int64, error) {
	if grace < 0 {
		grace = 0
	}
	now := s.now().UTC()
	cutoff := now.Add(-s.window - grace)
	ct, err := s.db.Exec(ctx,
		`DELETE FROM public.auth_throttle_buckets
		   WHERE window_start < $1 AND (blocked_until IS NULL OR blocked_until <= $2)`,
		cutoff, now)
	if err != nil {
		return 0, fmt.Errorf("throttle: cleanup: %w", err)
	}
	return ct.RowsAffected(), nil
}

// RunCleanup runs Cleanup on a ticker until ctx is cancelled (safe shutdown).
func (s *Store) RunCleanup(ctx context.Context, every, grace time.Duration) {
	if every <= 0 {
		every = 5 * time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = s.Cleanup(ctx, grace)
		}
	}
}
