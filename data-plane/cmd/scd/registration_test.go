package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// fakeClock hands out timer channels the test fires by hand and records the delays asked for.
type fakeClock struct {
	mu     sync.Mutex
	delays []time.Duration
	chans  []chan time.Time
	asked  chan struct{}
}

func newFakeClock() *fakeClock { return &fakeClock{asked: make(chan struct{}, 64)} }

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.delays = append(c.delays, d)
	c.chans = append(c.chans, ch)
	c.asked <- struct{}{}
	return ch
}

func (c *fakeClock) fireLast() {
	c.mu.Lock()
	ch := c.chans[len(c.chans)-1]
	c.mu.Unlock()
	ch <- time.Now()
}

func (c *fakeClock) waitAsked(t *testing.T) {
	t.Helper()
	select {
	case <-c.asked:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop never scheduled a retry")
	}
}

// F2: an appliance that boots while Central is down keeps retrying (30 s doubling to 5 min), records every
// outcome, and on the first success reports the identity exactly once.
func TestRegistrationLoopBacksOffAndStopsOnSuccess(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	attempts, registered := 0, 0
	var results []error
	failUntil := 5
	r := newRegistrar(
		func(context.Context) (*identity.Identity, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			if attempts <= failUntil {
				return nil, identity.ErrCentralUnreachable
			}
			return &identity.Identity{ApplianceID: "appl-1"}, nil
		},
		func(err error) { mu.Lock(); results = append(results, err); mu.Unlock() },
		func(*identity.Identity) { mu.Lock(); registered++; mu.Unlock() },
	)
	r.after = clock.after
	r.jitter = func(d time.Duration) time.Duration { return d } // deterministic

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()

	for i := 0; i < failUntil; i++ {
		clock.waitAsked(t)
		clock.fireLast()
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop must stop once registered")
	}
	want := []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute}
	clock.mu.Lock()
	defer clock.mu.Unlock()
	for i, d := range want {
		if clock.delays[i] != d {
			t.Fatalf("retry %d waited %s, want %s (all: %v)", i+1, clock.delays[i], d, clock.delays)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if registered != 1 || attempts != failUntil+1 || len(results) != attempts {
		t.Fatalf("registered=%d attempts=%d results=%d", registered, attempts, len(results))
	}
	if !errors.Is(results[0], identity.ErrCentralUnreachable) || results[len(results)-1] != nil {
		t.Fatal("every attempt's outcome must be reported, the last one a success")
	}
}

// Check now: a kick resets the backoff and retries immediately; an attempt made directly (the refresh handler)
// is serialised with the loop and a success is reported once.
func TestRegistrationKickAndDirectAttempt(t *testing.T) {
	clock := newFakeClock()
	var mu sync.Mutex
	succeed := false
	registered := 0
	r := newRegistrar(
		func(context.Context) (*identity.Identity, error) {
			mu.Lock()
			defer mu.Unlock()
			if succeed {
				return &identity.Identity{ApplianceID: "appl-2"}, nil
			}
			return nil, identity.ErrCentralUnreachable
		},
		nil,
		func(*identity.Identity) { mu.Lock(); registered++; mu.Unlock() },
	)
	r.after = clock.after
	r.jitter = func(d time.Duration) time.Duration { return d }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { r.run(ctx); close(done) }()

	clock.waitAsked(t) // first failure -> 30 s
	clock.fireLast()
	clock.waitAsked(t) // second failure -> 1 min
	r.Kick()
	clock.waitAsked(t) // kicked: retried at once and back to the initial delay
	clock.mu.Lock()
	last := clock.delays[len(clock.delays)-1]
	clock.mu.Unlock()
	if last != 30*time.Second {
		t.Fatalf("a kick must reset the backoff, next delay %s", last)
	}

	mu.Lock()
	succeed = true
	mu.Unlock()
	if id, err := r.attempt(context.Background()); err != nil || id == nil || id.ApplianceID != "appl-2" {
		t.Fatalf("direct attempt: %v %v", id, err)
	}
	r.Kick()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop must stop after a registration made by Check now")
	}
	mu.Lock()
	defer mu.Unlock()
	if registered != 1 {
		t.Fatalf("a registration must be reported exactly once, got %d", registered)
	}
}
