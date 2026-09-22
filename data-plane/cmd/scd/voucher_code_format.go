package main

// THE VOUCHER CODE FORMAT, AS THE HOTEL SET IT.
//
// A Product-Owner requirement: a voucher is issued either as DIGITS ONLY or as digits MIXED with letters,
// and neither may exceed EIGHT characters. Which of the two a property wants depends on how its guests
// enter the code -- a numeric keypad wants digits, a printed card wants the shorter mixed form -- so it is a
// property setting, persisted and audited, not a constant in this file. Migration 0085 is that setting;
// this is the read side.
//
// WHAT THIS REPLACED, and why the replacement is smaller than it looks. Issuance used to carry its own
// alphabet and its own bounds:
//
//     const voucherAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
//     func randomVoucherCode(n int) (string, error) { ... rand.Int over that constant ... }
//
// while internal/codegen already implemented exactly the configurable generator this needs -- the four
// modes, the always-excluded characters the appliance normaliser would otherwise fold, ambiguity exclusion,
// an unbiased crypto/rand draw, in-batch uniqueness and a code-space guard that refuses a batch too large
// for its alphabet. It had been left generating post-stay PINs after 0049 dropped the legacy columns that
// used to configure it, so the capability sat one import away from the path that needed it. Nothing new is
// generated here: this file chooses the options and calls that package.
//
// AMBIGUITY EXCLUSION IS ALWAYS ON, for both modes. Every code this appliance has ever issued was drawn
// from an alphabet without the characters guests misread from a card, and no one asked for the ability to
// turn that off. It is therefore not a setting -- see 0085.

import (
	"context"
	"fmt"

	"github.com/stayconnect/enterprise/data-plane/internal/codegen"
)

// voucherCodeFormat is the format in force for this site, as the database answered.
type voucherCodeFormat struct {
	Mode    string // "numbers" | "mixed" -- the operator vocabulary, not codegen's
	Length  int    // 6..8
	Version int64  // config_version; 0 means no row exists and these are the defaults
}

// voucherCodeMode maps the two operator-facing modes onto codegen's character modes.
//
// "mixed" is codegen.ModeAlnum: uppercase letters and digits. codegen also has ModeLetters and ModeComplex;
// neither is reachable from here, because 0085 stores only the two values the requirement names and an
// operator setting whose values nobody asked for is a surface nobody validated.
func voucherCodeMode(mode string) (string, error) {
	switch mode {
	case "numbers":
		return codegen.ModeNumbers, nil
	case "mixed":
		return codegen.ModeAlnum, nil
	default:
		return "", fmt.Errorf("unknown voucher code mode %q", mode)
	}
}

// voucherCodeFormatFor reads the site's format through iam_v2.voucher_code_settings_get.
//
// It FAILS rather than substituting a default of its own. The reader function already answers for a site
// with no row -- it returns the column defaults with config_version 0 -- so the only way this errors is that
// the function is missing or unreadable, and at that point the honest outcome is a refusal. A second set of
// fallback constants here is how a system ends up with two sources of truth and codes nobody chose.
func (s *server) voucherCodeFormatFor(ctx context.Context) (voucherCodeFormat, error) {
	var f voucherCodeFormat
	err := s.db.QueryRow(ctx,
		`SELECT code_mode, code_length, config_version FROM iam_v2.voucher_code_settings_get($1::uuid,$2::uuid)`,
		s.tenID, s.siteID).Scan(&f.Mode, &f.Length, &f.Version)
	if err != nil {
		return voucherCodeFormat{}, err
	}
	return f, nil
}

// voucherCodeOptions turns a stored format into the generator options, re-validating the bounds.
//
// The bounds are checked in three places on purpose: the CHECK constraint on the table, the setter
// function, and here. The first two stop a bad value being STORED; this one stops a bad value being USED,
// which matters because this is the only one of the three that a hand-edited row cannot bypass.
func (f voucherCodeFormat) voucherCodeOptions() (codegen.Options, error) {
	mode, err := voucherCodeMode(f.Mode)
	if err != nil {
		return codegen.Options{}, err
	}
	if f.Length < voucherCodeMinLength || f.Length > voucherCodeMaxLength {
		return codegen.Options{}, fmt.Errorf("stored voucher code length %d is outside %d..%d",
			f.Length, voucherCodeMinLength, voucherCodeMaxLength)
	}
	return codegen.Options{
		Length:           f.Length,
		Mode:             mode,
		ExcludeAmbiguous: true,
	}, nil
}

const (
	// The ceiling is the Product-Owner requirement: not more than eight characters, for either mode.
	voucherCodeMaxLength = 8
	// The floor is the floor the shipped issuance path already enforced ("length must be between 6 and 24"),
	// carried forward rather than invented. With ambiguous characters excluded, digits-only leaves seven
	// symbols; six of them is 117 649 codes and four would be 2 401.
	voucherCodeMinLength = 6
)
