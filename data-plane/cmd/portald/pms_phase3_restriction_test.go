package main

// WHAT A RESTRICTED GUEST IS TOLD.
//
// The Product Owner asked for one sentence, word for word, carrying the number of seconds still to wait, and
// for the countdown on the page to be driven by the SERVER's expiry rather than by anything the browser
// decided. These tests pin both halves of that: the sentence, and the fact that the number is the server's.

import (
	"encoding/json"
	"strings"
	"testing"
)

// The exact wording, as approved. It is written out here rather than built from the production constant on
// purpose — a test that composes the sentence the same way the code does would pass after somebody rewrote
// both, which is exactly the change that should have to be noticed.
const approvedRestrictionSentence = "Too many attempts. Please wait 42 seconds and try again."

func TestARestrictedGuestIsToldExactlyHowLongToWait(t *testing.T) {
	_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "RATE_LIMITED", "RATE_LIMITED", "", "", 42)

	if body.Message != approvedRestrictionSentence {
		t.Fatalf("message = %q, want %q", body.Message, approvedRestrictionSentence)
	}
	if body.RetryAfterSeconds != 42 {
		t.Fatalf("retry_after_seconds = %d, want 42 — the page counts down from the SERVER's number, so a "+
			"body that prints one wait and reports another would tick towards the wrong moment",
			body.RetryAfterSeconds)
	}
	if body.OK {
		t.Fatal("a restricted guest was reported as signed in")
	}
	if leaksDetail(body) {
		t.Fatalf("the restriction body leaks detail: %+v", body)
	}
	if term := bodyMentionsForbiddenTerm(body); term != "" {
		t.Fatalf("the restriction sentence mentions %q", term)
	}
}

// The sentence must be identical whoever is behind it. A guest who mistyped their own surname five times and
// somebody working through room numbers are told the same thing, because the restriction is about the rate
// of attempts and not about whether any of them were close.
func TestTheRestrictionSentenceSaysNothingAboutTheProperty(t *testing.T) {
	_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "RATE_LIMITED", "RATE_LIMITED", "", "", 60)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"room", "412", "device", "mac", "network", "attempt count", "threshold"} {
		if strings.Contains(strings.ToLower(string(raw)), forbidden) {
			t.Fatalf("the restriction body mentions %q: %s", forbidden, raw)
		}
	}
}

// A RETRY HINT ON ANY OTHER ANSWER IS AN ORACLE. Telling a guesser how long to wait between guesses is the
// one thing the control exists to take away from them, so the number is attached to the restricted answer
// and refused everywhere else — even if a server somewhere sent one.
func TestOnlyTheRestrictedAnswerCarriesAWait(t *testing.T) {
	for _, class := range []string{classCredential, classTechnical, classPostStay, "", "SOMETHING_NEW"} {
		_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "internal", class, "", "", 42)
		if body.RetryAfterSeconds != 0 {
			t.Errorf("class %q carried a retry hint of %d", class, body.RetryAfterSeconds)
		}
		if strings.Contains(body.Message, "42") {
			t.Errorf("class %q printed a wait in its message: %q", class, body.Message)
		}
	}
}

// WHEN THE SERVER QUOTES NO TIME, THE PAGE INVENTS NONE. A number this daemon made up would be the one thing
// on the page the appliance is not actually standing behind, and a guest who waited it out and was refused
// again would have been misled by us rather than by the control.
func TestNoInventedWait(t *testing.T) {
	_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "RATE_LIMITED", "RATE_LIMITED", "", "", 0)
	if body.Message != guestAuthRateLimitedUnknownMessage {
		t.Fatalf("message = %q, want the no-number sentence", body.Message)
	}
	if strings.ContainsAny(body.Message, "0123456789") {
		t.Fatalf("a wait nobody quoted was printed anyway: %q", body.Message)
	}
	if leaksDetail(body) {
		t.Fatalf("the no-number restriction body leaks detail: %+v", body)
	}
}

// scd's number crosses portald UNTOUCHED. portald does not compute, round, extend or shorten a restriction —
// the appliance holds the expiry, and a second opinion here would be a second thing to keep correct.
func TestPortaldForwardsTheServersWaitUntouched(t *testing.T) {
	h := stubHandler(t, &scdStub{resolve: map[string]any{
		"outcome": "NOT_VERIFIED", "failure_class": "RATE_LIMITED", "retry_after_seconds": 17}})
	_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "X", "request_id": "r"})

	if out.RetryAfterSeconds != 17 {
		t.Fatalf("retry_after_seconds = %d, want 17 verbatim from scd", out.RetryAfterSeconds)
	}
	if out.Message != "Too many attempts. Please wait 17 seconds and try again." {
		t.Fatalf("message = %q", out.Message)
	}
}

// The page's countdown is a convenience; the refusal is the server's. This is the contract that keeps that
// true: portald never decides a wait is over, it only reports what scd last said.
func TestPortaldNeverShortensAWait(t *testing.T) {
	h := stubHandler(t, &scdStub{resolve: map[string]any{
		"outcome": "NOT_VERIFIED", "failure_class": "RATE_LIMITED", "retry_after_seconds": 60}})
	for i := 0; i < 3; i++ {
		_, out := phase3Post(t, h, map[string]any{"room": "412", "last_name": "X", "request_id": "r"})
		if out.RetryAfterSeconds != 60 {
			t.Fatalf("submission %d: portald reported %d seconds, but scd still says 60 — a page that "+
				"counts down on its own would let a guest through before the appliance would",
				i+1, out.RetryAfterSeconds)
		}
	}
}
