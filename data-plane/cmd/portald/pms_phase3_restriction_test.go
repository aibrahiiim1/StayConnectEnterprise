package main

// A restricted guest is told one approved sentence carrying the server's remaining seconds.

import (
	"encoding/json"
	"strings"
	"testing"
)

// Written out literally, not built from the production constant, so rewriting both is noticed.
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

// The restriction is about the rate of attempts, so the sentence is identical whoever is behind it.
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

// A retry hint on any other answer tells a guesser how long to wait between guesses, even if a server sent one.
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

// When the server quotes no time, no number is invented.
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

// The appliance holds the expiry; portald never computes, rounds, extends or shortens it.
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

// The page's countdown is a convenience; portald only reports what scd last said.
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
