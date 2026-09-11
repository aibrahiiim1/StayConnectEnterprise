package signinattempt

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func testKeyring() (Keyring, string) {
	key := bytes.Repeat([]byte{0x2b}, 32)
	return MapKeyring{"11111111-1111-4111-8111-111111111111": key}, "11111111-1111-4111-8111-111111111111"
}

func sample() Sensitive {
	return Sensitive{
		SubmittedVerifier:         "  del carmen ",
		NormalizedVerifier:        "DEL CARMEN",
		AcceptedFirstName:         "MARIA DEL CARMEN",
		AcceptedFamilyName:        "OKONKWO",
		AcceptedReservationNumber: "RES-4001",
		AdditionalAcceptedGuests:  2,
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	kr, id := testKeyring()
	in := sample()
	sealed, err := Seal(kr, id, "t1", "s1", "a1", in)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.CipherVersion != CipherVersion || sealed.EncryptionKey != id {
		t.Fatalf("sealed metadata = %+v", sealed)
	}
	if len(sealed.Nonce) != 12 {
		t.Fatalf("nonce length = %d, want the GCM standard 12", len(sealed.Nonce))
	}
	out, err := Open(kr, sealed, "t1", "s1", "a1")
	if err != nil {
		t.Fatal(err)
	}
	if out != in {
		t.Fatalf("round trip changed the value:\n got %+v\nwant %+v", out, in)
	}
}

// NO PLAINTEXT SURVIVES IN THE CIPHERTEXT. The point of sealing is that a copy of the database yields
// nothing, so this asserts it directly against every sensitive field rather than trusting that AES did its
// job.
func TestSealedBytesCarryNoPlaintext(t *testing.T) {
	kr, id := testKeyring()
	in := sample()
	sealed, err := Seal(kr, id, "t1", "s1", "a1", in)
	if err != nil {
		t.Fatal(err)
	}
	blob := string(sealed.Ciphertext) + string(sealed.Nonce)
	for _, secret := range []string{
		in.SubmittedVerifier, in.NormalizedVerifier, in.AcceptedFirstName,
		in.AcceptedFamilyName, in.AcceptedReservationNumber,
		strings.TrimSpace(in.SubmittedVerifier),
	} {
		if secret == "" {
			continue
		}
		if strings.Contains(blob, secret) {
			t.Fatalf("the sealed bytes contain %q in the clear", secret)
		}
	}
}

// THE OWNER BINDING. A ciphertext lifted from one attempt, one site or one tenant and pasted into another
// must fail rather than quietly describe the wrong guest to an operator.
func TestSealedMaterialIsBoundToItsOwner(t *testing.T) {
	kr, id := testKeyring()
	sealed, err := Seal(kr, id, "t1", "s1", "a1", sample())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, tenant, site, attempt string }{
		{"another attempt", "t1", "s1", "a2"},
		{"another site", "t1", "s2", "a1"},
		{"another tenant", "t2", "s1", "a1"},
	} {
		if _, err := Open(kr, sealed, c.tenant, c.site, c.attempt); !errors.Is(err, ErrOpen) {
			t.Errorf("%s: err = %v, want ErrOpen", c.name, err)
		}
	}
}

func TestOpenRefusesAnotherKeyAndATamperedBlob(t *testing.T) {
	kr, id := testKeyring()
	sealed, err := Seal(kr, id, "t1", "s1", "a1", sample())
	if err != nil {
		t.Fatal(err)
	}

	other := MapKeyring{id: bytes.Repeat([]byte{0x7c}, 32)}
	if _, err := Open(other, sealed, "t1", "s1", "a1"); !errors.Is(err, ErrOpen) {
		t.Errorf("a different key opened the blob: %v", err)
	}

	tampered := sealed
	tampered.Ciphertext = append([]byte(nil), sealed.Ciphertext...)
	tampered.Ciphertext[0] ^= 0xff
	if _, err := Open(kr, tampered, "t1", "s1", "a1"); !errors.Is(err, ErrOpen) {
		t.Errorf("a tampered ciphertext opened: %v", err)
	}

	wrongVersion := sealed
	wrongVersion.CipherVersion = 99
	if _, err := Open(kr, wrongVersion, "t1", "s1", "a1"); !errors.Is(err, ErrOpen) {
		t.Errorf("an unknown cipher version opened: %v", err)
	}
}

// FAIL TOWARDS "NO SENSITIVE HALF", NEVER TOWARDS PLAINTEXT. Every way of not having a usable key returns an
// error and no ciphertext, so a caller that ignores the error writes nulls rather than the guest's data.
func TestAnUnusableKeyProducesNothingRatherThanPlaintext(t *testing.T) {
	_, id := testKeyring()
	cases := map[string]Keyring{
		"nil keyring":  nil,
		"absent key":   MapKeyring{},
		"short key":    MapKeyring{id: bytes.Repeat([]byte{1}, 16)},
		"oversize key": MapKeyring{id: bytes.Repeat([]byte{1}, 64)},
	}
	for name, kr := range cases {
		sealed, err := Seal(kr, id, "t1", "s1", "a1", sample())
		if !errors.Is(err, ErrKeyUnavailable) {
			t.Errorf("%s: err = %v, want ErrKeyUnavailable", name, err)
		}
		if sealed.Ciphertext != nil || sealed.Nonce != nil {
			t.Errorf("%s: material was produced without a usable key", name)
		}
	}
}

func TestEmpty(t *testing.T) {
	if !(Sensitive{}).Empty() {
		t.Error("a zero Sensitive is not reported empty")
	}
	if (Sensitive{SubmittedVerifier: "x"}).Empty() {
		t.Error("a submitted verifier alone is reported empty")
	}
	// A record with no submission but with accepted values is still worth sealing.
	if (Sensitive{AcceptedFamilyName: "X"}).Empty() {
		t.Error("accepted values alone are reported empty")
	}
}
