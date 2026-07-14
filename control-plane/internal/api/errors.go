package api

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// Standard machine-readable error codes. Keep this list stable — the UI and
// external API consumers branch on these.
const (
	CodeUnauthenticated = "unauthenticated"
	CodeForbidden       = "forbidden"
	CodePaymentRequired = "payment_required"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeBadRequest      = "bad_request"
	CodeLimitExceeded   = "limit_exceeded"
	CodeBadGateway      = "bad_gateway"
	CodeInternal        = "internal"
)

// Fail writes an error response in the standard envelope:
//
//	{
//	  "error":    "<machine_code>",      // stable, branch on this
//	  "message":  "<human-readable>",    // show to users
//	  "trace_id": "<request id>",        // matches X-Trace-Id header
//	  <extras>                           // flat extras (e.g. limit_key, limit, current)
//	}
//
// Extras are merged into the top-level object; pass one or more maps to include
// structured context (used by limit_exceeded responses).
func Fail(w http.ResponseWriter, r *http.Request, status int, code, message string, extras ...map[string]any) {
	body := map[string]any{
		"error":    code,
		"message":  message,
		"trace_id": TraceID(r),
	}
	for _, ex := range extras {
		for k, v := range ex {
			body[k] = v
		}
	}
	WriteJSON(w, status, body)
}

// TraceID returns the chi-assigned request id (also mirrored in X-Trace-Id).
func TraceID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return middleware.GetReqID(r.Context())
}
