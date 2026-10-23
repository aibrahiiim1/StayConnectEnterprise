package social

// Minimal JWT support for the OIDC providers: verify an RS256 id_token against the issuer's JWKS and sign
// Apple's ES256 client-secret JWT. Hand-rolled and deliberately narrow: RS256 is the only accepted alg and
// the kid must name a published key, so alg=none, alg confusion (HS256 verified with the RSA public key)
// and a missing kid falling through to "any key" cannot happen.
//
// The nonce is still checked (attempt.go): ids arrive over the server-to-server token response, so it is
// not an injection defence here but a binding of the id_token to the one attempt scd started, alongside
// `state` and the PKCE verifier, all kept on the single-use social_oauth_states row.

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

// clockSkew is the leeway on exp / nbf / iat, so a slow consent screen does not fail on a few seconds of drift.
const clockSkew = 2 * time.Minute

// An unknown kid forces a refetch (rate-limited by jwksMinRefetch), which picks up IdP key rotation.
const (
	jwksTTL        = 6 * time.Hour
	jwksMinRefetch = 1 * time.Minute
)

// errIDToken is wrapped by every verification failure, distinguishing a refused token from a transport error.
var errIDToken = errors.New("id_token rejected")

// idClaims types the standard claims; provider-specific ones stay in raw.
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

// boolish reads a claim encoded as a JSON bool or the string "true"/"false" (Apple does both).
func (c *idClaims) boolish(name string) (val, present bool) {
	switch v := c.raw[name].(type) {
	case bool:
		return v, true
	case string:
		return strings.EqualFold(v, "true"), true
	}
	return false, false
}

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
			// A failed refresh keeps serving the last good set so a transient key-endpoint outage does not reject known keys.
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
		// Skip non-RSA and non-signing keys rather than failing the whole set.
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

// verifyIDToken checks signature, time and audience; the issuer is left to the caller because it is
// provider-specific (Microsoft's multi-tenant issuer embeds the token's own tenant id).
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

// signES256 encodes the signature as fixed-width r||s (RFC 7518), not the ASN.1 DER that SignASN1 emits.
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

// ParseP256PrivateKey reads Apple's .p8 (PKCS#8 EC P-256; SEC 1 also accepted). Exported so edged can
// refuse an unusable key at save time.
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
