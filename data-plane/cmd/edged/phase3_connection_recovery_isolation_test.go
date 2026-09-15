//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TWO CONNECTIONS AT ONE PROPERTY MUST NOT SHARE A SET OF RETRY BOUNDS.
//
// Until now they did: the settings were keyed (tenant, site), and the screen warned the operator about it in
// a box -- "these apply to the whole site, not just this connection". A hotel running a second PMS, or an
// old and a new connector side by side during a migration, is exactly the case where one flaky link wants
// patient backoff and the healthy one must not be slowed to match. Under the old key, tuning either tuned
// both, silently.
//
// The isolation is asserted through the HTTP surface rather than against the table, because that is where an
// operator actually changes these: a definer function that separates two interfaces correctly, reached by a
// handler that passes the wrong id, is still one property with one set of bounds.
func TestIntegration_API_ConnectionRecoverySettingsAreIsolatedPerInterface(t *testing.T) {
	f := newAPI(t, "site_admin")
	ctx := context.Background()

	a, _, _ := f.seedInterface(t)
	b, _, _ := f.seedInterface(t)
	if a == b {
		t.Fatal("the fixture produced one interface twice; this test would prove nothing")
	}

	get := func(iface string) interfaceConnectionSettings {
		t.Helper()
		code, body := f.do(t, http.MethodGet, "/pms-interfaces/"+iface+"/connection-settings", nil)
		if code != http.StatusOK {
			t.Fatalf("GET settings for %s: %d %v", iface, code, body)
		}
		raw, _ := json.Marshal(body)
		var out interfaceConnectionSettings
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode settings: %v", err)
		}
		return out
	}

	// BOTH START UNCONFIGURED, on the approved defaults. is_default is the honest report that nobody has
	// touched them -- distinct from somebody having set them to the same numbers.
	beforeA, beforeB := get(a), get(b)
	for name, s := range map[string]interfaceConnectionSettings{"A": beforeA, "B": beforeB} {
		if !s.IsDefault {
			t.Errorf("%s: a brand-new interface should report is_default", name)
		}
		if s.BackoffMinMs != 500 || s.BackoffMaxMs != 30000 || s.StableResetSeconds != 60 ||
			s.LinkDownAlertSeconds != 900 || s.BlockedAfterRefusals != 3 {
			t.Errorf("%s: unexpected defaults: %+v", name, s)
		}
	}

	// TUNE ONLY A. Every field, so that a leak into B cannot hide behind a value that happened to match.
	code, body := f.do(t, http.MethodPut, "/pms-interfaces/"+a+"/connection-settings", map[string]any{
		"backoff_min_ms":          2000,
		"backoff_max_ms":          120000,
		"stable_reset_seconds":    300,
		"link_down_alert_seconds": 60,
		"blocked_after_refusals":  9,
		"reason":                  "this link sits behind a flaky VPN",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT settings for A: %d %v", code, body)
	}

	afterA, afterB := get(a), get(b)

	// A CHANGED, and says it is no longer on defaults.
	if afterA.BackoffMinMs != 2000 || afterA.BackoffMaxMs != 120000 || afterA.StableResetSeconds != 300 ||
		afterA.LinkDownAlertSeconds != 60 || afterA.BlockedAfterRefusals != 9 {
		t.Errorf("A did not take the new bounds: %+v", afterA)
	}
	if afterA.IsDefault {
		t.Error("A was configured and must no longer report is_default")
	}
	if afterA.ConfigVersion <= beforeA.ConfigVersion {
		t.Errorf("A's config_version should advance: %d -> %d", beforeA.ConfigVersion, afterA.ConfigVersion)
	}

	// B IS UNTOUCHED. This is the whole point, and it is asserted field by field rather than with a struct
	// comparison so a failure names the value that leaked.
	if afterB.BackoffMinMs != beforeB.BackoffMinMs {
		t.Errorf("B's backoff_min_ms moved with A: %d -> %d", beforeB.BackoffMinMs, afterB.BackoffMinMs)
	}
	if afterB.BackoffMaxMs != beforeB.BackoffMaxMs {
		t.Errorf("B's backoff_max_ms moved with A: %d -> %d", beforeB.BackoffMaxMs, afterB.BackoffMaxMs)
	}
	if afterB.StableResetSeconds != beforeB.StableResetSeconds {
		t.Errorf("B's stable_reset_seconds moved with A: %d -> %d", beforeB.StableResetSeconds, afterB.StableResetSeconds)
	}
	if afterB.LinkDownAlertSeconds != beforeB.LinkDownAlertSeconds {
		t.Errorf("B's link_down_alert_seconds moved with A: %d -> %d", beforeB.LinkDownAlertSeconds, afterB.LinkDownAlertSeconds)
	}
	if afterB.BlockedAfterRefusals != beforeB.BlockedAfterRefusals {
		t.Errorf("B's blocked_after_refusals moved with A: %d -> %d", beforeB.BlockedAfterRefusals, afterB.BlockedAfterRefusals)
	}
	if !afterB.IsDefault {
		t.Error("B was never configured and must still report is_default; configuring A must not create a row for B")
	}
	if afterB.ConfigVersion != beforeB.ConfigVersion {
		t.Errorf("B's config_version moved with A: %d -> %d", beforeB.ConfigVersion, afterB.ConfigVersion)
	}

	// AND THE REVERSE DIRECTION, because isolation that only holds one way is not isolation. Tuning B must
	// leave A's freshly-set values exactly as they are.
	code, body = f.do(t, http.MethodPut, "/pms-interfaces/"+b+"/connection-settings", map[string]any{
		"backoff_min_ms": 100,
		"reason":         "this one is on the local network",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT settings for B: %d %v", code, body)
	}
	finalA, finalB := get(a), get(b)
	if finalB.BackoffMinMs != 100 {
		t.Errorf("B did not take its own value: %+v", finalB)
	}
	// A PARTIAL WRITE LEAVES THE REST ALONE: B's other fields stay on the defaults it never changed.
	if finalB.BackoffMaxMs != 30000 || finalB.BlockedAfterRefusals != 3 {
		t.Errorf("B's untouched fields moved: %+v", finalB)
	}
	if finalA.BackoffMinMs != 2000 || finalA.BlockedAfterRefusals != 9 {
		t.Errorf("configuring B disturbed A: %+v", finalA)
	}

	// THE AUDIT TRAIL SEPARATES THEM TOO. One history per connection: a property with two links whose
	// changes landed in one undifferentiated list would be back where it started when asked "who slowed this
	// connection down, and why?".
	var changesA, changesB int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.pms_connection_settings_changes WHERE pms_interface_id=$1::uuid`, a).
		Scan(&changesA); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM iam_v2.pms_connection_settings_changes WHERE pms_interface_id=$1::uuid`, b).
		Scan(&changesB); err != nil {
		t.Fatal(err)
	}
	if changesA != 1 || changesB != 1 {
		t.Errorf("each interface should have exactly its own one change; A=%d B=%d", changesA, changesB)
	}
	var reasonA string
	if err := f.pool.QueryRow(ctx,
		`SELECT reason FROM iam_v2.pms_connection_settings_changes WHERE pms_interface_id=$1::uuid`, a).
		Scan(&reasonA); err != nil {
		t.Fatal(err)
	}
	if reasonA != "this link sits behind a flaky VPN" {
		t.Errorf("A's recorded reason is wrong: %q", reasonA)
	}
}

// AN INTERFACE THAT IS NOT THIS SITE'S IS NOT ADDRESSABLE.
//
// Without this an operator scoped to one property could write retry bounds onto another property's
// connection by passing its id, and the foreign key alone would happily permit it -- the row would be valid,
// just not theirs.
func TestIntegration_API_ConnectionRecoverySettingsRefuseAnotherSitesInterface(t *testing.T) {
	f := newAPI(t, "site_admin")
	other := newAPIIn(t, f.tenant, "site_admin") // same customer, DIFFERENT property: the near miss that matters
	foreign, _, _ := other.seedInterface(t)

	code, body := f.do(t, http.MethodGet, "/pms-interfaces/"+foreign+"/connection-settings", nil)
	if code != http.StatusNotFound {
		t.Errorf("reading another site's interface should be 404, got %d %v", code, body)
	}

	code, body = f.do(t, http.MethodPut, "/pms-interfaces/"+foreign+"/connection-settings",
		map[string]any{"backoff_min_ms": 5000, "reason": "should never land"})
	if code != http.StatusNotFound {
		t.Errorf("writing another site's interface should be 404, got %d %v", code, body)
	}

	// AND NOTHING WAS WRITTEN. A refusal that still left a row behind would be the worst of both.
	var rows int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.pms_connection_settings WHERE pms_interface_id=$1::uuid`, foreign).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("the refused write still created %d row(s) for another site's interface", rows)
	}
}
