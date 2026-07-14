package applianceauth

import (
	"crypto/ed25519"
	"testing"
	"time"
)

func mint(t *testing.T, priv ed25519.PrivateKey, c Claims) string {
	t.Helper()
	tok, err := Encode(priv, c)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return tok
}

func baseClaims(pub ed25519.PublicKey, body []byte) Claims {
	now := time.Now()
	return Claims{
		Iss: "11111111-1111-1111-1111-111111111111",
		Kid: KeyID(pub),
		Iat: now.Unix(),
		Exp: now.Add(30 * time.Second).Unix(),
		Jti: "nonce-1",
		Aud: Audience,
		Mth: "POST",
		Pth: "/v1/appliance/thing",
		Bsh: BodyHash(body),
		Ver: "1.2.3",
	}
}

func params(pub ed25519.PublicKey, body []byte) RequestParams {
	return RequestParams{Audience: Audience, Method: "POST", Path: "/v1/appliance/thing", Body: body, KeyID: KeyID(pub)}
}

func TestVerifyRequest_HappyPath(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"x":1}`)
	tok := mint(t, priv, baseClaims(pub, body))
	if _, err := VerifyRequest(tok, pub, time.Now(), params(pub, body)); err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
}

func TestVerifyRequest_BodySubstitutionRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{"x":1}`)
	tok := mint(t, priv, baseClaims(pub, body))
	// Attacker swaps the body but keeps the signed token.
	if _, err := VerifyRequest(tok, pub, time.Now(), params(pub, []byte(`{"x":999}`))); err != ErrBody {
		t.Fatalf("expected ErrBody, got %v", err)
	}
}

func TestVerifyRequest_WrongMethodPathAudRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{}`)
	tok := mint(t, priv, baseClaims(pub, body))
	p := params(pub, body)
	p.Method = "GET"
	if _, err := VerifyRequest(tok, pub, time.Now(), p); err != ErrMethod {
		t.Fatalf("expected ErrMethod, got %v", err)
	}
	p = params(pub, body)
	p.Path = "/v1/appliance/other"
	if _, err := VerifyRequest(tok, pub, time.Now(), p); err != ErrPath {
		t.Fatalf("expected ErrPath, got %v", err)
	}
	p = params(pub, body)
	p.Audience = "someone-else"
	if _, err := VerifyRequest(tok, pub, time.Now(), p); err != ErrAudience {
		t.Fatalf("expected ErrAudience, got %v", err)
	}
}

func TestVerifyRequest_WrongKeyIDRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	body := []byte(`{}`)
	c := baseClaims(pub, body)
	c.Kid = "deadbeefdeadbeef" // claims a different key id than the real pub
	tok := mint(t, priv, c)
	if _, err := VerifyRequest(tok, pub, time.Now(), params(pub, body)); err != ErrKeyID {
		t.Fatalf("expected ErrKeyID, got %v", err)
	}
}

func TestVerify_ExpiredAndLifetimeRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Now()
	c := baseClaims(pub, nil)
	c.Iat = now.Add(-2 * time.Minute).Unix()
	c.Exp = now.Add(-1 * time.Minute).Unix()
	if _, err := Verify(mint(t, priv, c), pub, now); err != ErrExpired {
		t.Fatalf("expected ErrExpired, got %v", err)
	}
	c = baseClaims(pub, nil)
	c.Iat = now.Unix()
	c.Exp = now.Add(5 * time.Minute).Unix() // exceeds MaxLifetime
	if _, err := Verify(mint(t, priv, c), pub, now); err != ErrLifetimeTooLong {
		t.Fatalf("expected ErrLifetimeTooLong, got %v", err)
	}
}

func TestReplayCache(t *testing.T) {
	rc := NewReplayCache(time.Minute, 16)
	now := time.Now()
	if err := rc.Use("j1", now); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := rc.Use("j1", now); err != ErrReplay {
		t.Fatalf("expected ErrReplay on reuse, got %v", err)
	}
}
