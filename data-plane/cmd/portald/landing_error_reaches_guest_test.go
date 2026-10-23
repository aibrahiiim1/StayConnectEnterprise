package main

// The landing page must render the .Error that landing() passes: the template once discarded every refusal message, and a fixture parsing a stub template hid it. These tests parse landingHTML itself.

import (
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realLandingHandler builds a handler around the shipped landing template.
func realLandingHandler(t *testing.T) *handler {
	t.Helper()
	tmpl, err := template.New("land").Parse(landingHTML)
	if err != nil {
		t.Fatalf("the shipped landing template does not parse: %v", err)
	}
	return &handler{tmplLand: tmpl}
}

func TestARefusalTheServerComposedIsVisibleOnThePageTheGuestGetsBack(t *testing.T) {
	h := realLandingHandler(t)
	const msg = "Internet packages are not available right now. Please contact the site team for assistance."

	rec := httptest.NewRecorder()
	h.landing(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), msg)

	body := rec.Body.String()
	if !strings.Contains(body, msg) {
		t.Fatal("landing() was given a message for the guest and the rendered page does not contain it; " +
			"the guest gets back a page identical to the one they submitted, with no explanation")
	}
}

func TestEveryRefusalLandingHandlesActuallyReachesTheGuest(t *testing.T) {
	// The strings the handlers pass to landing().
	for _, msg := range []string{
		"Please enter a voucher code.",
		"Your device isn't connected to this Wi-Fi network.",
		"Unable to detect your device address.",
		"Internet packages are not available right now. Please contact the site team for assistance.",
		"Please sign in again.",
		"That package is not available. Please choose another.",
	} {
		t.Run(msg, func(t *testing.T) {
			h := realLandingHandler(t)
			rec := httptest.NewRecorder()
			h.landing(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), msg)
			// Compared against the escaped form (isn&#39;t); searching for the raw string would push someone to remove the escaping.
			if !strings.Contains(rec.Body.String(), html.EscapeString(msg)) {
				t.Errorf("the guest is never shown %q", msg)
			}
		})
	}
}

func TestNoErrorMeansNoEmptyErrorBanner(t *testing.T) {
	// The ordinary landing page must carry no error region at all.
	h := realLandingHandler(t)
	rec := httptest.NewRecorder()
	h.landing(rec, httptest.NewRequest(http.MethodGet, "/", nil), "")

	if strings.Contains(rec.Body.String(), `id="server-error"`) {
		t.Error("the first-visit landing page renders an error banner with nothing in it")
	}
}

func TestAMessageIsEscapedRatherThanRendered(t *testing.T) {
	// A banner that interpolates into the page must render guest-supplied content as text.
	h := realLandingHandler(t)
	rec := httptest.NewRecorder()
	h.landing(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), `<script>alert(1)</script>`)

	if strings.Contains(rec.Body.String(), "<script>alert(1)</script>") {
		t.Error("the message was rendered as markup rather than escaped as text")
	}
}
