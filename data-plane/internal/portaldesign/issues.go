// Package portaldesign is the single allowlist of what a hotel may put on its guest sign-in page (the page
// that collects room numbers, surnames and voucher codes), shared by both services:
//
//   - edged refuses a design whose sanitised form differs from what the operator wrote and says what would be
//     removed (silent repair would teach an operator their template "worked");
//   - portald sanitises again just before serving, so a document that reached the database another way
//     (backup, hand edit) still reaches a guest only in safe form.
package portaldesign

import (
	"fmt"
	"sort"
	"strings"
)

type Severity string

const (
	// SeverityError content is not served; a design carrying one is refused on save.
	SeverityError Severity = "error"
	// SeverityWarning content is served changed or with a consequence; the operator need not fix it.
	SeverityWarning Severity = "warning"
)

type Issue struct {
	Field    string   `json:"field"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

func (i Issue) String() string { return i.Field + ": " + i.Message }

// issueSet de-duplicates and counts findings in first-seen order ("(×40)", not forty lines).
type issueSet struct {
	field string
	order []string
	count map[string]int
	sev   map[string]Severity
}

func newIssues(field string) *issueSet {
	return &issueSet{field: field, count: map[string]int{}, sev: map[string]Severity{}}
}

func (s *issueSet) add(sev Severity, format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	if _, seen := s.count[msg]; !seen {
		s.order = append(s.order, msg)
		s.sev[msg] = sev
	}
	s.count[msg]++
}

func (s *issueSet) errorf(format string, a ...any) { s.add(SeverityError, format, a...) }
func (s *issueSet) warnf(format string, a ...any)  { s.add(SeverityWarning, format, a...) }

func (s *issueSet) list() []Issue {
	out := make([]Issue, 0, len(s.order))
	for _, m := range s.order {
		msg := m
		if n := s.count[m]; n > 1 {
			msg = fmt.Sprintf("%s (×%d)", m, n)
		}
		out = append(out, Issue{Field: s.field, Message: msg, Severity: s.sev[m]})
	}
	return out
}

func Errors(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.Severity == SeverityError {
			out = append(out, i)
		}
	}
	return out
}

func Summary(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, i := range issues {
		parts = append(parts, i.String())
	}
	return strings.Join(parts, "; ")
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
