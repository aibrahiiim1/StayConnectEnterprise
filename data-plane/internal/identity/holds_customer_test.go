package identity

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// captureRegistration answers a registration and keeps the body and the bearer token it carried.
func captureRegistration(body *[]byte, token *string) *fakeCentral {
	return &fakeCentral{resp: func(r *http.Request) (*http.Response, error) {
		*body, _ = io.ReadAll(r.Body)
		*token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		return answer(201, `{"appliance_id":"appl-h"}`)(r)
	}}
}

// The registration token signs the method, the path and the SHA-256 of the body, so holds_customer_id in the
// body is covered by the identity key's signature: it cannot be stripped or edited without Central refusing
// the signature.
func assertBodySigned(t *testing.T, s *Store, body []byte, token string) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a signed token: %q", token)
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	id, err := s.load()
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(id.privKey.Public().(ed25519.PublicKey), []byte(parts[0]+"."+parts[1]), sig) {
		t.Fatal("the registration token is not signed by the identity key")
	}
	var c struct {
		Bsh string `json:"bsh"`
	}
	_ = json.Unmarshal(payload, &c)
	sum := sha256.Sum256(body)
	if c.Bsh != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("the signed token does not cover this body")
	}
}

func TestRegisterCarriesTheHeldCustomerInsideTheSignedBody(t *testing.T) {
	s := &Store{Dir: t.TempDir(), HeldCustomer: func(context.Context) (string, error) {
		return "11111111-1111-1111-1111-111111111111", nil
	}}
	var body []byte
	var tok string
	if _, err := s.Register(context.Background(), "https://central.invalid", captureRegistration(&body, &tok)); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var m map[string]string
	_ = json.Unmarshal(body, &m)
	if m["holds_customer_id"] != "11111111-1111-1111-1111-111111111111" {
		t.Fatalf("holds_customer_id missing from the registration: %s", body)
	}
	assertBodySigned(t, s, body, tok)
}

func TestFactoryCleanRegistrationOmitsTheHeldCustomer(t *testing.T) {
	for name, s := range map[string]*Store{
		"holds nothing": {Dir: t.TempDir(), HeldCustomer: func(context.Context) (string, error) { return "", nil }},
		"no reporter":   {Dir: t.TempDir()},
	} {
		var body []byte
		var tok string
		if _, err := s.Register(context.Background(), "https://central.invalid", captureRegistration(&body, &tok)); err != nil {
			t.Fatalf("%s: Register: %v", name, err)
		}
		if strings.Contains(string(body), "holds_customer_id") {
			t.Fatalf("%s: a factory-clean registration carried holds_customer_id: %s", name, body)
		}
		assertBodySigned(t, s, body, tok)
	}
}

// An appliance that cannot tell what it holds -- or holds more than one customer -- does not register at all:
// presenting itself as clean is exactly what this field exists to prevent.
func TestRegisterRefusedWhenHeldCustomerCannotBeRead(t *testing.T) {
	s := &Store{Dir: t.TempDir(), HeldCustomer: func(context.Context) (string, error) {
		return "", errors.New("database unavailable")
	}}
	fc := &fakeCentral{resp: answer(201, `{"appliance_id":"x"}`)}
	if _, err := s.Register(context.Background(), "https://central.invalid", fc); err == nil {
		t.Fatal("registration went ahead without knowing what the appliance holds")
	}
	if fc.calls != 0 {
		t.Fatal("Central was contacted")
	}
}
