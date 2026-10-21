// Package opsignin signs a test operator in to ctrlapi through both steps (password, then authenticator code;
// migration 0052) for the TEST tools: cmd/acceptance, cmd/pkitest and the regression scripts through cmd/regtest.
// It is never part of the production runtime.
//
// A tool plays the operator's authenticator app, so it needs the operator's TOTP secret. Central shows that
// secret once, at set-up, and stores it encrypted, so the tool keeps the secrets of the operators it set up in a
// keyring file of its own (0600). An operator who set up an authenticator elsewhere cannot be signed in by a tool;
// the error names the reset that hands it back.
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

// KeyringEnv overrides where the keyring lives.
const KeyringEnv = "OPERATOR_TOTP_KEYRING"

type entry struct {
	Secret   string `json:"secret"`
	LastStep int64  `json:"last_step"`
}

// Keyring holds the TOTP secrets of the test operators this machine's tools set up, by email.
type Keyring struct {
	path string
	mu   sync.Mutex
	m    map[string]*entry
}

// DefaultKeyringPath is $OPERATOR_TOTP_KEYRING, else stayconnect/test-operator-totp.json in the user's config
// directory.
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

// Open reads the keyring at path; a missing file is an empty keyring.
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

// SignIn takes cl (which must carry a cookie jar) through both sign-in steps; on success the jar holds the
// session cookie.
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
	case "enrol":
		var en struct {
			Secret string `json:"secret"`
		}
		if err := call(cl, base+"/v1/auth/login/enrol", map[string]string{}, &en); err != nil {
			return fmt.Errorf("authenticator set-up for %s: %w", email, err)
		}
		e = &entry{Secret: en.Secret}
		k.m[key(email)] = e
		// Kept before the code is confirmed: once confirmed, Central never shows this secret again.
		if err := k.save(); err != nil {
			return fmt.Errorf("keyring %s: %w", k.path, err)
		}
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

// nextStep is the step whose code to send. Central accepts the steps either side of now and refuses a step at or
// before the last one it accepted (replay), so a second sign-in inside one step uses the next step's code, and a
// third waits for the clock.
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

// WriteCurlJar writes the client's cookies for base in the Netscape format curl reads with -b/-c, so a shell
// script can carry on with the session a SignIn opened.
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
