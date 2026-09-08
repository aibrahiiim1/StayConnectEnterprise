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

func TestRetryCannotProduceASecondGrant(t *testing.T) {
	s := phase3Script(t)
	// The request id is retained across a failure ON PURPOSE: signing in again with the same details returns
	// the SAME Auth Context rather than minting a second one, so the trip back to sign-in cannot become a
	// route to two grants. Only a SUCCESSFUL grant clears it.
	success := s[strings.Index(s, "if (j.ok && j.session_id)"):]
	success = success[:strings.Index(success, "if (j.ok && j.needs_choice)")]
	if !strings.Contains(success, "PMS_REQUEST_ID = ''") {
		t.Error("a successful grant no longer clears the request id, so a replay could resolve again")
	}
	reset := s[strings.Index(s, "function resetPhase3ToSignIn"):]
	reset = reset[:strings.Index(reset, "function renderPhase3Choices")]
	if strings.Contains(reset, "PMS_REQUEST_ID = ''") {
		t.Error("the failure path clears the request id, which turns a lost-in-transit grant into a second resolution")
	}
}
