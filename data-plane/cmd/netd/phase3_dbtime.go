package main

// Database call timing for the pass log: how many calls of each kind, their total and slowest time. The pass makes serial round trips to the
// database for admission, proof and teardown bookkeeping; when the database is slow the pass is slow, and this is how that shows.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

type dbStat struct {
	n          int
	total, max time.Duration
}

type dbTimer struct {
	mu sync.Mutex
	m  map[string]*dbStat
}

var dbTimes = &dbTimer{m: map[string]*dbStat{}}

func (t *dbTimer) observe(name string, d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.m[name]
	if s == nil {
		s = &dbStat{}
		t.m[name] = s
	}
	s.n++
	s.total += d
	if d > s.max {
		s.max = d
	}
}

// drain reports and resets the calls made since the last drain, as "calls/total ms/slowest ms" per call kind.
func (t *dbTimer) drain() []slog.Attr {
	t.mu.Lock()
	defer t.mu.Unlock()
	names := make([]string, 0, len(t.m))
	for k := range t.m {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]slog.Attr, 0, len(names))
	for _, k := range names {
		s := t.m[k]
		out = append(out, slog.String("db_"+k, fmt.Sprintf("%d/%d/%d", s.n, s.total.Milliseconds(), s.max.Milliseconds())))
	}
	t.m = map[string]*dbStat{}
	return out
}

type timedEnforcement struct{ enforcementRecorder }

func (e timedEnforcement) Activate(ctx context.Context, sessionID, bridge string, minor int, epoch int64) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
	defer cancel()
	t0 := time.Now()
	defer func() { dbTimes.observe("activate", time.Since(t0)) }()
	return e.enforcementRecorder.Activate(ctx, sessionID, bridge, minor, epoch)
}

func (e timedEnforcement) Ended(ctx context.Context, sessionID, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
	defer cancel()
	t0 := time.Now()
	defer func() { dbTimes.observe("ended", time.Since(t0)) }()
	return e.enforcementRecorder.Ended(ctx, sessionID, reason)
}

func (e timedEnforcement) Confirm(ctx context.Context, sessionID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
	defer cancel()
	t0 := time.Now()
	defer func() { dbTimes.observe("confirm", time.Since(t0)) }()
	return e.enforcementRecorder.Confirm(ctx, sessionID)
}

// IsEnded forwards the plain read when the recorder has one; otherwise the caller falls back to the writing call.
func (e timedEnforcement) IsEnded(ctx context.Context, sessionID string) (bool, error) {
	er, ok := e.enforcementRecorder.(endedReader)
	if !ok {
		return false, errors.New("the recorder cannot answer without writing")
	}
	ctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
	defer cancel()
	t0 := time.Now()
	defer func() { dbTimes.observe("is_ended", time.Since(t0)) }()
	return er.IsEnded(ctx, sessionID)
}

type timedOrigins struct{ originRegistrar }

func (o timedOrigins) RegisterClassOrigin(ctx context.Context, c classOrigin) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
	defer cancel()
	t0 := time.Now()
	defer func() { dbTimes.observe("origin", time.Since(t0)) }()
	return o.originRegistrar.RegisterClassOrigin(ctx, c)
}

type timedGenerations struct{ generationAllocator }

func (g timedGenerations) AllocateClassGeneration(ctx context.Context, tenant, site, appliance string) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, phase3DBCallTimeout)
	defer cancel()
	t0 := time.Now()
	defer func() { dbTimes.observe("alloc", time.Since(t0)) }()
	return g.generationAllocator.AllocateClassGeneration(ctx, tenant, site, appliance)
}
