// Package applianceauth handles the Ed25519-signed JWTs appliances use to prove identity. A signed request binds
// the whole HTTP call (method, path, body hash, audience), so a captured token cannot be replayed elsewhere.
// Mirrored in data-plane/internal/applianceauth; the two must stay in sync.
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

// Audience is the only audience accepted.
const Audience = "stayconnect-cloud-api"

// MaxLifetime bounds how long a signed request is usable; a short window keeps the replay surface small.
const MaxLifetime = 60 * time.Second

// SkewFuture is how far in the future iat may be.
const SkewFuture = 5 * time.Second

type header struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
}

type Claims struct {
	Iss string `json:"iss"` // appliance ID
	Kid string `json:"kid"` // public-key fingerprint
	Iat int64  `json:"iat"` // unix seconds
	Exp int64  `json:"exp"` // unix seconds; exp-iat is held to MaxLifetime
	Jti string `json:"jti"` // random nonce, cached by the verifier to block replay
	Aud string `json:"aud"`
	Mth string `json:"mth"`
	Pth string `json:"pth"` // canonical path, no query
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

// KeyID is the `kid` claim: the first 16 hex chars of sha256(raw pubkey).
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return fmt.Sprintf("%x", sum[:8])
}

func BodyHash(body []byte) string {
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// RequestParams is the HTTP call the verifier observed; the signed claims must match it exactly.
type RequestParams struct {
	Audience string
	Method   string
	Path     string
	Body     []byte
	KeyID    string // expected kid derived from the looked-up public key
}

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

// Verify checks only the signature and temporal claims. It is stateless: callers must also run the jti through a
// ReplayCache and use VerifyRequest for request binding.
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

// VerifyRequest runs Verify then requires audience, method, path, body hash and key id to match the observed
// request, turning a bearer token into a per-request signature.
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

// ReplayCache holds recently seen jti values in process. That is authoritative only while ctrlapi runs as a
// single instance; scaling out requires Redis (SETNX with TTL).
//
// Entries expire in arrival order from a queue, so expiry is O(1) amortised instead of a scan of the whole map
// per insert; past a hard ceiling of unexpired entries (replayCeilingFactor × max) Use refuses with
// ErrReplayCacheFull rather than grow without bound (audit Phase 1 L20). max stays the expected working set.
type ReplayCache struct {
	mu    sync.Mutex
	seen  map[string]time.Time
	order []replayEntry
	head  int
	ttl   time.Duration
	max   int
}

type replayEntry struct {
	jti string
	at  time.Time
}

// replayCeilingFactor puts the refusal far above the measured fleet load (about 5,200 tokens per 2 minutes at
// 1,000 appliances against max 8,192).
const replayCeilingFactor = 16

// ErrReplayCacheFull: too many unexpired tokens are held; the request is refused, not remembered.
var ErrReplayCacheFull = errors.New("applianceauth: replay cache full")

func NewReplayCache(ttl time.Duration, max int) *ReplayCache {
	return &ReplayCache{seen: make(map[string]time.Time, max), ttl: ttl, max: max}
}

func (rc *ReplayCache) Use(jti string, now time.Time) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.expire(now)
	if t, ok := rc.seen[jti]; ok && now.Sub(t) < rc.ttl {
		return ErrReplay
	}
	if len(rc.seen) >= rc.max*replayCeilingFactor {
		return ErrReplayCacheFull
	}
	rc.seen[jti] = now
	rc.order = append(rc.order, replayEntry{jti: jti, at: now})
	return nil
}

// expire drops entries older than ttl from the front of the queue. A jti re-used after its expiry has a newer
// queue entry; the older one only deletes the map entry when the times still match.
func (rc *ReplayCache) expire(now time.Time) {
	cutoff := now.Add(-rc.ttl)
	for rc.head < len(rc.order) && !rc.order[rc.head].at.After(cutoff) {
		e := rc.order[rc.head]
		if t, ok := rc.seen[e.jti]; ok && t.Equal(e.at) {
			delete(rc.seen, e.jti)
		}
		rc.order[rc.head] = replayEntry{}
		rc.head++
	}
	if rc.head > 0 && rc.head*2 >= len(rc.order) {
		rc.order = append(rc.order[:0], rc.order[rc.head:]...)
		rc.head = 0
	}
}

func Describe(err error) string {
	if err == nil {
		return "ok"
	}
	return fmt.Sprintf("applianceauth: %v", err)
}
