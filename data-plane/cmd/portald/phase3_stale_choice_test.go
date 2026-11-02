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

	// The selection must not re-enable the buttons it just disabled (room chooser only; post-stay has its own).
	choose := s[strings.Index(s, "async function phase3Choose"):]
	choose = choose[:strings.Index(choose, "function phase3ConfirmRoomCharge")]
	if strings.Contains(choose, "x.disabled = false") {
		t.Error("a failed selection re-enables the same buttons; that is the defect this replaced")
	}
	if !strings.Contains(choose, "resetPhase3ToSignIn(errEl)") {
		t.Error("a failed selection does not return the guest to sign-in")
	}
}

// Room-charge confirmation (2026-11-02): window.confirm returns false at once in the iOS and Android captive-portal
// windows, so every room-charge tap did nothing at all. The confirmation is an in-page panel with the exact package
// and amount; Cancel sends nothing; every other tap leads to a grant or a visible message.
func TestARoomChargeTapIsConfirmedInThePageNeverByWindowConfirm(t *testing.T) {
	s := phase3Script(t)
	for _, banned := range []string{"window.confirm(", "window.alert(", "window.prompt("} {
		if strings.Contains(s, banned) {
			t.Errorf("the portal calls %s; captive-portal windows suppress it and the tap silently does nothing", banned)
		}
	}
	room := s[strings.Index(s, "function renderPhase3Choices"):]
	click := room[strings.Index(room, "b.addEventListener('click'"):]
	click = click[:strings.Index(click, "box.appendChild(b);")]
	if !strings.Contains(click, "c.method === 'PMS_POSTING'") || !strings.Contains(click, "phase3ConfirmRoomCharge(box, c, errEl)") {
		t.Error("a room-charge choice is not routed through the in-page confirmation")
	}
	if !strings.Contains(click, "phase3Choose(box, c, errEl)") {
		t.Error("a choice that needs no confirmation does not send its grant")
	}

	panel := s[strings.Index(s, "function phase3ConfirmRoomCharge"):]
	panel = panel[:strings.Index(panel, "// RETURNING TO SIGN-IN")]
	for _, want := range []string{"t('room.confirm')", "'{price}'", "'{name}'", "t('room.confirm.yes')", "t('room.confirm.no')"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the confirmation does not show %s", want)
		}
	}
	no := panel[strings.Index(panel, "no.addEventListener('click'"):]
	no = no[:strings.Index(no, "});")]
	if strings.Contains(no, "submitPhase3") || strings.Contains(no, "phase3Choose") || strings.Contains(no, "fetch(") {
		t.Error("Cancel sends a request; it must perform no purchase and no posting")
	}
	yes := panel[strings.Index(panel, "yes.addEventListener('click'"):]
	if !strings.Contains(yes[:strings.Index(yes, ");")+2], "phase3Choose(box, c, errEl)") {
		t.Error("Yes does not send the grant")
	}

	// One grant per choice: every control is disabled before the request, and a thrown error is still a visible answer.
	choose := s[strings.Index(s, "async function phase3Choose"):]
	choose = choose[:strings.Index(choose, "function phase3ConfirmRoomCharge")]
	disable := strings.Index(choose, "x.disabled = true")
	send := strings.Index(choose, "submitPhase3(")
	if disable < 0 || send < 0 || disable > send {
		t.Error("the buttons are not disabled before the grant is sent, so a double tap can send two")
	}
	if !strings.Contains(choose, "catch (e)") {
		t.Error("a script or network error during the grant would leave the guest with no answer")
	}
}

// The confirmation words exist in every portal language, so the panel is never blank or shows a key.
func TestTheRoomChargeConfirmationIsTranslated(t *testing.T) {
	for lang, words := range acquisitionStrings {
		for _, k := range []string{"room.confirm", "room.confirm.yes", "room.confirm.no"} {
			if strings.TrimSpace(words[k]) == "" {
				t.Errorf("%s has no %s", lang, k)
			}
		}
		if !strings.Contains(words["room.confirm"], "{price}") || !strings.Contains(words["room.confirm"], "{name}") {
			t.Errorf("%s room.confirm does not name the package and the amount: %q", lang, words["room.confirm"])
		}
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
