package main

import (
	"net/http"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// defaultBodyLimit caps every request body edged reads. Without it any signed-in operator could make edged hold a
// multi-gigabyte JSON string in memory (backend audit P9 M1). Handlers that read less keep their own tighter limit.
const defaultBodyLimit int64 = 1 << 20

// bodyLimitFor returns the ceiling for one request path. Only the routes that legitimately carry more than 1 MiB
// get more: portal image uploads, portal designs and the offline licence package.
func bodyLimitFor(path string) int64 {
	switch {
	case strings.Contains(path, "/portal-assets"):
		return maxAssetBytes + 64<<10
	case strings.Contains(path, "/branding"), strings.Contains(path, "/portal-branding"):
		return portaldesign.MaxBodyBytes
	case strings.HasSuffix(path, "/central/offline-package"):
		return 2<<20 + 1
	}
	return defaultBodyLimit
}

// noStore marks every Admin Console API answer uncacheable. Generated passwords, voucher codes and new account
// credentials are returned in ordinary responses; one header on all of them is simpler and safer than
// remembering it per handler (audit P9 L2). A handler may still set a stricter value.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// limitRequestBody refuses a declared over-size body with 413 before any handler runs, and caps an undeclared
// (chunked) one so the read fails at the limit instead of buffering it.
func limitRequestBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := bodyLimitFor(r.URL.Path)
		if r.ContentLength > limit {
			jsonErr(w, http.StatusRequestEntityTooLarge, "body_too_large", "the request body is too large")
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
