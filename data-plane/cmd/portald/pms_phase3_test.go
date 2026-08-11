package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// The uniform-response contract, proven exhaustively rather than argued: EVERY non-success outcome must
// produce a byte-identical guest-facing body, so the portal cannot be used as an oracle to enumerate rooms,
// guests or which PMS a property runs.
func TestEveryFailureIsByteIdentical(t *testing.T) {
	outcomes := []struct {
		outcome pmsOutcome
		reason  string
	}{
		{outcomeNoMatch, "NO_INTERFACE_MATCH"},
		{outcomeAmbiguous, "DISCRIMINATOR_REQUIRED"},
		{outcomeIndeterminate, "INCOMPLETE_EVIDENCE"},
		{outcomeIndeterminate, "UNMAPPED"},
		{outcomeIndeterminate, "VERIFIED_WITHOUT_STAY"},
		{outcomeThrottled, "RATE_LIMITED"},
		{"SOMETHING_NEW", "UNKNOWN_TO_THIS_BUILD"},
		{outcomeVerified, "SINGLE_VERIFIED"}, // VERIFIED with no session is still a failure to the guest
	}
	var first []byte
	for i, o := range outcomes {
		status, body, audit := buildGuestPMSResponse(o.outcome, o.reason, "", "")
		if status != 200 {
			t.Fatalf("%s: status %d — a distinct status is itself a signal", o.outcome, status)
		}
		if body.OK {
			t.Fatalf("%s: reported success without a session", o.outcome)
		}
		if leaksDetail(body) {
			t.Fatalf("%s: guest body leaks detail: %+v", o.outcome, body)
		}
		if term := bodyMentionsForbiddenTerm(body); term != "" {
			t.Fatalf("%s: guest message mentions %q", o.outcome, term)
		}
		// the internal reason survives for audit/metrics — it just never reaches the guest
		if audit.ReasonCode != o.reason || audit.Outcome != o.outcome {
			t.Fatalf("%s: audit fields lost (%+v)", o.outcome, audit)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = raw
			continue
		}
		if string(raw) != string(first) {
			t.Fatalf("%s produced a DISTINGUISHABLE body:\n first: %s\n this:  %s", o.outcome, first, raw)
		}
	}
}

// A successful verification legitimately returns the guest's own session — and nothing about the resolution.
func TestSuccessReturnsOnlyTheGuestsOwnSession(t *testing.T) {
	status, body, audit := buildGuestPMSResponse(outcomeVerified, "SINGLE_VERIFIED", "sess-42", "/success")
	if status != 200 || !body.OK || body.SessionID != "sess-42" || body.RedirectTo != "/success" {
		t.Fatalf("unexpected success body: %d %+v", status, body)
	}
	if body.Message != "" {
		t.Fatalf("a success must not carry a failure message: %q", body.Message)
	}
	if audit.Outcome != outcomeVerified {
		t.Fatalf("audit outcome = %s", audit.Outcome)
	}
	raw, _ := json.Marshal(body)
	for _, term := range forbiddenGuestTerms {
		if term == "pms" {
			continue // the field names themselves never contain it; this guards message content
		}
		if containsFold(string(raw), term) {
			t.Fatalf("success body mentions %q: %s", term, raw)
		}
	}
}

// The wire response must not be cacheable and must not vary in shape.
func TestWireResponseIsUncacheableAndUniform(t *testing.T) {
	rec := httptest.NewRecorder()
	_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "NO_INTERFACE_MATCH", "", "")
	writeGuestPMSResponse(rec, 200, body)
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var decoded guestPMSResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.OK || decoded.Message != guestAuthMessage {
		t.Fatalf("wire body = %+v", decoded)
	}
}

func containsFold(hay, needle string) bool {
	h, n := []rune(hay), []rune(needle)
	if len(n) == 0 || len(n) > len(h) {
		return false
	}
	lower := func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return r
	}
	for i := 0; i+len(n) <= len(h); i++ {
		ok := true
		for j := range n {
			if lower(h[i+j]) != lower(n[j]) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
