package iamv2

// THE PACKAGE LIST MUST ONLY READ WHAT svc_edged IS ALLOWED TO READ.
//
// This reproduces a real PRE-LIVE failure. The operator's package screen returned HTTP 500 and Hotel Admin
// showed "List failed", because the list query counted rows in iam_v2.package_eligibility_rules and
// iam_v2.package_grant_tiers. edged AUTHORS both tables — through the controlled SECURITY DEFINER writers —
// and holds no SELECT on either, so the statement died with "permission denied for table
// package_eligibility_rules" under the real runtime role while passing in every test, which runs as a
// superuser.
//
// A privilege-guarded CASE does not fix it: PostgreSQL resolves permissions for every relation a statement
// references, whichever branch would execute. The only correct fix without a grant is not to reference them,
// so this asserts on the statement itself. It is the cheapest test that would have caught the outage, and it
// keeps working with no database at all.
//
// If svc_edged is later granted SELECT on both tables, this test is what must be revisited first — deliberately,
// rather than by someone quietly re-adding a subquery.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// deniedToEdged are the iam_v2 tables the Hotel-Admin service writes through controlled operations but may
// not read back. Naming them here rather than in a comment is what makes the assertion below enforceable.
var deniedToEdged = []string{
	"package_eligibility_rules",
	"package_grant_tiers",
}

// withoutSQLComments strips -- comments so the assertion is about what the statement REFERENCES, not what it
// explains. The comment in the query names the forbidden tables on purpose, to say why they are absent.
func withoutSQLComments(q string) string {
	var b strings.Builder
	for _, line := range strings.Split(q, "\n") {
		if i := strings.Index(line, "--"); i >= 0 {
			line = line[:i]
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

func TestPackageListQueryTouchesNoTableEdgedCannotRead(t *testing.T) {
	sql := withoutSQLComments(packagesListSQL())
	for _, table := range deniedToEdged {
		if strings.Contains(sql, table) {
			t.Fatalf("the package list reads iam_v2.%s, which svc_edged holds no SELECT on. Under the real "+
				"runtime role the whole statement fails with \"permission denied for table %s\" and the "+
				"operator's package screen returns 500 — which is exactly the PRE-LIVE outage this guards.",
				table, table)
		}
	}
}

// The list must still project what the screen is for, so removing the denied reads cannot be "fixed" by
// quietly dropping everything else with them.
func TestPackageListStillProjectsWhatTheOperatorNeeds(t *testing.T) {
	sql := withoutSQLComments(packagesListSQL())
	for _, needed := range []string{
		"down_kbps", "up_kbps", "data_quota_bytes", "time_quota_seconds",
		"max_concurrent_devices", "speed_allocation", "price_minor",
	} {
		if !strings.Contains(sql, needed) {
			t.Fatalf("the package list no longer projects %s; the screen cannot say what a package gives", needed)
		}
	}
}

// CLASSIFYING THE DENIAL, WHEREVER pgx REPORTS IT.
//
// The first fix wrapped only the error returned by Query. pgx surfaces a denied SELECT when the rows are
// DRAINED — from rows.Err() — so the real condition came back unwrapped and the operator was told "no current
// configuration for this package": a missing-package message for a permission problem. These cases pin the
// classifier itself, which is the part that was wrong.
func TestWrapIfDenied(t *testing.T) {
	if got := wrapIfDenied(nil); got != nil {
		t.Fatalf("nil became %v", got)
	}
	denied := &pgconn.PgError{Code: "42501", Message: "permission denied for table package_eligibility_rules"}
	if !errors.Is(wrapIfDenied(denied), ErrPackageConditionsUnreadable) {
		t.Fatal("a 42501 was not classified as unreadable conditions, so the caller would report it as not-found")
	}
	// wrapped the way pgx hands it back through a call chain
	if !errors.Is(wrapIfDenied(fmt.Errorf("query rules: %w", denied)), ErrPackageConditionsUnreadable) {
		t.Fatal("a wrapped 42501 was not classified")
	}
	other := &pgconn.PgError{Code: "42P01", Message: "relation does not exist"}
	if errors.Is(wrapIfDenied(other), ErrPackageConditionsUnreadable) {
		t.Fatal("a non-privilege error was misclassified as a privilege problem")
	}
	if got := wrapIfDenied(errors.New("boom")); errors.Is(got, ErrPackageConditionsUnreadable) {
		t.Fatal("a plain error was misclassified")
	}
}
