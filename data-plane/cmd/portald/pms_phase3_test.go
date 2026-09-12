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
		class   string
	}{
		// EVERY ONE OF THESE CAN DESCRIBE ONE ROOM, and they must therefore be byte-identical. "No such
		// room", "the name did not match", "two candidates", "that stay is not eligible" and an unknown code
		// all answer the same way, because anything else lets someone in the lobby submit room numbers and
		// read off which are occupied, which have departed, and which are shared.
		{outcomeNoMatch, "NO_INTERFACE_MATCH", classCredential},
		{outcomeAmbiguous, "DISCRIMINATOR_REQUIRED", classCredential},
		{outcomeNoMatch, "ROOM_NOT_IN_MIRROR", classCredential},
		{outcomeNoMatch, "STAY_NOT_ELIGIBLE", classCredential},
		{"SOMETHING_NEW", "UNKNOWN_TO_THIS_BUILD", ""},        // an absent class must fall to this same answer
		{outcomeVerified, "SINGLE_VERIFIED", classCredential}, // VERIFIED with no session is still a failure
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

// THE OTHER HALF OF THE CONTRACT: the answers that DO differ, and why they are allowed to.
//
// A guest may learn which of three things to do — re-read what they typed, try again later, or wait out a
// rate limit. Each of those is either about their own submission or true for every guest on the property at
// that moment, so none of them identifies a room. This test pins the set at three so a fourth, finer message
// cannot be added without someone deciding here that it discloses nothing.
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

	// The credential message must tell the guest what is accepted. A guest whose given name is stored as
	// several words in one field has to be told to enter the FULL name, or they will keep typing one word.
	cred := messageForClass(classCredential, 0)
	for _, want := range []string{"room number", "full first name", "family name", "reservation number"} {
		if !containsFold(cred, want) {
			t.Errorf("the incorrect-details message does not mention %q: %q", want, cred)
		}
	}
	// ...and it must NOT send them to Reception. That is the technical message's job, and conflating the two
	// is the specific behaviour this change removes.
	if containsFold(cred, "contact reception") {
		t.Errorf("an ordinary wrong value tells the guest to contact Reception: %q", cred)
	}
	if !containsFold(messageForClass(classTechnical, 0), "contact reception") {
		t.Errorf("the technical message does not offer Reception: %q", messageForClass(classTechnical, 0))
	}
}

// An unknown class must not become the technical message. Defaulting that way would let anything unexpected
// tell a guest with a plain typo to go and queue at the desk, and would give an attacker a way to make every
// answer look like a system fault.
func TestAnUnknownClassFallsToTheCredentialMessage(t *testing.T) {
	for _, class := range []string{"", "SOMETHING_NEW", "verified", "credential"} {
		if got := messageForClass(class, 0); got != guestAuthMessage {
			t.Errorf("class %q produced %q, want the incorrect-details message", class, got)
		}
	}
}

// The post-stay PIN flow keeps the wording it always had. Its guests have no room number, family name or
// reservation number to re-read, so the room sign-in sentence would be advice they cannot act on.
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

// A successful verification legitimately returns the guest's own session — and nothing about the resolution.
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
