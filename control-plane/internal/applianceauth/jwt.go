// Package applianceauth handles Ed25519-signed JWTs used by appliances
// (scd) to prove their identity to the control plane.
//
// The on-wire format is a tiny JWT: header.payload.signature, each segment
// base64url-encoded (no padding). alg="EdDSA", typ="JWT".
//
// A signed request BINDS the whole HTTP call, not just the identity, so a
// captured token cannot be replayed against a different method, path, body,
// or audience. Claims:
//
//	iss — appliance UUID
//	kid — key id (fingerprint of the signing public key), for rotation
//	iat — issued at (unix seconds)
//	exp — expiration (unix seconds); we enforce exp-iat <= MaxLifetime
//	jti — random nonce; the verifier caches seen jti's to block replay
//	aud — audience (required; must equal the server's expected audience)
//	mth — HTTP method
//	pth — canonical request path (no query)
//	bsh — base64url(sha256(request body))
//	ver — appliance software version (optional, informational + audited)
//
// Keep the surface minimal and mirrored on the data-plane side
// (data-plane/internal/applianceauth) — the two MUST stay in sync.
package applianceauth

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Audience is the single cloud-API audience every appliance request must
// carry. A token minted for any other audience is rejected.
const Audience = "stayconnect-cloud-api"

// MaxLifetime bounds how long a signed request is usable. Clients sign a
// fresh token per request; a short window keeps the replay surface tiny.
const MaxLifetime = 60 * time.Second

// SkewFuture is how far in the future iat may be (clock-skew tolerance).
const SkewFuture = 5 * time.Second

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

// Claims are the signed payload.
type Claims struct {
	Iss string `json:"iss"` // appliance ID (uuid)
	Kid string `json:"kid"` // signing-key id (public-key fingerprint)
	Iat int64  `json:"iat"` // issued-at, unix seconds
	Exp int64  `json:"exp"` // expiration, unix seconds
	Jti string `json:"jti"` // random nonce
	Aud string `json:"aud"` // audience (required)
	Mth string `json:"mth"` // HTTP method
	Pth string `json:"pth"` // canonical request path
	Bsh string `json:"bsh"` // base64url(sha256(body))
	Ver string `json:"ver,omitempty"`
}

var (
	ErrMalformed       = errors.New("applianceauth: malformed token")
	ErrBadSignature    = errors.New("applianceauth: bad signature")
	ErrExpired         = errors.New("applianceauth: expired")
	ErrFuture          = errors.New("applianceauth: iat in the future")
	ErrLifetimeTooLong = errors.New("applianceauth: lifetime exceeds MaxLifetime")
	ErrReplay          = errors.New("applianceauth: replay detected")
	ErrAudience        = errors.New("applianceauth: wrong audience")
	ErrMethod          = errors.New("applianceauth: method mismatch")
	ErrPath            = errors.New("applianceauth: path mismatch")
	ErrBody            = errors.New("applianceauth: body hash mismatch")
	ErrKeyID           = errors.New("applianceauth: key id mismatch")
)

// KeyID returns the canonical short fingerprint of a public key used as the
// `kid` claim: first 16 hex chars of sha256(raw pubkey).
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return fmt.Sprintf("%x", sum[:8])
}

// BodyHash is the canonical base64url(sha256(body)) used in the `bsh` claim.
func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RequestParams describes the actual HTTP call the verifier observed. The
// signed claims must match these exactly.
type RequestParams struct {
	Audience string
	Method   string
	Path     string
	Body     []byte
	KeyID    string // expected kid derived from the looked-up public key
}

// Encode produces a signed JWT from fully-populated claims.
func Encode(key ed25519.PrivateKey, c Claims) (string, error) {
	hb, _ := json.Marshal(header{Alg: "EdDSA", Typ: "JWT"})
	pb, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	signingInput := b64(hb) + "." + b64(pb)
	sig := ed25519.Sign(key, []byte(signingInput))
	return signingInput + "." + b64(sig), nil
}

// Verify checks only the signature + temporal claims and returns the parsed
// claims. It is stateless; callers must also run the jti through a
// ReplayCache and (for request binding) call VerifyRequest.
func Verify(token string, pub ed25519.PublicKey, now time.Time) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformed
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrMalformed
	}
	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		return nil, ErrBadSignature
	}
	hdrRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrMalformed
	}
	var h header
	if err := json.Unmarshal(hdrRaw, &h); err != nil || h.Alg != "EdDSA" {
		return nil, ErrMalformed
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrMalformed
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrMalformed
	}
	if c.Iat == 0 || c.Exp == 0 || c.Jti == "" || c.Iss == "" {
		return nil, ErrMalformed
	}
	if time.Unix(c.Iat, 0).After(now.Add(SkewFuture)) {
		return nil, ErrFuture
	}
	if time.Unix(c.Exp, 0).Before(now) {
		return nil, ErrExpired
	}
	if time.Unix(c.Exp, 0).Sub(time.Unix(c.Iat, 0)) > MaxLifetime {
		return nil, ErrLifetimeTooLong
	}
	return &c, nil
}

// VerifyRequest runs Verify then binds the claims to the observed request:
// audience, method, path, body hash, and key id must all match. This is the
// defense that turns a bearer identity token into a per-request signature.
func VerifyRequest(token string, pub ed25519.PublicKey, now time.Time, p RequestParams) (*Claims, error) {
	c, err := Verify(token, pub, now)
	if err != nil {
		return nil, err
	}
	if c.Aud != p.Audience {
		return nil, ErrAudience
	}
	if c.Mth != p.Method {
		return nil, ErrMethod
	}
	if c.Pth != p.Path {
		return nil, ErrPath
	}
	if subtle.ConstantTimeCompare([]byte(c.Bsh), []byte(BodyHash(p.Body))) != 1 {
		return nil, ErrBody
	}
	if p.KeyID != "" && c.Kid != p.KeyID {
		return nil, ErrKeyID
	}
	return c, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// ----- Replay cache -----

// ReplayCache is an in-process LRU of recently-seen jti values. ctrlapi runs
// as a single instance on Central, so an in-process cache is authoritative;
// if it is ever scaled out this must move to Redis (SETNX with TTL).
type ReplayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
	ttl  time.Duration
	max  int
}

func NewReplayCache(ttl time.Duration, max int) *ReplayCache {
	return &ReplayCache{seen: make(map[string]time.Time, max), ttl: ttl, max: max}
}

// Use returns ErrReplay if jti was seen within ttl; otherwise records it.
func (rc *ReplayCache) Use(jti string, now time.Time) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if t, ok := rc.seen[jti]; ok && now.Sub(t) < rc.ttl {
		return ErrReplay
	}
	if len(rc.seen) >= rc.max {
		cutoff := now.Add(-rc.ttl)
		for k, t := range rc.seen {
			if t.Before(cutoff) {
				delete(rc.seen, k)
			}
		}
	}
	rc.seen[jti] = now
	return nil
}

// Describe renders a claims failure compactly for logs/audit.
func Describe(err error) string {
	if err == nil {
		return "ok"
	}
	return fmt.Sprintf("applianceauth: %v", err)
}
