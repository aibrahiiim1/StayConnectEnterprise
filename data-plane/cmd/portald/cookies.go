package main

// Every portal cookie is set here: HttpOnly, SameSite=Lax, and Secure over TLS so it is never sent back over the HTTP fallback.
// On plain HTTP it cannot be Secure (captive devices reach the fallback first), so cookie credentials are device-bound server-side or short-lived.

import (
	"net/http"
	"time"
)

// setPortalCookie: maxAge > 0 bounds it, maxAge < 0 deletes it.
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

func clearPortalCookie(w http.ResponseWriter, r *http.Request, name string) {
	setPortalCookie(w, r, name, "", -1)
}
