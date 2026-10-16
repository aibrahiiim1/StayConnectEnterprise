package main

// SIGN-OUT IS A DELIBERATE ACT OF THIS DEVICE'S OWN PAGE (audit P-8).
//
// /logout ends the session of whatever device the connection belongs to. It used to accept any POST, so any web
// page a guest opened -- on the portal or, once online, anywhere on the Internet that could reach it -- could
// submit a form to it and sign the device out. Every Disconnect / Sign out form the portal renders now carries a
// "leave" token: an HMAC of the device's address under a key that exists only in this process. A page elsewhere
// cannot compute it, and it survives the 307 an HTTP form gets when HTTPS comes up (the body is kept), which an
// Origin check would not (the redirected request's Origin is "null").
//
// A POST without a valid token -- a stale page from before a restart, or a forged one -- signs nobody out. It is
// answered with a page that asks, with a fresh token, so a real client who tapped Disconnect is never stuck.

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"html/template"
	"net"
	"net/http"
)

var leaveKey = func() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// No randomness: no token can ever validate, so every sign-out asks once more. Safe, never open.
		return nil
	}
	return b
}()

// leaveToken is the sign-out token for the device at ip ("" when it cannot be made).
func leaveToken(ip net.IP) string {
	if ip == nil || len(leaveKey) == 0 {
		return ""
	}
	m := hmac.New(sha256.New, leaveKey)
	m.Write([]byte("onegate-portal-leave:v1\x00" + ip.String()))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:18])
}

func validLeaveToken(ip net.IP, got string) bool {
	want := leaveToken(ip)
	return want != "" && got != "" && hmac.Equal([]byte(want), []byte(got))
}

type leaveView struct {
	guestPage
}

var leaveTmpl = template.Must(template.New("leave").Parse(compactMarkup(leaveHTML)))

// renderLeaveConfirm asks the client to confirm a sign-out that arrived without this device's token.
func (h *handler) renderLeaveConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	nonce := setPortalCSP(w)
	p := h.newGuestPage(r, nonce)
	w.WriteHeader(http.StatusForbidden)
	_ = leaveTmpl.Execute(w, leaveView{guestPage: p})
}

const leaveHTML = guestHead + `
<title>{{index .T "leave.title"}}</title>
</head><body>
<div class="page">` + guestChrome + `
<main class="card card--page">` + guestBrandblock + `
<div class="sc-body">
  <h1 class="page-title" id="leave-title">{{index .T "leave.title"}}</h1>
  <p class="page-lead">{{index .T "leave.lead"}}</p>
  <div class="actions">
    <form method="POST" action="/logout"><input type="hidden" name="leave" value="{{.Leave}}"><button class="btn" type="submit">{{index .T "online.disconnect"}}</button></form>
    <a class="btn btn--outline" href="/">{{index .T "errpage.back"}}</a>
  </div>
</div>` + guestFoot + `
</main>
</div>` + guestScripts + `
</body></html>`
