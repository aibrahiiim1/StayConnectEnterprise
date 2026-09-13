package cloudmode

// EVERY UNCERTAINTY MUST RESOLVE TO LICENSING-ONLY.
//
// The Product-Owner requirement this package exists for is that non-licensing communication "must not
// silently reactivate through defaults, service restart or configuration reconciliation". That is a
// statement about the failure modes, not the happy path, so the failure modes are what is tested here: no
// row, no scope, a database that will not answer, a value neither side recognises.
//
// The asymmetry is deliberate and worth stating. Being wrong towards licensing-only costs a property its
// telemetry until somebody notices. Being wrong the other way sends guest-adjacent data out of a building
// that decided it should not leave. Those are not comparable, and every ambiguous case below resolves the
// cheap way.

import (
	"context"
	"errors"
	"testing"
)

type fakeRow struct {
	val string
	err error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) > 0 {
		if p, ok := dest[0].(*string); ok {
			*p = r.val
		}
	}
	return nil
}

type fakeQ struct {
	row    fakeRow
	called int
}

func (q *fakeQ) QueryRow(ctx context.Context, sql string, args ...any) Row {
	q.called++
	return q.row
}

func TestResolve_EveryUncertaintyIsLicensingOnly(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name   string
		q      Querier
		tenant string
		site   string
		want   Mode
		why    string
	}{
		{
			name: "an explicit FULL is honoured",
			q:    &fakeQ{row: fakeRow{val: "FULL"}}, tenant: "t", site: "s", want: Full,
			why: "somebody wrote this choice down; the setting must mean something",
		},
		{
			name: "an explicit LICENSING_ONLY is honoured",
			q:    &fakeQ{row: fakeRow{val: "LICENSING_ONLY"}}, tenant: "t", site: "s", want: LicensingOnly,
			why: "the ordinary case",
		},
		{
			name: "a database that will not answer does NOT open the transport",
			q:    &fakeQ{row: fakeRow{err: errors.New("connection refused")}}, tenant: "t", site: "s",
			want: LicensingOnly,
			why:  "an unreadable setting is not permission; a restored or unavailable database must not reactivate reporting",
		},
		{
			name: "no querier at all",
			q:    nil, tenant: "t", site: "s", want: LicensingOnly,
			why: "no way to ask means no answer means no talking",
		},
		{
			name: "an appliance that does not yet know its tenant",
			q:    &fakeQ{row: fakeRow{val: "FULL"}}, tenant: "", site: "s", want: LicensingOnly,
			why: "awaiting assignment: it cannot have been configured to report for a tenant it does not have",
		},
		{
			name: "an appliance that does not yet know its site",
			q:    &fakeQ{row: fakeRow{val: "FULL"}}, tenant: "t", site: "", want: LicensingOnly,
			why: "same reason, other half of the scope",
		},
		{
			name: "a value neither side recognises",
			q:    &fakeQ{row: fakeRow{val: "SOMETHING_NEW"}}, tenant: "t", site: "s", want: LicensingOnly,
			why: "the schema and this binary disagree, which is a reason to stay quiet rather than guess",
		},
		{
			name: "an empty value",
			q:    &fakeQ{row: fakeRow{val: ""}}, tenant: "t", site: "s", want: LicensingOnly,
			why: "absence is not permission",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Resolve(ctx, c.q, c.tenant, c.site); got != c.want {
				t.Errorf("Resolve = %s, want %s — %s", got, c.want, c.why)
			}
		})
	}
}

// An unscoped appliance must not even ASK. The query would be meaningless, and a call that cannot produce a
// usable answer is a call that should not be made.
func TestResolve_UnscopedApplianceDoesNotQuery(t *testing.T) {
	q := &fakeQ{row: fakeRow{val: "FULL"}}
	if got := Resolve(context.Background(), q, "", ""); got != LicensingOnly {
		t.Fatalf("got %s", got)
	}
	if q.called != 0 {
		t.Errorf("queried the database %d time(s) for an appliance with no tenant or site", q.called)
	}
}

// TelemetryAllowed is the single predicate every caller uses. Only Full opens anything.
func TestTelemetryAllowed(t *testing.T) {
	if !Full.TelemetryAllowed() {
		t.Error("Full must allow the telemetry transport")
	}
	if LicensingOnly.TelemetryAllowed() {
		t.Error("LicensingOnly must not allow the telemetry transport")
	}
	if Mode("").TelemetryAllowed() || Mode("anything").TelemetryAllowed() {
		t.Error("an unrecognised mode must never allow the telemetry transport")
	}
}

// The operator-facing string is not the wire value. A screen that showed LICENSING_ONLY would be showing a
// database enum to a hotel receptionist.
func TestString(t *testing.T) {
	if LicensingOnly.String() != "Licensing only" {
		t.Errorf("got %q", LicensingOnly.String())
	}
	if Full.String() != "Full cloud reporting" {
		t.Errorf("got %q", Full.String())
	}
}
