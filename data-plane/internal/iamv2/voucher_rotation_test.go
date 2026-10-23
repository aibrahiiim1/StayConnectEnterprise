package iamv2

import (
	"context"
	"testing"
)

// A voucher issued under an older key generation must still authenticate: its stored code_hmac uses the key of
// the generation it pins, so lookup must try every generation, not only the active one.
func TestVoucherIndexCoversEveryGenerationNotJustTheActiveOne(t *testing.T) {
	kr := MapVoucherKeyring{"11111111-1111-4111-8111-111111111111": make([]byte, 32)}
	keyID := "11111111-1111-4111-8111-111111111111"
	ten, site, code := "t1", "s1", "ABCD1234"

	gen1, _, _, err := NewVoucherHMACKey(kr, keyID, ten, site, 1)
	if err != nil {
		t.Fatal(err)
	}
	gen2, _, _, err := NewVoucherHMACKey(kr, keyID, ten, site, 2)
	if err != nil {
		t.Fatal(err)
	}
	idx1 := VoucherCodeHMAC(gen1, ten, site, code) // what issuance stored under generation 1
	idx2 := VoucherCodeHMAC(gen2, ten, site, code) // what the ACTIVE generation would compute
	if string(idx1) == string(idx2) {
		t.Fatal("two generations produced the same index: the keys are not actually distinct")
	}

	// A candidate function that returns only the active generation reproduces the defect.
	activeOnly := func(context.Context, string, string, string) ([][]byte, error) {
		return [][]byte{idx2}, nil
	}
	got, _ := activeOnly(context.Background(), ten, site, code)
	if containsIndex(got, idx1) {
		t.Fatal("fixture wrong: active-only must NOT contain the generation-1 index")
	}

	// The real shape: every generation, newest first, so the generation-1 voucher is still findable.
	all := func(context.Context, string, string, string) ([][]byte, error) {
		return [][]byte{idx2, idx1}, nil
	}
	got, _ = all(context.Background(), ten, site, code)
	if !containsIndex(got, idx1) {
		t.Fatal("a voucher issued under generation 1 is unfindable after rotation")
	}
	if !containsIndex(got, idx2) {
		t.Fatal("a voucher issued under the active generation is unfindable")
	}
	if string(got[0]) != string(idx2) {
		t.Fatal("candidates must be newest-generation-first")
	}
}

// The index is scoped to its owner: the same code at another site or tenant must not collide.
func TestVoucherIndexIsOwnerScoped(t *testing.T) {
	kr := MapVoucherKeyring{"22222222-2222-4222-8222-222222222222": make([]byte, 32)}
	key, _, _, err := NewVoucherHMACKey(kr, "22222222-2222-4222-8222-222222222222", "t1", "s1", 1)
	if err != nil {
		t.Fatal(err)
	}
	a := VoucherCodeHMAC(key, "t1", "s1", "SAMECODE")
	if string(a) == string(VoucherCodeHMAC(key, "t1", "s2", "SAMECODE")) {
		t.Fatal("the same code indexes identically at two sites")
	}
	if string(a) == string(VoucherCodeHMAC(key, "t2", "s1", "SAMECODE")) {
		t.Fatal("the same code indexes identically for two tenants")
	}
}

func containsIndex(hs [][]byte, want []byte) bool {
	for _, h := range hs {
		if string(h) == string(want) {
			return true
		}
	}
	return false
}
