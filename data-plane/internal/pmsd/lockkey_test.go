package pmsd

import "testing"

// TestLockKey_FixedVectors pins the SHA-256 advisory-lock derivation to exact numeric outputs so an
// accidental change to the namespace, byte order, or hashing cannot silently move every interface to a
// different lock (which would break single-owner mutual exclusion across a rolling deploy).
func TestLockKey_FixedVectors(t *testing.T) {
	cases := []struct {
		t, s, i string
		want    int64
	}{
		{"00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000", 2907869803971324694},
		{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333", -2988407477788400983},
		{"0a8d2f3e-1b4c-4d5e-8f60-71829a3b4c5d", "fedcba98-7654-3210-fedc-ba9876543210", "deadbeef-dead-beef-dead-beefdeadbeef", 2377297775024380786},
	}
	for _, c := range cases {
		got, err := LockKey(c.t, c.s, c.i)
		if err != nil {
			t.Fatalf("LockKey(%s,%s,%s) unexpected error: %v", c.t, c.s, c.i, err)
		}
		if got != c.want {
			t.Errorf("LockKey(%s,%s,%s) = %d, want %d", c.t, c.s, c.i, got, c.want)
		}
	}
}

func TestLockKey_Deterministic(t *testing.T) {
	a, err1 := LockKey("0a8d2f3e-1b4c-4d5e-8f60-71829a3b4c5d", "fedcba98-7654-3210-fedc-ba9876543210", "deadbeef-dead-beef-dead-beefdeadbeef")
	b, err2 := LockKey("0a8d2f3e-1b4c-4d5e-8f60-71829a3b4c5d", "fedcba98-7654-3210-fedc-ba9876543210", "deadbeef-dead-beef-dead-beefdeadbeef")
	if err1 != nil || err2 != nil {
		t.Fatalf("unexpected error: %v %v", err1, err2)
	}
	if a != b {
		t.Errorf("non-deterministic: %d != %d", a, b)
	}
}

// TestLockKey_UniquePerComponent proves each identity component affects the key (no collisions from
// swapping tenant/site/interface).
func TestLockKey_UniquePerComponent(t *testing.T) {
	base, _ := LockKey("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "33333333-3333-3333-3333-333333333333")
	swapped, _ := LockKey("22222222-2222-2222-2222-222222222222", "11111111-1111-1111-1111-111111111111", "33333333-3333-3333-3333-333333333333")
	diffIface, _ := LockKey("11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222", "44444444-4444-4444-4444-444444444444")
	if base == swapped {
		t.Error("tenant/site swap produced same key")
	}
	if base == diffIface {
		t.Error("different interface produced same key")
	}
}

func TestLockKey_RejectsMalformedUUID(t *testing.T) {
	good := "11111111-1111-1111-1111-111111111111"
	bad := []string{
		"", "not-a-uuid", "111111111111111111111111111111111111", // 36 chars, no dashes
		"11111111_1111-1111-1111-111111111111",  // wrong separator
		"1111111-11111-1111-1111-111111111111",  // misplaced dashes
		"gggggggg-1111-1111-1111-111111111111",  // non-hex
		"11111111-1111-1111-1111-11111111111",   // too short
		"11111111-1111-1111-1111-1111111111110", // too long
	}
	for _, b := range bad {
		if _, err := LockKey(b, good, good); err == nil {
			t.Errorf("expected error for malformed tenant %q", b)
		}
		if _, err := LockKey(good, b, good); err == nil {
			t.Errorf("expected error for malformed site %q", b)
		}
		if _, err := LockKey(good, good, b); err == nil {
			t.Errorf("expected error for malformed interface %q", b)
		}
	}
}
