package main

// The voucher code format, pinned where it is decided.
//
// These are pure tests over the mapping and the bounds -- the database side (the CHECK constraint, the
// setter's own re-validation and the reader's defaults) is exercised by the migration and its integration
// coverage. What is pinned here is the part that decides what a guest is handed: that only the two modes the
// requirement names are reachable, that the eight-character ceiling is applied at the point of USE and not
// only at the point of storage, and that ambiguity exclusion is not optional.

import (
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/codegen"
)

func TestOnlyTheTwoRequiredModesAreReachable(t *testing.T) {
	if got, err := voucherCodeMode("numbers"); err != nil || got != codegen.ModeNumbers {
		t.Errorf(`voucherCodeMode("numbers") = %q, %v; want %q, nil`, got, err, codegen.ModeNumbers)
	}
	if got, err := voucherCodeMode("mixed"); err != nil || got != codegen.ModeAlnum {
		t.Errorf(`voucherCodeMode("mixed") = %q, %v; want %q, nil`, got, err, codegen.ModeAlnum)
	}
	// codegen has these two as well. They are deliberately unreachable from a voucher: 0085 stores only the
	// two values the requirement names, and a mode nobody asked for is a surface nobody validated.
	for _, unreachable := range []string{codegen.ModeLetters, codegen.ModeComplex, "", "NUMBERS", "alnum"} {
		if _, err := voucherCodeMode(unreachable); err == nil {
			t.Errorf("voucherCodeMode(%q) was accepted; only numbers and mixed may reach a voucher", unreachable)
		}
	}
}

func TestTheEightCharacterCeilingIsAppliedWhereTheCodeIsUsed(t *testing.T) {
	// The Product-Owner requirement, as a bound this code enforces rather than trusts.
	if voucherCodeMaxLength != 8 {
		t.Fatalf("voucherCodeMaxLength = %d; the requirement is eight characters for both modes", voucherCodeMaxLength)
	}
	// The floor is the one the shipped issuance path already enforced, carried forward.
	if voucherCodeMinLength != 6 {
		t.Fatalf("voucherCodeMinLength = %d; want the 6 the previous issuance path enforced", voucherCodeMinLength)
	}

	for _, mode := range []string{"numbers", "mixed"} {
		for n := voucherCodeMinLength; n <= voucherCodeMaxLength; n++ {
			if _, err := (voucherCodeFormat{Mode: mode, Length: n}).voucherCodeOptions(); err != nil {
				t.Errorf("mode %q length %d should be usable: %v", mode, n, err)
			}
		}
		// A row hand-edited past the CHECK constraint must still not produce a code. This is the only one of
		// the three bound checks that a direct database write cannot bypass.
		for _, n := range []int{0, 1, 5, 9, 16, 24} {
			if _, err := (voucherCodeFormat{Mode: mode, Length: n}).voucherCodeOptions(); err == nil {
				t.Errorf("mode %q length %d was accepted; it is outside %d..%d",
					mode, n, voucherCodeMinLength, voucherCodeMaxLength)
			}
		}
	}
}

func TestAmbiguityExclusionIsNotOptional(t *testing.T) {
	for _, mode := range []string{"numbers", "mixed"} {
		opts, err := (voucherCodeFormat{Mode: mode, Length: 8}).voucherCodeOptions()
		if err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		if !opts.ExcludeAmbiguous {
			t.Errorf("mode %q: ExcludeAmbiguous is false; every code this appliance has issued excluded the "+
				"characters guests misread, and 0085 does not offer turning that off", mode)
		}
		if opts.Prefix != "" {
			t.Errorf("mode %q: a prefix was set (%q); the requirement names no prefix", mode, opts.Prefix)
		}
	}
}

// A stored format that cannot be mapped must refuse rather than fall back. Issuance treats this as a 422,
// which is what "the stored setting is not usable" should look like to a caller.
func TestAnUnusableStoredFormatIsRefusedRatherThanSubstituted(t *testing.T) {
	for _, f := range []voucherCodeFormat{
		{Mode: "", Length: 8},
		{Mode: "letters", Length: 8},
		{Mode: "mixed", Length: 0},
		{Mode: "numbers", Length: 24},
	} {
		if _, err := f.voucherCodeOptions(); err == nil {
			t.Errorf("format %+v was accepted; an unusable stored format must refuse", f)
		}
	}
}

// End to end over the pure part: the format a site stores decides the alphabet a guest is handed.
func TestTheStoredFormatDecidesWhatTheGuestIsHanded(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		length    int
		wantAlpha string
	}{
		{"numbers", 6, "2346789"},
		{"numbers", 8, "2346789"},
		{"mixed", 8, codegen.Alphabet(codegen.ModeAlnum, true)},
	} {
		opts, err := (voucherCodeFormat{Mode: tc.mode, Length: tc.length}).voucherCodeOptions()
		if err != nil {
			t.Fatalf("%s/%d: %v", tc.mode, tc.length, err)
		}
		codes, err := codegen.GenerateN(20, opts)
		if err != nil {
			t.Fatalf("%s/%d: %v", tc.mode, tc.length, err)
		}
		for _, c := range codes {
			if len(c) != tc.length {
				t.Errorf("%s/%d: code %q has length %d", tc.mode, tc.length, c, len(c))
			}
			for _, r := range c {
				if !containsRune(tc.wantAlpha, r) {
					t.Errorf("%s/%d: code %q contains %q, outside %q", tc.mode, tc.length, c, r, tc.wantAlpha)
				}
			}
		}
	}
}

func containsRune(s string, r rune) bool {
	for _, x := range s {
		if x == r {
			return true
		}
	}
	return false
}
