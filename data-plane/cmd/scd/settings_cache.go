package main

// AD-52: the settings every guest sign-in reads (the method switches on the tenant row, the sign-in limits) are kept for
// a few seconds and cleared the moment the database says one changed. Two things bound how stale a value can be:
//
//   - the TTL, which is the whole guarantee when the notification connection is down (so an Admin Console "off" takes
//     effect within settingsCacheTTL at worst, never "eventually");
//   - the notification on channel onegate_settings, sent by a trigger on the settings tables, which clears the cache at
//     once. A dropped notification therefore costs seconds, never correctness.
//
// The same connection carries onegate_session_active, which wakes the last sign-in step instead of it polling every
// 100 ms. The poll stays as the fallback (and is the only path while the connection is down).
//
// Only settings are cached: no guest data, no credential, no decision about a particular guest.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const settingsCacheTTL = 5 * time.Second

type ttlEntry struct {
	val   any
	until time.Time
	epoch uint64
}

type ttlCache struct {
	ttl   time.Duration
	now   func() time.Time
	epoch atomic.Uint64
	mu    sync.Mutex
	m     map[string]ttlEntry
}

func newTTLCache(ttl time.Duration) *ttlCache {
	return &ttlCache{ttl: ttl, now: time.Now, m: map[string]ttlEntry{}}
}

// get returns the cached value for key or loads it. A load that fails is returned and never cached, so one database
// error does not become a window of refusals. A value loaded across an invalidation is not stored.
func (c *ttlCache) get(key string, load func() (any, error)) (any, error) {
	if c == nil {
		return load()
	}
	epoch := c.epoch.Load()
	c.mu.Lock()
	e, ok := c.m[key]
	c.mu.Unlock()
	if ok && e.epoch == epoch && c.now().Before(e.until) {
		return e.val, nil
	}
	v, err := load()
	if err != nil {
		return nil, err
	}
	if c.epoch.Load() == epoch {
		c.mu.Lock()
		c.m[key] = ttlEntry{val: v, until: c.now().Add(c.ttl), epoch: epoch}
		c.mu.Unlock()
	}
	return v, nil
}

// invalidate makes every entry stale at once.
func (c *ttlCache) invalidate() {
	if c != nil {
		c.epoch.Add(1)
	}
}

// sessionWaiters wakes whoever awaits a session becoming active.
type sessionWaiters struct {
	mu sync.Mutex
	m  map[string][]chan struct{}
	up atomic.Bool // the notification connection is established
}

func newSessionWaiters() *sessionWaiters { return &sessionWaiters{m: map[string][]chan struct{}{}} }

// wait registers for id; the returned release must be called.
func (w *sessionWaiters) wait(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	w.mu.Lock()
	w.m[id] = append(w.m[id], ch)
	w.mu.Unlock()
	return ch, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		l := w.m[id]
		for i := range l {
			if l[i] == ch {
				l = append(l[:i], l[i+1:]...)
				break
			}
		}
		if len(l) == 0 {
			delete(w.m, id)
		} else {
			w.m[id] = l
		}
	}
}

func (w *sessionWaiters) wake(id string) {
	w.mu.Lock()
	for _, ch := range w.m[id] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	w.mu.Unlock()
}

// runNotifications holds one connection LISTENing on both channels until ctx ends, reconnecting with a capped backoff.
// While it is down the TTL and the poll are the only mechanisms, which is the designed fallback.
func (s *server) runNotifications(ctx context.Context, dsn string) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := s.listenOnce(ctx, dsn)
		s.waiters.up.Store(false)
		// Whatever was cached while the connection was away may be stale.
		s.settings.invalidate()
		if ctx.Err() != nil {
			return
		}
		slog.Warn("settings notifications: connection lost; the cache TTL and the poll are the fallback", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func (s *server) listenOnce(ctx context.Context, dsn string) error {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, err := pgx.Connect(cctx, dsn)
	cancel()
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, `LISTEN onegate_settings`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `LISTEN onegate_session_active`); err != nil {
		return err
	}
	s.waiters.up.Store(true)
	s.settings.invalidate()
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		switch n.Channel {
		case "onegate_settings":
			s.settings.invalidate()
		case "onegate_session_active":
			s.waiters.wake(n.Payload)
		}
	}
}

// scdDefaultPoolMaxConns sizes scd's database pool explicitly (AD-26). pgx's default is max(4, CPUs): four connections
// for up to maxGuestInFlight guest requests, each of which may hold one for several statements, so a burst of 300-500
// clients connecting at once queued on the pool before it ever reached a CPU limit. Sixteen is well inside PostgreSQL's
// connection limit (scd, edged, pmsd, acctd, netd and portald together stay under 80 of the default 100). A
// pool_max_conns in SCD_DB_URL still wins.
const scdDefaultPoolMaxConns = 16

func scdPoolConfig(dsn string) (*pgxpool.Config, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = scdDefaultPoolMaxConns
	}
	return cfg, nil
}
