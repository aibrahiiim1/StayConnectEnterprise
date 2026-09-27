package main

// TOKEN-LESS REGISTRATION THAT KEEPS TRYING.
//
// Registration used to happen once, at boot, inside identity loading. The comment there promised "a periodic
// re-register loop" that did not exist: an appliance that booted while Central (or its uplink, or its DNS) was
// down never registered until somebody restarted it, and nothing on any screen said why it was missing from
// the control panel.
//
// The loop below retries with a jittered backoff (30 s doubling to a 5 minute cap) for as long as the appliance
// is unregistered, and Check now in Hotel Admin runs an attempt immediately. Attempts are serialised, so the
// loop and an operator can never register twice at once.
//
// WHAT HAPPENS ON SUCCESS. Everything that talks to Central -- the assignment agent, the certificate manager,
// the licence fetch and the hello probe -- is constructed at startup from the appliance identity. Rather than
// retrofit each of them to start late, scd re-executes itself (syscall.Exec, same PID, the mechanism the
// assignment agent already uses to adopt a new assignment), so the next process comes up holding the identity
// and starts every agent the ordinary way. Guest service is unaffected: an unregistered appliance has no
// assignment and therefore authorises no guests.

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

// jitterDuration spreads retries by +/-20% so a fleet that lost Central at the same moment does not return in
// lockstep.
func jitterDuration(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
}

// attempt performs one registration attempt, serialised with every other.
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

// Kick asks the background loop to try again now, resetting its backoff.
func (r *registrar) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// run retries until registration succeeds or ctx ends. The first attempt is immediate.
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
