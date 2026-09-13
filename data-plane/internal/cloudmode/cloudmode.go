// Package cloudmode answers one question for every daemon that might speak to Central: may this appliance
// say anything beyond licensing?
//
// IT IS ONE PACKAGE BECAUSE THE ANSWER MUST BE ONE ANSWER. scd decides whether to open the telemetry
// transport; edged decides whether to enqueue service-health; the Hotel Admin screen tells the property what
// is happening. Three independent readings of the same setting is how a product ends up with a screen that
// says one thing and a socket that does another.
//
// WHY THE DEFAULT IS LICENSING-ONLY. The Product-Owner requirement is that non-licensing communication must
// not silently reactivate "through defaults, service restart or configuration reconciliation". Any default of
// Full would do precisely that on the first read that failed, the first restored database, the first fresh
// install. So every uncertainty resolves to LicensingOnly: no row, no scope, an unreadable setting, a
// database that will not answer. Running any other way requires somebody to have written that choice down.
//
// The cost of being wrong in this direction is that an appliance stops reporting telemetry until a human
// notices. The cost of being wrong in the other direction is guest-adjacent data leaving a property that
// decided it should not. Those are not comparable.
package cloudmode

import (
	"context"
	"log/slog"
	"strings"
)

// Mode is what this site may say to Central.
type Mode string

const (
	// LicensingOnly permits licence activation, retrieval, renewal and validation, plus the appliance
	// identity and certificate lifecycle those depend on. All of it HTTPS to ctrlapi. Nothing else.
	LicensingOnly Mode = "LICENSING_ONLY"
	// Full additionally opens the cloud telemetry transport: the outbox, its subscriptions, the signed
	// command channel and the software-update agent.
	Full Mode = "FULL"
)

// TelemetryAllowed reports whether the non-licensing cloud transport may be opened at all.
func (m Mode) TelemetryAllowed() bool { return m == Full }

// String renders the mode for an operator, not for a wire format.
func (m Mode) String() string {
	if m == Full {
		return "Full cloud reporting"
	}
	return "Licensing only"
}

// Querier is the subset of a pgx pool this package needs, so callers can pass a pool or a tx and tests need
// no database at all.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// Row is the one method we use from a pgx row.
type Row interface{ Scan(dest ...any) error }

// Resolve reads the site's mode, resolving every uncertainty to LicensingOnly.
//
// It NEVER returns an error. A caller that had to decide what an error meant would be re-implementing this
// decision at each call site, and the whole point is that the decision is made once, here, in the safe
// direction. What it does instead is SAY SO: an unreadable setting is logged at warn, because silently
// running licensing-only when somebody expected full reporting should be visible in the journal rather than
// inferred from an absence of traffic.
func Resolve(ctx context.Context, q Querier, tenantID, siteID string) Mode {
	if q == nil || strings.TrimSpace(tenantID) == "" || strings.TrimSpace(siteID) == "" {
		// An appliance that does not yet know which tenant it serves cannot have been configured to report
		// for one. This is the awaiting-assignment case, and it is exactly when NOT to open a transport.
		return LicensingOnly
	}
	var mode string
	err := q.QueryRow(ctx,
		`SELECT mode FROM iam_v2.cloud_mode_get($1::uuid, $2::uuid)`, tenantID, siteID).Scan(&mode)
	if err != nil {
		slog.Warn("cloud mode unreadable; running licensing-only",
			"err", err, "tenant_id", tenantID, "site_id", siteID)
		return LicensingOnly
	}
	if Mode(mode) == Full {
		return Full
	}
	// Anything unrecognised is not a licence to talk. The database CHECK constrains this column, so reaching
	// here with an unknown value means the schema and this binary disagree -- which is itself a reason to
	// stay quiet rather than to guess.
	if mode != string(LicensingOnly) {
		slog.Warn("unrecognised cloud mode; running licensing-only", "mode", mode)
	}
	return LicensingOnly
}
