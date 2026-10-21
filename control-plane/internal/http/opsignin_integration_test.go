package http

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/opsignin"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

// The test tools (cmd/acceptance, cmd/pkitest, cmd/regtest -signin) sign in through the real two-step handlers:
// the first sign-in sets the authenticator up and keeps its secret, a second one straight after uses the next
// step's code (the last one is refused as a replay), and an operator the keyring does not know gets the reset.
func TestTestToolsSignInThroughBothSteps(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("correct-horse-battery")
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO operators (email, password_hash, status) VALUES ('tool@central.test',$1,'active') RETURNING id::text`, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'platform_admin')`, id); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	mfa, err := auth.NewMFA(pool, key)
	if err != nil {
		t.Fatal(err)
	}
	sessions := 0
	d := authDeps{Repo: &auth.Repo{DB: pool}, DB: pool, Failures: ratebudget.New(50, 300, time.Minute), MFA: mfa,
		NewSession: func(_ context.Context, s auth.Session) (*auth.Session, error) {
			sessions++
			s.Token = "tok-" + strings.Repeat("x", sessions)
			s.CreatedAt, s.LastSeenAt, s.ExpiresAt = time.Now(), time.Now(), time.Now().Add(12*time.Hour)
			return &s, nil
		}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/auth/login", d.login)
	mux.HandleFunc("POST /v1/auth/login/enrol", d.loginEnrol)
	mux.HandleFunc("POST /v1/auth/login/code", d.loginCode)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ringPath := filepath.Join(t.TempDir(), "ring.json")
	signIn := func(kr *opsignin.Keyring) (*http.Client, error) {
		jar, _ := cookiejar.New(nil)
		cl := &http.Client{Jar: jar, Timeout: 10 * time.Second}
		return cl, kr.SignIn(cl, srv.URL, "tool@central.test", "correct-horse-battery")
	}

	kr, err := opsignin.Open(ringPath)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := signIn(kr)
	if err != nil {
		t.Fatalf("first sign-in (set-up): %v", err)
	}
	if st, _ := mfa.Status(ctx, id); !st.Enrolled {
		t.Fatal("the first sign-in did not finish setting the authenticator up")
	}
	if fi, err := os.Stat(ringPath); err != nil || fi.Size() == 0 {
		t.Fatalf("the keyring was not written: %v", err)
	}
	jarPath := filepath.Join(t.TempDir(), "cj.txt")
	if err := opsignin.WriteCurlJar(cl, srv.URL, jarPath); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(jarPath); !strings.Contains(string(b), "\t"+auth.SessionCookieName+"\ttok-x\n") {
		t.Fatalf("the curl jar does not carry the session:\n%s", b)
	}

	// A fresh process reads the keyring back and signs in again at once.
	kr2, err := opsignin.Open(ringPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signIn(kr2); err != nil {
		t.Fatalf("second sign-in (code): %v", err)
	}
	if sessions != 2 {
		t.Fatalf("want 2 sessions, got %d", sessions)
	}

	// An authenticator set up elsewhere: the tool says how to hand it back instead of guessing.
	empty, _ := opsignin.Open(filepath.Join(t.TempDir(), "none.json"))
	if _, err := signIn(empty); err == nil || !strings.Contains(err.Error(), "reset-mfa") {
		t.Fatalf("want the reset-mfa hint, got %v", err)
	}
}
