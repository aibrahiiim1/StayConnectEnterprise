package iamv2

import "strings"

// Authoritative ISO-4217 currency metadata (versioned, internal; no runtime external calls). Maps the
// alpha-3 code to its minor-unit exponent. Amounts are always integer minor units. Extend as needed;
// unknown codes are rejected (fail closed).
const currencyMetaVersion = 1

var iso4217Exponent = map[string]int{
	// exponent 0 (no minor unit)
	"JPY": 0, "KRW": 0, "CLP": 0, "ISK": 0, "VND": 0, "XAF": 0, "XOF": 0, "XPF": 0, "UGX": 0, "RWF": 0, "PYG": 0,
	// exponent 2 (cents)
	"USD": 2, "EUR": 2, "GBP": 2, "CAD": 2, "AUD": 2, "CHF": 2, "CNY": 2, "HKD": 2, "SGD": 2, "AED": 2,
	"SAR": 2, "EGP": 2, "ZAR": 2, "INR": 2, "BRL": 2, "MXN": 2, "TRY": 2, "PLN": 2, "SEK": 2, "NOK": 2,
	"DKK": 2, "NZD": 2, "THB": 2, "MYR": 2, "PHP": 2, "IDR": 2, "QAR": 2, "ILS": 2, "RON": 2, "CZK": 2,
	// exponent 3 (mils)
	"BHD": 3, "KWD": 3, "OMR": 3, "JOD": 3, "TND": 3, "LYD": 3, "IQD": 3,
}

// NormalizeCurrency upper-cases and trims a currency code.
func NormalizeCurrency(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// CurrencyExponent returns the authoritative minor-unit exponent for a code, and whether the code is
// known.
func CurrencyExponent(code string) (int, bool) {
	e, ok := iso4217Exponent[NormalizeCurrency(code)]
	return e, ok
}

// ValidateCurrency verifies the code is a known ISO-4217 currency and the supplied exponent equals the
// authoritative exponent for that currency. Fails closed on unknown code or mismatch.
func ValidateCurrency(code string, exponent int) (string, error) {
	c := NormalizeCurrency(code)
	want, ok := iso4217Exponent[c]
	if !ok {
		return "", &Error{Code: ErrInvalidInput, Msg: "unknown currency " + c}
	}
	if exponent != want {
		return "", &Error{Code: ErrInvalidInput, Msg: "currency exponent mismatch for " + c}
	}
	return c, nil
}
