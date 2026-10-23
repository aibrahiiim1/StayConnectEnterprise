package main

// Card payment is ready only while an authenticated, non-financial provider check (TestConnection) has succeeded for exactly those credentials.
// The check runs in the background and is cached so guest requests never wait on the provider: success lasts 30 minutes, failure is retried after 5.
// Rotating a secret changes the fingerprint, so new credentials are verified before they are trusted.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/payment"
)

const (
	cardVerifiedFor  = 30 * time.Minute
	cardRefreshAfter = 25 * time.Minute
	cardRetryAfter   = 5 * time.Minute
	cardCheckTimeout = 15 * time.Second
)

type cardCheck struct {
	ok       bool
	at       time.Time
	inFlight bool
}

type cardVerifier struct {
	mu     sync.Mutex
	checks map[string]*cardCheck
	now    func() time.Time
	run    func(ctx context.Context) error
}

func newCardVerifier() *cardVerifier {
	return &cardVerifier{checks: map[string]*cardCheck{}, now: time.Now}
}

// credentialFingerprint identifies one account's exact credential set without retaining any secret.
func credentialFingerprint(accountID string, mode payment.Mode, c payment.Credentials) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	h.Write([]byte(accountID + "\x00" + string(mode)))
	for _, k := range keys {
		h.Write([]byte("\x00" + k + "\x00" + c[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ready reports whether these credentials are verified now. When a check is due it is started in the background
// with check, and the answer is the last known result (never "ready" before the first success).
func (v *cardVerifier) ready(fp string, check func(ctx context.Context) error) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	c := v.checks[fp]
	if c == nil {
		c = &cardCheck{}
		v.checks[fp] = c
	}
	due := c.at.IsZero() ||
		(c.ok && now.Sub(c.at) > cardRefreshAfter) ||
		(!c.ok && now.Sub(c.at) > cardRetryAfter)
	if due && !c.inFlight {
		c.inFlight = true
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), cardCheckTimeout)
			defer cancel()
			err := check(ctx)
			v.record(fp, err == nil)
		}()
	}
	return c.ok && now.Sub(c.at) <= cardVerifiedFor
}

func (v *cardVerifier) record(fp string, ok bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	c := v.checks[fp]
	if c == nil {
		c = &cardCheck{}
		v.checks[fp] = c
	}
	c.ok, c.at, c.inFlight = ok, v.now(), false
}
