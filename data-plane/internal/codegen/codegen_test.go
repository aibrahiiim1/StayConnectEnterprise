package codegen

// This package had no test file at all, which is how it came to be the configurable voucher generator that
// generated no vouchers: nothing pinned its behaviour, so nothing noticed when the voucher path stopped
// calling it. These tests pin the properties the voucher code format depends on -- the two modes the
// Product-Owner requirement names, the eight-character ceiling as it is used, the always-excluded
// characters, and the two refusals that keep a short code from being quietly unsafe.

import (
	"strings"
	"testing"
)

// The appliance lookup normaliser folds I/L to 1 and O to 0 and drops U, so a code containing any of them
// could never be redeemed. That is the reason the exclusion is unconditional rather than an option.
func TestAlwaysExcludedCharactersAreNeverGenerated(t *testing.T) {
	for _, mode := range []string{ModeNumbers, ModeLetters, ModeAlnum, ModeComplex} {
		for _, excl := range []bool{false, true} {
			alpha := Alphabet(mode, excl)
			if alpha == "" {
				t.Fatalf("mode %q excludeAmbiguous=%v produced an empty alphabet", mode, excl)
			}
			for _, bad := range "ILOU" {
				if strings.ContainsRune(alpha, bad) {
					t.Errorf("mode %q excludeAmbiguous=%v alphabet contains %q, which the normaliser would fold",
						mode, excl, bad)
				}
			}
		}
	}
}

// The two modes the requirement names, with the exclusion the voucher path always applies.
func TestVoucherModesProduceTheExpectedAlphabets(t *testing.T) {
	if got, want := Alphabet(ModeNumbers, true), "2346789"; got != want {
		t.Errorf("numbers alphabet = %q, want %q (0, 1 and 5 are the ambiguous digits)", got, want)
	}
	mixed := Alphabet(ModeAlnum, true)
	for _, bad := range "ILOU015S" {
		if strings.ContainsRune(mixed, bad) {
			t.Errorf("mixed alphabet contains %q", bad)
		}
	}
	// 21 letters (26 less I, L, O, U, S) + 7 digits (10 less 0, 1, 5).
	if len(mixed) != 28 {
		t.Errorf("mixed alphabet has %d symbols (%q), want 28", len(mixed), mixed)
	}
}

func TestGeneratedCodesHonourLengthAndAlphabet(t *testing.T) {
	for _, mode := range []string{ModeNumbers, ModeAlnum} {
		for _, n := range []int{6, 7, 8} {
			codes, err := GenerateN(25, Options{Length: n, Mode: mode, ExcludeAmbiguous: true})
			if err != nil {
				t.Fatalf("mode %q length %d: %v", mode, n, err)
			}
			if len(codes) != 25 {
				t.Fatalf("mode %q length %d: got %d codes, want 25", mode, n, len(codes))
			}
			alpha := Alphabet(mode, true)
			for _, c := range codes {
				if len(c) != n {
					t.Errorf("mode %q: code %q has length %d, want %d", mode, c, len(c), n)
				}
				for _, r := range c {
					if !strings.ContainsRune(alpha, r) {
						t.Errorf("mode %q: code %q contains %q, outside the alphabet %q", mode, c, r, alpha)
					}
				}
			}
		}
	}
}

// A batch is internally unique. The voucher path relies on this rather than on UNIQUE (code_hmac), because
// a collision caught at INSERT surfaces after part of the batch has already committed.
func TestBatchIsInternallyUnique(t *testing.T) {
	codes, err := GenerateN(500, Options{Length: 8, Mode: ModeAlnum, ExcludeAmbiguous: true})
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]bool, len(codes))
	for _, c := range codes {
		if seen[c] {
			t.Fatalf("duplicate code %q in one batch", c)
		}
		seen[c] = true
	}
}

// The code-space guard. Digits-only at six characters is 7^6 = 117 649, so a quarter of it is 29 412: a
// batch of 500 is fine and a batch of 100 000 must be refused rather than generated at low entropy.
func TestCodeSpaceGuardRefusesABatchTooLargeForItsAlphabet(t *testing.T) {
	if _, err := GenerateN(500, Options{Length: 6, Mode: ModeNumbers, ExcludeAmbiguous: true}); err != nil {
		t.Fatalf("500 six-digit codes should be allowed: %v", err)
	}
	if _, err := GenerateN(100000, Options{Length: 6, Mode: ModeNumbers, ExcludeAmbiguous: true}); err == nil {
		t.Fatal("100 000 six-digit codes were generated; the code-space guard did not refuse")
	}
}

func TestUnknownModeIsRefusedRatherThanDefaulted(t *testing.T) {
	if _, err := GenerateN(1, Options{Length: 8, Mode: "hexadecimal"}); err == nil {
		t.Fatal("an unknown char_mode was accepted")
	}
	// The empty mode IS defaulted, deliberately, and the voucher path never sends one.
	if _, err := GenerateN(1, Options{Length: 8, Mode: ""}); err != nil {
		t.Fatalf("the empty mode should default to alnum: %v", err)
	}
}

func TestLengthOutsideTheBoundsIsRefused(t *testing.T) {
	if _, err := GenerateN(1, Options{Length: MinLength - 1, Mode: ModeAlnum}); err == nil {
		t.Fatalf("length %d was accepted below MinLength %d", MinLength-1, MinLength)
	}
	if _, err := GenerateN(1, Options{Length: MaxLength + 1, Mode: ModeAlnum}); err == nil {
		t.Fatalf("length %d was accepted above MaxLength %d", MaxLength+1, MaxLength)
	}
}

// Normalize is what makes a generated code equal to what the guest types, so it must be a no-op on
// everything this package produces.
func TestNormalizeIsIdentityOnGeneratedCodes(t *testing.T) {
	codes, err := GenerateN(50, Options{Length: 8, Mode: ModeAlnum, ExcludeAmbiguous: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range codes {
		if got := Normalize(c); got != c {
			t.Errorf("Normalize(%q) = %q; a generated code must survive normalisation unchanged", c, got)
		}
	}
}

func TestNormalizeStripsWhatAGuestTypes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"a2b4-c6d8", "A2B4C6D8"},
		{" 2346 789 ", "2346789"},
		{"2346\t789", "2346789"},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPrefixIsBoundedAndUppercased(t *testing.T) {
	// "vp", not "vip": uppercasing "vip" yields an I, which the prefix rule excludes for exactly the reason
	// the alphabet does. That is the rule working, and the last assertion below pins it.
	codes, err := GenerateN(1, Options{Length: 6, Mode: ModeNumbers, Prefix: "vp", ExcludeAmbiguous: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(codes[0], "VP") {
		t.Errorf("code %q does not carry the uppercased prefix", codes[0])
	}
	if _, err := GenerateN(1, Options{Length: 6, Mode: ModeNumbers, Prefix: strings.Repeat("A", MaxPrefix+1)}); err == nil {
		t.Fatal("an over-long prefix was accepted")
	}
	// A prefix may not contain a character the normaliser folds, for the same reason the alphabet may not.
	if _, err := GenerateN(1, Options{Length: 6, Mode: ModeNumbers, Prefix: "IOU"}); err == nil {
		t.Fatal("a prefix containing I, O or U was accepted")
	}
}
