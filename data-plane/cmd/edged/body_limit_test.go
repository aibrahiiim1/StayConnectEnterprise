package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// P9 M1: a 2 MB body to an ordinary write route is refused; the routes that carry larger payloads still pass.
func TestRequestBodiesAreBounded(t *testing.T) {
	var decodeErr error
	h := limitRequestBody(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		decodeErr = decodeJSON(r, &v)
		w.WriteHeader(http.StatusNoContent)
	}))
	big := `{"name":"` + strings.Repeat("a", 2<<20) + `"}`

	// Declared length over the limit: 413 before the handler.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("PATCH", "/edge/v1/guest-accounts/x", strings.NewReader(big)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared 2 MB body: status %d, want 413", rec.Code)
	}

	// Undeclared (chunked) length: the read stops at the limit and the decode fails.
	req := httptest.NewRequest("PATCH", "/edge/v1/guest-accounts/x", io.NopCloser(strings.NewReader(big)))
	req.ContentLength = -1
	decodeErr = nil
	h.ServeHTTP(httptest.NewRecorder(), req)
	if decodeErr == nil {
		t.Fatal("a chunked 2 MB body must not decode")
	}

	// A normal body still decodes.
	decodeErr = nil
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("PATCH", "/edge/v1/guest-accounts/x", strings.NewReader(`{"a":1}`)))
	if decodeErr != nil {
		t.Fatalf("small body: %v", decodeErr)
	}

	// Larger payload routes keep their own ceilings.
	for path, want := range map[string]int64{
		"/edge/v1/portal-assets":                maxAssetBytes + 64<<10,
		"/edge/v1/portal-branding/draft":        2 << 20,
		"/edge/v1/portal-profiles/p1/branding/": 2 << 20,
		"/edge/v1/central/offline-package":      2<<20 + 1,
		"/edge/v1/operators":                    defaultBodyLimit,
	} {
		if got := bodyLimitFor(path); got != want {
			t.Errorf("bodyLimitFor(%q) = %d, want %d", path, got, want)
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/edge/v1/portal-assets", bytes.NewReader(make([]byte, 3<<20))))
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatal("a 3 MB portal image upload must reach its handler")
	}
}
