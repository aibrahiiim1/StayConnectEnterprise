// Package totp is the time-based one-time password of RFC 6238 that every authenticator app speaks: HMAC-SHA1,
// 30-second steps, 6 digits. Those are the parameters Google Authenticator, Microsoft Authenticator and the rest
// accept without asking, which is why they are fixed rather than configurable.
//
// The same code as data-plane/internal/totp (the appliance's): Central is a separate module and cannot import an
// appliance internal package. Both are checked against RFC 6238's own test vectors; keep them identical.
//
// It only computes and checks codes. Keeping a code from being accepted twice (RFC 6238 section 5.2) needs a
// record of the last step accepted, which the caller keeps: Check returns the step it matched.
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
	// Period is the length of one step.
	Period = 30 * time.Second
	// Digits is the length of a code.
	Digits = 6
	// SecretBytes is the secret size RFC 4226 recommends (160 bits, the HMAC-SHA1 block's natural key).
	SecretBytes = 20
	// Skew is how many steps either side of now a code is accepted for: one, so a phone clock 30 seconds off,
	// or a code typed as it changes, still works (RFC 6238 section 6).
	Skew = 1
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSecret returns a fresh random secret.
func NewSecret() ([]byte, error) {
	s := make([]byte, SecretBytes)
	if _, err := rand.Read(s); err != nil {
		return nil, err
	}
	return s, nil
}

// EncodeSecret is the secret as people and apps type it: unpadded base32.
func EncodeSecret(secret []byte) string { return b32.EncodeToString(secret) }

// Step is the step number a moment falls in.
func Step(t time.Time) int64 { return t.Unix() / int64(Period/time.Second) }

// Code is the code for one step.
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

// Normalize strips the spaces and dashes people type and the app shows ("123 456").
func Normalize(code string) string {
	return strings.NewReplacer(" ", "", "-", "", " ", "").Replace(strings.TrimSpace(code))
}

// Check reports whether code is valid at now, and the step it matched. Every step in the window is compared in
// constant time, so the answer takes the same time whichever step matched or none did.
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

// URI is the otpauth:// link an authenticator app reads from the QR code. issuer and account are what the app
// shows; both appear in the label and the issuer parameter, as the de-facto key-uri format asks.
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
