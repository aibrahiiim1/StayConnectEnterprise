// Package opsignin signs a test operator in for test tools only, never production: the password alone for an
// account without two-step sign-in, otherwise also the code. Central shows the TOTP secret once, so the tool keeps
// the secrets of operators it turned it on for in its own keyring (0600; Enable).
package opsignin

import (
	"bytes"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/totp"
)

const KeyringEnv = "OPERATOR_TOTP_KEYRING"

type entry struct {
	Secret   string `json:"secret"`
	LastStep int64  `json:"last_step"`
}

type Keyring struct {
	path string
	mu   sync.Mutex
	m    map[string]*entry
}

func DefaultKeyringPath() string {
	if p := os.Getenv(KeyringEnv); p != "" {
		return p
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "stayconnect", "test-operator-totp.json")
}

func Open(path string) (*Keyring, error) {
	k := &Keyring{path: path, m: map[string]*entry{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return k, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &k.m); err != nil {
		return nil, fmt.Errorf("keyring %s: %w", path, err)
	}
	return k, nil
}

func (k *Keyring) save() error {
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(k.m, "", "  ")
	tmp := k.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, k.path)
}

func key(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func (k *Keyring) SignIn(cl *http.Client, base, email, password string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if cl.Jar == nil {
		return errors.New("the client needs a cookie jar")
	}
	var first struct {
		SecondStep string `json:"second_step"`
	}
	if err := call(cl, base+"/v1/auth/login", map[string]string{"email": email, "password": password}, &first); err != nil {
		return fmt.Errorf("password step for %s: %w", email, err)
	}
	e := k.m[key(email)]
	switch first.SecondStep {
	case "none":
		// Two-step sign-in is optional (D68): an account that never turned it on is signed in by the password.
		return nil
	case "code":
		if e == nil {
			return fmt.Errorf("%s already has an authenticator that keyring %s does not know; on Central run "+
				"`ctrlapi reset-mfa --email %s --reason \"test tool set-up\"` and try again", email, k.path, email)
		}
	default:
		return fmt.Errorf("password step for %s: unexpected second_step %q", email, first.SecondStep)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(e.Secret))
	if err != nil {
		return fmt.Errorf("keyring secret for %s: %w", email, err)
	}
	step := nextStep(e.LastStep, time.Now(), time.Sleep)
	if err := call(cl, base+"/v1/auth/login/code", map[string]string{"code": totp.Code(secret, step)}, nil); err != nil {
		return fmt.Errorf("code step for %s: %w", email, err)
	}
	e.LastStep = step
	if err := k.save(); err != nil {
		return fmt.Errorf("keyring %s: %w", k.path, err)
	}
	return nil
}

// Enable turns two-step sign-in on for the operator signed in on cl, keeping the secret in the keyring.
func (k *Keyring) Enable(cl *http.Client, base, email, password string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := call(cl, base+"/v1/auth/reauth", map[string]string{"password": password}, nil); err != nil {
		return fmt.Errorf("password step-up for %s: %w", email, err)
	}
	var en struct {
		Secret string `json:"secret"`
	}
	if err := call(cl, base+"/v1/auth/mfa/setup", map[string]string{}, &en); err != nil {
		return fmt.Errorf("authenticator set-up for %s: %w", email, err)
	}
	e := &entry{Secret: en.Secret}
	k.m[key(email)] = e
	// Save before confirming: Central never shows this secret again.
	if err := k.save(); err != nil {
		return fmt.Errorf("keyring %s: %w", k.path, err)
	}
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(e.Secret))
	if err != nil {
		return fmt.Errorf("keyring secret for %s: %w", email, err)
	}
	step := totp.Step(time.Now())
	if err := call(cl, base+"/v1/auth/mfa/confirm", map[string]string{"code": totp.Code(secret, step)}, nil); err != nil {
		return fmt.Errorf("confirming the authenticator for %s: %w", email, err)
	}
	e.LastStep = step
	return k.save()
}

// nextStep is the step whose code to send: Central refuses a step at or before the last accepted (replay), so
// repeat sign-ins use the next step and wait for the clock once past the skew window.
func nextStep(last int64, now time.Time, sleep func(time.Duration)) int64 {
	cur := totp.Step(now)
	s := last + 1
	if s < cur {
		s = cur
	}
	if s > cur+totp.Skew {
		start := time.Unix((s-totp.Skew)*int64(totp.Period/time.Second), 0)
		sleep(start.Sub(now) + 500*time.Millisecond)
	}
	return s
}

func call(cl *http.Client, u string, in any, out any) error {
	b, _ := json.Marshal(in)
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out != nil {
		return json.Unmarshal(body, out)
	}
	return nil
}

// WriteCurlJar writes the client's cookies in the Netscape format curl reads.
func WriteCurlJar(cl *http.Client, base, path string) error {
	u, err := url.Parse(base)
	if err != nil {
		return err
	}
	secure := "FALSE"
	if u.Scheme == "https" {
		secure = "TRUE"
	}
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n")
	for _, c := range cl.Jar.Cookies(u) {
		fmt.Fprintf(&b, "#HttpOnly_%s\tFALSE\t/\t%s\t0\t%s\t%s\n", u.Hostname(), secure, c.Name, c.Value)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}
