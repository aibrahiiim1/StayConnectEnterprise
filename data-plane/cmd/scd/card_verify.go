package main

// CARD PAYMENT IS READY ONLY ON CREDENTIALS THE PROVIDER HAS ACCEPTED.
//
// Readiness used to require only that the stored credentials were present and complete. Revoked, malformed or
// mode-mismatched keys therefore made card payment effective and it was offered to guests, who then took a quote
// and created a purchase before the provider refused the checkout (automated review finding on PR #202).
//
// Now a card account is ready only while an authenticated, non-financial provider check (the adapter's
// TestConnection: a balance or token read that moves no money) has succeeded for exactly that account and exactly
// those credentials. The check runs in the background and is cached, so a guest request never waits on a
// provider: a fresh success lasts 30 minutes and is refreshed before it lapses; a failure is retried after 5.
// Rotating a secret changes the fingerprint, so the new credentials are verified before they are trusted. An
// operator's "Test connection" records its result here too.

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
	// run performs the provider check; replaced in tests.
	run func(ctx context.Context) error
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

// record stores a check result (background, or an operator's Test connection).
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
