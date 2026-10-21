package backupcrypt

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

func TestEveryHolderOpensTheBackupAloneAndNoOtherKeyDoes(t *testing.T) {
	s1, p1, _ := GenerateKey()
	s2, p2, _ := GenerateKey()
	outsider, _, _ := GenerateKey()
	rs, err := ParseRecipients([]string{p1, p2})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	w, err := Encrypt(&buf, rs)
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(w, "guest data")
	w.Close()
	if bytes.Contains(buf.Bytes(), []byte("guest data")) {
		t.Fatal("the plaintext is visible in the encrypted file")
	}
	for _, secret := range []string{s1, s2} {
		id, err := ParseIdentity(KeyFile("IT manager", "Hotel", secret, "x", "ABCD-EFGH", time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		r, err := Decrypt(bytes.NewReader(buf.Bytes()), id)
		if err != nil {
			t.Fatalf("a holder could not open the backup: %v", err)
		}
		if got, _ := io.ReadAll(r); string(got) != "guest data" {
			t.Fatalf("decrypted %q", got)
		}
	}
	id, _ := ParseIdentity(outsider)
	if _, err := Decrypt(bytes.NewReader(buf.Bytes()), id); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("a key that is not a holder's = %v, want ErrWrongKey", err)
	}
}

func TestNothingToEncryptToIsRefused(t *testing.T) {
	if _, err := ParseRecipients(nil); !errors.Is(err, ErrNoRecipients) {
		t.Fatal("an empty holder list was accepted")
	}
	if _, err := Encrypt(io.Discard, nil); !errors.Is(err, ErrNoRecipients) {
		t.Fatal("encrypting to nobody was accepted")
	}
	if _, err := ParseRecipients([]string{"age1notakey"}); err == nil {
		t.Fatal("a malformed public key was accepted")
	}
}

func TestIdentityParsing(t *testing.T) {
	if _, err := ParseIdentity("# just a comment\n"); !errors.Is(err, ErrNoIdentity) {
		t.Fatal("a file with no key was accepted")
	}
	s, _, _ := GenerateKey()
	if _, err := ParseIdentity("  " + strings.ToLower(s) + "  "); err != nil {
		t.Fatalf("a lower-cased pasted key was refused: %v", err)
	}
}

func TestCheckCode(t *testing.T) {
	c, err := NewCheckCode()
	if err != nil || len(c) != 9 || c[4] != '-' || strings.ContainsAny(c, "01OILS5") {
		t.Fatalf("check code %q", c)
	}
	if CheckCodeHash(c) != CheckCodeHash(strings.ToLower(strings.ReplaceAll(c, "-", " "))) {
		t.Fatal("the check code is not case and separator insensitive")
	}
	if CheckCodeHash("ABCD-EFGH") == CheckCodeHash("ABCD-EFGJ") {
		t.Fatal("different codes hash alike")
	}
}
