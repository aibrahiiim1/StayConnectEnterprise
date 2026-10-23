package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// Every per-room non-success must produce a byte-identical body, so the portal is no oracle for rooms,
// guests or the PMS in use.
func TestEveryFailureIsByteIdentical(t *testing.T) {
	outcomes := []struct {
		outcome pmsOutcome
		reason  string
		class   string
	}{
		// Each can describe one room, so all must be byte-identical.
		{outcomeNoMatch, "NO_INTERFACE_MATCH", classCredential},
		{outcomeAmbiguous, "DISCRIMINATOR_REQUIRED", classCredential},
		{outcomeNoMatch, "ROOM_NOT_IN_MIRROR", classCredential},
		{outcomeNoMatch, "STAY_NOT_ELIGIBLE", classCredential},
		{"SOMETHING_NEW", "UNKNOWN_TO_THIS_BUILD", ""},        // absent class falls to this answer
		{outcomeVerified, "SINGLE_VERIFIED", classCredential}, // VERIFIED with no session is a failure
	}
	var first []byte
	for i, o := range outcomes {
		status, body, audit := buildGuestPMSResponse(o.outcome, o.reason, o.class, "", "", 0)
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

// The answers that may differ: re-read, try later, wait out a rate limit. Each concerns the guest's own
// submission or the whole property, so none identifies a room. Pinned at three so a finer message needs a
// deliberate decision here.
func TestTheGuestLearnsWhatToDoAndNothingMore(t *testing.T) {
	seen := map[string]string{}
	for _, class := range []string{classCredential, classTechnical, "RATE_LIMITED"} {
		_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "internal", class, "", "", 45)
		if body.Message == "" {
			t.Fatalf("class %s produced no message", class)
		}
		if term := bodyMentionsForbiddenTerm(body); term != "" {
			t.Fatalf("class %s mentions %q", class, term)
		}
		if prev, dup := seen[body.Message]; dup {
			t.Fatalf("classes %s and %s share a message, so the distinction the Product Owner asked for "+
				"does not exist", prev, class)
		}
		seen[body.Message] = class
	}

	// A multi-word given name must be entered in full.
	cred := messageForClass(classCredential, 0)
	for _, want := range []string{"room number", "full first name", "family name", "reservation number"} {
		if !containsFold(cred, want) {
			t.Errorf("the incorrect-details message does not mention %q: %q", want, cred)
		}
	}
	// Sending a mistyped value to the site team is the technical message's job only.
	if containsFold(cred, "contact the site team") {
		t.Errorf("an ordinary wrong value tells the guest to contact the site team: %q", cred)
	}
	if !containsFold(messageForClass(classTechnical, 0), "contact the site team") {
		t.Errorf("the technical message does not offer the site team: %q", messageForClass(classTechnical, 0))
	}
}

// Defaulting to the technical message would let an attacker make every answer look like a fault.
func TestAnUnknownClassFallsToTheCredentialMessage(t *testing.T) {
	for _, class := range []string{"", "SOMETHING_NEW", "verified", "credential"} {
		if got := messageForClass(class, 0); got != guestAuthMessage {
			t.Errorf("class %q produced %q, want the incorrect-details message", class, got)
		}
	}
}

// Post-stay guests have no room number or name to re-read.
func TestPostStayKeepsItsOwnWording(t *testing.T) {
	_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "poststay_not_verified", classPostStay, "", "", 0)
	if body.Message != guestPostStayMessage {
		t.Fatalf("the post-stay message changed to %q", body.Message)
	}
	if body.Message == guestAuthMessage {
		t.Fatal("post-stay now tells a PIN guest to check their room number")
	}
	if leaksDetail(body) {
		t.Fatal("the post-stay failure body leaks detail")
	}
}

func TestSuccessReturnsOnlyTheGuestsOwnSession(t *testing.T) {
	status, body, audit := buildGuestPMSResponse(outcomeVerified, "SINGLE_VERIFIED", "", "sess-42", "/success", 0)
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
			continue
		}
		if containsFold(string(raw), term) {
			t.Fatalf("success body mentions %q: %s", term, raw)
		}
	}
}

func TestWireResponseIsUncacheableAndUniform(t *testing.T) {
	rec := httptest.NewRecorder()
	_, body, _ := buildGuestPMSResponse(outcomeNoMatch, "NO_INTERFACE_MATCH", classCredential, "", "", 0)
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
