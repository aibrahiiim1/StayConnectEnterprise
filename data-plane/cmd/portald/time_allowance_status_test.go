package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A validity-window plan's time allowance is enforced, so the guest must see the number accrual is spending: scd marks online_time_allowance.
const scdAllowance = `{"ip":"10.77.0.43","session_id":"sess-10","active":true,"time_mode":"VALIDITY_WINDOW",` +
	`"remaining_online_seconds":7800,"online_time_allowance":true,"hard_expiry":"2026-09-30T12:00:00Z"}` + "\n"

func TestStatusPageShowsAValidityWindowTimeAllowance(t *testing.T) {
	h := statusHandler(t, shotsDesign("classic"), scdAllowance, 200)
	w := serve(h, http.MethodGet, "/status?s=sess-10", map[string]string{"Accept": browserAccept, "Accept-Language": "en"})
	page := w.Body.String()
	en := builtinStrings["en"]
	for _, want := range []string{en["online.remaining"], "2 h 10 min", `data-at="2026-09-30T12:00:00Z"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the status page of a time-allowance package lacks %q", want)
		}
	}
}

// Without an allowance a validity-window package's status page is what it always was: no time-left panel.
func TestStatusPageWithoutAllowanceShowsNoTimeLeft(t *testing.T) {
	h := statusHandler(t, shotsDesign("classic"), `{"ip":"10.77.0.44","session_id":"sess-11","active":true}`+"\n", 200)
	w := serve(h, http.MethodGet, "/status?s=sess-11", map[string]string{"Accept": browserAccept, "Accept-Language": "en"})
	if strings.Contains(w.Body.String(), "2 h 10 min") || strings.Contains(w.Body.String(), `data-at=`) {
		t.Fatal("a package with no time allowance shows a time-left panel")
	}
}

// The online page's own script reveals the panel for either kind of allowance.
func TestRemainingTimePanelRevealsForAnAllowance(t *testing.T) {
	panel := timePanel(t, renderSuccess(t))
	if !strings.Contains(panel, "!st.online_time_allowance") {
		t.Fatal("the panel ignores scd's online_time_allowance mark; a time-allowance package would show nothing")
	}
}

// RFC 8908 seconds-remaining is the EARLIER of the two clocks: the allowance left and the hard expiry.
func TestCaptiveAPIReportsTheEarlierClock(t *testing.T) {
	soon := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	h := httpsHandler(t, activeState, func(r *http.Request) (int, any) {
		return 200, map[string]any{"active": true, "session_id": "s", "remaining_online_seconds": 3540.0,
			"online_time_allowance": true, "time_mode": "VALIDITY_WINDOW", "hard_expiry": soon}
	})
	srv := h.tlsFront(h.routes())
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, tlsReq(http.MethodGet, tCustom, "/captive-portal/api"))
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	n, _ := got["seconds-remaining"].(float64)
	if n <= 0 || n > 600 {
		t.Fatalf("seconds-remaining %v; with the window ending in 10 minutes and 59 left on the allowance it must be the window", got["seconds-remaining"])
	}
}
