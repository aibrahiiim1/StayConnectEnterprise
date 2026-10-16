package main

// EVERY PORTAL COOKIE IS SET IN ONE PLACE (audit P-17).
//
// The portal's cookies are all HttpOnly, SameSite=Lax and site-wide. Over TLS each one is now also Secure: a
// cookie set on the HTTPS portal must never be sent back over the HTTP fallback in clear text. Over plain HTTP it
// cannot be Secure -- a captive device reaches the fallback before it has any access at all, and a Secure cookie
// would simply never be returned there -- which is why the credentials kept in cookies are bound server-side to
// the device that holds them (og_client, og_resume) or live for minutes (sc_commerce).

import (
	"net/http"
	"time"
)

// setPortalCookie sets one portal cookie: HttpOnly, SameSite=Lax, Path=/, and Secure when the request came over TLS.
// maxAge > 0 bounds it; maxAge < 0 deletes it.
func setPortalCookie(w http.ResponseWriter, r *http.Request, name, value string, maxAge time.Duration) {
	c := &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: r != nil && r.TLS != nil}
	switch {
	case maxAge < 0:
		c.MaxAge = -1
	case maxAge > 0:
		c.MaxAge = int(maxAge / time.Second)
		c.Expires = time.Now().Add(maxAge)
	}
	http.SetCookie(w, c)
}

// clearPortalCookie deletes one portal cookie.
func clearPortalCookie(w http.ResponseWriter, r *http.Request, name string) {
	setPortalCookie(w, r, name, "", -1)
}
