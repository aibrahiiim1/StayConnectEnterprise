package main

// Protective bounds on what one guest device may hold open. Every guest network reaches portald before sign-in, so
// without them a device trickling bodies or parking keep-alive connections held memory and file descriptors until no
// guest could load the sign-in page (audit P5 M1). Protocol safety bounds, not hotel settings.

import (
	"net/http"
	"regexp"
	"time"
)

const (
	// portalReadTimeout bounds reading a whole request; a sign-in body is a few hundred bytes.
	portalReadTimeout = 15 * time.Second
	// portalWriteTimeout covers the request timeout (timeout.go) plus margin to write its "timed out" page.
	portalWriteTimeout   = portalRequestTimeout + 10*time.Second
	portalIdleTimeout    = 60 * time.Second
	portalHeaderTimeout  = 5 * time.Second
	portalMaxHeaderBytes = 16 << 10
	// portalMaxBodyBytes is far above any guest form or JSON body the portal accepts.
	portalMaxBodyBytes = 64 << 10

	scdMaxIdleConns    = 32
	scdIdleConnTimeout = 90 * time.Second
)

// newPortalServer is the one constructor for both listeners, so neither can be left without the bounds.
func newPortalServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: portalHeaderTimeout,
		ReadTimeout:       portalReadTimeout,
		WriteTimeout:      portalWriteTimeout,
		IdleTimeout:       portalIdleTimeout,
		MaxHeaderBytes:    portalMaxHeaderBytes,
	}
}

// limitGuestBodies refuses a declared oversize body before any handler runs and caps an undeclared one while it is read.
func limitGuestBodies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ContentLength > portalMaxBodyBytes {
			w.Header().Set("Connection", "close")
			http.Error(w, http.StatusText(http.StatusRequestEntityTooLarge), http.StatusRequestEntityTooLarge)
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, portalMaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// contentAddressedAsset matches the names edged gives uploads (a SHA-256 prefix and the type's extension), so a name
// always means the same bytes and a browser may keep it (perf D3-L2).
var contentAddressedAsset = regexp.MustCompile(`^[0-9a-f]{16}\.[a-z0-9]{2,5}$`)

const immutableCacheControl = "public, max-age=31536000, immutable"

type assetCacheWriter struct {
	http.ResponseWriter
}

func (a assetCacheWriter) WriteHeader(code int) {
	if code == http.StatusOK || code == http.StatusNotModified {
		a.Header().Set("Cache-Control", immutableCacheControl)
	}
	a.ResponseWriter.WriteHeader(code)
}

func (a assetCacheWriter) Unwrap() http.ResponseWriter { return a.ResponseWriter }

// immutableAssets marks a served content-addressed upload cacheable; a miss or any other name is answered as before.
func immutableAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentAddressedAsset.MatchString(r.URL.Path) {
			w = assetCacheWriter{w}
		}
		next.ServeHTTP(w, r)
	})
}
