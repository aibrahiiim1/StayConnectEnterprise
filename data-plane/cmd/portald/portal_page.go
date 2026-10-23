package main

// Presentation only: language, the hotel's design and translation keys for server-rendered guest pages.
// The handlers decide everything else.

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// offeredLanguages: the hotel's configured languages, else the six shipped; same rule as the sign-in page's script.
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

// rtlCode covers languages the portal does not ship (a hotel may add Hebrew or Persian with its own words).
func rtlCode(code string) bool {
	switch strings.SplitN(strings.ToLower(code), "-", 2)[0] {
	case "ar", "he", "fa", "ur":
		return true
	}
	return false
}

// guestLanguage: the sc-lang cookie, then Accept-Language in its own order, then English.
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

// guestWords layers English, the shipped language, then the hotel's wording; a blank override is ignored.
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

// guestBrand holds values already validated by portaldesign.ForGuests.
type guestBrand struct {
	Name, Welcome, Help string
	// Logo was checked by ForGuests; template.URL keeps html/template from replacing a data:image logo.
	Logo                                          template.URL
	Template, Density, Panel, HeroHeight, Surface string
	HeroLogo, CardLogo                            bool
	// Style: validated values only; see cssURL for the one that is not a token.
	Style template.CSS
}

func brandFor(d map[string]any) guestBrand {
	b := guestBrand{Template: portaldesign.TemplateID(d)}
	str := func(k string) string { s, _ := d[k].(string); return s }
	b.Name, b.Welcome, b.Help, b.Logo = str("hotel_name"), str("welcome_text"), str("help_text"), template.URL(str("logo_url"))
	var style []string
	set := func(prop, v string) {
		if v != "" {
			style = append(style, prop+":"+v)
		}
	}
	set("--sc-brand", str("brand_color"))
	set("--sc-brand-dark", str("brand_color_dark"))
	set("--sc-radius", str("corner_radius"))
	set("font-family", str("font_family"))
	if v := str("background_url"); v != "" {
		set("--sc-bg", cssURL(v))
	}
	// --sc-hero defaults to var(--sc-bg) in the stylesheet, so a background photograph is not written twice.
	if v := str("hero_image_url"); v != "" {
		set("--sc-hero", cssURL(v))
	}
	photo := str("hero_image_url") != "" || str("background_url") != ""
	overlay := defaultOverlay
	overlaySet := false
	if o, ok := d["template_options"].(map[string]any); ok {
		pick := func(k string) string { s, _ := o[k].(string); return s }
		b.Density, b.Panel, b.HeroHeight, b.Surface = pick("density"), pick("panel_position"), pick("hero_height"), pick("surface")
		if n, ok := number(o["overlay"]); ok && n >= 0 && n <= 90 {
			overlay = n / 100
			overlaySet = true
		}
		set("--sc-heading-font", pick("heading_font"))
	}
	// Always darken enough under white text over a photograph, including the stylesheet default, which is below the floor.
	if photo && overlay < minPhotoOverlay {
		overlay = minPhotoOverlay
		overlaySet = true
	}
	if overlaySet {
		set("--sc-overlay", fmt.Sprintf("%.2f", overlay))
	}
	// Text colours; --sc-ink is the hotel's text colour only when it reads on the card.
	for _, kv := range derivedColours(str("brand_color"), str("brand_color_dark"), str("text_color"), overlay, photo) {
		set(kv[0], kv[1])
	}
	b.HeroLogo = b.Logo != "" && heroTemplates[b.Template]
	b.CardLogo = b.Logo != "" && !heroTemplates[b.Template]
	b.Style = template.CSS(strings.Join(style, ";"))
	return b
}

// heroTemplates show the logo in the hero rather than the card, so the page draws it only where its layout shows it.
var heroTemplates = map[string]bool{"split": true, "immersive": true, "editorial": true, "headerbar": true}

// cssURL percent-encodes characters that could end the string or function, so the value stays one url().
func cssURL(u string) string {
	r := strings.NewReplacer(`"`, "%22", `'`, "%27", `(`, "%28", `)`, "%29", `\`, "%5C", "\n", "", "\r", "", " ", "%20")
	return `url("` + r.Replace(u) + `")`
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

type guestPage struct {
	Lang      string
	Dir       string
	Nonce     string
	T         map[string]string // the whole dictionary for Lang, for {{index .T "key"}}
	JS        template.JS       // the part of it the page's own script reads, as JSON
	Languages []portalLanguage
	Brand     guestBrand
	// HotelSheet is the hotel's custom CSS (<style id="sc-hotel"> in @layer hotel) for pages after sign-in; the sign-in page's script applies it itself.
	HotelSheet template.HTML
	Leave      string
}

var reImportant = regexp.MustCompile(`(?i)!\s*important`)

// hotelSheet is the hotel's CSS from ForGuests, with !important stripped (it would let a layered rule outrank the
// unlayered guard) and wrapped in `@layer hotel`. A sheet that could still close its element is not served.
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
	p := guestPage{
		Lang: lang.Code, Dir: dir, Nonce: nonce, T: words, JS: jsData(subset(words, jsPrefixes...)),
		Languages: offeredLanguages(d), Brand: brandFor(d), HotelSheet: hotelSheet(d),
	}
	if r != nil {
		p.Leave = leaveToken(clientIP(r))
	}
	return p
}

func capRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

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

// serverMessageKeys maps each sentence a handler passes to landing() to its translation key.
var serverMessageKeys = map[string]string{
	"Please enter a voucher code.":             "err.voucher.empty",
	"Invalid voucher.":                         "err.voucher.invalid",
	msgVoucherDevices:                          "err.voucher.devices",
	"Please enter your username and password.": "err.account.empty",
	"Invalid username or password.":            "err.account.invalid",
	msgAccountDevices:                          "err.account.devices",
	guestRoomDeviceLimitMessage:                "err.room.devices",
	"This sign-in method is not available. Please contact the site team for assistance.": "err.method.disabled",
	"Too many attempts. Please wait a minute and try again.":                             "err.attempts",
	"The Wi-Fi network is at capacity. Please try again shortly.":                        "err.capacity",
	"Your device isn't connected to this Wi-Fi network.":                                 "err.device.network",
	"Unable to detect your device address.":                                              "err.device.detect",
	"Service unavailable. Please try again.":                                             "err.service",
	"Something went wrong. Please try again.":                                            "err.service",
	"We could not complete that. Please try again.":                                      "err.service",
	"Bad request.": "err.service",
	"Internet packages are not available right now. Please contact the site team for assistance.": "err.packages.off",
	"Internet packages are unavailable right now.":                                                "err.packages.off",
	"No internet packages are available for you right now.":                                       "err.packages.none",
	"That package is not available. Please choose another.":                                       "err.package.gone",
	"Please choose a package.":                                                                    "err.package.gone",
	"Please sign in again.":                                                                       "err.signin.again",
	"We could not connect your device.":                                                           "err.connect",
	"We could not bring your device online. Please try again in a moment.":                        "err.connect",
	msgAcctEmpty:    "err.acct.empty",
	msgAcctInvalid:  "err.acct.invalid",
	msgAcctTaken:    "err.acct.taken",
	msgAcctEmail:    "err.acct.email",
	msgAcctWeak:     "err.acct.weak",
	msgAcctMismatch: "err.acct.mismatch",
	msgAcctLimit:    "err.acct.limit",
}

// serverMessageKey maps every voucher refusal to the invalid-voucher message; an unknown sentence is shown as written
// (composed on the server, never by the guest).
func serverMessageKey(msg string) string {
	if k, ok := serverMessageKeys[msg]; ok {
		return k
	}
	if strings.HasPrefix(msg, "Voucher ") && strings.HasSuffix(msg, ".") {
		return "err.voucher.invalid"
	}
	return ""
}

// localMessage carries the key and English so the sign-in page can re-translate when the guest switches language.
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

// landingView: Error stays a plain string so a fixture rendering {{.Error}} still reads the message.
type landingView struct {
	guestPage
	Error, ErrorKey, ErrorEn string
	// ErrorForm names the form ("voucher", "account" or "email") the refusal answers; ErrorInvalid means the input was wrong, not the network.
	ErrorForm    string
	ErrorInvalid bool
	EmailState   string
	// What the guest typed, given back so a typo can be corrected rather than retyped. Never the password.
	VoucherValue, UsernameValue, EmailValue string
	ClientIP, ClientMAC                     string
	BrandName                               string // the hotel's name, or the neutral fallback in the guest's language
	Help                                    string
	HasExtras                               bool
	Terms                                   template.URL // checked by ForGuests (CheckLinkURL)
	// The hotel's privacy notice (GDPR Art. 13): its own text, shown in place, and/or a link to a full notice.
	PrivacyText string
	PrivacyURL  template.URL // checked by ForGuests (CheckLinkURL)
	// Shipped sign-in wording in every language, so switching needs no round trip.
	AllLanguages template.JS
	Strings      template.JS
	// Hotel wording and language list, so the first paint already uses them.
	HotelWords template.JS
	Configured template.JS
	// Welcome: a valid remembered-device credential; the page leads with one Connect button.
	Welcome *welcomeView
}

var afterSignInPrefixes = []string{"pkg.", "online.", "tl.", "dev.", "errpage."}

var (
	landingStringsOnce sync.Once
	landingStringsMap  map[string]map[string]string
	landingStringsJSON template.JS
)

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
		EmailState:   "signin",
	}
	if v.BrandName == "" {
		v.BrandName = p.T["brand.fallback"]
	}
	if errMsg != "" && r != nil && r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/auth/voucher":
			v.ErrorForm = "voucher"
			v.VoucherValue = capRunes(strings.TrimSpace(r.PostFormValue("code")), 32)
		case "/auth/credentials":
			v.ErrorForm = "account"
			v.UsernameValue = capRunes(strings.TrimSpace(r.PostFormValue("username")), 64)
		case "/auth/account/signin", "/auth/account/grace":
			v.ErrorForm = "email"
			v.EmailValue = capRunes(strings.TrimSpace(r.PostFormValue("email")), 254)
		case "/auth/account/register":
			v.ErrorForm, v.EmailState = "email", "create"
			v.EmailValue = capRunes(strings.TrimSpace(r.PostFormValue("email")), 254)
			// An already-registered address is answered on Sign in with the address filled in.
			if msg.Key == "err.acct.taken" {
				v.EmailState = "signin"
			}
		case "/auth/account/forgot":
			v.ErrorForm, v.EmailState = "email", "forgot"
			v.EmailValue = capRunes(strings.TrimSpace(r.PostFormValue("email")), 254)
		}
		switch msg.Key {
		case "err.voucher.empty", "err.voucher.invalid", "err.account.empty", "err.account.invalid",
			"err.acct.empty", "err.acct.invalid", "err.acct.email", "err.acct.weak", "err.acct.mismatch":
			v.ErrorInvalid = true
		}
	}
	v.Help, _ = d["help_text"].(string)
	html, _ := d["custom_html"].(string)
	v.HasExtras = v.Help != "" || html != ""
	if s, _ := d["terms_url"].(string); s != "" {
		v.Terms = template.URL(s)
	}
	v.PrivacyText, _ = d["privacy_text"].(string)
	if s, _ := d["privacy_url"].(string); s != "" {
		v.PrivacyURL = template.URL(s)
	}
	if tr, ok := d["translations"].(map[string]any); ok && len(tr) > 0 {
		v.HotelWords = jsData(tr)
	}
	if l, ok := d["languages"].([]any); ok && len(l) > 0 {
		v.Configured = jsData(l)
	}
	return v
}

type successView struct {
	guestPage
	Reminder        *onlineReminder
	SessionID       string
	DurationSeconds int
	HumanRemaining  string
	// Unlimited: access has no end. Neither it nor HumanRemaining is set when the appliance could not be asked.
	Unlimited  bool
	ReturnCode string
}

type packageRow struct{ ID, Name, Detail, Method, MethodKey, Price string }

type packagesView struct {
	guestPage
	Packages   []packageRow
	ReturnCode string
	Identity   string
	// FreeUsed: a free package was withheld only because this Client already had it (contract §6.1).
	FreeUsed   bool
	VoucherCTA bool
	// Error is why the last choice failed, shown above the choices: a refused acquisition returns to package selection, not sign-in.
	Error string
}

type guestPackage struct {
	PackageID        string         `json:"package_id"`
	Display          map[string]any `json:"display"`
	Methods          []string       `json:"methods"`
	PriceMinor       int64          `json:"price_minor"`
	Currency         string         `json:"currency"`
	CurrencyExponent int            `json:"currency_exponent"`
}

var methodKeys = map[string]string{
	"NOT_REQUIRED": "acq.connect", "ONLINE_PAYMENT": "acq.card", "PMS_POSTING": "acq.room", "PREPAID": "acq.connect",
}

// formatPrice renders minor units in the package currency, e.g. EGP 25.00; a free package reads "Free".
func formatPrice(words map[string]string, minor int64, currency string, exp int) string {
	if minor == 0 {
		return words["acq.free"]
	}
	if exp < 0 || exp > 4 {
		exp = 2
	}
	div := int64(1)
	for i := 0; i < exp; i++ {
		div *= 10
	}
	s := strconv.FormatInt(minor/div, 10)
	if exp > 0 {
		frac := strconv.FormatInt(minor%div, 10)
		for len(frac) < exp {
			frac = "0" + frac
		}
		s += "." + frac
	}
	return strings.TrimSpace(currency + " " + s)
}

func packageRows(words map[string]string, pkgs []guestPackage) []packageRow {
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
		methods := p.Methods
		if len(methods) == 0 {
			methods = []string{"NOT_REQUIRED"}
		}
		price := formatPrice(words, p.PriceMinor, p.Currency, p.CurrencyExponent)
		for _, m := range methods {
			key := methodKeys[m]
			if key == "" {
				continue
			}
			rows = append(rows, packageRow{ID: p.PackageID, Name: name, Detail: strings.Join(detail, " · "),
				Method: m, MethodKey: key, Price: price})
		}
	}
	return rows
}

type errorView struct {
	guestPage
	Title     string
	Message   string
	BackHref  string
	BackLabel string
}

var errorTmpl = template.Must(template.New("error").Parse(compactMarkup(errorHTML)))

// renderGuestError draws the branded failure page for a flow that left the sign-in page; the status code is the caller's.
func (h *handler) renderGuestError(w http.ResponseWriter, r *http.Request, status int, key string) {
	h.renderGuestNotice(w, r, status, "errpage.title", key, "/", "errpage.back")
}

// renderGuestNotice is the same page with its own heading and way back, for a guest past sign-in.
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

type statusView struct {
	guestPage
	HasTime    bool
	TimeLeft   string
	HardExpiry string
	BackHref   string
}

var statusTmpl = template.Must(template.New("status").Parse(compactMarkup(statusHTML)))

// scdStatus: scd always sends ip, session_id and active, and the online-time fields only for a package with an online-time allowance.
type scdStatus struct {
	SessionID              string   `json:"session_id"`
	Active                 bool     `json:"active"`
	TimeMode               string   `json:"time_mode"`
	RemainingOnlineSeconds *float64 `json:"remaining_online_seconds"`
	HardExpiry             string   `json:"hard_expiry"`
	AccessEndsAt           string   `json:"access_ends_at"`
	// OnlineTimeAllowance: remaining_online_seconds is a time allowance to show.
	OnlineTimeAllowance bool `json:"online_time_allowance"`
}

// remaining is the earliest of online time left, hard expiry and calendar end; nil when none (access with no end).
func (st scdStatus) remaining(now time.Time) *time.Duration {
	var out *time.Duration
	take := func(d time.Duration) {
		if d < 0 {
			d = 0
		}
		if out == nil || d < *out {
			out = &d
		}
	}
	if st.RemainingOnlineSeconds != nil && *st.RemainingOnlineSeconds >= 0 && !math.IsInf(*st.RemainingOnlineSeconds, 0) {
		take(time.Duration(*st.RemainingOnlineSeconds * float64(time.Second)))
	}
	for _, at := range []string{st.HardExpiry, st.AccessEndsAt} {
		if t, err := time.Parse(time.RFC3339, at); err == nil {
			take(t.Sub(now))
		}
	}
	return out
}

func (h *handler) deviceStatus(ctx context.Context, ip net.IP) (scdStatus, bool) {
	var st scdStatus
	if ip == nil || h.scd == nil {
		return st, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix/v1/sessions/status?ip="+ip.String(), nil)
	if err != nil {
		return st, false
	}
	resp, err := h.scd.Do(req)
	if err != nil {
		return st, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st) != nil {
		return scdStatus{}, false
	}
	return st, true
}

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
	if (st.TimeMode == "AGGREGATE_ONLINE_TIME" || st.OnlineTimeAllowance) && st.RemainingOnlineSeconds != nil {
		v.HasTime = true
		v.TimeLeft = humanSpan(p.T, time.Duration(*st.RemainingOnlineSeconds)*time.Second)
		if v.TimeLeft == "" {
			v.TimeLeft = p.T["tl.none"]
		}
		v.HardExpiry = st.HardExpiry
	}
	_ = statusTmpl.Execute(w, v)
}

// wantsHTML: a browser navigation prefers text/html; a fetch() with no Accept header sends */* and keeps JSON.
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
