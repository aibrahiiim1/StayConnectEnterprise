package main

// THE MESSAGE THE SERVER COMPOSED AND THE GUEST NEVER SAW.
//
// landing() takes an error string and has done since it was written. Every refusal it handles passes one:
// an empty voucher box, a device it cannot place on the guest network, packages that are unavailable. It put
// that string into the template data as .Error.
//
// The landing template contained no reference to .Error at all. Every one of those messages was discarded,
// and the guest got back a page indistinguishable from the one they had just submitted.
//
// It survived because of which forms it affects. The voucher and personal-account forms are plain HTML
// POSTs, so the server's response IS the page the guest lands on -- and those are exactly the flows whose
// messages vanished. The PMS, OTP and post-stay flows submit with fetch() and render errors into their own
// .err boxes from the JSON reply, so their messages always appeared. Testing the portal through those paths
// would never have revealed it.
//
// IT ALSO SURVIVED A TEST WRITTEN SPECIFICALLY ABOUT THIS BEHAVIOUR. The commerce-dark fix asserted that a
// guest is told packages are unavailable, and passed -- because its fixture parses a stub template,
// `{{.Error}}`, instead of the one that ships. The assertion was true of the stub and false of the product,
// and it was reported as verified before a live check on the appliance showed the page arriving blank.
//
// So these tests parse landingHTML itself. A fixture that substitutes its own template cannot prove anything
// about what a guest reads.

import (
	"html"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// realLandingHandler builds a handler around the SHIPPED landing template, which is the whole point.
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
	const msg = "Internet packages are not available right now. Please ask reception."

	rec := httptest.NewRecorder()
	h.landing(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), msg)

	body := rec.Body.String()
	if !strings.Contains(body, msg) {
		t.Fatal("landing() was given a message for the guest and the rendered page does not contain it; " +
			"the guest gets back a page identical to the one they submitted, with no explanation")
	}
}

func TestEveryRefusalLandingHandlesActuallyReachesTheGuest(t *testing.T) {
	// These are the strings the handlers in this package pass to landing(). Each one is a moment where a
	// guest did something and needs to know what happened.
	for _, msg := range []string{
		"Please enter a voucher code.",
		"Your device isn't on the guest network.",
		"Unable to detect your device address.",
		"Internet packages are not available right now. Please ask reception.",
		"Please sign in again.",
		"That package is not available. Please choose another.",
	} {
		t.Run(msg, func(t *testing.T) {
			h := realLandingHandler(t)
			rec := httptest.NewRecorder()
			h.landing(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), msg)
			// Compared against the ESCAPED form, because that is what being rendered correctly looks like.
			// "Your device isn't on the guest network." reaches the page as `isn&#39;t`, which a browser
			// shows as the apostrophe the guest expects. Searching for the raw string failed this one
			// message while the product was right -- a test that mistakes correct escaping for a missing
			// message would push somebody to remove the escaping to make it pass.
			if !strings.Contains(rec.Body.String(), html.EscapeString(msg)) {
				t.Errorf("the guest is never shown %q", msg)
			}
		})
	}
}

func TestNoErrorMeansNoEmptyErrorBanner(t *testing.T) {
	// The ordinary landing page -- a guest arriving for the first time -- must not carry an error region at
	// all. An empty red box is its own kind of lie.
	h := realLandingHandler(t)
	rec := httptest.NewRecorder()
	h.landing(rec, httptest.NewRequest(http.MethodGet, "/", nil), "")

	if strings.Contains(rec.Body.String(), `id="server-error"`) {
		t.Error("the first-visit landing page renders an error banner with nothing in it")
	}
}

func TestAMessageIsEscapedRatherThanRendered(t *testing.T) {
	// These strings are composed in this package today, but a banner that interpolates into the page is
	// worth pinning: the day one of them carries something a guest supplied, it must arrive as text.
	h := realLandingHandler(t)
	rec := httptest.NewRecorder()
	h.landing(rec, httptest.NewRequest(http.MethodPost, "/auth/voucher", nil), `<script>alert(1)</script>`)

	if strings.Contains(rec.Body.String(), "<script>alert(1)</script>") {
		t.Error("the message was rendered as markup rather than escaped as text")
	}
}
