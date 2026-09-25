// Package portaldesign is the ONE definition of what a hotel may put on its guest sign-in page.
//
// WHY A PACKAGE AND NOT A FUNCTION IN EACH SERVICE. Two services touch a hotel's portal design: edged, which
// accepts it from an operator, and portald, which hands it to every guest device on the network. They used to
// share nothing -- edged ran a regular-expression denylist on save, portald served whatever the database held,
// and the page inserted the custom markup with innerHTML. Any spelling the denylist missed (an <img/onerror>
// with no space before the handler, a <base href> that silently re-pointed the voucher form's relative action
// at another host, an entity-encoded javascript&#58;) went straight to guests on the page that collects their
// room numbers, surnames and voucher codes.
//
// Here the rule is written once, as an ALLOWLIST, and both services apply it:
//
//   - edged refuses a design whose sanitised form would differ from what the operator wrote, and says exactly
//     what would have been removed. Silently repairing it would teach an operator their template "worked".
//   - portald sanitises again immediately before serving, so a document that reached the database some other
//     way -- written before this package existed, restored from a backup, edited by hand -- still reaches a
//     guest only in its safe form. Defence in depth, not a second opinion: the two cannot disagree, because
//     they are the same code.
package portaldesign

import (
	"fmt"
	"sort"
	"strings"
)

// Severity says whether an issue stops a design from being saved.
type Severity string

const (
	// SeverityError is content the portal will not serve. A design carrying one is refused on save.
	SeverityError Severity = "error"
	// SeverityWarning is content the portal serves in a changed form (for example with !important removed)
	// that an operator should know about but does not have to fix.
	SeverityWarning Severity = "warning"
)

// Issue is one precise, operator-readable finding about one field.
type Issue struct {
	Field    string   `json:"field"`
	Message  string   `json:"message"`
	Severity Severity `json:"severity"`
}

func (i Issue) String() string { return i.Field + ": " + i.Message }

// issueSet collects findings for one field, de-duplicated and counted, in the order they were first met. A
// fragment with forty images each missing an alt is one finding "(×40)", not forty lines.
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

// Errors returns only the issues that stop a design from being saved.
func Errors(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if i.Severity == SeverityError {
			out = append(out, i)
		}
	}
	return out
}

// Summary joins issues into one sentence-per-line message for an API error body.
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
