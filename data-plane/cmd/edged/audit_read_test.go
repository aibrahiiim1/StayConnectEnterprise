package main

// A FAILED READ IS NOT AN EMPTY ONE.
//
// The PRE-LIVE Audit log screen said "No audit entries. Operator and system actions are recorded here."
// while 345 rows for that tenant sat in the table, going back to the day the appliance was commissioned.
// svc_edged had INSERT and not SELECT, and pgx reports a permission failure on the first Next() rather than
// at Query() -- so "permission denied for table audit_log" arrived at the operator as HTTP 200 and a
// reassuring sentence.
//
// The grant is migration 0081. This is the other half: the handler must distinguish "nothing happened" from
// "I could not look", because those two are opposites and only one of them is safe to believe.

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

	// The check that was missing. Without it the loop simply ends and an error becomes an empty page.
	if !strings.Contains(body, "rows.Err()") {
		t.Error("the audit read does not check rows.Err(); a failed read is reported as no entries")
	}
	// An operator-facing message must not be a database noun, and the server must leave a trace: the original
	// 500 on a neighbouring screen was diagnosable only by reproducing it, because nothing was logged.
	if !strings.Contains(body, "the audit trail could not be read") {
		t.Error("the failure message does not say what could not be done")
	}
	if !strings.Contains(body, "slog.Error(\"audit read failed\"") {
		t.Error("an audit read failure is not logged, so it cannot be diagnosed from the appliance")
	}
	// An empty trail must serialise as [] rather than null, or the client cannot tell the two apart either.
	if !strings.Contains(body, "out := []auditRow{}") {
		t.Error("the empty result is a nil slice; it should be an empty list")
	}
}

// A DISABLED FEATURE MUST NOT BE READ AS SOMETHING ELSE.
//
// With Phase 6 off, /sessions/aggregate-time was not registered, so chi fell through to /sessions/{id} and
// looked up the guest session whose id is the literal string "aggregate-time". PostgreSQL was asked to
// compare a uuid with it and the Online-time screen reported HTTP 500 "query failed" -- a server error for a
// capability this appliance simply does not run.
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
	// The static route must still be declared BEFORE the {id} pattern in both states.
	off := strings.Index(body, "not_enabled")
	id := strings.Index(body, `r.Get("/{id}"`)
	if off < 0 || id < 0 || off > id {
		t.Error("the disabled-route answer must be registered before the {id} pattern")
	}
}
