package main

// WHAT THESE PIN, AND WHY THEY ARE HERE RATHER THAN IN AN INTEGRATION SUITE.
//
// The database owns the invariants that matter most -- the append-only trigger, the CHECK constraints, the
// revoke kernel -- and they are proved against real DDL on a disposable PostgreSQL. What is left for a unit
// test is the part written in Go, and in this surface that part is almost entirely REFUSALS: a window that
// can never open, an actor the caller forgot to resolve, a reason too short to be a reason.
//
// The refusals are the interesting half. A voucher surface that accepts a bad validity window does not fail
// -- it prints two hundred cards that a guest discovers are dead, and the failure surfaces as "that code is
// not valid", which is indistinguishable from a wrong code.

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestARedemptionWindowThatCanNeverOpenIsRefused(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	soon := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	later := time.Now().Add(96 * time.Hour).UTC().Format(time.RFC3339)

	cases := []struct {
		name, from, until, want string
	}{
		{"until already past", "", past, "could never be redeemed"},
		{"until before from", later, soon, "must be after valid_from"},
		{"until equal to from", soon, soon, "must be after valid_from"},
		{"from is not a timestamp", "next tuesday", soon, "RFC3339"},
		{"until is not a timestamp", "", "soon-ish", "RFC3339"},
	}
	for _, c := range cases {
		_, _, err := parseRedemptionWindow(c.from, c.until)
		if err == nil {
			t.Errorf("%s: accepted, and a card printed from it could never be redeemed", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused with %q, which does not say %q", c.name, err.Error(), c.want)
		}
	}
}

// NO WINDOW IS THE NORMAL CASE and must stay legal: NULL on both columns means unbounded, which is what
// every voucher this system could previously issue got, and what a hotel printing evergreen cards wants.
func TestNoWindowIsTwoNullsRatherThanAnError(t *testing.T) {
	from, until, err := parseRedemptionWindow("", "")
	if err != nil {
		t.Fatalf("an absent window must be legal: %v", err)
	}
	if from != nil || until != nil {
		t.Errorf("an absent window must be NULL in both columns, got from=%v until=%v", from, until)
	}
}

func TestAWindowThatOpensLaterIsAccepted(t *testing.T) {
	from := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	until := time.Now().Add(72 * time.Hour).UTC().Format(time.RFC3339)
	gotFrom, gotUntil, err := parseRedemptionWindow(from, until)
	if err != nil {
		t.Fatalf("a future window is legal -- cards printed today for a conference next week: %v", err)
	}
	if gotFrom == nil || gotUntil == nil {
		t.Error("both bounds should have been returned")
	}
}

// THE ACTOR IS NOT OPTIONAL. Every mutating route here receives the operator edged resolved from the
// SESSION. If that could be defaulted, an audit row would name whoever the default was.
func TestTheActorAndTheReasonAreRefusedWhenMissing(t *testing.T) {
	cases := []struct {
		name       string
		a          voucherActor
		needReason bool
		wantErr    bool
	}{
		{"no operator at all", voucherActor{Reason: "a good reason"}, true, true},
		{"operator id but no label", voucherActor{OperatorID: "op-1", Reason: "a good reason"}, true, true},
		{"label but no id", voucherActor{OperatorLabel: "desk@hotel", Reason: "a good reason"}, true, true},
		{"whitespace operator", voucherActor{OperatorID: "  ", OperatorLabel: " ", Reason: "a good reason"}, true, true},
		{"reason of three characters", voucherActor{OperatorID: "op-1", OperatorLabel: "desk", Reason: "abc"}, true, true},
		{"reason that is only spaces", voucherActor{OperatorID: "op-1", OperatorLabel: "desk", Reason: "        "}, true, true},
		{"complete", voucherActor{OperatorID: "op-1", OperatorLabel: "desk", Reason: "guest lost the card"}, true, false},
		{"no reason needed", voucherActor{OperatorID: "op-1", OperatorLabel: "desk"}, false, false},
	}
	for _, c := range cases {
		err := c.a.validate(c.needReason)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err=%v, wanted error=%v", c.name, err, c.wantErr)
		}
	}
}

// A reason of 501 characters is refused at the top end too: an unbounded free-text field on an append-only
// table is somewhere to put something that is not a reason.
func TestAnUnboundedReasonIsRefused(t *testing.T) {
	a := voucherActor{OperatorID: "op-1", OperatorLabel: "desk", Reason: strings.Repeat("x", 501)}
	if err := a.validate(true); err == nil {
		t.Error("a 501-character reason was accepted")
	}
	a.Reason = strings.Repeat("x", 500)
	if err := a.validate(true); err != nil {
		t.Errorf("500 characters is the documented limit and must be accepted: %v", err)
	}
}

// THE AUDIT TABLE MUST NEVER GAIN A COLUMN THAT COULD HOLD A CODE.
//
// iam_v2.voucher_code_reveals records that a code was read, never the code. It is append-only and nothing
// prunes it, so a code column would be a second, permanent copy of every guest secret anybody ever looked
// at -- in the one table designed to be impossible to clean up.
//
// This reads the migration rather than the database so that it runs in the ordinary test suite, with no
// container: the cost of catching this late is a schema that has already shipped.
func TestTheRevealAuditHasNoColumnThatCouldHoldACode(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0087_the_operator_surface_a_voucher_needs.up.sql")
	if err != nil {
		t.Fatalf("0086 is unreadable: %v", err)
	}
	body, ok := tableBody(string(raw), "iam_v2.voucher_code_reveals")
	if !ok {
		t.Fatal("0086 no longer creates iam_v2.voucher_code_reveals")
	}
	for _, banned := range []string{"code", "cipher", "plain", "secret", "last4"} {
		for _, line := range strings.Split(body, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "--") {
				continue
			}
			// The column name is the first token of a definition line. `voucher_count` and the CHECK
			// clauses mention other things; only the declared name matters.
			name := strings.ToLower(strings.SplitN(trimmed, " ", 2)[0])
			if strings.Contains(name, banned) {
				t.Errorf("iam_v2.voucher_code_reveals declares %q, which could hold a code or part of one; "+
					"this table records THAT a code was read, never the code", name)
			}
		}
	}
	// ...and the append-only trigger must still be created, because without it the table is merely a log.
	if !strings.Contains(string(raw), "CREATE TRIGGER voucher_code_reveals_append_only") {
		t.Error("the append-only trigger is gone: the reveal record could then be edited or deleted by " +
			"whoever wanted it to say something else")
	}
}

// tableBody returns the text between CREATE TABLE <name> ( and its closing );
func tableBody(sql, name string) (string, bool) {
	i := strings.Index(sql, "CREATE TABLE "+name)
	if i < 0 {
		return "", false
	}
	open := strings.Index(sql[i:], "(")
	if open < 0 {
		return "", false
	}
	rest := sql[i+open+1:]
	end := strings.Index(rest, "\n);")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
