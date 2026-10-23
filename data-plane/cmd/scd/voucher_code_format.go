package main

// Voucher code format: a property setting (digits only, or digits mixed with letters; at most EIGHT characters), persisted and audited; this is the read side.
// Generation reuses internal/codegen. Ambiguity exclusion is ALWAYS on (guests misread those characters), so it is not a setting.

import (
	"context"
	"fmt"

	"github.com/stayconnect/enterprise/data-plane/internal/codegen"
)

type voucherCodeFormat struct {
	Mode    string // "numbers" | "mixed" -- the operator vocabulary, not codegen's
	Length  int    // 6..8
	Version int64  // config_version; 0 means no row exists and these are the defaults
}

// Maps the two operator-facing modes onto codegen modes ("mixed" is ModeAlnum). Other codegen modes are deliberately unreachable: only two values are stored and validated.
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

// Reads the site format via iam_v2.voucher_code_settings_get and FAILS rather than substituting a default of its own: the function already answers for a site with no row, and a second set of fallback constants would be two sources of truth.
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

// The bounds are checked in three places on purpose (table CHECK, setter, here): this is the only one a hand-edited row cannot bypass, since it stops a bad value being USED.
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
	// The ceiling: not more than eight characters, for either mode.
	voucherCodeMaxLength = 8
	// The floor is the one issuance already enforced ("length must be between 6 and 24"); digits-only leaves seven symbols, so six is 117 649 codes and four would be 2 401.
	voucherCodeMinLength = 6
)
