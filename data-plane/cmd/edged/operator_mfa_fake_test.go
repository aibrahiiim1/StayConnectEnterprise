//go:build integration

package main

import (
	"context"
	"encoding/base32"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/totp"
)

// fakeMFA stands in for scd in edged's tests: the same answers, real TOTP codes, the secret in memory instead of
// sealed. It mirrors a confirmed authenticator into public.operator_mfa (with a dummy ciphertext) so what edged
// reads from the database -- the Operators list -- sees what scd would have written. scd's own handling of the
// sealed secret, replay and recovery codes is tested in cmd/scd.
type fakeMFA struct {
	mu  sync.Mutex
	ops map[string]*fakeAuthenticator
	db  *pgxpool.Pool
}

type fakeAuthenticator struct {
	secret    []byte
	confirmed bool
	recovery  map[string]bool // code -> used
}

func newFakeMFA(db *pgxpool.Pool) *fakeMFA { return &fakeMFA{ops: map[string]*fakeAuthenticator{}, db: db} }

// codeFor is what the operator's phone shows now.
func (f *fakeMFA) codeFor(operatorID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.ops[operatorID]
	if a == nil {
		return ""
	}
	return totp.Code(a.secret, totp.Step(time.Now()))
}

func (f *fakeMFA) Status(_ context.Context, id string) (mfaStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.ops[id]
	if a == nil || !a.confirmed {
		return mfaStatus{}, nil
	}
	left := 0
	for _, used := range a.recovery {
		if !used {
			left++
		}
	}
	now := time.Now()
	return mfaStatus{Enrolled: true, ConfirmedAt: &now, RecoveryCodesLeft: left}, nil
}

func (f *fakeMFA) Enrol(_ context.Context, id, account string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if a := f.ops[id]; a != nil && a.confirmed {
		return "", "", errMFAAlreadyEnrolled
	}
	secret, _ := totp.NewSecret()
	f.ops[id] = &fakeAuthenticator{secret: secret}
	return totp.EncodeSecret(secret), totp.URI(secret, mfaIssuer, account), nil
}

func (f *fakeMFA) Confirm(_ context.Context, id, code string) ([]string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.ops[id]
	if a == nil || a.confirmed {
		return nil, false, errMFANotEnrolled
	}
	if _, ok := totp.Check(a.secret, code, time.Now()); !ok {
		return nil, false, nil
	}
	a.confirmed = true
	if _, err := f.db.Exec(context.Background(), `INSERT INTO public.operator_mfa
		(operator_id, key_id, nonce, ciphertext, cipher_version, confirmed_at) VALUES ($1,'fake',decode('00','hex'),decode('00','hex'),1,now())
		ON CONFLICT (operator_id) DO UPDATE SET confirmed_at = now()`, id); err != nil {
		return nil, false, err
	}
	return f.newCodes(a), true, nil
}

func (f *fakeMFA) Verify(_ context.Context, id, code string) (string, int, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.ops[id]
	if a == nil || !a.confirmed {
		return "", 0, false, errMFANotEnrolled
	}
	if c := totp.Normalize(code); len(c) == totp.Digits {
		_, ok := totp.Check(a.secret, c, time.Now())
		return "totp", 0, ok, nil
	}
	c := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
	if used, ok := a.recovery[c]; !ok || used {
		return "recovery_code", 0, false, nil
	}
	a.recovery[c] = true
	left := 0
	for _, used := range a.recovery {
		if !used {
			left++
		}
	}
	return "recovery_code", left, true, nil
}

func (f *fakeMFA) NewRecoveryCodes(_ context.Context, id string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.ops[id]
	if a == nil || !a.confirmed {
		return nil, errMFANotEnrolled
	}
	return f.newCodes(a), nil
}

func (f *fakeMFA) Reset(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, had := f.ops[id]
	delete(f.ops, id)
	_, err := f.db.Exec(context.Background(), `DELETE FROM public.operator_mfa WHERE operator_id = $1`, id)
	return had, err
}

func (f *fakeMFA) newCodes(a *fakeAuthenticator) []string {
	a.recovery = map[string]bool{}
	out := make([]string, 10)
	for i := range out {
		s, _ := totp.NewSecret()
		c := base32.StdEncoding.EncodeToString(s)[:10]
		a.recovery[c] = false
		out[i] = fmt.Sprintf("%s-%s", c[:5], c[5:])
	}
	return out
}
