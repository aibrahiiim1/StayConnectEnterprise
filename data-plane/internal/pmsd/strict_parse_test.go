package pmsd

import (
	"errors"
	"testing"
)

// TestStrictParse_Grammar exercises the strict FIAS record grammar: the normal trailing pipe is accepted,
// while internal empty tokens, one-character tokens, malformed field ids and control bytes are rejected
// (typed ErrRecordMalformed, never silent skipping). Valid unknown/duplicate/reordered fields are preserved.
func TestStrictParse_Grammar(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantErr bool
		wantN   int // expected field count when valid
	}{
		{"valid trailing pipe", "GI|RN1408|G#12345|", false, 2},
		{"valid present-but-empty field", "GI|RN1408|GN|G#1|", false, 3},
		{"control frame no fields", "LA|", false, 0},
		{"internal empty token ||", "GI|RN1408||G#12345|", true, 0},
		{"leading empty token", "GI||RN1408|", true, 0},
		{"one-character token", "GI|R|G#12345|", true, 0},
		{"missing trailing pipe", "GI|RN1408|G#12345", true, 0},
		{"double trailing pipe", "GI|RN1408|G#12345||", true, 0},
		{"malformed field id (space in code)", "GI|R N1408|G#1|", true, 0},
		{"unsupported record id", "ZZ|RN1408|", true, 0},
		{"record id only ZZ", "ZZ", true, 0},
		{"unknown valid field preserved", "GI|RN1408|G#1|ZQhello|", false, 3},
		{"duplicate unknown field preserved", "GI|RN1408|G#1|ZQa|ZQb|", false, 4},
		{"reordered fields", "GI|G#12345|RN1408|", false, 2},
	}
	for _, tc := range cases {
		pr, err := parseStrictRecord(tc.body)
		if tc.wantErr {
			if !errors.Is(err, ErrRecordMalformed) {
				t.Errorf("%s: want ErrRecordMalformed, got %v", tc.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
			continue
		}
		if len(pr.Fields) != tc.wantN {
			t.Errorf("%s: field count = %d, want %d (%+v)", tc.name, len(pr.Fields), tc.wantN, pr.Fields)
		}
	}
}

// TestStrictParse_ControlBytesRejected proves a control byte in a value is rejected (it belongs only inside
// the raw frame, never a parsed record).
func TestStrictParse_ControlBytesRejected(t *testing.T) {
	if _, err := parseStrictRecord("GI|RN14\x0208|G#1|"); !errors.Is(err, ErrRecordMalformed) {
		t.Errorf("control byte in value must be rejected, got %v", err)
	}
}

// TestStrictParse_PreservesOrderDuplicatesEmpty proves the parser keeps source order, duplicate occurrences
// and present-but-empty values (the fingerprint depends on all three).
func TestStrictParse_PreservesOrderDuplicatesEmpty(t *testing.T) {
	pr, err := parseStrictRecord("GI|RN1408|ZQa|ZQ|ZQb|G#1|")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []FieldPair{{"RN", "1408"}, {"ZQ", "a"}, {"ZQ", ""}, {"ZQ", "b"}, {"G#", "1"}}
	if len(pr.Fields) != len(want) {
		t.Fatalf("got %d fields, want %d: %+v", len(pr.Fields), len(want), pr.Fields)
	}
	for i, w := range want {
		if pr.Fields[i] != w {
			t.Errorf("field[%d] = %+v, want %+v", i, pr.Fields[i], w)
		}
	}
}

// TestStrictParse_ExactRetransmissionSameFingerprint proves that a byte-identical record retransmission
// yields the same complete-record fingerprint (idempotent), while a reordering yields a different one.
func TestStrictParse_ExactRetransmissionSameFingerprint(t *testing.T) {
	a := testAdapter()
	base := "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|"
	e1 := mustEvent(t, a, base)
	e2 := mustEvent(t, a, base)
	if e1.SourceEventFingerprint != e2.SourceEventFingerprint {
		t.Error("exact retransmission must reproduce the same fingerprint")
	}
	reordered := "GI|G#12345|RN1408|GNSmith|GFJohn|GA260101|GD260105|"
	if mustEvent(t, a, reordered).SourceEventFingerprint == e1.SourceEventFingerprint {
		t.Error("a reordered record must produce a different fingerprint")
	}
}

// TestStrictParse_MalformedProducesNoEvent proves a malformed record yields NO Event from the adapter (the
// caller turns the typed parse error into a continuity fault + resync; nothing reaches the queue).
func TestStrictParse_MalformedProducesNoEvent(t *testing.T) {
	a := testAdapter()
	for _, bad := range []string{
		"GI|RN1408||G#12345|", // internal empty
		"GI|R|G#12345|",       // one-char token
		"GI|RN1408|G#12345",   // missing trailing pipe
		"GI|R N1408|G#12345|", // malformed code
		"GI|RN14\x0208|G#1|",  // control byte
	} {
		if ev, err := a.toEvent(bad); err == nil {
			t.Errorf("malformed %q must produce no Event, got %+v", bad, ev)
		}
	}
}

// TestDuplicateTypedFields_FailClosed proves §2: an ambiguous duplicate of any IDENTITY or typed-evidence
// field (RN/G#/GN/GF/FO/GA/GD) fails closed — the adapter yields NO Event (first-wins/last-wins is never
// used), so a continuity fault is driven and zero events are admitted. The complete-record fingerprint still
// preserves the duplicate occurrences (verified by the parser tests); only the TYPED projection rejects.
func TestDuplicateTypedFields_FailClosed(t *testing.T) {
	a := testAdapter()
	cases := map[string]string{
		"duplicate RN identical": "GI|RN1408|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|",
		"duplicate RN different": "GI|RN1408|RN1500|G#12345|GNSmith|GFJohn|GA260101|GD260105|",
		"duplicate G#":           "GI|RN1408|G#12345|G#99999|GNSmith|GFJohn|GA260101|GD260105|",
		"duplicate GN":           "GI|RN1408|G#12345|GNSmith|GNJones|GFJohn|GA260101|GD260105|",
		"duplicate GF":           "GI|RN1408|G#12345|GNSmith|GFJohn|GFJane|GA260101|GD260105|",
		"duplicate FO":           "GI|RN1408|G#12345|GNSmith|GFJohn|FOF900|FOF901|GA260101|GD260105|",
		"duplicate GA":           "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GA260202|GD260105|",
		"duplicate GD":           "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|GD260106|",
	}
	for name, rec := range cases {
		// the raw record is well-formed at the grammar level (duplicates are legal tokens)...
		if _, perr := parseStrictRecord(rec); perr != nil {
			t.Errorf("%s: grammar should accept duplicate tokens, got %v", name, perr)
		}
		// ...but the typed projection must fail closed and produce NO Event.
		if ev, err := a.toEvent(rec); err == nil {
			t.Errorf("%s: ambiguous duplicate typed field must produce no Event, got %+v", name, ev)
		}
	}
}

// TestDuplicateUnknownField_Allowed proves an unknown well-formed field may repeat freely (it stays
// fingerprint-only and never blocks a valid domain record).
func TestDuplicateUnknownField_Allowed(t *testing.T) {
	a := testAdapter()
	rec := "GI|RN1408|G#12345|GNSmith|GFJohn|GA260101|GD260105|ZQx|ZQy|"
	ev, err := a.toEvent(rec)
	if err != nil {
		t.Fatalf("duplicate UNKNOWN field must be allowed, got %v", err)
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("record with duplicate unknown field must validate: %v", err)
	}
}
