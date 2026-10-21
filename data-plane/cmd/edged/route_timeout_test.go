package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// N-3: the network applies wait longer than every other request; the rest keep the short bound.
func TestRouteTimeoutGivesNetworkAppliesTheirOwnBound(t *testing.T) {
	var sawDeadline time.Duration
	h := routeTimeout(50*time.Millisecond, 5*time.Second)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d, _ := r.Context().Deadline()
		sawDeadline = time.Until(d)
		w.WriteHeader(http.StatusOK)
	}))
	for path, long := range map[string]bool{
		"/edge/v1/network/apply":                    true,
		"/edge/v1/network/validate":                 true,
		"/edge/v1/network/system/apply":             true,
		"/edge/v1/network/system/confirm":           true,
		"/edge/v1/network/system/rollback":          true,
		"/edge/v1/network/revisions/abc-1/confirm":  true,
		"/edge/v1/network/revisions/abc-1/rollback": true,
		"/edge/v1/network/guest-networks":           false,
		"/edge/v1/network/apply/extra":              false,
		"/edge/v1/operators":                        false,
		// the requests that commit to the erasure ledger (two copies, one of them remote) get the ledger's bound
		"/edge/v1/guest-data-rights/erase":               true,
		"/edge/v1/guest-data-rights/assisted/abc/erase":  true,
		"/edge/v1/guest-data-rights/assisted/abc/close":  true,
		"/edge/v1/guest-data-rights/register/abc/answer": true,
		"/edge/v1/guest-data-rights/export":              false,
		"/edge/v1/guest-data-rights/assisted":            false,
		"/edge/v1/guest-data-rights/register/abc/extend": false,
	} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, path, nil))
		if long && sawDeadline < time.Second {
			t.Errorf("%s got the short bound (%v)", path, sawDeadline)
		}
		if !long && sawDeadline > time.Second {
			t.Errorf("%s got the long bound (%v)", path, sawDeadline)
		}
	}
	// A GET of an apply path is not an apply.
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/edge/v1/network/apply", nil))
	if sawDeadline > time.Second {
		t.Error("a GET got the long bound")
	}
}
