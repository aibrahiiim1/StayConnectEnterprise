package iamv2

import "strings"

// ISO-4217 minor-unit exponents. Amounts are integer minor units; unknown codes are rejected (fail closed).
const currencyMetaVersion = 1

var iso4217Exponent = map[string]int{
	"JPY": 0, "KRW": 0, "CLP": 0, "ISK": 0, "VND": 0, "XAF": 0, "XOF": 0, "XPF": 0, "UGX": 0, "RWF": 0, "PYG": 0,
	"USD": 2, "EUR": 2, "GBP": 2, "CAD": 2, "AUD": 2, "CHF": 2, "CNY": 2, "HKD": 2, "SGD": 2, "AED": 2,
	"SAR": 2, "EGP": 2, "ZAR": 2, "INR": 2, "BRL": 2, "MXN": 2, "TRY": 2, "PLN": 2, "SEK": 2, "NOK": 2,
	"DKK": 2, "NZD": 2, "THB": 2, "MYR": 2, "PHP": 2, "IDR": 2, "QAR": 2, "ILS": 2, "RON": 2, "CZK": 2,
	"BHD": 3, "KWD": 3, "OMR": 3, "JOD": 3, "TND": 3, "LYD": 3, "IQD": 3,
}

func NormalizeCurrency(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

func CurrencyExponent(code string) (int, bool) {
	e, ok := iso4217Exponent[NormalizeCurrency(code)]
	return e, ok
}

// ValidateCurrency fails closed on an unknown code or an exponent that differs from the authoritative one.
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
