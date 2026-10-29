// Package codegen generates configurable voucher codes with unbiased crypto/rand selection.
//
// The voucher lookup normalizer folds I/L->1, O->0 and drops U, so a code containing any of them could never be
// redeemed; those four characters are always excluded.
//
// ModeComplex is an alias of ModeAlnum (AD-29): there is no symbol or mixed-case alphabet, the voucher lookup
// normaliser is case-folding, so a "complex" code can never be richer than letters plus digits. It is accepted as
// input for compatibility and is normalised to "alnum".
//
// ACCEPTED RESIDUAL RISK (Product-Owner decision on AD-29, not fixed): the voucher format is unchanged. There is no
// length floor, so a short digits-only voucher (for example 6 digits) can in principle be guessed by a device that
// rotates its MAC address. The per-device and per-network sign-in throttles reduce but do not remove that exposure;
// hotels that want more margin should issue longer alphanumeric codes.
package codegen

import (
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strings"
)

const (
	ModeNumbers = "numbers"
	ModeLetters = "letters"
	ModeAlnum   = "alnum"
	ModeComplex = "complex"
)

const (
	MinLength = 4
	MaxLength = 16
	MinCount  = 1
	MaxCount  = 10000
	MaxPrefix = 8
)

const alwaysExclude = "ILOU"

// ambiguousExtra is removed when ExcludeAmbiguous is set (0/O, 1/I/L, 5/S confusion; O, I, L already gone).
const ambiguousExtra = "015S"

type Options struct {
	Length           int // random characters; the prefix is added on top
	Mode             string
	Prefix           string // uppercased
	ExcludeAmbiguous bool
}

func Alphabet(mode string, excludeAmbiguous bool) string {
	var base string
	switch mode {
	case ModeNumbers:
		base = "0123456789"
	case ModeLetters:
		base = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	default: // alnum (complex is an alias, see the package comment)
		base = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	}
	remove := alwaysExclude
	if excludeAmbiguous {
		remove += ambiguousExtra
	}
	var b strings.Builder
	for i := 0; i < len(base); i++ {
		if !strings.ContainsRune(remove, rune(base[i])) {
			b.WriteByte(base[i])
		}
	}
	return b.String()
}

// Normalize uppercases and strips dashes/whitespace; compatible with the crockford normalizer for this alphabet.
func Normalize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r == '-' || r == ' ' || r == '\t' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (o Options) normalized() (Options, string, error) {
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	switch o.Mode {
	case ModeNumbers, ModeLetters, ModeAlnum:
	case ModeComplex:
		o.Mode = ModeAlnum
	case "":
		o.Mode = ModeAlnum
	default:
		return o, "", fmt.Errorf("char_mode must be numbers|letters|alnum|complex")
	}
	if o.Length == 0 {
		o.Length = 8
	}
	if o.Length < MinLength || o.Length > MaxLength {
		return o, "", fmt.Errorf("code length must be %d..%d", MinLength, MaxLength)
	}
	o.Prefix = strings.ToUpper(strings.TrimSpace(o.Prefix))
	if len(o.Prefix) > MaxPrefix {
		return o, "", fmt.Errorf("prefix must be at most %d characters", MaxPrefix)
	}
	for _, r := range o.Prefix {
		if strings.ContainsRune(alwaysExclude, r) || !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			return o, "", fmt.Errorf("prefix may only contain A-Z (except I,L,O,U) and 0-9")
		}
	}
	alpha := Alphabet(o.Mode, o.ExcludeAmbiguous)
	if len(alpha) < 2 {
		return o, "", fmt.Errorf("character set too small after exclusions")
	}
	return o, alpha, nil
}

// GenerateN refuses a batch above 25% of the code space so it never yields heavily colliding codes.
func GenerateN(n int, opts Options) ([]string, error) {
	o, alpha, err := opts.normalized()
	if err != nil {
		return nil, err
	}
	if n < MinCount || n > MaxCount {
		return nil, fmt.Errorf("count must be %d..%d", MinCount, MaxCount)
	}
	// Huge lengths overflow, so the check only applies to spaces <= 1e15.
	space := math.Pow(float64(len(alpha)), float64(o.Length))
	if space <= 1e15 && float64(n) > space*0.25 {
		return nil, fmt.Errorf("code space (%s^%d) too small for %d codes — increase length or use a richer character set",
			plural(len(alpha)), o.Length, n)
	}
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	maxAttempts := n*4 + 64
	for attempts := 0; len(out) < n; attempts++ {
		if attempts > maxAttempts {
			return nil, fmt.Errorf("code generation exhausted retries — increase length or character set")
		}
		c, err := one(alpha, o.Length)
		if err != nil {
			return nil, err
		}
		code := o.Prefix + c
		if _, dup := seen[code]; dup {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
	}
	return out, nil
}

func one(alpha string, length int) (string, error) {
	max := big.NewInt(int64(len(alpha)))
	b := make([]byte, length)
	for i := 0; i < length; i++ {
		idx, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = alpha[idx.Int64()]
	}
	return string(b), nil
}

func plural(n int) string { return fmt.Sprintf("%d", n) }
