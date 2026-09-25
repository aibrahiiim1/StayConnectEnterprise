package main

// THE PAGES A GUEST READS, RENDERED IN THEIR LANGUAGE AND IN THE HOTEL'S LOOK.
//
// Presentation only. Nothing in this file decides whether a guest is let in, what they are told about why
// not, how long anything waits, or which route a form posts to -- the handlers decide all of that and hand
// this file a sentence or a page to draw. What lives here is:
//
//   - which of the hotel's languages this request should be answered in (the guest's own choice, then what
//     their device asks for, then English), and the dictionary for it;
//   - the hotel's published design as the handful of values a server-rendered page needs (name, logo,
//     layout attributes, colours as CSS custom properties);
//   - the map from the sentences the handlers already compose to translation keys, so a refusal reaches a
//     guest in their language -- and so the raw codes a few paths used to pass straight through ("Voucher
//     AUTH_DENIED.") become a sentence a person can act on.

import (
	"fmt"
	"html/template"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// ---- language -------------------------------------------------------------------------------------------

// offeredLanguages is the list the selector offers: the hotel's configured languages when it chose some
// (each backed by shipped words or the hotel's own), otherwise the six the portal ships. The same rule the
// sign-in page's script applies.
func offeredLanguages(d map[string]any) []portalLanguage {
	var out []portalLanguage
	if list, ok := d["languages"].([]any); ok {
		tr, _ := d["translations"].(map[string]any)
		for _, item := range list {
			m, _ := item.(map[string]any)
			code, _ := m["code"].(string)
			if code == "" {
				continue
			}
			label, _ := m["label"].(string)
			meta, shipped := shippedLanguage(code)
			if !shipped {
				if words, ok := tr[code].(map[string]any); !ok || len(words) == 0 {
					continue
				}
				meta = portalLanguage{Code: code, Label: strings.ToUpper(code), RTL: rtlCode(code)}
			}
			if label != "" {
				meta.Label = label
			}
			out = append(out, meta)
		}
	}
	if len(out) == 0 {
		out = append(out, portalLanguages...)
	}
	return out
}

func shippedLanguage(code string) (portalLanguage, bool) {
	for _, l := range portalLanguages {
		if l.Code == code {
			return l, true
		}
	}
	return portalLanguage{}, false
}

// rtlCode answers for a language the portal does not ship: a hotel may add Hebrew or Persian with its own
// words, and those read right to left too.
func rtlCode(code string) bool {
	switch strings.SplitN(strings.ToLower(code), "-", 2)[0] {
	case "ar", "he", "fa", "ur":
		return true
	}
	return false
}

// guestLanguage picks the language for one response: the guest's remembered choice (the sc-lang cookie the
// selector writes), then the device's Accept-Language in its own order, then English.
func guestLanguage(r *http.Request, d map[string]any) portalLanguage {
	offered := offeredLanguages(d)
	find := func(code string) (portalLanguage, bool) {
		code = strings.ToLower(strings.TrimSpace(code))
		if code == "" {
			return portalLanguage{}, false
		}
		for _, l := range offered {
			if strings.ToLower(l.Code) == code {
				return l, true
			}
		}
		for _, l := range offered {
			if strings.SplitN(strings.ToLower(l.Code), "-", 2)[0] == code {
				return l, true
			}
		}
		return portalLanguage{}, false
	}
	if r != nil {
		if c, err := r.Cookie("sc-lang"); err == nil {
			if v, err := url.QueryUnescape(c.Value); err == nil {
				if l, ok := find(v); ok {
					return l
				}
			}
		}
		for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
			tag := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
			tag = strings.ReplaceAll(tag, "_", "-")
			if tag == "" || tag == "*" {
				continue
			}
			if l, ok := find(tag); ok {
				return l
			}
			if l, ok := find(strings.SplitN(tag, "-", 2)[0]); ok {
				return l
			}
		}
	}
	if l, ok := find("en"); ok {
		return l
	}
	return offered[0]
}

// guestWords is the dictionary for one language: English underneath, the shipped language over it, the
// hotel's published wording over that. A blank override is not an override.
func guestWords(code string, d map[string]any) map[string]string {
	out := make(map[string]string, len(builtinStrings["en"]))
	for k, v := range builtinStrings["en"] {
		out[k] = v
	}
	for k, v := range builtinStrings[code] {
		out[k] = v
	}
	if tr, ok := d["translations"].(map[string]any); ok {
		if words, ok := tr[code].(map[string]any); ok {
			for k, v := range words {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					out[k] = s
				}
			}
		}
	}
	return out
}

// subset returns the keys with any of the prefixes -- what a page's own script needs, and nothing else.
func subset(words map[string]string, prefixes ...string) map[string]string {
	out := map[string]string{}
	for k, v := range words {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				out[k] = v
				break
			}
		}
	}
	return out
}

// ---- the hotel's design, for a server-rendered page -------------------------------------------------------

// guestBrand is what a server-rendered page draws from the published design. Every value has already been
// through portaldesign.ForGuests (colour and length patterns, font-stack characters, image URL rules).
type guestBrand struct {
	Name, Welcome string
	// Logo was checked by ForGuests (an appliance path, an https URL or an inline image), which is what
	// makes it safe to mark as a URL the template need not filter -- a data:image logo would otherwise be
	// replaced by html/template's placeholder.
	Logo                                          template.URL
	Template, Density, Panel, HeroHeight, Surface string
	// Style is the design as custom properties on <html>, the same properties the sign-in page's script
	// sets. It is built only from validated values; see cssURL for the one value that is not a token.
	Style template.CSS
}

func brandFor(d map[string]any) guestBrand {
	b := guestBrand{Template: portaldesign.TemplateID(d)}
	str := func(k string) string { s, _ := d[k].(string); return s }
	b.Name, b.Welcome, b.Logo = str("hotel_name"), str("welcome_text"), template.URL(str("logo_url"))
	var style []string
	set := func(prop, v string) {
		if v != "" {
			style = append(style, prop+":"+v)
		}
	}
	set("--sc-brand", str("brand_color"))
	set("--sc-brand-dark", str("brand_color_dark"))
	set("--sc-ink", str("text_color"))
	set("--sc-radius", str("corner_radius"))
	set("font-family", str("font_family"))
	if v := str("brand_color"); v != "" && lightColour(v) {
		set("--sc-on-brand", "#14161a")
	}
	if v := str("background_url"); v != "" {
		set("--sc-bg", cssURL(v))
	}
	if v := str("hero_image_url"); v != "" {
		set("--sc-hero", cssURL(v))
	} else if v := str("background_url"); v != "" {
		set("--sc-hero", cssURL(v))
	}
	if o, ok := d["template_options"].(map[string]any); ok {
		pick := func(k string) string { s, _ := o[k].(string); return s }
		b.Density, b.Panel, b.HeroHeight, b.Surface = pick("density"), pick("panel_position"), pick("hero_height"), pick("surface")
		if n, ok := number(o["overlay"]); ok && n >= 0 && n <= 90 {
			set("--sc-overlay", fmt.Sprintf("%.2f", n/100))
		}
		set("--sc-heading-font", pick("heading_font"))
	}
	b.Style = template.CSS(strings.Join(style, ";"))
	return b
}

// cssURL writes an image address as a CSS url(). The address was checked by ForGuests (an appliance path,
// an https URL or an inline image); the characters that could end the string or the function are
// percent-encoded anyway, so the value cannot become anything but one url().
func cssURL(u string) string {
	r := strings.NewReplacer(`"`, "%22", `'`, "%27", `(`, "%28", `)`, "%29", `\`, "%5C", "\n", "", "\r", "", " ", "%20")
	return `url("` + r.Replace(u) + `")`
}

// lightColour reports whether white text on this colour would be hard to read, so a hotel that picks a pale
// brand colour gets dark button text instead of an unreadable button. Hex only; rgb() stays white.
func lightColour(c string) bool {
	c = strings.TrimPrefix(c, "#")
	if len(c) == 3 || len(c) == 4 {
		c = string([]byte{c[0], c[0], c[1], c[1], c[2], c[2]})
	}
	if len(c) < 6 {
		return false
	}
	var r, g, b int
	if _, err := fmt.Sscanf(c[:6], "%02x%02x%02x", &r, &g, &b); err != nil {
		return false
	}
	lin := func(v int) float64 {
		f := float64(v) / 255
		if f <= 0.03928 {
			return f / 12.92
		}
		return math.Pow((f+0.055)/1.055, 2.4)
	}
	l := 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
	// White on this colour below 3:1 -- the threshold for large, bold button text.
	return (1.05)/(l+0.05) < 3
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case interface{ Float64() (float64, error) }:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// ---- the page model every server-rendered guest page shares ------------------------------------------------

// guestPage is the data a guest page template draws from.
type guestPage struct {
	Lang      string
	Dir       string
	Nonce     string
	T         map[string]string // the whole dictionary for Lang, for {{index .T "key"}}
	JS        template.JS       // the part of it the page's own script reads, as JSON
	Languages []portalLanguage
	Brand     guestBrand
	// HotelSheet is the hotel's custom CSS as the pages after sign-in carry it: one <style id="sc-hotel">
	// wrapping the sheet in `@layer hotel`, or empty. The sign-in page ignores it; its script applies the same
	// sheet from /api/branding in the same way.
	HotelSheet template.HTML
}

// reImportant matches what the sign-in page's script removes a second time before layering the hotel's sheet.
var reImportant = regexp.MustCompile(`(?i)!\s*important`)

// hotelSheet is the hotel's custom CSS for a server-rendered page, or "" when there is none.
//
// The value has already been through portaldesign.ForGuests -- rebuilt rule by rule from the allowlist edged
// applies on save, with !important and any </style removed -- which is the same text /api/branding hands the
// sign-in page. Two things are done again here, exactly as the sign-in page's script does them: !important is
// stripped (it is the one thing that lets a layered rule outrank the unlayered guard), and the sheet goes
// inside `@layer hotel`, below the guard. A sheet that could still close its element is not served at all.
func hotelSheet(d map[string]any) template.HTML {
	css, _ := d["custom_css"].(string)
	css = strings.TrimSpace(reImportant.ReplaceAllString(css, ""))
	if css == "" || strings.Contains(strings.ToLower(css), "</style") {
		return ""
	}
	return template.HTML("<style id=\"sc-hotel\">@layer hotel {\n" + css + "\n}</style>")
}

func (h *handler) newGuestPage(r *http.Request, nonce string, jsPrefixes ...string) guestPage {
	d := map[string]any{}
	if r != nil {
		if got, ok := h.guestDesign(r.Context()); ok && got != nil {
			d = got
		}
	}
	return buildGuestPage(r, d, nonce, jsPrefixes...)
}

func buildGuestPage(r *http.Request, d map[string]any, nonce string, jsPrefixes ...string) guestPage {
	lang := guestLanguage(r, d)
	words := guestWords(lang.Code, d)
	dir := "ltr"
	if lang.RTL {
		dir = "rtl"
	}
	return guestPage{
		Lang: lang.Code, Dir: dir, Nonce: nonce, T: words, JS: jsData(subset(words, jsPrefixes...)),
		Languages: offeredLanguages(d), Brand: brandFor(d), HotelSheet: hotelSheet(d),
	}
}

// fill replaces {name} placeholders.
func fill(s string, kv ...string) string {
	for i := 0; i+1 < len(kv); i += 2 {
		s = strings.ReplaceAll(s, "{"+kv[i]+"}", kv[i+1])
	}
	return s
}

// humanSpan renders a duration with the language's abbreviated units: "2 h 15 min", "3 d", "45 min".
func humanSpan(words map[string]string, d time.Duration) string {
	if d <= 0 {
		return ""
	}
	mins := int(math.Round(d.Minutes()))
	if mins < 1 {
		mins = 1
	}
	days, hours, m := mins/1440, (mins%1440)/60, mins%60
	var parts []string
	if days > 0 {
		parts = append(parts, fill(words["unit.d"], "n", fmt.Sprint(days)))
	}
	if hours > 0 {
		parts = append(parts, fill(words["unit.h"], "n", fmt.Sprint(hours)))
	}
	if m > 0 && days == 0 {
		parts = append(parts, fill(words["unit.min"], "n", fmt.Sprint(m)))
	}
	return strings.Join(parts, " ")
}

// speed renders a rate in Mbps; below one megabit it keeps one decimal rather than rounding to zero.
func speed(words map[string]string, kbps float64) string {
	if kbps <= 0 {
		return ""
	}
	mb := kbps / 1000
	n := fmt.Sprintf("%.0f", mb)
	if mb < 10 && math.Abs(mb-math.Round(mb)) > 0.05 {
		n = fmt.Sprintf("%.1f", mb)
	}
	return fill(words["unit.mbps"], "n", n)
}

// ---- the sentences the handlers compose, as translation keys -----------------------------------------------

// serverMessageKeys maps every sentence a handler in this package passes to landing() to its key. The English
// of each key is the sentence itself wherever that sentence was already fit for a guest, so an English guest
// reads exactly what the handler wrote.
var serverMessageKeys = map[string]string{
	"Please enter a voucher code.": "err.voucher.empty",
	"Invalid voucher.":             "err.voucher.invalid",
	"This voucher has reached its device limit. Disconnect another device and try again.": "err.voucher.devices",
	"Please enter your username and password.":                                            "err.account.empty",
	"Invalid username or password.":                                                       "err.account.invalid",
	"This account has reached its device limit. Disconnect another device and try again.": "err.account.devices",
	"Too many attempts. Please wait a minute and try again.":                              "err.attempts",
	"The guest network is at capacity. Please try again shortly.":                         "err.capacity",
	"Your device isn't on the guest network.":                                             "err.device.network",
	"Unable to detect your device address.":                                               "err.device.detect",
	"Service unavailable. Please try again.":                                              "err.service",
	"Something went wrong. Please try again.":                                             "err.service",
	"We could not complete that. Please try again.":                                       "err.service",
	"Bad request.": "err.service",
	"Internet packages are not available right now. Please ask reception.": "err.packages.off",
	"Internet packages are unavailable right now.":                         "err.packages.off",
	"No internet packages are available for you right now.":                "err.packages.none",
	"That package is not available. Please choose another.":                "err.package.gone",
	"Please choose a package.":                                             "err.package.gone",
	"Please sign in again.":                                                "err.signin.again",
	"We could not connect your device.":                                    "err.connect",
	"We could not bring your device online. Please try again in a moment.": "err.connect",
}

// serverMessageKey returns the key for a sentence a handler composed. The voucher refusal used to append
// scd's raw code ("Voucher AUTH_DENIED."); every such sentence is the invalid-voucher message, which says no
// more than the code did about why and says it in words. A sentence this map does not know is returned
// with no key and is shown as written -- it was composed on the server, never by the guest.
func serverMessageKey(msg string) string {
	if k, ok := serverMessageKeys[msg]; ok {
		return k
	}
	if strings.HasPrefix(msg, "Voucher ") && strings.HasSuffix(msg, ".") {
		return "err.voucher.invalid"
	}
	return ""
}

// localMessage is a server sentence in the guest's language, with its key and English so the sign-in page
// can re-translate it when the guest switches language.
type localMessage struct{ Key, En, Text string }

func localise(words map[string]string, msg string) localMessage {
	if msg == "" {
		return localMessage{}
	}
	k := serverMessageKey(msg)
	if k == "" {
		return localMessage{En: msg, Text: msg}
	}
	en := builtinStrings["en"][k]
	text := words[k]
	if text == "" {
		text = en
	}
	return localMessage{Key: k, En: en, Text: text}
}

// ---- the sign-in page --------------------------------------------------------------------------------------

// landingView is the sign-in page's data. Error keeps its name and stays a plain string (the handler's
// sentence, in the guest's language), so a test fixture that renders {{.Error}} still reads the message.
type landingView struct {
	guestPage
	Error, ErrorKey, ErrorEn string
	ClientIP, ClientMAC      string
	BrandName                string // the hotel's name, or the neutral fallback in the guest's language
	Help                     string
	HasExtras                bool
	Terms                    template.URL // checked by ForGuests (CheckLinkURL)
	// The shipped wording for the script, from languages.go -- the words the sign-in page can show, in every
	// language, so a guest switching language needs no round trip. The after-sign-in pages' words are
	// rendered by those pages and are not repeated here.
	AllLanguages template.JS
	Strings      template.JS
	// The hotel's own wording and language list, so the first paint already uses them; the script reads the
	// same two from /api/branding afterwards.
	HotelWords template.JS
	Configured template.JS
}

// afterSignInPrefixes are the keys only the pages after sign-in use.
var afterSignInPrefixes = []string{"pkg.", "online.", "tl.", "dev.", "errpage."}

var (
	landingStringsOnce sync.Once
	landingStringsMap  map[string]map[string]string
	landingStringsJSON template.JS
)

// landingStringsJS is the same, as the JSON the page's script reads; built once.
func landingStringsJS() template.JS {
	landingStrings()
	return landingStringsJSON
}

func landingStrings() map[string]map[string]string {
	landingStringsOnce.Do(func() {
		out := make(map[string]map[string]string, len(builtinStrings))
		for code, words := range builtinStrings {
			one := make(map[string]string, len(words))
		next:
			for k, v := range words {
				for _, p := range afterSignInPrefixes {
					if strings.HasPrefix(k, p) {
						continue next
					}
				}
				one[k] = v
			}
			out[code] = one
		}
		landingStringsMap = out
		landingStringsJSON = jsData(out)
	})
	return landingStringsMap
}

func newLandingView(r *http.Request, d map[string]any, nonce, errMsg, ip, mac string) landingView {
	p := buildGuestPage(r, d, nonce)
	msg := localise(p.T, errMsg)
	v := landingView{
		guestPage: p,
		Error:     msg.Text, ErrorKey: msg.Key, ErrorEn: msg.En,
		ClientIP: ip, ClientMAC: mac,
		BrandName:    p.Brand.Name,
		AllLanguages: jsData(portalLanguages),
		Strings:      landingStringsJS(),
		HotelWords:   template.JS("null"),
		Configured:   template.JS("null"),
	}
	if v.BrandName == "" {
		v.BrandName = p.T["brand.fallback"]
	}
	v.Help, _ = d["help_text"].(string)
	html, _ := d["custom_html"].(string)
	v.HasExtras = v.Help != "" || html != ""
	if s, _ := d["terms_url"].(string); s != "" {
		v.Terms = template.URL(s)
	}
	if tr, ok := d["translations"].(map[string]any); ok && len(tr) > 0 {
		v.HotelWords = jsData(tr)
	}
	if l, ok := d["languages"].([]any); ok && len(l) > 0 {
		v.Configured = jsData(l)
	}
	return v
}

// ---- you're online -----------------------------------------------------------------------------------------

type successView struct {
	guestPage
	SessionID       string
	DurationSeconds int
	HumanRemaining  string
	CommerceEnabled bool
	// The commerce panel's own words (commerce_strings.go), for the server-rendered heading (CX) and the
	// panel's script (CXJS). Set only when the panel is rendered.
	CX   map[string]string
	CXJS template.JS
}

// ---- the package choice ------------------------------------------------------------------------------------

type packageRow struct{ ID, Name, Detail string }

type packagesView struct {
	guestPage
	Packages []packageRow
}

func packageRows(words map[string]string, pkgs []struct {
	PackageID string         `json:"package_id"`
	Display   map[string]any `json:"display"`
}) []packageRow {
	rows := make([]packageRow, 0, len(pkgs))
	for _, p := range pkgs {
		name, _ := p.Display["name"].(string)
		if name == "" {
			name = words["pkg.default"]
		}
		var detail []string
		if d, ok := p.Display["down_kbps"].(float64); ok && d > 0 {
			detail = append(detail, speed(words, d))
		}
		if t, ok := p.Display["time_quota_seconds"].(float64); ok && t > 0 {
			detail = append(detail, humanSpan(words, time.Duration(t)*time.Second))
		}
		rows = append(rows, packageRow{ID: p.PackageID, Name: name, Detail: strings.Join(detail, " · ")})
	}
	return rows
}

// ---- the friendly failure page -----------------------------------------------------------------------------

type errorView struct {
	guestPage
	Title     string
	Message   string
	BackHref  string
	BackLabel string
}

var errorTmpl = template.Must(template.New("error").Parse(compactMarkup(errorHTML)))

// renderGuestError draws the branded, translated failure page for a sign-in flow that has left the sign-in
// page (the social provider's leg). The status code is the caller's and is unchanged; only what the guest
// reads is.
func (h *handler) renderGuestError(w http.ResponseWriter, r *http.Request, status int, key string) {
	h.renderGuestNotice(w, r, status, "errpage.title", key, "/", "errpage.back")
}

// renderGuestNotice is the same page with its own heading and way back, for a guest who is past sign-in (the
// status page when the appliance cannot answer, a disconnect that could not tell which device asked).
func (h *handler) renderGuestNotice(w http.ResponseWriter, r *http.Request, status int, titleKey, key, backHref, backKey string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	w.WriteHeader(status)
	_ = errorTmpl.Execute(w, errorView{
		guestPage: p, Title: p.T[titleKey], Message: p.T[key], BackHref: backHref, BackLabel: p.T[backKey],
	})
}

// ---- connection status -------------------------------------------------------------------------------------

type statusView struct {
	guestPage
	HasTime    bool
	TimeLeft   string
	HardExpiry string
	BackHref   string
}

var statusTmpl = template.Must(template.New("status").Parse(compactMarkup(statusHTML)))

// scdStatus is the part of scd's /v1/sessions/status answer the page reads. scd sends ip, session_id and
// active always, and the three online-time fields only for a package with an online-time allowance.
type scdStatus struct {
	SessionID              string   `json:"session_id"`
	Active                 bool     `json:"active"`
	TimeMode               string   `json:"time_mode"`
	RemainingOnlineSeconds *float64 `json:"remaining_online_seconds"`
	HardExpiry             string   `json:"hard_expiry"`
}

// onlinePageHref is the way back to "You're online": the page the guest came from, rebuilt from the two values
// the Status link carries (the session, and the duration that page was showing), or from scd's session id.
func onlinePageHref(r *http.Request, sessionID string) string {
	q := url.Values{}
	s := r.URL.Query().Get("s")
	if sessionID != "" {
		s = sessionID
	}
	if s == "" {
		return "/"
	}
	q.Set("s", s)
	if t := r.URL.Query().Get("t"); t != "" && strings.Trim(t, "0123456789") == "" {
		q.Set("t", t)
	}
	return "/success?" + q.Encode()
}

func (h *handler) renderStatus(w http.ResponseWriter, r *http.Request, st scdStatus) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	v := statusView{guestPage: p, BackHref: onlinePageHref(r, st.SessionID)}
	if st.TimeMode == "AGGREGATE_ONLINE_TIME" && st.RemainingOnlineSeconds != nil {
		v.HasTime = true
		v.TimeLeft = humanSpan(p.T, time.Duration(*st.RemainingOnlineSeconds)*time.Second)
		if v.TimeLeft == "" {
			v.TimeLeft = p.T["tl.none"]
		}
		v.HardExpiry = st.HardExpiry
	}
	_ = statusTmpl.Execute(w, v)
}

// wantsHTML reports whether a request is a browser navigating rather than a script asking for data: it
// prefers text/html to application/json. A fetch() with no Accept header sends */*, which prefers neither and
// so keeps the JSON it has always had; a browser's navigation lists text/html first.
func wantsHTML(r *http.Request) bool {
	var html, jsonQ float64 = -1, -1
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		fields := strings.Split(part, ";")
		mt := strings.ToLower(strings.TrimSpace(fields[0]))
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if strings.HasPrefix(f, "q=") {
				if _, err := fmt.Sscanf(f[2:], "%g", &q); err != nil {
					q = 0
				}
			}
		}
		switch mt {
		case "text/html", "application/xhtml+xml":
			if q > html {
				html = q
			}
		case "application/json":
			if q > jsonQ {
				jsonQ = q
			}
		}
	}
	return html > 0 && html > jsonQ
}
