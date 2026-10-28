package main

import "net/http"

// Request body ceilings. A guest body is forwarded by portald from what a guest sent, so it is small and bounded; an
// admin body comes from edged and can carry a licence, an offline package or a ledger batch (the largest, 4 MiB).
// Handlers keep their own tighter limits; this is the backstop for the many that decode with no limit.
const (
	maxGuestBody = 64 << 10
	maxAdminBody = 4 << 20
)

// bodyLimit caps every request body with http.MaxBytesReader, which also closes the connection on an oversized body.
func bodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Body != http.NoBody {
			limit := int64(maxAdminBody)
			if classifyRoute(r.URL.Path) == guestRoute {
				limit = maxGuestBody
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}
