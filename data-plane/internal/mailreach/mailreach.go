// Package mailreach lists the host names a client's device must reach, BEFORE it has signed in, to read a
// one-time code in its mail app or webmail.
//
// THE PROBLEM IT ANSWERS. A client asks for an email code on the captive portal, opens Mail, and the phone has
// no internet yet: the code cannot arrive unless the mail service itself is reachable through the walled
// garden. A client with mobile data switches to it; one without -- a visitor from abroad, a tablet -- is stuck.
// Each preset is the documented minimum set of hosts for that service's apps and webmail (sign-in, API, mail
// transport, static assets). The Admin Console offers the presets as switches and lets the site add host names
// for a service the presets do not know. The host names open only while the Email code method is on (scd's
// walled-garden reconcile), and a device still cannot reach anything else.
//
// A HOST NAME, NEVER A WILDCARD. The garden opens the addresses a NAME resolves to, so every entry is a host a
// mail app actually contacts. Services change their hosts; a site adds what changed under Sign-in methods.
package mailreach

import (
	"sort"
	"strings"
)

// Provider is one preset the Admin Console offers.
type Provider struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

// Providers are the presets, in the order the Admin Console lists them.
var Providers = []Provider{
	{ID: "gmail", Name: "Gmail / Google Workspace", Domains: []string{
		"mail.google.com", "gmail.com", "www.gmail.com", "googlemail.com", "www.googlemail.com", "inbox.google.com",
		"accounts.google.com", "accounts.youtube.com", "oauth2.googleapis.com", "www.googleapis.com", "gmail.googleapis.com",
		"people-pa.googleapis.com", "clients1.google.com", "clients2.google.com", "clients3.google.com", "clients4.google.com",
		"clients5.google.com", "clients6.google.com", "android.clients.google.com", "play.googleapis.com", "mtalk.google.com",
		"alt1-mtalk.google.com", "alt2-mtalk.google.com", "alt3-mtalk.google.com", "alt4-mtalk.google.com",
		"ssl.gstatic.com", "www.gstatic.com", "fonts.gstatic.com", "fonts.googleapis.com", "apis.google.com",
		"lh3.googleusercontent.com", "www.google.com", "ogs.google.com", "imap.gmail.com", "smtp.gmail.com", "pop.gmail.com",
	}},
	{ID: "outlook", Name: "Outlook / Hotmail / Live / Microsoft 365", Domains: []string{
		"outlook.live.com", "outlook.office.com", "outlook.office365.com", "www.outlook.com", "outlook.com", "hotmail.com",
		"www.hotmail.com", "live.com", "www.live.com", "login.live.com", "login.microsoftonline.com", "login.microsoft.com",
		"account.live.com", "account.microsoft.com", "substrate.office.com", "attachments.office.net", "res.cdn.office.net",
		"r4.res.office365.com", "r1.res.office365.com", "acctcdn.msauth.net", "logincdn.msauth.net", "aadcdn.msauth.net",
		"aadcdn.msftauth.net", "logincdn.msftauth.net", "imap-mail.outlook.com", "smtp-mail.outlook.com", "pop-mail.outlook.com",
		"outlook.office365.com", "autodiscover-s.outlook.com", "graph.microsoft.com", "wns.windows.com", "client.wns.windows.com",
		"ecs.office.com", "config.edge.skype.com", "mobile.events.data.microsoft.com", "prod.outlookmobile.com",
	}},
	{ID: "yahoo", Name: "Yahoo Mail", Domains: []string{
		"mail.yahoo.com", "www.yahoo.com", "yahoo.com", "login.yahoo.com", "api.login.yahoo.com", "guce.yahoo.com",
		"consent.yahoo.com", "s.yimg.com", "l.yimg.com", "mg.mail.yahoo.com", "data.mail.yahoo.com", "apis.mail.yahoo.com",
		"api.mail.yahoo.com", "edge.gycpi.b.yahoodns.net", "imap.mail.yahoo.com", "smtp.mail.yahoo.com", "pop.mail.yahoo.com",
		"yep.video.yahoo.com", "geo.yahoo.com", "udc.yahoo.com", "mobile.yahoo.com", "m.yahoo.com",
	}},
	{ID: "icloud", Name: "iCloud Mail (Apple)", Domains: []string{
		"www.icloud.com", "icloud.com", "mail.icloud.com", "setup.icloud.com", "gateway.icloud.com", "idmsa.apple.com",
		"appleid.apple.com", "appleid.cdn-apple.com", "gsa.apple.com", "gsas.apple.com", "p01-mailws.icloud.com",
		"p02-mailws.icloud.com", "p03-mailws.icloud.com", "p04-mailws.icloud.com", "p05-mailws.icloud.com",
		"imap.mail.me.com", "smtp.mail.me.com", "www.apple.com", "apple.com", "static.cdn-apple.com", "cvws.icloud-content.com",
		"init.push.apple.com", "courier.push.apple.com", "1-courier.push.apple.com", "2-courier.push.apple.com",
		"3-courier.push.apple.com", "4-courier.push.apple.com", "gateway.push.apple.com",
	}},
	{ID: "proton", Name: "Proton Mail", Domains: []string{
		"mail.proton.me", "account.proton.me", "proton.me", "www.proton.me", "mail-api.proton.me", "api.protonmail.ch",
		"protonmail.com", "mail.protonmail.com", "account-api.proton.me", "proton-api.ch", "mail.proton.black",
	}},
	{ID: "zoho", Name: "Zoho Mail", Domains: []string{
		"mail.zoho.com", "accounts.zoho.com", "www.zoho.com", "zoho.com", "mail.zoho.eu", "accounts.zoho.eu", "mail.zoho.in",
		"accounts.zoho.in", "static.zohocdn.com", "js.zohocdn.com", "imap.zoho.com", "smtp.zoho.com", "imap.zoho.eu", "smtp.zoho.eu",
	}},
	{ID: "aol", Name: "AOL Mail", Domains: []string{
		"mail.aol.com", "www.aol.com", "aol.com", "login.aol.com", "api.login.aol.com", "guce.aol.com", "s.aolcdn.com",
		"imap.aol.com", "smtp.aol.com", "pop.aol.com", "s.yimg.com",
	}},
	{ID: "gmx", Name: "GMX / WEB.DE / mail.com", Domains: []string{
		"www.gmx.net", "gmx.net", "www.gmx.com", "gmx.com", "navigator.gmx.net", "login.gmx.net", "www.web.de", "web.de",
		"navigator.web.de", "login.web.de", "www.mail.com", "mail.com", "navigator.mail.com", "login.mail.com",
		"uim.gmx.net", "uim.web.de", "imap.gmx.net", "mail.gmx.net", "imap.web.de", "smtp.web.de", "imap.mail.com", "smtp.mail.com",
	}},
	{ID: "yandex", Name: "Yandex Mail", Domains: []string{
		"mail.yandex.com", "mail.yandex.ru", "passport.yandex.com", "passport.yandex.ru", "yandex.com", "yandex.ru",
		"yastatic.net", "mail.yandex.com.tr", "imap.yandex.com", "smtp.yandex.com", "api.passport.yandex.ru",
	}},
	{ID: "mailru", Name: "Mail.ru", Domains: []string{
		"mail.ru", "e.mail.ru", "account.mail.ru", "auth.mail.ru", "portal.mail.ru", "imgsmail.ru", "imap.mail.ru", "smtp.mail.ru",
	}},
}

// ByID returns a preset.
func ByID(id string) (Provider, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// Domains expands the chosen presets and the site's own host names into one sorted, de-duplicated list of
// host names. Unknown preset ids and invalid host names are skipped, never an error: the garden must not
// stop reconciling because one stored value went stale.
func Domains(providers, custom []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] || !ValidHost(d) {
			return
		}
		seen[d] = true
		out = append(out, d)
	}
	for _, id := range providers {
		if p, ok := ByID(id); ok {
			for _, d := range p.Domains {
				add(d)
			}
		}
	}
	for _, d := range custom {
		add(d)
	}
	sort.Strings(out)
	return out
}

// MaxCustomDomains bounds the site's own list.
const MaxCustomDomains = 50

// ValidHost accepts a fully-qualified host name: labels of letters, digits and hyphens, at least one dot, no
// wildcard, no scheme, no path, no port, at most 253 characters.
func ValidHost(h string) bool {
	h = strings.TrimSpace(h)
	if len(h) < 3 || len(h) > 253 || !strings.Contains(h, ".") || strings.HasPrefix(h, ".") || strings.HasSuffix(h, ".") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	// A bare public suffix ("com", "co.uk") is not a host; the last label must be alphabetic (no IP literals).
	last := h[strings.LastIndex(h, ".")+1:]
	for _, r := range last {
		if r >= '0' && r <= '9' {
			return false
		}
	}
	return true
}

// Validate checks a site's chosen presets and custom host names as the Admin Console saves them.
func Validate(providers, custom []string) (bad string, ok bool) {
	for _, id := range providers {
		if _, known := ByID(id); !known {
			return id, false
		}
	}
	if len(custom) > MaxCustomDomains {
		return "too many", false
	}
	for _, d := range custom {
		if !ValidHost(d) {
			return d, false
		}
	}
	return "", true
}
