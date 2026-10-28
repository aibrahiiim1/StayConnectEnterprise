package phone

import "testing"

// P4 L2: other digit scripts map to ASCII (one number, one destination) and length counts digits, not bytes.
func TestNormalizeDigitScripts(t *testing.T) {
	ascii, err := Normalize("+20 100 123 4567")
	if err != nil || ascii != "+201001234567" {
		t.Fatalf("ascii: %q %v", ascii, err)
	}
	for _, in := range []string{
		"+٢٠١٠٠١٢٣٤٥٦٧",  // Arabic-Indic
		"+۲۰۱۰۰۱۲۳۴۵۶۷",  // Extended Arabic-Indic (Persian)
		"+２０１００１２３４５６７",  // fullwidth
		"٠٠٢٠١٠٠١٢٣٤٥٦٧", // "00" prefix in Arabic-Indic digits
	} {
		got, err := Normalize(in)
		if err != nil || got != ascii {
			t.Fatalf("%q: got %q %v, want %q", in, got, err, ascii)
		}
	}
	// Four Arabic-Indic digits are 8 bytes but only 4 digits: too short.
	if _, err := Normalize("+٢٠١٠"); err != ErrTooShort {
		t.Fatalf("4 digits in a 2-byte script: want ErrTooShort, got %v", err)
	}
	if _, err := Normalize("+2010012345a"); err != ErrBadChars {
		t.Fatalf("letters: want ErrBadChars, got %v", err)
	}
	if _, err := Normalize("201001234567"); err != ErrNoPlus {
		t.Fatalf("no plus: want ErrNoPlus, got %v", err)
	}
}
