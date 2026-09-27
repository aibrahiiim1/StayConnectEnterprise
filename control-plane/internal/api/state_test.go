package api

import (
	"testing"
	"time"
)

func TestDeriveState(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	lic := func(status string, untilFromNow time.Duration, grace int) *LicenseFacts {
		return &LicenseFacts{Status: status, ValidUntil: now.Add(untilFromNow), GracePeriodDays: grace, OfflineGraceDays: 30}
	}
	day := 24 * time.Hour

	cases := []struct {
		name string
		f    ApplianceFacts
		want DerivedState
	}{
		{"fresh registration", ApplianceFacts{Lifecycle: "pending_approval", LastSeenAt: ago(10 * time.Second)},
			DerivedState{ActivationWaiting, ConnectionConnected, LicenseNone}},
		{"empty lifecycle is waiting", ApplianceFacts{},
			DerivedState{ActivationWaiting, ConnectionNever, LicenseNone}},
		{"activated by operator, nothing collected yet", ApplianceFacts{Lifecycle: "assigned", License: lic("active", 365*day, 30)},
			DerivedState{ActivationActivating, ConnectionNever, LicenseActive}},
		{"certificate but no licence", ApplianceFacts{Lifecycle: "assigned", HasActiveCert: true, LastSeenAt: ago(time.Hour)},
			DerivedState{ActivationActivating, ConnectionRecentlySeen, LicenseNone}},
		{"fully activated", ApplianceFacts{Lifecycle: "assigned", HasActiveCert: true, LastSeenAt: ago(time.Minute), License: lic("active", 365*day, 30)},
			DerivedState{ActivationActivated, ConnectionConnected, LicenseActive}},
		{"suspended licence stays activated", ApplianceFacts{Lifecycle: "assigned", HasActiveCert: true, License: lic("suspended", 365*day, 30)},
			DerivedState{ActivationActivated, ConnectionNever, LicenseSuspended}},
		{"revoked licence stays activated", ApplianceFacts{Lifecycle: "assigned", HasActiveCert: true, License: lic("revoked", 365*day, 30)},
			DerivedState{ActivationActivated, ConnectionNever, LicenseRevoked}},
		{"retirement pending", ApplianceFacts{Lifecycle: "assigned", HasActiveCert: true, TerminalDelivery: "terminal_delivery_pending", License: lic("revoked", 10*day, 0)},
			DerivedState{ActivationRetiring, ConnectionNever, LicenseRevoked}},
		{"retirement failed is still retiring", ApplianceFacts{Lifecycle: "assigned", TerminalDelivery: "terminal_delivery_failed"},
			DerivedState{ActivationRetiring, ConnectionNever, LicenseNone}},
		{"decommissioned", ApplianceFacts{Lifecycle: "decommissioned", TerminalDelivery: "credential_revoked", LastSeenAt: ago(48 * time.Hour)},
			DerivedState{ActivationRetired, ConnectionOffline, LicenseNone}},
		{"revoked identity", ApplianceFacts{Lifecycle: "revoked"},
			DerivedState{ActivationRetired, ConnectionNever, LicenseNone}},
	}
	for _, c := range cases {
		if got := DeriveState(c.f, now); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestDeriveConnectionBoundaries(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }
	cases := []struct {
		last *time.Time
		want string
	}{
		{nil, ConnectionNever},
		{at(0), ConnectionConnected},
		{at(5 * time.Minute), ConnectionConnected},
		{at(5*time.Minute + time.Second), ConnectionRecentlySeen},
		{at(24 * time.Hour), ConnectionRecentlySeen},
		{at(24*time.Hour + time.Second), ConnectionOffline},
	}
	for _, c := range cases {
		if got := deriveConnection(c.last, now); got != c.want {
			t.Errorf("last=%v: got %s want %s", c.last, got, c.want)
		}
	}
}

func TestDeriveLicense(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	l := func(status string, until time.Duration, grace, offline int) *LicenseFacts {
		return &LicenseFacts{Status: status, ValidUntil: now.Add(until), GracePeriodDays: grace, OfflineGraceDays: offline}
	}
	cases := []struct {
		name string
		l    *LicenseFacts
		want string
	}{
		{"none", nil, LicenseNone},
		{"active, 31 days left", l("active", 31*day, 30, 30), LicenseActive},
		{"expiring, exactly 30 days left", l("active", 30*day, 30, 30), LicenseExpiring},
		{"expiring, 1 hour left", l("active", time.Hour, 30, 30), LicenseExpiring},
		{"valid_until is now: still expiring", l("active", 0, 30, 30), LicenseExpiring},
		{"grace, 1 day past", l("active", -day, 7, 30), LicenseGrace},
		{"grace ends exactly now", l("active", -7*day, 7, 30), LicenseGrace},
		{"expired after grace", l("active", -8*day, 7, 30), LicenseExpired},
		{"grace 0 falls back to offline grace (appliance rule)", l("active", -20*day, 0, 30), LicenseGrace},
		{"no grace at all", l("active", -time.Second, 0, 0), LicenseExpired},
		{"suspended beats time", l("suspended", -100*day, 0, 0), LicenseSuspended},
		{"revoked", l("revoked", 100*day, 30, 30), LicenseRevoked},
		{"superseded is never a fact", l("superseded", 100*day, 30, 30), LicenseNone},
	}
	for _, c := range cases {
		if got := DeriveLicense(c.l, now); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
