package main

// A refused package must not stay on screen looking valid: the offer set is taken down, not re-armed.
// These assert the page template directly because there is no server round trip to observe.

import (
	"strings"
	"testing"
)

func phase3Script(t *testing.T) string {
	t.Helper()
	s := landingHTML
	if !strings.Contains(s, "renderPhase3Choices") {
		t.Fatalf("the portal template no longer contains the package chooser; these assertions are stale")
	}
	return s
}

func TestFailedSelectionTakesTheChoicesDown(t *testing.T) {
	s := phase3Script(t)

	if !strings.Contains(s, "function resetPhase3ToSignIn") {
		t.Fatal("there is no path back to sign-in after a failed selection")
	}
	reset := s[strings.Index(s, "function resetPhase3ToSignIn"):]
	reset = reset[:strings.Index(reset, "function renderPhase3Choices")]
	for _, want := range []string{"box.innerHTML = ''", "box.style.display = 'none'", "form.style.display = ''"} {
		if !strings.Contains(reset, want) {
			t.Errorf("the reset does not %q, so a stale choice can survive it", want)
		}
	}

	// The click handler must not re-enable the buttons it just disabled (room chooser only; post-stay has its own).
	room := s[strings.Index(s, "function renderPhase3Choices"):]
	click := room[strings.Index(room, "b.addEventListener('click'"):]
	click = click[:strings.Index(click, "box.appendChild(b);")]
	if strings.Contains(click, "x.disabled = false") {
		t.Error("a failed selection re-enables the same buttons; that is the defect this replaced")
	}
	if !strings.Contains(click, "resetPhase3ToSignIn(errEl)") {
		t.Error("a failed selection does not return the guest to sign-in")
	}
}

func TestTheResetSaysNothingNewToTheGuest(t *testing.T) {
	s := phase3Script(t)
	reset := s[strings.Index(s, "function resetPhase3ToSignIn"):]
	reset = reset[:strings.Index(reset, "function renderPhase3Choices")]

	// A second distinct message would be a distinguishable outcome.
	if !strings.Contains(reset, "PHASE3_FAIL") {
		t.Error("the reset does not use the uniform failure message")
	}
	for _, leak := range []string{"context", "expired", "offer", "grant", "quota", "lock", "permission", "stay is"} {
		if strings.Contains(strings.ToLower(reset), leak) &&
			!strings.Contains(strings.ToLower(reset), "// ") {
			t.Errorf("the reset mentions %q to the guest", leak)
		}
	}
	body := guestPMSResponse{OK: false, Message: guestAuthMessage}
	if leaksDetail(body) {
		t.Error("the uniform failure body leaks detail")
	}
	if term := bodyMentionsForbiddenTerm(body); term != "" {
		t.Errorf("the uniform message mentions %q", term)
	}
}

// A page that keeps no request id between submissions cannot replay a recorded resolution for a corrected
// field; the server replays by request id.
func TestEveryDeliberateSubmissionMintsItsOwnRequestID(t *testing.T) {
	s := phase3Script(t)

	for _, gone := range []string{"PMS_REQUEST_ID", "PMS_ATTEMPT_KEY", "phase3RequestID"} {
		if strings.Contains(s, gone) {
			t.Errorf("%s is back: a request id that outlives its submission can freeze a corrected one", gone)
		}
	}

	submit := s[strings.Index(s, "document.getElementById('form-pms').addEventListener('submit'"):]
	if !strings.Contains(submit, "body.request_id = newRequestID();") {
		t.Error("the sign-in submission does not mint a fresh request id")
	}

	// The UUID shape is asserted in the browser test; this only proves the generator is called.
	if !strings.Contains(s, "function newRequestID()") {
		t.Fatal("newRequestID is gone, so the submission cannot be minting a canonical UUID")
	}
}
