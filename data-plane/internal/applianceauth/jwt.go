// Package applianceauth signs outbound requests from scd/portald to the
// control plane with an Ed25519-backed JWT. The format is mirrored on the
// control-plane side (control-plane/internal/applianceauth); keep the two
// in sync.
//
// A signed request BINDS the whole HTTP call (method, path, body hash,
// audience, key id) so a captured token cannot be replayed against a
// different call. Format: header.payload.signature, base64url (no padding),
// alg="EdDSA".
package applianceauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// Audience must match the control-plane's expected audience.
const Audience = "stayconnect-cloud-api"

// DefaultLifetime is how long a signed request stays valid. Short is good —
// scd mints per-call; the verifier caps this at 60s and allows ~5s skew.
const DefaultLifetime = 30 * time.Second

// Version is the appliance software version stamped into signed requests.
var Version = "dev"

// SetVersion sets the software version reported in signed requests.
func SetVersion(v string) {
	if v != "" {
		Version = v
	}
}

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type Claims struct {
	Iss string `json:"iss"`
	Kid string `json:"kid"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Jti string `json:"jti"`
	Aud string `json:"aud"`
	Mth string `json:"mth"`
	Pth string `json:"pth"`
	Bsh string `json:"bsh"`
	Ver string `json:"ver,omitempty"`
}

// KeyID mirrors the control-plane fingerprint: first 16 hex of sha256(pub).
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return fmt.Sprintf("%x", sum[:8])
}

// SignRequest produces a JWT that binds appliance identity to this exact
// HTTP call. body may be nil for GET/DELETE.
func SignRequest(key ed25519.PrivateKey, applianceID, method, path string, body []byte) (string, error) {
	now := time.Now().UTC()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	pub := key.Public().(ed25519.PublicKey)
	bodySum := sha256.Sum256(body)
	c := Claims{
		Iss: applianceID,
		Kid: KeyID(pub),
		Iat: now.Unix(),
		Exp: now.Add(DefaultLifetime).Unix(),
		Jti: hex.EncodeToString(nonce[:]),
		Aud: Audience,
		Mth: method,
		Pth: path,
		Bsh: base64.RawURLEncoding.EncodeToString(bodySum[:]),
		Ver: Version,
	}
	hb, _ := json.Marshal(header{Alg: "EdDSA", Typ: "JWT"})
	pb, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signingInput := b64(hb) + "." + b64(pb)
	sig := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + b64(sig), nil
}

// Sign is a compatibility wrapper for a GET identity assertion against a
// fixed path.
//
// Deprecated: prefer SignRequest with the real method/path/body.
func Sign(key ed25519.PrivateKey, applianceID string) (string, error) {
	return SignRequest(key, applianceID, "GET", "/v1/appliance/hello", nil)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
