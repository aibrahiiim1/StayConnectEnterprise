package main

// A failed read is not an empty one: pgx reports a permission failure on the first Next() rather than at
// Query(), which once reached the operator as HTTP 200 and "No audit entries". The handler must tell "nothing
// happened" from "I could not look".

import (
	"strings"
	"testing"
)

func TestTheAuditReadDistinguishesEmptyFromFailed(t *testing.T) {
	src := readSourceFile(t, "resources_site.go")
	i := strings.Index(src, "func (s *server) auditRoutes()")
	if i < 0 {
		t.Fatal("auditRoutes not found")
	}
	body := src[i:]
	if end := strings.Index(body, "\n\treturn r\n}"); end > 0 {
		body = body[:end]
	}

	if !strings.Contains(body, "rows.Err()") {
		t.Error("the audit read does not check rows.Err(); a failed read is reported as no entries")
	}
	if !strings.Contains(body, "the audit trail could not be read") {
		t.Error("the failure message does not say what could not be done")
	}
	if !strings.Contains(body, "slog.Error(\"audit read failed\"") {
		t.Error("an audit read failure is not logged, so it cannot be diagnosed from the appliance")
	}
	if !strings.Contains(body, "out := []auditRow{}") {
		t.Error("the empty result is a nil slice; it should be an empty list")
	}
}

// With Phase 6 off an unregistered /sessions/aggregate-time fell through to /sessions/{id} and 500ed comparing
// a uuid with the literal string; a disabled feature must answer for itself.
func TestADisabledSessionsRouteAnswersForItself(t *testing.T) {
	src := readSourceFile(t, "resources_sessions.go")
	i := strings.Index(src, "func (s *server) sessionsRoutes()")
	if i < 0 {
		t.Fatal("sessionsRoutes not found")
	}
	body := src[i:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	if !strings.Contains(body, "} else {") || !strings.Contains(body, "not_enabled") {
		t.Error("aggregate-time is unregistered when off, so /sessions/{id} swallows it and 500s")
	}
	off := strings.Index(body, "not_enabled")
	id := strings.Index(body, `r.Get("/{id}"`)
	if off < 0 || id < 0 || off > id {
		t.Error("the disabled-route answer must be registered before the {id} pattern")
	}
}
