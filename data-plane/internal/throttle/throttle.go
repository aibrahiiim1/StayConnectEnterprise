// Package throttle is a durable authentication throttle on public.auth_throttle_buckets. It stores only an
// HMAC of each scope value, keeps counters per auth method, and fails closed on database errors.
package throttle

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ScopeKind must match the DB CHECK constraint.
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

// MethodAny is a policy shared across all auth methods.
const MethodAny = "*"

func validMethod(m string) bool {
	switch m {
	// post_stay_pin is separate so post-stay brute force neither consumes nor is masked by the portal budget.
	case "account", "otp", "voucher", "social", "pms", "post_stay_pin", MethodAny:
		return true
	}
	return false
}

// Rule is one scope's limit for the current attempt. Method is required; MethodAny only for shared policies.
type Rule struct {
	Kind   ScopeKind
	Value  string // raw value, HMAC'd before storage; "" allowed only for endpoint
	Method string // account|otp|voucher|social|pms|post_stay_pin|*
	Limit  int
	Block  time.Duration // if >0, once the cap is exceeded, hard-block for this long across windows
	// Global marks an appliance-wide damper. It is charged only when every non-global rule allowed the
	// attempt, so one refused client cannot exhaust the shared budget and lock everybody out.
	Global bool
}

type Decision struct {
	Allowed    bool
	RetryAfter time.Duration
	Reason     ScopeKind
}

type Store struct {
	db     *pgxpool.Pool
	key    []byte
	window time.Duration
	now    func() time.Time
	// noCharge is set once the database reports it has no public.auth_throttle_charge (a database that predates
	// migration 0163); Allow then uses the multi-statement path below. Any other error denies.
	noCharge atomic.Bool
}

// New builds a Store. key must be at least 16 bytes; window is the fixed-window length.
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

type chargeable struct {
	kind   ScopeKind
	sk     string
	method string
	limit  int
	block  time.Duration
	global bool
}

// normalizeRules drops no-op rules, dedups identical identities (tightest limit, longest block) and sorts
// deterministically so concurrent Allow calls take row locks in one order and cannot deadlock.
func (s *Store) normalizeRules(rules []Rule) ([]chargeable, error) {
	merged := make(map[string]*chargeable, len(rules))
	for _, r := range rules {
		if !r.Kind.valid() {
			return nil, fmt.Errorf("throttle: invalid scope %q", r.Kind)
		}
		method := strings.ToLower(strings.TrimSpace(r.Method))
		if method == "" {
			return nil, errors.New("throttle: rule is missing an explicit method")
		}
		if !validMethod(method) {
			return nil, fmt.Errorf("throttle: invalid method %q", method)
		}
		// A limit of zero or less is a configuration error, never "no limit": refusing it keeps a typo from silently
		// switching a rule off. An empty value is an identity the caller does not have (no device, no account) and is skipped.
		if r.Limit <= 0 {
			return nil, fmt.Errorf("throttle: rule %s/%s has no positive limit", r.Kind, method)
		}
		if r.Value == "" && r.Kind != ScopeEndpoint {
			continue
		}
		sk := s.scopeKey(r.Kind, r.Value)
		id := string(r.Kind) + "\x00" + sk + "\x00" + method
		if e, ok := merged[id]; ok {
			if r.Limit < e.limit {
				e.limit = r.Limit
			}
			if r.Block > e.block {
				e.block = r.Block
			}
			// Both global and client-scoped in one call counts as client-scoped (always charged).
			e.global = e.global && r.Global
			continue
		}
		merged[id] = &chargeable{kind: r.Kind, sk: sk, method: method, limit: r.Limit, block: r.Block, global: r.Global}
	}
	out := make([]chargeable, 0, len(merged))
	for _, e := range merged {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		// Global rules last: their charge depends on the others' verdict.
		if out[i].global != out[j].global {
			return !out[i].global
		}
		if out[i].kind != out[j].kind {
			return out[i].kind < out[j].kind
		}
		if out[i].sk != out[j].sk {
			return out[i].sk < out[j].sk
		}
		return out[i].method < out[j].method
	})
	return out, nil
}

// Allow charges one attempt against every rule. Any database error denies the attempt (fail closed), and
// a hard block holds across window boundaries.
func (s *Store) Allow(ctx context.Context, rules ...Rule) (Decision, error) {
	entries, err := s.normalizeRules(rules)
	if err != nil {
		return Decision{}, err
	}
	now := s.now().UTC()
	ws := s.windowStart(now)
	wl := int(s.window / time.Second)
	if !s.noCharge.Load() {
		d, err := s.allowOneTrip(ctx, entries, now, ws, wl)
		if err == nil {
			return d, nil
		}
		var pgErr *pgconn.PgError
		if !(errors.As(err, &pgErr) && pgErr.Code == "42883") {
			return Decision{Reason: ScopeEndpoint}, err
		}
		s.noCharge.Store(true)
		slog.Warn("throttle: auth_throttle_charge is missing (database predates migration 0163); using the multi-statement path")
	}
	allowed := true
	var reason ScopeKind
	var worstRetry time.Duration

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Decision{Reason: ScopeEndpoint}, fmt.Errorf("throttle: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, r := range entries {
		method := r.method
		sk := r.sk
		if r.global && !allowed {
			continue
		}

		// The greatest blocked_until across all windows, so a block outlives the fixed window.
		var maxBlock *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT max(blocked_until) FROM public.auth_throttle_buckets
			   WHERE scope_kind=$1 AND scope_key=$2 AND method=$3 AND blocked_until > $4`,
			string(r.kind), sk, method, now).Scan(&maxBlock); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Decision{Reason: r.kind}, fmt.Errorf("throttle: block-read: %w", err)
		}
		if maxBlock != nil && maxBlock.After(now) {
			allowed = false
			if d := maxBlock.Sub(now); d > worstRetry || reason == "" {
				worstRetry, reason = max(d, worstRetry), r.kind
			}
			continue
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
			string(r.kind), sk, method, ws, wl).Scan(&count); err != nil {
			return Decision{Reason: r.kind}, fmt.Errorf("throttle: incr: %w", err)
		}

		if count > r.limit {
			allowed = false
			retry := ws.Add(s.window).Sub(now)
			if r.block > 0 {
				bu := now.Add(r.block)
				if _, err := tx.Exec(ctx,
					`UPDATE public.auth_throttle_buckets SET blocked_until=$5, updated_at=now()
					   WHERE scope_kind=$1 AND scope_key=$2 AND method=$3 AND window_start=$4`,
					string(r.kind), sk, method, ws, bu); err != nil {
					return Decision{Reason: r.kind}, fmt.Errorf("throttle: block: %w", err)
				}
				retry = r.block
			}
			// The reported reason is the rule with the longest wait, so it matches Retry-After.
			if retry > worstRetry || reason == "" {
				worstRetry, reason = max(retry, worstRetry), r.kind
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Decision{Reason: ScopeEndpoint}, fmt.Errorf("throttle: commit: %w", err)
	}
	return Decision{Allowed: allowed, RetryAfter: worstRetry, Reason: reason}, nil
}

// allowOneTrip charges every rule in one database round trip (AD-51; migration 0163), a port of the loop below.
func (s *Store) allowOneTrip(ctx context.Context, entries []chargeable, now, ws time.Time, wl int) (Decision, error) {
	n := len(entries)
	kinds, keys, methods := make([]string, n), make([]string, n), make([]string, n)
	limits, blocks, globals := make([]int32, n), make([]int32, n), make([]bool, n)
	for i, e := range entries {
		kinds[i], keys[i], methods[i] = string(e.kind), e.sk, e.method
		limits[i], blocks[i], globals[i] = int32(e.limit), int32(e.block/time.Second), e.global
	}
	var allowed bool
	var retryMS int64
	var reason *string
	if err := s.db.QueryRow(ctx,
		`SELECT allowed, retry_milliseconds, reason
		   FROM public.auth_throttle_charge($1::text[],$2::text[],$3::text[],$4::int[],$5::int[],$6::bool[],$7,$8,$9)`,
		kinds, keys, methods, limits, blocks, globals, now, ws, wl).Scan(&allowed, &retryMS, &reason); err != nil {
		return Decision{}, fmt.Errorf("throttle: charge: %w", err)
	}
	d := Decision{Allowed: allowed, RetryAfter: time.Duration(retryMS) * time.Millisecond}
	if reason != nil {
		d.Reason = ScopeKind(*reason)
	}
	return d, nil
}

// Cleanup deletes expired buckets, never one with an unexpired blocked_until.
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

// RunCleanup runs Cleanup on a ticker until ctx is cancelled.
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
			if _, err := s.Cleanup(ctx, grace); err != nil && ctx.Err() == nil {
				slog.Warn("throttle: cleanup failed", "err", err)
			}
		}
	}
}
