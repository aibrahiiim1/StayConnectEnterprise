package api

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// Error codes are stable: the UI and external API consumers branch on them.
const (
	CodeUnauthenticated = "unauthenticated"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeConflict        = "conflict"
	CodeBadRequest      = "bad_request"
	CodeInternal        = "internal"
)

// Fail writes {error, message, trace_id} plus any extras merged into the top-level object.
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

func TraceID(r *http.Request) string {
	if r == nil {
		return ""
	}
	return middleware.GetReqID(r.Context())
}
