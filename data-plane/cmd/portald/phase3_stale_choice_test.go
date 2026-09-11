package main

// A REFUSED PACKAGE MUST NOT STAY ON SCREEN LOOKING VALID.
//
// On 2026-09-07 a real guest reached the package chooser, pressed a package, and the grant was refused. The
// three buttons were then RE-ENABLED and left exactly where they were, so they read as still valid. The guest
// pressed again. And again -- three attempts in fourteen seconds, all refused identically, because nothing on
// screen indicated that the offer set was no longer usable.
//
// These assert the template's behaviour directly, because the behaviour IS the template: there is no server
// round trip to observe. The guest-facing message is unchanged and still uniform; what changes is that the
// offer set is taken down instead of re-armed.

import (
	"strings"
	"testing"
)

// phase3Script returns the portal template text the assertions below read.
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

	// The reset path exists and does the three things that matter: empties the offer set, hides it, and puts
	// the sign-in form back.
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

	// THE REGRESSION ITSELF: the click handler must not re-enable the buttons it just disabled.
	click := s[strings.Index(s, "b.addEventListener('click'"):]
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

	// It must use the ONE uniform message, not a message of its own -- a second distinct string is a second
	// distinguishable outcome, which is precisely what the uniform envelope exists to prevent.
	if !strings.Contains(reset, "PHASE3_FAIL") {
		t.Error("the reset does not use the uniform failure message")
	}
	for _, leak := range []string{"context", "expired", "offer", "grant", "quota", "lock", "permission", "stay is"} {
		if strings.Contains(strings.ToLower(reset), leak) &&
			!strings.Contains(strings.ToLower(reset), "// ") {
			t.Errorf("the reset mentions %q to the guest", leak)
		}
	}
	// ...and the uniform message itself still discloses nothing.
	body := guestPMSResponse{OK: false, Message: guestAuthMessage}
	if leaksDetail(body) {
		t.Error("the uniform failure body leaks detail")
	}
	if term := bodyMentionsForbiddenTerm(body); term != "" {
		t.Errorf("the uniform message mentions %q", term)
	}
}

// THE REPLAY DEFECT, ASSERTED AT ITS SOURCE.
//
// The portal used to carry a request id across submissions and re-use it whenever a derived "attempt key"
// looked unchanged. The key was built from last_name / first_name / reservation_number, and room_any — the
// combined mode a property actually runs — puts the typed value in 'verification', which the key never read.
// So every tap on one page sent the SAME id, the server replayed the resolution recorded under it, and a
// corrected surname was answered by the guest's own typo until the page was reloaded.
//
// These assertions are about STATE, not about that one forgotten field. A page that keeps no request id
// between submissions cannot freeze on any field, including one added later.
func TestEveryDeliberateSubmissionMintsItsOwnRequestID(t *testing.T) {
	s := phase3Script(t)

	// (1) no page-level request-id state survives a submission, and no derivation decides whether to reuse
	// one. Either would reintroduce the freeze.
	for _, gone := range []string{"PMS_REQUEST_ID", "PMS_ATTEMPT_KEY", "phase3RequestID"} {
		if strings.Contains(s, gone) {
			t.Errorf("%s is back: a request id that outlives its submission can freeze a corrected one", gone)
		}
	}

	// (2) the id is minted where the deliberate submission is handled.
	submit := s[strings.Index(s, "document.getElementById('form-pms').addEventListener('submit'"):]
	if !strings.Contains(submit, "body.request_id = newRequestID();") {
		t.Error("the sign-in submission does not mint a fresh request id")
	}

	// (3) and the minted value is still the canonical UUID the server accepts — the shape assertion itself
	// lives in the browser test, which runs the function; this only proves the generator is what is called.
	if !strings.Contains(s, "function newRequestID()") {
		t.Fatal("newRequestID is gone, so the submission cannot be minting a canonical UUID")
	}
}
