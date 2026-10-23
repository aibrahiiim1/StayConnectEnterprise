package main

// Token-less registration retries with jittered backoff (30 s doubling to a 5 minute cap) while the appliance
// is unregistered; Check now runs an attempt immediately, and attempts are serialised.
//
// On success scd re-executes itself (syscall.Exec, same PID) because every Central-facing agent is built at
// startup from the identity. Guest service is unaffected: an unregistered appliance authorises no guests.

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

const (
	registerFirstRetry = 30 * time.Second
	registerMaxRetry   = 5 * time.Minute
)

type registrar struct {
	register     func(context.Context) (*identity.Identity, error)
	onResult     func(error)              // every attempt's outcome (Central contact tracking)
	onRegistered func(*identity.Identity) // called once, on the first success

	initial, max time.Duration
	jitter       func(time.Duration) time.Duration
	after        func(time.Duration) <-chan time.Time

	mu    sync.Mutex // serialises attempts: the background loop and Check now
	once  sync.Once
	kick  chan struct{}
	ident *identity.Identity
}

func newRegistrar(register func(context.Context) (*identity.Identity, error), onResult func(error),
	onRegistered func(*identity.Identity)) *registrar {
	return &registrar{
		register: register, onResult: onResult, onRegistered: onRegistered,
		initial: registerFirstRetry, max: registerMaxRetry,
		jitter: jitterDuration, after: time.After,
		kick: make(chan struct{}, 1),
	}
}

// jitterDuration spreads retries by +/-20% so a fleet does not return in lockstep.
func jitterDuration(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

func (r *registrar) attempt(ctx context.Context) (*identity.Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ident != nil {
		return r.ident, nil
	}
	id, err := r.register(ctx)
	if r.onResult != nil {
		r.onResult(err)
	}
	if err == nil && id != nil && id.ApplianceID != "" {
		r.ident = id
		r.once.Do(func() {
			if r.onRegistered != nil {
				r.onRegistered(id)
			}
		})
		return id, nil
	}
	return nil, err
}

func (r *registrar) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

func (r *registrar) run(ctx context.Context) {
	delay := r.initial
	for {
		if id, _ := r.attempt(ctx); id != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-r.kick:
			delay = r.initial
			continue
		case <-r.after(r.jitter(delay)):
		}
		delay *= 2
		if delay > r.max {
			delay = r.max
		}
	}
}
