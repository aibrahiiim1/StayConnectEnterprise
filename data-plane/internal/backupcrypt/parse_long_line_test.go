package backupcrypt

import (
	"errors"
	"strings"
	"testing"
)

// P2 L21: a key file with a line over the scanner's limit is reported as unreadable, not as "no key found".
func TestParseIdentityReportsScannerErrors(t *testing.T) {
	secret, _, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	text := "# " + strings.Repeat("x", 70*1024) + "\n" + secret + "\n"
	_, err = ParseIdentity(text)
	if err == nil || errors.Is(err, ErrNoIdentity) {
		t.Fatalf("over-long line: want a read error, got %v", err)
	}
	if _, err := ParseIdentity(secret); err != nil {
		t.Fatalf("a bare key must still parse: %v", err)
	}
}
