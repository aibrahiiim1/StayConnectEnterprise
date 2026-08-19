package iamv2

import "testing"

// The properties the contract requires, asserted rather than assumed.
func TestVoucherCodecKeySeparationAndOwnerBinding(t *testing.T) {
	kr := MapVoucherKeyring{"dek-1": make([]byte, 32)}
	ten, site := "t1", "s1"

	clear, sealed, nonce, err := NewVoucherHMACKey(kr, "dek-1", ten, site, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The per-generation HMAC key must NOT be the DEK: separate roles, separate material.
	dek, _ := kr.Key("dek-1")
	if string(clear) == string(dek) {
		t.Fatal("the blind-index key equals the DEK: one key is doing two roles")
	}
	got, err := OpenVoucherHMACKey(kr, "dek-1", ten, site, 1, sealed, nonce)
	if err != nil || string(got) != string(clear) {
		t.Fatalf("generation key did not round-trip: %v", err)
	}
	// Owner binding: the same ciphertext under a different generation must FAIL, not decrypt.
	if _, err := OpenVoucherHMACKey(kr, "dek-1", ten, site, 2, sealed, nonce); err == nil {
		t.Fatal("a generation key opened under the wrong generation number")
	}
	if _, err := OpenVoucherHMACKey(kr, "dek-1", "other", site, 1, sealed, nonce); err == nil {
		t.Fatal("a generation key opened under the wrong tenant")
	}

	// Code sealing binds the voucher row.
	ct, cn, err := SealVoucherCode(kr, "dek-1", ten, site, "v1", "g1", "ABCD1234")
	if err != nil {
		t.Fatal(err)
	}
	if code, err := OpenVoucherCode(kr, "dek-1", ten, site, "v1", "g1", ct, cn); err != nil || code != "ABCD1234" {
		t.Fatalf("code did not round-trip: %q %v", code, err)
	}
	if _, err := OpenVoucherCode(kr, "dek-1", ten, site, "v2", "g1", ct, cn); err == nil {
		t.Fatal("a voucher code opened under a different voucher id")
	}
	// A missing DEK must fail closed, never silently produce plaintext.
	if _, _, err := SealVoucherCode(MapVoucherKeyring{}, "dek-1", ten, site, "v1", "g1", "X"); err != ErrVoucherKeyUnavailable {
		t.Fatalf("missing DEK must fail closed, got %v", err)
	}
	// Blind index is deterministic per (tenant, site, code) and site-separated.
	a := VoucherCodeHMAC(clear, ten, site, "ABCD1234")
	if string(a) != string(VoucherCodeHMAC(clear, ten, site, "ABCD1234")) {
		t.Fatal("blind index is not deterministic")
	}
	if string(a) == string(VoucherCodeHMAC(clear, ten, "s2", "ABCD1234")) {
		t.Fatal("the same code indexes identically at two sites")
	}
	if Last4("ABCD1234") != "1234" || Last4("AB") != "AB" {
		t.Fatal("last4 hint wrong")
	}
}
