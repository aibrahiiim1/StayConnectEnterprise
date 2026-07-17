// Package throttle is a durable, local-first authentication throttle backed by the
// public.auth_throttle_buckets table (decision D4). It replaces the process-only counters as the
// AUTHORITATIVE attempt state — it survives service restart and appliance reboot, is safe across
// multiple worker processes (atomic INSERT ... ON CONFLICT DO UPDATE), and stores only an
// irreversible HMAC of each scope value (no raw identity / IP / MAC / username / OTP / credential).
//
// It has no Central/Redis dependency and uses only the local site database. On database failure it
// FAILS CLOSED (denies the attempt) so a broken store can never silently permit unlimited attempts.
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
	ScopeEndpoint ScopeKind = "endpoint" // whole appliance / endpoint
	ScopeIdentity ScopeKind = "identity" // username / principal / account
	ScopeIP       ScopeKind = "ip"       // source IP
	ScopeDevice   ScopeKind = "device"   // device MAC
	ScopeMethod   ScopeKind = "method"   // auth method (voucher/account/otp/social)
)

func (s ScopeKind) valid() bool {
	switch s {
	case ScopeEndpoint, ScopeIdentity, ScopeIP, ScopeDevice, ScopeMethod:
		return true
	}
	return false
}

// Rule is one scope's limit for the current attempt.
type Rule struct {
	Kind  ScopeKind
	Value string        // raw value (hmac'd before storage; "" allowed only for endpoint)
	Limit int           // max attempts per window
	Block time.Duration // if >0, once the cap is exceeded, hard-block for this long
}

// Decision is the result of charging an attempt.
type Decision struct {
	Allowed    bool
	RetryAfter time.Duration // hint until the caller may retry (0 when Allowed)
	Reason     ScopeKind     // the scope that denied (when !Allowed)
}

// Store is the durable throttle repository.
type Store struct {
	db     *pgxpool.Pool
	key    []byte        // HMAC key for scope-key derivation (appliance-local; never stored)
	window time.Duration // fixed-window length
	now    func() time.Time
}

// New builds a Store. key must be non-empty (appliance-local secret). window is the fixed-window size.
func New(db *pgxpool.Pool, key []byte, window time.Duration) (*Store, error) {
	if db == nil {
		return nil, errors.New("throttle: nil db")
	}
	if len(key) == 0 {
		return nil, errors.New("throttle: empty HMAC key")
	}
	if window <= 0 {
		window = time.Minute
	}
	return &Store{db: db, key: key, window: window, now: time.Now}, nil
}

// scopeKey returns the irreversible per-scope key (hex HMAC-SHA256). No raw value is stored.
func (s *Store) scopeKey(kind ScopeKind, value string) string {
	m := hmac.New(sha256.New, s.key)
	m.Write([]byte(string(kind)))
	m.Write([]byte{0})
	m.Write([]byte(strings.ToLower(strings.TrimSpace(value))))
	return hex.EncodeToString(m.Sum(nil))
}

// windowStart truncates now to the fixed window.
func (s *Store) windowStart(now time.Time) time.Time {
	return now.Truncate(s.window).UTC()
}

// Allow charges one attempt against every rule and returns whether the attempt may proceed.
// Every rule is charged (not short-circuited) so counts stay honest under concurrency. FAILS CLOSED:
// any database error denies the attempt.
func (s *Store) Allow(ctx context.Context, rules ...Rule) (Decision, error) {
	now := s.now().UTC()
	ws := s.windowStart(now)
	wl := int(s.window / time.Second)
	allowed := true
	var reason ScopeKind
	var worstRetry time.Duration

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Decision{Allowed: false, Reason: ScopeEndpoint}, fmt.Errorf("throttle: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	for _, r := range rules {
		if !r.Kind.valid() {
			return Decision{Allowed: false}, fmt.Errorf("throttle: invalid scope %q", r.Kind)
		}
		if r.Value == "" && r.Kind != ScopeEndpoint {
			continue // nothing to key on (e.g. unknown MAC); skip that layer
		}
		if r.Limit <= 0 {
			continue
		}
		sk := s.scopeKey(r.Kind, r.Value)

		// Existing hard block?
		var blockedUntil *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT blocked_until FROM public.auth_throttle_buckets
			   WHERE scope_kind=$1 AND scope_key=$2 AND window_start=$3`,
			string(r.Kind), sk, ws).Scan(&blockedUntil); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Decision{Allowed: false, Reason: r.Kind}, fmt.Errorf("throttle: read: %w", err)
		}
		if blockedUntil != nil && blockedUntil.After(now) {
			allowed = false
			reason = r.Kind
			if d := blockedUntil.Sub(now); d > worstRetry {
				worstRetry = d
			}
			continue
		}

		// Atomic increment (concurrency-safe).
		var count int
		if err := tx.QueryRow(ctx,
			`INSERT INTO public.auth_throttle_buckets
			     (scope_kind, scope_key, window_start, window_len_s, attempt_count, updated_at)
			 VALUES ($1,$2,$3,$4,1, now())
			 ON CONFLICT (scope_kind, scope_key, window_start)
			 DO UPDATE SET attempt_count = public.auth_throttle_buckets.attempt_count + 1,
			               updated_at = now()
			 RETURNING attempt_count`,
			string(r.Kind), sk, ws, wl).Scan(&count); err != nil {
			return Decision{Allowed: false, Reason: r.Kind}, fmt.Errorf("throttle: incr: %w", err)
		}

		if count > r.Limit {
			allowed = false
			reason = r.Kind
			retry := s.windowStart(now).Add(s.window).Sub(now)
			if r.Block > 0 {
				bu := now.Add(r.Block)
				if _, err := tx.Exec(ctx,
					`UPDATE public.auth_throttle_buckets SET blocked_until=$4, updated_at=now()
					   WHERE scope_kind=$1 AND scope_key=$2 AND window_start=$3`,
					string(r.Kind), sk, ws, bu); err != nil {
					return Decision{Allowed: false, Reason: r.Kind}, fmt.Errorf("throttle: block: %w", err)
				}
				retry = r.Block
			}
			if retry > worstRetry {
				worstRetry = retry
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Decision{Allowed: false, Reason: ScopeEndpoint}, fmt.Errorf("throttle: commit: %w", err)
	}
	return Decision{Allowed: allowed, RetryAfter: worstRetry, Reason: reason}, nil
}

// Cleanup deletes buckets whose window (plus grace) has fully expired and whose block, if any, has
// passed. Returns the number of rows removed. Bounded retention; safe to run periodically.
func (s *Store) Cleanup(ctx context.Context, grace time.Duration) (int64, error) {
	if grace < 0 {
		grace = 0
	}
	cutoff := s.now().UTC().Add(-s.window - grace)
	ct, err := s.db.Exec(ctx,
		`DELETE FROM public.auth_throttle_buckets
		   WHERE window_start < $1 AND (blocked_until IS NULL OR blocked_until < now())`,
		cutoff)
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
