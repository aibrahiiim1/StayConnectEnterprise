package social

// Minimal, dependency-free JWT support for the OIDC providers (Microsoft,
// Apple): verify an RS256 id_token against the issuer's published JWKS, and
// sign the ES256 client-secret JWT Apple demands in place of a static
// secret.
//
// Why hand-rolled rather than a JWT library:
//   - The data plane carries no JWT dependency today, and the surface we need
//     is tiny: one algorithm to verify (RS256 — the only one Microsoft and
//     Apple sign id_tokens with) and one to sign (ES256, Apple only).
//   - A narrow verifier is easier to reason about than a general one. The
//     classic JWT failures — alg=none, alg confusion (an HS256 token
//     "verified" with the RSA public key as the HMAC secret), a missing kid
//     falling through to "any key" — cannot happen here, because RS256 is the
//     only accepted alg and the kid must name a key the issuer published.
//
// No nonce is sent or checked. OIDC requires a nonce for the implicit and
// hybrid flows, where the id_token travels through the browser. Here it never
// does: every id_token arrives on the server-to-server token response, over
// TLS, in exchange for a single-use code plus our client credentials, and the
// CSRF binding of the browser leg is the `state` row scd keeps (device IP/MAC,
// expiry, single consumption).

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// clockSkew is the leeway allowed on exp / nbf / iat. The appliance keeps NTP
// time, but a guest waiting on a slow consent screen should not fail on a few
// seconds of drift between us and the IdP.
const clockSkew = 2 * time.Minute

// jwksTTL bounds how long a fetched key set is trusted before a refetch.
// Unknown kids also force a refetch (rate-limited by jwksMinRefetch), which
// is how key rotation at the IdP is picked up without waiting out the TTL.
const (
	jwksTTL        = 6 * time.Hour
	jwksMinRefetch = 1 * time.Minute
)

// errIDToken is wrapped by every id_token verification failure so callers
// and tests can tell "the IdP sent us a token we refuse" from transport
// errors.
var errIDToken = errors.New("id_token rejected")

// idClaims is the decoded payload of a verified id_token. Standard claims are
// typed; everything provider-specific stays in raw for the adapter to read.
type idClaims struct {
	Iss string
	Sub string
	Aud []string
	Exp time.Time
	raw map[string]any
}

func (c *idClaims) str(name string) string {
	s, _ := c.raw[name].(string)
	return s
}

// boolish reads a claim that providers encode as either a JSON bool or the
// string "true"/"false" (Apple does both, depending on the account).
func (c *idClaims) boolish(name string) (val, present bool) {
	switch v := c.raw[name].(type) {
	case bool:
		return v, true
	case string:
		return strings.EqualFold(v, "true"), true
	}
	return false, false
}

// jwksCache fetches and caches one issuer's JSON Web Key Set.
type jwksCache struct {
	url    string
	client func() *http.Client

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func newJWKSCache(url string, client func() *http.Client) *jwksCache {
	return &jwksCache{url: url, client: client}
}

// key returns the RSA key for kid, refetching the set when it is stale or the
// kid is unknown (the IdP rotated its keys).
func (j *jwksCache) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	if k, ok := j.keys[kid]; ok && now.Sub(j.fetchedAt) < jwksTTL {
		return k, nil
	}
	if j.keys == nil || now.Sub(j.fetchedAt) >= jwksMinRefetch {
		keys, err := fetchJWKS(ctx, j.client(), j.url)
		if err != nil {
			// A failed refresh keeps serving the last good set: a transient
			// outage of the key endpoint must not reject tokens signed by a
			// key we already know.
			if k, ok := j.keys[kid]; ok {
				return k, nil
			}
			return nil, err
		}
		j.keys, j.fetchedAt = keys, now
	}
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("%w: signing key %q is not in the issuer's key set", errIDToken, kid)
}

func fetchJWKS(ctx context.Context, hc *http.Client, url string) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("jwks fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("jwks fetch: status=%d", resp.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&set); err != nil {
		return nil, fmt.Errorf("jwks decode: %w", err)
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		// Only RSA signing keys are usable for RS256; anything else in the
		// set (encryption keys, EC keys) is skipped rather than failing the
		// whole set.
		if k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") || k.Kid == "" {
			continue
		}
		nb, err1 := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.N, "="))
		eb, err2 := base64.RawURLEncoding.DecodeString(strings.TrimRight(k.E, "="))
		if err1 != nil || err2 != nil || len(eb) == 0 || len(eb) > 4 {
			continue
		}
		e := 0
		for _, b := range eb {
			e = e<<8 | int(b)
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}
		if pub.N.BitLen() < 2048 {
			continue // refuse toy keys outright
		}
		out[k.Kid] = pub
	}
	if len(out) == 0 {
		return nil, errors.New("jwks: no usable RSA signing keys")
	}
	return out, nil
}

// verifyIDToken checks an RS256 id_token's signature against the issuer's
// key set and its time and audience claims. Issuer checking is left to the
// caller because it is provider-specific (Microsoft's multi-tenant issuer
// embeds the tenant id from the token itself).
func verifyIDToken(ctx context.Context, raw string, keys *jwksCache, audience string, now time.Time) (*idClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: not a compact JWS", errIDToken)
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, fmt.Errorf("%w: header encoding", errIDToken)
	}
	var hdr struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return nil, fmt.Errorf("%w: header json", errIDToken)
	}
	if hdr.Alg != "RS256" {
		return nil, fmt.Errorf("%w: alg %q is not accepted", errIDToken, hdr.Alg)
	}
	if hdr.Kid == "" {
		return nil, fmt.Errorf("%w: no kid", errIDToken)
	}
	pub, err := keys.key(ctx, hdr.Kid)
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("%w: signature encoding", errIDToken)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], sig); err != nil {
		return nil, fmt.Errorf("%w: bad signature", errIDToken)
	}

	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("%w: payload encoding", errIDToken)
	}
	dec := json.NewDecoder(strings.NewReader(string(pb)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%w: payload json", errIDToken)
	}
	c := &idClaims{raw: m}
	c.Iss, _ = m["iss"].(string)
	c.Sub, _ = m["sub"].(string)
	switch a := m["aud"].(type) {
	case string:
		c.Aud = []string{a}
	case []any:
		for _, v := range a {
			if s, ok := v.(string); ok {
				c.Aud = append(c.Aud, s)
			}
		}
	}
	exp, ok := numericDate(m["exp"])
	if !ok {
		return nil, fmt.Errorf("%w: no exp", errIDToken)
	}
	c.Exp = exp
	if now.After(exp.Add(clockSkew)) {
		return nil, fmt.Errorf("%w: expired", errIDToken)
	}
	if nbf, ok := numericDate(m["nbf"]); ok && now.Add(clockSkew).Before(nbf) {
		return nil, fmt.Errorf("%w: not yet valid", errIDToken)
	}
	if iat, ok := numericDate(m["iat"]); ok && now.Add(clockSkew).Before(iat) {
		return nil, fmt.Errorf("%w: issued in the future", errIDToken)
	}
	audOK := false
	for _, a := range c.Aud {
		if a == audience {
			audOK = true
			break
		}
	}
	if !audOK {
		return nil, fmt.Errorf("%w: audience does not match this client", errIDToken)
	}
	if c.Sub == "" {
		return nil, fmt.Errorf("%w: no sub", errIDToken)
	}
	return c, nil
}

func numericDate(v any) (time.Time, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(int64(f), 0), true
	case float64:
		return time.Unix(int64(n), 0), true
	}
	return time.Time{}, false
}

// signES256 produces a compact JWS over header+claims with an EC P-256 key.
// JWS encodes the ECDSA signature as the fixed-width concatenation r||s
// (RFC 7518 §3.4), not the ASN.1 DER that crypto/ecdsa's SignASN1 emits.
func signES256(key *ecdsa.PrivateKey, header, claims map[string]any) (string, error) {
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signing := base64.RawURLEncoding.EncodeToString(hb) + "." + base64.RawURLEncoding.EncodeToString(cb)
	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// ParseP256PrivateKey reads the PEM private key Apple issues for Sign in with
// Apple (the downloaded AuthKey_XXXXXXXXXX.p8: a PKCS#8 "PRIVATE KEY" block
// holding an EC P-256 key). SEC 1 "EC PRIVATE KEY" is accepted too, for keys
// that were converted along the way. Exported so the edged configuration API
// can refuse a key that will never sign, at save time rather than at a
// guest's first sign-in.
func ParseP256PrivateKey(pemText string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, errors.New("not a PEM private key (paste the whole .p8 file, including the BEGIN/END lines)")
	}
	var key any
	var err error
	switch block.Type {
	case "PRIVATE KEY":
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unexpected PEM block %q (expected PRIVATE KEY)", block.Type)
	}
	if err != nil {
		return nil, errors.New("the private key could not be read")
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok || ec.Curve.Params().Name != "P-256" {
		return nil, errors.New("the private key must be an EC P-256 key")
	}
	return ec, nil
}
