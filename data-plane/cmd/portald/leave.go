package main

// Sign-out is a deliberate act of this device's own page: every Disconnect form carries a "leave" token, an HMAC of the device address under a process-local key, so a page elsewhere cannot forge a sign-out. It survives the 307 an HTTP form gets when HTTPS comes up, which an Origin check would not.
// A POST without a valid token signs nobody out and gets a page asking again with a fresh token.

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
		return nil
	}
	return b
}()

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
