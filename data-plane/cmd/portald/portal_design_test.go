package main

// THE DESIGNER'S TEMPLATES, THE CONTENT-SECURITY-POLICY, AND THE DESIGN AS A GUEST RECEIVES IT.

import (
	"context"
	"encoding/json"
	"html/template"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// designHandler is a portald handler whose scd answers /v1/tenant/branding with the given design.
func designHandler(t *testing.T, design map[string]any) *handler {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/v1/tenant/branding" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"design": design})
	}))
	t.Cleanup(ts.Close)
	tland, err := template.New("land").Parse(landingHTML)
	if err != nil {
		t.Fatal(err)
	}
	tsucc, err := template.New("succ").Parse(successHTML)
	if err != nil {
		t.Fatal(err)
	}
	addr := ts.Listener.Addr().String()
	return &handler{
		tmplLand: tland,
		tmplSucc: tsucc,
		designs:  &designCache{},
		arpCache: func(net.IP) (net.HardwareAddr, bool) { return nil, false },
		scd: &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			},
		}},
	}
}

func get(t *testing.T, h *handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "10.77.0.42:51000"
	h.routes().ServeHTTP(w, r)
	return w
}

// Everything the sign-in script and the two plain-HTML forms depend on. A template is presentation: if any of
// these went missing under one layout, a sign-in method would silently stop working there.
var functionalIDs = []string{
	"lang", "brand-logo", "brand-name", "brand-welcome", "access-ended", "site-notice", "tabs",
	"panel-accountlogin", "use-personal", "form-voucher", "voucher", "form-credentials", "ga-username", "ga-password",
	"panel-email", "email", "panel-sms", "phone", "panel-pms", "form-pms", "pms-room", "pms-secondary", "pms-prompt",
	"pms-err", "pms-choices", "panel-poststay", "form-poststay", "ps-pin", "ps-err", "panel-social",
	"social-providers", "alt-methods", "brand-help", "custom-html", "brand-terms-wrap", "brand-terms",
	"info-btn", "info-panel",
}

func TestEveryTemplateRendersEveryFunctionalElement(t *testing.T) {
	for _, tp := range portaldesign.Templates {
		t.Run(tp.ID, func(t *testing.T) {
			h := designHandler(t, map[string]any{"template_id": tp.ID, "hotel_name": "Semantics Demo Hotel"})
			w := get(t, h, "/")
			html := w.Body.String()
			if !strings.Contains(html, `data-template="`+tp.ID+`"`) {
				t.Errorf("the page was not rendered with template %s on first paint", tp.ID)
			}
			for _, id := range functionalIDs {
				if n := strings.Count(html, `id="`+id+`"`); n != 1 {
					t.Errorf("id %q appears %d times under %s; the sign-in script needs exactly one", id, n, tp.ID)
				}
			}
			for _, frag := range []string{
				`action="/auth/voucher"`, `name="code"`, `action="/auth/credentials"`, `name="username"`,
				`name="password"`, `'/auth/pms/phase3'`, `'/auth/post-stay-pin'`, `'/auth/otp/request'`,
				`'/auth/otp/verify'`, `'/auth/social/start?provider='`,
			} {
				if !strings.Contains(html, frag) {
					t.Errorf("sign-in contract %q is missing under %s", frag, tp.ID)
				}
			}
		})
	}
}

func TestEveryTemplateHasItsLayoutAndTheScriptKnowsIt(t *testing.T) {
	for _, tp := range portaldesign.Templates {
		if tp.ID == "classic" {
			continue // the original layout: the base stylesheet, untouched
		}
		if !strings.Contains(landingHTML, `[data-template="`+tp.ID+`"]`) {
			t.Errorf("template %s has no CSS in the portal", tp.ID)
		}
	}
	m := regexp.MustCompile(`const TEMPLATES = \[([^\]]*)\]`).FindStringSubmatch(landingHTML)
	if m == nil {
		t.Fatal("the page's template list is missing")
	}
	for _, tp := range portaldesign.Templates {
		if !strings.Contains(m[1], "'"+tp.ID+"'") {
			t.Errorf("the page's script does not know template %s", tp.ID)
		}
	}
}

func TestAnUnknownOrMissingDesignRendersClassic(t *testing.T) {
	// No scd at all (the tests' literal handler), an empty design, and a template this build does not have.
	w := httptest.NewRecorder()
	tmpl, _ := template.New("land").Parse(landingHTML)
	(&handler{tmplLand: tmpl}).landing(w, httptest.NewRequest(http.MethodGet, "/", nil), "")
	if !strings.Contains(w.Body.String(), `data-template="classic"`) {
		t.Error("a portal with no design source did not render classic")
	}
	for _, d := range []map[string]any{{}, {"template_id": "brutalist"}} {
		if html := get(t, designHandler(t, d), "/").Body.String(); !strings.Contains(html, `data-template="classic"`) {
			t.Errorf("design %v did not render classic", d)
		}
	}
}

var reNonceAttr = regexp.MustCompile(`<script([^>]*)>`)

func assertNonced(t *testing.T, w *httptest.ResponseRecorder, what string) string {
	t.Helper()
	csp := w.Header().Get("Content-Security-Policy")
	m := regexp.MustCompile(`script-src 'nonce-([A-Za-z0-9_-]+)'`).FindStringSubmatch(csp)
	if m == nil {
		t.Fatalf("%s: no nonce-based script-src in %q", what, csp)
	}
	for _, d := range []string{"object-src 'none'", "base-uri 'none'", "form-action 'self'", "frame-ancestors 'none'",
		"img-src 'self' data: https:", "style-src 'self' 'unsafe-inline'", "connect-src 'self'"} {
		if !strings.Contains(csp, d) {
			t.Errorf("%s: the policy lacks %q", what, d)
		}
	}
	if !strings.Contains(csp, "script-src 'nonce-"+m[1]+"';") {
		t.Errorf("%s: script-src must be the nonce and nothing else: %q", what, csp)
	}
	tags := reNonceAttr.FindAllStringSubmatch(w.Body.String(), -1)
	if len(tags) == 0 {
		t.Fatalf("%s: no scripts found", what)
	}
	for _, tag := range tags {
		if !strings.Contains(tag[1], `nonce="`+m[1]+`"`) {
			t.Errorf("%s: a script does not carry this response's nonce: <script%s>", what, tag[1])
		}
	}
	return m[1]
}

func TestTheSignInPageCarriesANonceBasedPolicy(t *testing.T) {
	h := designHandler(t, map[string]any{})
	a := assertNonced(t, get(t, h, "/"), "landing")
	b := assertNonced(t, get(t, h, "/"), "landing")
	if a == b {
		t.Error("two responses carried the same nonce; it must be minted per response")
	}
	if len(a) < 20 {
		t.Errorf("the nonce is too short to be unguessable: %q", a)
	}
}

func TestTheSuccessPageCarriesTheSamePolicy(t *testing.T) {
	h := designHandler(t, map[string]any{})
	assertNonced(t, get(t, h, "/success?s=abc&t=3600"), "success")
}

func TestBrandingIsSanitisedAgainOnTheWayToTheGuest(t *testing.T) {
	// A design that reached the database without passing edged -- written before the allowlist existed,
	// restored from a backup -- must still reach a guest only in its safe form.
	h := designHandler(t, map[string]any{
		"hotel_name":  "Semantics Demo Hotel",
		"terms_url":   "javascript:alert(1)",
		"logo_url":    "//evil.example/logo.png",
		"custom_html": `<p>Pool</p><img src=x onerror=alert(1)><base href="https://evil.example/"><form action="https://evil.example"><input name="room"></form>`,
		"custom_css":  `form { display: none !important } @import url(https://evil.example/x.css);`,
		"template_id": "split",
		"draft":       map[string]any{"custom_html": "<script>x</script>"},
	})
	w := get(t, h, "/api/branding")
	body := w.Body.String()
	for _, bad := range []string{"onerror", "<base", "<form", "javascript:", "evil.example", "important", "@import", "<script"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(bad)) {
			t.Errorf("/api/branding served %q: %s", bad, body)
		}
	}
	var got struct {
		Design map[string]any `json:"design"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Design["hotel_name"] != "Semantics Demo Hotel" || got.Design["template_id"] != "split" {
		t.Errorf("valid fields were lost: %v", got.Design)
	}
	if h, _ := got.Design["custom_html"].(string); !strings.Contains(h, "<p>Pool</p>") {
		t.Errorf("the safe part of the fragment was lost: %q", h)
	}
}

func TestBrandingFailsOpenToTheDefaults(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	h := designHandler(t, nil)
	addr := ts.Listener.Addr().String()
	h.scd = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}}}
	w := get(t, h, "/api/branding")
	if strings.TrimSpace(w.Body.String()) != "{}" {
		t.Errorf("a failing design source should serve the defaults, got %q", w.Body.String())
	}
	if html := get(t, h, "/").Body.String(); !strings.Contains(html, `data-template="classic"`) {
		t.Error("a failing design source should render classic")
	}
}

// THE CASCADE THE GUARD DEPENDS ON. The portal's own styling and the templates are layered, the hotel's CSS
// goes into a layer above them, and the guard sheet is unlayered and comes after both -- so a normal hotel
// rule can restyle anything but hide none of the sign-in controls. Asserted on the served text; the browser
// test (hotel-admin/e2e/portal-templates.spec.ts) proves the effect.
func TestTheGuardSheetOutranksTheHotelsLayer(t *testing.T) {
	html := landingHTML
	order := []string{
		`@layer sc-base, sc-template, hotel;`, `@layer sc-base {`, `<style id="sc-templates">`,
		`@layer sc-template {`, `<style id="sc-guard">`, `.panels form`, `</head>`,
	}
	last := -1
	for _, frag := range order {
		i := strings.Index(html, frag)
		if i < 0 {
			t.Fatalf("missing %q", frag)
		}
		if i < last {
			t.Errorf("%q is out of order", frag)
		}
		last = i
	}
	guard := html[strings.Index(html, `<style id="sc-guard">`):]
	guard = guard[:strings.Index(guard, "</style>")]
	rules := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(guard, "")
	if strings.Contains(rules, "@layer") {
		t.Error("the guard sheet must be unlayered")
	}
	// The guard must LOSE to the sign-in script's own inline styles, which show and hide its forms; an
	// !important here would beat them and break the voucher/personal-account switch.
	if strings.Contains(rules, "!important") {
		t.Error("the guard uses !important")
	}
	// And the page wraps the hotel's sheet in its layer, with !important removed a second time.
	for _, frag := range []string{`'@layer hotel {\n' + css + '\n}'`, `replace(/!\s*important/gi, '')`} {
		if !strings.Contains(html, frag) {
			t.Errorf("the hotel stylesheet is not applied as designed (missing %q)", frag)
		}
	}
}

// The package-selection page runs no script, but it is still a guest-facing page reached without a proxy, and
// its forms must only ever post back to this portal.
func TestThePolicyHelperSetsEveryHeaderAGuestPageNeeds(t *testing.T) {
	w := httptest.NewRecorder()
	nonce := setPortalCSP(w)
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "form-action 'self'") || !strings.Contains(csp, "'nonce-"+nonce+"'") {
		t.Errorf("unexpected policy %q", csp)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff is not set")
	}
}
