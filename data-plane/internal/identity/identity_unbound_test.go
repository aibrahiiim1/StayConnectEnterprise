package identity

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/hwid"
)

type fakeCentral struct {
	calls int
	resp  func(*http.Request) (*http.Response, error)
}

func (f *fakeCentral) Do(r *http.Request) (*http.Response, error) {
	f.calls++
	return f.resp(r)
}

func answer(code int, body string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}
}

func init() { detectHW = func() hwid.Info { return hwid.Info{Serial: "SC-TEST"} } }

// An appliance that downloads ONE offline activation request must not lose the online path: an unbound keypair
// is not a registered identity.
func TestUnboundKeypairDoesNotCountAsEnrolled(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}

	id, err := s.EnsureLocalKeypair()
	if err != nil {
		t.Fatalf("EnsureLocalKeypair: %v", err)
	}
	if id.ApplianceID != "" {
		t.Fatal("a locally generated keypair must not invent an appliance id")
	}
	if _, err := os.Stat(filepath.Join(dir, "identity.json")); err != nil {
		t.Fatalf("identity.json should exist: %v", err)
	}
	got, err := s.LoadBound()
	if err != nil {
		t.Fatalf("LoadBound: %v", err)
	}
	if got != nil {
		t.Fatalf("an unbound keypair must not be returned as a registered identity (got %q)", got.ApplianceID)
	}
}

// Once Central has bound an appliance id, that identity IS registered and is returned as-is -- Register does
// not call Central again and never replaces the key.
func TestBoundIdentityIsReturnedUnchanged(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	if _, err := s.EnsureLocalKeypair(); err != nil {
		t.Fatalf("EnsureLocalKeypair: %v", err)
	}
	if err := s.AdoptApplianceID("appliance-42"); err != nil {
		t.Fatalf("AdoptApplianceID: %v", err)
	}
	before, _ := s.load()
	fc := &fakeCentral{resp: answer(500, "should not be called")}
	got, err := s.Register(context.Background(), "https://central.invalid", fc)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if fc.calls != 0 {
		t.Fatal("a registered appliance must not register again")
	}
	if got == nil || got.ApplianceID != "appliance-42" || got.PublicKeyB64 != before.PublicKeyB64 {
		t.Fatalf("a bound identity must be returned unchanged, got %+v", got)
	}
}

// Registration reuses ONE key across every attempt: an unanswered attempt must not mint a second identity,
// and the key an offline request already names must be the one that gets registered.
func TestRegisterReusesTheSameKeyAcrossRetries(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Dir: dir}
	down := &fakeCentral{resp: func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: connection refused") }}
	if _, err := s.Register(context.Background(), "https://central.invalid", down); !errors.Is(err, ErrCentralUnreachable) {
		t.Fatalf("an unreachable Central must be reported as ErrCentralUnreachable, got %v", err)
	}
	first, _ := s.load()
	if first == nil || first.PublicKeyB64 == "" {
		t.Fatal("the keypair must be persisted before the first attempt")
	}
	up := &fakeCentral{resp: func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/appliances/register" || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		return answer(201, `{"appliance_id":"appl-1","tenant_id":"t-should-be-ignored"}`)(r)
	}}
	got, err := s.Register(context.Background(), "https://central.invalid", up)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if got.ApplianceID != "appl-1" || got.PublicKeyB64 != first.PublicKeyB64 {
		t.Fatalf("registration must bind the SAME key: got %+v, first key %s", got, first.PublicKeyB64)
	}
	if got.TenantID != "" {
		t.Fatal("registration must never take a tenant from Central's answer; the signed assignment is the only authority")
	}
	if b, _ := s.LoadBound(); b == nil || b.ApplianceID != "appl-1" {
		t.Fatal("the registered identity must be persisted")
	}
}

func TestRegisterRefusalIsAStatusError(t *testing.T) {
	s := &Store{Dir: t.TempDir()}
	_, err := s.Register(context.Background(), "https://central.invalid", &fakeCentral{resp: answer(403, "forbidden")})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 403 {
		t.Fatalf("want StatusError 403, got %v", err)
	}
}
