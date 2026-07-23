package authctx

import "testing"

// TestValidPinUUID proves pin/presenter validation happens in Go BEFORE any SQL: a malformed, whitespace-only,
// overlong, or nil UUID is rejected; only a canonical non-nil UUID passes.
func TestValidPinUUID(t *testing.T) {
	valid := []string{
		"11111111-1111-1111-1111-111111111111",
		"a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		"A0EEBC99-9C0B-4EF8-BB6D-6BB9BD380A11", // upper-case hex accepted
	}
	for _, s := range valid {
		if !validPinUUID(s) {
			t.Fatalf("validPinUUID(%q) = false, want true", s)
		}
	}
	invalid := []string{
		"",                                       // empty
		"   ",                                    // whitespace-only
		"not-a-uuid",                             // malformed
		"11111111-1111-1111-1111-111111111111 ",  // trailing space (overlong / not 36)
		"11111111-1111-1111-1111-1111111111111",  // overlong (37)
		"11111111-1111-1111-1111-11111111111",    // too short (35)
		"1111111111111-111-1111-111111111111",    // dashes misplaced
		"g1111111-1111-1111-1111-111111111111",   // non-hex
		"00000000-0000-0000-0000-000000000000",   // nil UUID is NOT a real identity pin
		"11111111_1111_1111_1111_111111111111",   // wrong separators
		"11111111-1111-1111-1111-111111111111\n", // trailing newline
	}
	for _, s := range invalid {
		if validPinUUID(s) {
			t.Fatalf("validPinUUID(%q) = true, want false", s)
		}
	}
}

// TestGrantValidRejectsMalformed proves a grant with any malformed/whitespace/overlong/nil id is invalid
// (so IssuePMS/IssuePMSTx return ErrGrantIncomplete before touching the DB), and a fully-valid grant passes.
func TestGrantValidRejectsMalformed(t *testing.T) {
	good := PMSGrant{
		Tenant: "11111111-1111-1111-1111-111111111111", Site: "22222222-2222-2222-2222-222222222222",
		Interface: "33333333-3333-3333-3333-333333333333", Revision: "44444444-4444-4444-4444-444444444444",
		Stay: "55555555-5555-5555-5555-555555555555", Device: "66666666-6666-6666-6666-666666666666",
		GuestNetwork: "77777777-7777-7777-7777-777777777777", TTLSeconds: 600,
	}
	if !good.valid() {
		t.Fatalf("fully-valid grant reported invalid")
	}
	for name, mut := range map[string]func(*PMSGrant){
		"malformed":  func(g *PMSGrant) { g.Stay = "not-a-uuid" },
		"whitespace": func(g *PMSGrant) { g.Site = "   " },
		"overlong":   func(g *PMSGrant) { g.Interface = good.Interface + "x" },
		"nil-uuid":   func(g *PMSGrant) { g.Device = "00000000-0000-0000-0000-000000000000" },
		"empty":      func(g *PMSGrant) { g.Revision = "" },
		"ttl-zero":   func(g *PMSGrant) { g.TTLSeconds = 0 },
		"ttl-huge":   func(g *PMSGrant) { g.TTLSeconds = maxTTLSeconds + 1 },
	} {
		m := good
		mut(&m)
		if m.valid() {
			t.Fatalf("grant with %s id reported valid", name)
		}
	}
}

// TestPresenterValidRejectsMalformed proves presenter validation is uniform (a malformed presenter is rejected
// before SQL, yielding ErrContextInvalid rather than a raw cast error).
func TestPresenterValidRejectsMalformed(t *testing.T) {
	good := Presenter{
		Tenant: "11111111-1111-1111-1111-111111111111", Site: "22222222-2222-2222-2222-222222222222",
		Device: "33333333-3333-3333-3333-333333333333", GuestNetwork: "44444444-4444-4444-4444-444444444444",
	}
	if !good.valid() {
		t.Fatalf("fully-valid presenter reported invalid")
	}
	for name, mut := range map[string]func(*Presenter){
		"malformed":  func(p *Presenter) { p.Device = "xyz" },
		"whitespace": func(p *Presenter) { p.Tenant = "  " },
		"overlong":   func(p *Presenter) { p.GuestNetwork = good.GuestNetwork + "0" },
		"nil-uuid":   func(p *Presenter) { p.Site = "00000000-0000-0000-0000-000000000000" },
		"empty":      func(p *Presenter) { p.Device = "" },
	} {
		m := good
		mut(&m)
		if m.valid() {
			t.Fatalf("presenter with %s id reported valid", name)
		}
	}
}
