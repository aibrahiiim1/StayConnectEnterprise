// Package totp implements RFC 6238 with the fixed parameters authenticator apps assume (HMAC-SHA1, 30 s, 6 digits).
// It duplicates data-plane/internal/totp (separate module); keep them identical. Replay protection is the
// caller's: Check returns the matched step so the last accepted one can be recorded.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	Period      = 30 * time.Second
	Digits      = 6
	SecretBytes = 20 // 160 bits, as RFC 4226 recommends
	// Skew is the steps accepted either side of now, tolerating a phone clock 30 s off.
	Skew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

func NewSecret() ([]byte, error) {
	s := make([]byte, SecretBytes)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return s, nil
}

func EncodeSecret(secret []byte) string { return b32.EncodeToString(secret) }

func Step(t time.Time) int64 { return t.Unix() / int64(Period/time.Second) }

func Code(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, v%1_000_000)
}

func Normalize(code string) string {
	return strings.NewReplacer(" ", "", "-", "", " ", "").Replace(strings.TrimSpace(code))
}

// Check reports whether code is valid at now and the step it matched; every step is compared in constant time.
func Check(secret []byte, code string, now time.Time) (int64, bool) {
	code = Normalize(code)
	if len(code) != Digits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	cur := Step(now)
	matched, ok := int64(0), false
	for d := int64(-Skew); d <= Skew; d++ {
		if subtle.ConstantTimeCompare([]byte(Code(secret, cur+d)), []byte(code)) == 1 && !ok {
			matched, ok = cur+d, true
		}
	}
	return matched, ok
}

// URI is the otpauth:// link for the QR code.
func URI(secret []byte, issuer, account string) string {
	label := url.PathEscape(issuer) + ":" + url.PathEscape(account)
	q := url.Values{}
	q.Set("secret", EncodeSecret(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(Digits))
	q.Set("period", fmt.Sprint(int(Period/time.Second)))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
