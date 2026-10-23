package iamv2

// The package list must only read what svc_edged may read: it has no SELECT on package_eligibility_rules or package_grant_tiers.
// Postgres checks permissions on every referenced relation even in an unexecuted CASE branch, and tests run as superuser, so this asserts on the statement text.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// deniedToEdged are the iam_v2 tables edged writes through controlled operations but may not read.
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

// The list must still project what the screen needs.
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

// pgx reports a denied SELECT from rows.Err() when rows are drained, not only from Query; the classifier must catch both.
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

// Package conditions must load through iam_v2.p2_package_current_conditions: svc_edged has INSERT but no SELECT on the tables.
func TestPackageConditionsAreReadThroughTheScopedReader(t *testing.T) {
	sql := withoutSQLComments(packageConditionsSQL())
	for _, table := range deniedToEdged {
		if strings.Contains(sql, table) {
			t.Fatalf("the conditions read references iam_v2.%s directly; svc_edged holds no SELECT on it and "+
				"the whole statement fails under the real runtime role", table)
		}
	}
	if !strings.Contains(sql, "p2_package_current_conditions") {
		t.Fatal("the conditions read no longer goes through the scoped reader")
	}
}

// The function takes tenant, site and package only: the revision is resolved inside, so no argument can reach a historical one.
func TestScopedReaderTakesTenantSiteAndPackageOnly(t *testing.T) {
	sql := withoutSQLComments(packageConditionsSQL())
	for _, arg := range []string{"$1::uuid", "$2::uuid", "$3::uuid"} {
		if !strings.Contains(sql, arg) {
			t.Fatalf("the scoped reader is not passed %s; tenant, site and package are all required for scoping", arg)
		}
	}
	if strings.Contains(sql, "$4") {
		t.Fatal("the scoped reader gained a fourth argument — a revision parameter would put scoping back in " +
			"the caller's hands and allow a historical revision to be requested")
	}
}
