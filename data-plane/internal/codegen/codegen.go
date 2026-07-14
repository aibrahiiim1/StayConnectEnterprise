// Package codegen generates configurable voucher codes: operator-chosen length,
// character mode, optional prefix, and ambiguous-character exclusion. Codes are
// produced with crypto/rand using unbiased selection.
//
// Round-trip guarantee: the appliance voucher lookup normalizer folds I/L→1,
// O→0 and drops U, so any code containing I, L, O or U could never be redeemed.
// Those four characters are therefore ALWAYS excluded from generation, so every
// generated code is stable under normalization (uppercase, no dashes) and matches
// exactly what the guest types.
package codegen

import (
	"crypto/rand"
	"fmt"
	"math"
	"math/big"
	"strings"
)

// Character modes.
const (
	ModeNumbers = "numbers" // digits only
	ModeLetters = "letters" // uppercase letters only
	ModeAlnum   = "alnum"   // uppercase letters + digits
	ModeComplex = "complex" // uppercase letters + digits (max charset)
)

// Production-safe bounds.
const (
	MinLength = 4
	MaxLength = 16
	MinCount  = 1
	MaxCount  = 10000
	MaxPrefix = 8
)

// I, L, O, U are ALWAYS excluded (see package doc — normalizer round-trip).
const alwaysExclude = "ILOU"

// Additionally removed when ExcludeAmbiguous is set: the operator-requested
// 0/O, 1/I/L, 5/S visual-confusion sets (O,I,L already gone) — i.e. 0, 1, 5, S.
const ambiguousExtra = "015S"

// Options configures a batch generation.
type Options struct {
	Length           int    // number of RANDOM characters (prefix is added on top)
	Mode             string // numbers|letters|alnum|complex
	Prefix           string // optional, prepended verbatim (uppercased)
	ExcludeAmbiguous bool
}

// Alphabet returns the effective random alphabet for a mode + ambiguity choice.
func Alphabet(mode string, excludeAmbiguous bool) string {
	var base string
	switch mode {
	case ModeNumbers:
		base = "0123456789"
	case ModeLetters:
		base = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	default: // alnum, complex
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

// Normalize returns the canonical form used for storage and lookup: uppercase,
// with dashes/whitespace stripped. (I/L/O/U are never generated, so no folding is
// needed; this is deliberately compatible with the crockford normalizer for the
// generated alphabet.)
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
	case ModeNumbers, ModeLetters, ModeAlnum, ModeComplex:
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

// GenerateN returns n in-memory-unique codes. It refuses to generate when the
// code space is too small for the batch (count must be ≤ 25% of the space) so a
// large batch never silently produces low-entropy or heavily-colliding codes.
func GenerateN(n int, opts Options) ([]string, error) {
	o, alpha, err := opts.normalized()
	if err != nil {
		return nil, err
	}
	if n < MinCount || n > MaxCount {
		return nil, fmt.Errorf("count must be %d..%d", MinCount, MaxCount)
	}
	// Approximate space; huge lengths overflow, so cap the exponent for the check.
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

// one builds a single length-char code with unbiased crypto/rand selection.
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
