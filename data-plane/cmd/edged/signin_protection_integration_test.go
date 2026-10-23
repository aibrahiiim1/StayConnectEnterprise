//go:build integration

package main

// Guest sign-in protection against a real PostgreSQL: the policy (count, rolling window, restriction, release)
// lives in the database, where concurrent failures are serialised. All rooms, names and addresses are invented.
// Elapsed time is simulated by moving stored timestamps backwards as a superuser (see rewind); no runtime role
// holds that privilege.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testMAC = "02:00:00:aa:bb:01"
const otherMAC = "02:00:00:aa:bb:02"

// fail records one attempt as scd does: the attempt row first, then the report to the policy (the count is derived from the attempt rows).
func (f *apiFixture) fail(t *testing.T, mac, room, result string) (restricted bool, count int) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.sign_in_attempts
		  (tenant_id, site_id, occurred_at, guest_network_name, submitted_room, verifier_kind, result,
		   request_id, device_ip, device_mac)
		VALUES ($1,$2, now(), 'Guest WiFi', $3, 'FULL_NAME', $4, gen_random_uuid(), '10.77.9.9'::inet, $5::macaddr)`,
		f.tenant, f.site, room, result, mac); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	// Only counting results are reported (Result.CountsAsCredentialFailure).
	if result != "CREDENTIAL_MISMATCH" && result != "ROOM_NOT_IN_MIRROR" {
		return f.gate(t, mac)
	}
	if err := f.pool.QueryRow(ctx, `
		SELECT restricted, failure_count
		  FROM iam_v2.guest_signin_note_failure($1::uuid,$2::uuid,$3::macaddr,NULL,'Guest WiFi',$4)`,
		f.tenant, f.site, mac, room).Scan(&restricted, &count); err != nil {
		t.Fatalf("note failure: %v", err)
	}
	return restricted, count
}

func (f *apiFixture) gate(t *testing.T, mac string) (restricted bool, remaining int) {
	t.Helper()
	var rem *int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT restricted, remaining_seconds FROM iam_v2.guest_signin_gate($1::uuid,$2::uuid,$3::macaddr)`,
		f.tenant, f.site, mac).Scan(&restricted, &rem); err != nil {
		t.Fatalf("gate: %v", err)
	}
	if rem != nil {
		remaining = *rem
	}
	return restricted, remaining
}

func (f *apiFixture) succeed(t *testing.T, mac, room string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.sign_in_attempts
		  (tenant_id, site_id, occurred_at, guest_network_name, submitted_room, verifier_kind, result,
		   request_id, device_ip, device_mac)
		VALUES ($1,$2, now(), 'Guest WiFi', $3, 'FULL_NAME', 'VERIFIED', gen_random_uuid(),
		        '10.77.9.9'::inet, $4::macaddr)`, f.tenant, f.site, room, mac); err != nil {
		t.Fatalf("seed success: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.guest_signin_note_success($1::uuid,$2::uuid,$3::macaddr)`,
		f.tenant, f.site, mac); err != nil {
		t.Fatalf("note success: %v", err)
	}
}

// rewind moves a device's stored times backwards to stand for elapsed seconds.
func (f *apiFixture) rewind(t *testing.T, mac string, seconds int) {
	t.Helper()
	ctx := context.Background()
	iv := fmt.Sprintf("%d seconds", seconds)
	if _, err := f.pool.Exec(ctx, `
		UPDATE iam_v2.guest_signin_restrictions
		   SET restricted_at = restricted_at - $4::interval,
		       expires_at    = expires_at    - $4::interval,
		       counter_reset_at = CASE WHEN counter_reset_at = '-infinity'::timestamptz
		                               THEN counter_reset_at ELSE counter_reset_at - $4::interval END
		 WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, mac, iv); err != nil {
		t.Fatalf("rewind restriction: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `
		UPDATE iam_v2.sign_in_attempts SET occurred_at = occurred_at - $4::interval
		 WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, mac, iv); err != nil {
		t.Fatalf("rewind attempts: %v", err)
	}
}

// The fifth incorrect submission triggers, not the fourth (an off-by-one would be invisible).
func TestIntegration_Protection_TheFifthFailureTriggersAndTheFourthDoesNot(t *testing.T) {
	f := newAPI(t)
	for i := 1; i <= 4; i++ {
		restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
		if restricted {
			t.Fatalf("attempt %d restricted the device; the configured threshold is 5", i)
		}
		if count != i {
			t.Fatalf("attempt %d counted as %d", i, count)
		}
		if blocked, _ := f.gate(t, testMAC); blocked {
			t.Fatalf("the gate refused after %d failures", i)
		}
	}
	restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if !restricted || count != 5 {
		t.Fatalf("the fifth failure did not trigger: restricted=%v count=%d", restricted, count)
	}
	blocked, remaining := f.gate(t, testMAC)
	if !blocked {
		t.Fatal("the gate allowed a device that had just been restricted")
	}
	if remaining < 55 || remaining > 60 {
		t.Fatalf("remaining = %ds, want about 60", remaining)
	}
}

// A blocked attempt must not extend the wait (guests tap Connect while waiting).
func TestIntegration_Protection_AttemptsDuringTheWaitDoNotExtendIt(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	var firstExpiry time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT expires_at FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&firstExpiry); err != nil {
		t.Fatalf("read expiry: %v", err)
	}

	f.rewind(t, testMAC, 5)
	for i := 0; i < 3; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}

	var afterExpiry time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT expires_at FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&afterExpiry); err != nil {
		t.Fatalf("read expiry: %v", err)
	}
	if !afterExpiry.Equal(firstExpiry.Add(-5 * time.Second)) {
		t.Fatalf("the wait moved: %v then %v — further attempts must not extend a restriction already running",
			firstExpiry, afterExpiry)
	}
}

// The wait ends on its own and the next failure starts a fresh count (else the wait would be permanent).
func TestIntegration_Protection_ExpiryEndsTheWaitAndStartsACleanCount(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	if blocked, _ := f.gate(t, testMAC); !blocked {
		t.Fatal("setup: the device should be restricted")
	}

	f.rewind(t, testMAC, 61)

	if blocked, _ := f.gate(t, testMAC); blocked {
		t.Fatal("the wait did not end when it expired")
	}
	restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if restricted || count != 1 {
		t.Fatalf("after expiry the count was %d (restricted=%v); the failures before a restriction must not "+
			"count towards the next one", count, restricted)
	}
}

// A success clears the counter.
func TestIntegration_Protection_SuccessClearsTheCounter(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 4; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	f.succeed(t, testMAC, "412")

	restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if restricted || count != 1 {
		t.Fatalf("count after a success was %d (restricted=%v), want 1", count, restricted)
	}
}

// The window rolls continuously, not at a clock boundary.
func TestIntegration_Protection_TheWindowRollsRatherThanResetting(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 3; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	f.rewind(t, testMAC, 65)

	restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if restricted || count != 1 {
		t.Fatalf("a failure after the window had passed counted %d with the old ones still in it", count)
	}

	for i := 0; i < 3; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	restricted, count = f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if !restricted || count != 5 {
		t.Fatalf("five failures inside one rolling window did not trigger: count=%d restricted=%v",
			count, restricted)
	}
}

// Technical failures the guest cannot fix must not count, or an outage becomes an outage plus a lockout.
func TestIntegration_Protection_TechnicalFailuresDoNotCount(t *testing.T) {
	f := newAPI(t)
	for _, r := range []string{
		"MIRROR_STALE_OR_MISSING_CHANGE", "ROUTING_OR_INTERFACE_FAILURE", "SERVICE_UNAVAILABLE",
		"MALFORMED_SUBMISSION", "SPENT_REQUEST_ID", "VERIFIED_NO_ELIGIBLE_PACKAGE",
		"STAY_NOT_ELIGIBLE", "AMBIGUOUS_ROOM_CANDIDATES", "RATE_LIMITED",
	} {
		for i := 0; i < 6; i++ {
			f.fail(t, testMAC, "412", r)
		}
		if blocked, _ := f.gate(t, testMAC); blocked {
			t.Fatalf("%s restricted the device; nothing the guest typed could have prevented it", r)
		}
	}
	// The counter is untouched, not merely below the threshold.
	restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if restricted || count != 1 {
		t.Fatalf("after %d non-counting results the count was %d, want 1", 9*6, count)
	}
}

// A restriction is scoped to the device only; if it were room-scoped, a stranger could lock out a resident with five submissions.
func TestIntegration_Protection_OnlyTheOffendingDeviceIsRestricted(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	if blocked, _ := f.gate(t, testMAC); !blocked {
		t.Fatal("setup: the offending device should be restricted")
	}
	if blocked, _ := f.gate(t, otherMAC); blocked {
		t.Fatal("a second device was restricted by the first device's behaviour")
	}
	f.succeed(t, otherMAC, "412")
	if blocked, _ := f.gate(t, otherMAC); blocked {
		t.Fatal("the room, not the device, was restricted")
	}
}

// Changing the submitted room does not reset the counter (the device is the subject).
func TestIntegration_Protection_ChangingTheSubmittedRoomDoesNotResetAnything(t *testing.T) {
	f := newAPI(t)
	for i, room := range []string{"101", "102", "103", "104"} {
		_, count := f.fail(t, testMAC, room, "ROOM_NOT_IN_MIRROR")
		if count != i+1 {
			t.Fatalf("room %s counted as %d; a different room must not start a new count", room, count)
		}
	}
	restricted, _ := f.fail(t, testMAC, "105", "ROOM_NOT_IN_MIRROR")
	if !restricted {
		t.Fatal("five rooms from one device did not reach the threshold")
	}
}

// Site isolation: a tenant-only scope check would pass until a second site exists.
func TestIntegration_Protection_ARestrictionDoesNotCrossSites(t *testing.T) {
	a := newAPI(t)
	b := newAPIIn(t, a.tenant)

	for i := 0; i < 5; i++ {
		a.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	if blocked, _ := a.gate(t, testMAC); !blocked {
		t.Fatal("setup: the device should be restricted at site A")
	}
	if blocked, _ := b.gate(t, testMAC); blocked {
		t.Fatal("the same device was restricted at a DIFFERENT site of the same customer")
	}

	status, raw := b.doRaw(t, http.MethodGet, "/guest-signin-restrictions", nil)
	if status != http.StatusOK {
		t.Fatalf("list at site B: %d %s", status, raw)
	}
	if strings.Contains(raw, strings.ToLower(testMAC)) {
		t.Fatalf("site B's operator can see site A's restriction: %s", raw)
	}
}

// The counter and expiry are rows, not process state; this fails if the gate is later cached.
func TestIntegration_Protection_TheRestrictionSurvivesAServiceRestart(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	// A second fixture over the same database stands for a restart.
	again := &apiFixture{pool: f.pool, tenant: f.tenant, site: f.site}
	if blocked, remaining := again.gate(t, testMAC); !blocked || remaining <= 0 {
		t.Fatalf("the restriction did not survive: blocked=%v remaining=%d", blocked, remaining)
	}
}

// Concurrent fifth failures produce one restriction with one expiry (the row lock serialises them). The
// assertion is the shared expiry, not how many callers answer "restricted".
func TestIntegration_Protection_ConcurrentFailuresAreSerialised(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 4; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	done := make(chan time.Time, 6)
	for i := 0; i < 6; i++ {
		go func() {
			_, expires, _ := f.failExpiring(t, testMAC, "412")
			done <- expires
		}()
	}
	var first time.Time
	for i := 0; i < 6; i++ {
		got := <-done
		if got.IsZero() {
			t.Fatalf("a concurrent submission was told nothing about the wait it is subject to")
		}
		if first.IsZero() {
			first = got
			continue
		}
		if !got.Equal(first) {
			t.Fatalf("concurrent submissions were given different expiries (%v and %v), so at least one of "+
				"them imposed a second wait", first, got)
		}
	}
	var rows, restrictions int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*), count(*) FILTER (WHERE restricted_at IS NOT NULL)
		  FROM iam_v2.guest_signin_restrictions
		 WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&rows, &restrictions); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || restrictions != 1 {
		t.Fatalf("%d rows / %d restrictions for one device", rows, restrictions)
	}
	var failures int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT failure_count FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures < 5 {
		t.Fatalf("the recorded triggering count is %d, below the threshold", failures)
	}
}

// failExpiring is fail() that also returns the reported expiry.
func (f *apiFixture) failExpiring(t *testing.T, mac, room string) (bool, time.Time, int) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.sign_in_attempts
		  (tenant_id, site_id, occurred_at, guest_network_name, submitted_room, verifier_kind, result,
		   request_id, device_ip, device_mac)
		VALUES ($1,$2, now(), 'Guest WiFi', $3, 'FULL_NAME', 'CREDENTIAL_MISMATCH', gen_random_uuid(),
		        '10.77.9.9'::inet, $4::macaddr)`, f.tenant, f.site, room, mac); err != nil {
		t.Errorf("seed attempt: %v", err)
		return false, time.Time{}, 0
	}
	var restricted bool
	var expires *time.Time
	var count int
	if err := f.pool.QueryRow(ctx, `
		SELECT restricted, expires_at, failure_count
		  FROM iam_v2.guest_signin_note_failure($1::uuid,$2::uuid,$3::macaddr,NULL,'Guest WiFi',$4)`,
		f.tenant, f.site, mac, room).Scan(&restricted, &expires, &count); err != nil {
		t.Errorf("note failure: %v", err)
		return false, time.Time{}, 0
	}
	if expires == nil {
		return restricted, time.Time{}, count
	}
	return restricted, *expires, count
}

// The list labels the room as an unverified submission.
func TestIntegration_Protection_TheActiveListCarriesTheOperatorsFacts(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	status, raw := f.doRaw(t, http.MethodGet, "/guest-signin-restrictions", nil)
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, raw)
	}
	var body struct {
		Restrictions []struct {
			ID                string `json:"id"`
			DeviceMAC         string `json:"device_mac"`
			GuestNetwork      string `json:"guest_network"`
			LastSubmittedRoom string `json:"last_submitted_room"`
			FailureCount      int    `json:"failure_count"`
			Reason            string `json:"reason"`
			RemainingSeconds  int    `json:"remaining_seconds"`
		} `json:"restrictions"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("undecodable list %q: %v", raw, err)
	}
	if len(body.Restrictions) != 1 {
		t.Fatalf("%d restrictions listed, want 1: %s", len(body.Restrictions), raw)
	}
	r := body.Restrictions[0]
	if r.FailureCount != 5 || r.Reason != "FAILED_CREDENTIAL_THRESHOLD" {
		t.Fatalf("the triggering failure count and reason are wrong: %+v", r)
	}
	if r.LastSubmittedRoom != "412" || r.GuestNetwork != "Guest WiFi" {
		t.Fatalf("the operator cannot tell which device or network this is: %+v", r)
	}
	if r.RemainingSeconds <= 0 || r.RemainingSeconds > 60 {
		t.Fatalf("remaining_seconds = %d", r.RemainingSeconds)
	}
	// A field called "room" would read as an identity.
	if !strings.Contains(raw, "last_submitted_room") {
		t.Fatalf("the room is not labelled as a submission: %s", raw)
	}
}

// Release ends the wait, clears the counter and grants nothing.
func TestIntegration_Protection_ReleasePermitsAnotherAttemptAndNothingMore(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	id := f.firstRestrictionID(t)

	// A reason is mandatory.
	if status, _ := f.doRaw(t, http.MethodPost, "/guest-signin-restrictions/"+id+"/release",
		map[string]any{"reason": "x"}); status != http.StatusBadRequest {
		t.Fatalf("a one-character reason was accepted: %d", status)
	}

	status, raw := f.doRaw(t, http.MethodPost, "/guest-signin-restrictions/"+id+"/release",
		map[string]any{"reason": "guest at the desk, identity confirmed"})
	if status != http.StatusOK {
		t.Fatalf("release: %d %s", status, raw)
	}
	if blocked, _ := f.gate(t, testMAC); blocked {
		t.Fatal("the device is still restricted after a release")
	}
	// The counter is cleared too, or the next mistake would restrict the device again.
	restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if restricted || count != 1 {
		t.Fatalf("after a release the count was %d (restricted=%v), want 1", count, restricted)
	}

	// Nothing is granted: a release only lets the guest try again.
	var sessions int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.sessions WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 0 {
		t.Fatalf("releasing a restriction created %d session(s); it must grant no access", sessions)
	}
}

// The release is audited with actor, device and reason.
func TestIntegration_Protection_TheReleaseIsRecordedAgainstTheOperator(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	id := f.firstRestrictionID(t)
	if status, raw := f.doRaw(t, http.MethodPost, "/guest-signin-restrictions/"+id+"/release",
		map[string]any{"reason": "verified at reception"}); status != http.StatusOK {
		t.Fatalf("release: %d %s", status, raw)
	}

	var actor, payload string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT COALESCE(actor_id,''), payload::text FROM public.audit_log
		 WHERE action='guest_signin_restriction.release' ORDER BY ts DESC LIMIT 1`).
		Scan(&actor, &payload); err != nil {
		t.Fatalf("the release was not audited: %v", err)
	}
	if actor != f.operator {
		t.Fatalf("audited actor %q, want the operator who released it", actor)
	}
	for _, want := range []string{"verified at reception", "grants_access", "last_submitted_room_unverified"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("the audit payload is missing %q: %s", want, payload)
		}
	}

	// The row also carries the release so it survives audit-log retention.
	var by, reason string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT COALESCE(released_by,''), COALESCE(release_reason,'')
		   FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&by, &reason); err != nil {
		t.Fatal(err)
	}
	if by == "" || reason != "verified at reception" {
		t.Fatalf("the restriction row does not record who released it and why: by=%q reason=%q", by, reason)
	}
}

// Defaults are active without saving anything: no row means 5 / 60 / 60, not "off".
func TestIntegration_Protection_TheDefaultsAreActiveOnAFreshSite(t *testing.T) {
	f := newAPI(t)
	status, raw := f.doRaw(t, http.MethodGet, "/guest-signin-protection", nil)
	if status != http.StatusOK {
		t.Fatalf("get policy: %d %s", status, raw)
	}
	var p struct {
		Max       int  `json:"max_failed_attempts"`
		Window    int  `json:"observation_window_seconds"`
		Restrict  int  `json:"restriction_seconds"`
		IsDefault bool `json:"is_default"`
		Limits    struct {
			MinFailed int `json:"min_failed_attempts"`
		} `json:"limits"`
	}
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatalf("undecodable policy %q: %v", raw, err)
	}
	if p.Max != 5 || p.Window != 60 || p.Restrict != 60 || !p.IsDefault {
		t.Fatalf("a fresh site is not running the approved policy: %+v", p)
	}
	if p.Limits.MinFailed == 0 {
		t.Fatalf("the bounds were not sent to the UI, so the form and the server can drift: %s", raw)
	}
	// The default is enforced, not merely reported.
	for i := 0; i < 4; i++ {
		if restricted, _ := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH"); restricted {
			t.Fatalf("the default policy triggered on attempt %d", i+1)
		}
	}
	if restricted, _ := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH"); !restricted {
		t.Fatal("the default policy did not trigger on the fifth attempt")
	}
}

// A saved setting changes enforcement immediately, with no restart (a later cache would break this).
func TestIntegration_Protection_ChangingTheSettingChangesEnforcementWithoutARestart(t *testing.T) {
	f := newAPI(t)
	status, raw := f.doRaw(t, http.MethodPut, "/guest-signin-protection", map[string]any{
		"max_failed_attempts":        3,
		"observation_window_seconds": 120,
		"restriction_seconds":        90,
		"reason":                     "conference floor, guests kept mistyping",
		"password":                   f.password,
	})
	if status != http.StatusOK {
		t.Fatalf("save policy: %d %s", status, raw)
	}

	for i := 0; i < 2; i++ {
		if restricted, _ := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH"); restricted {
			t.Fatalf("the new threshold of 3 triggered on attempt %d", i+1)
		}
	}
	restricted, count := f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	if !restricted || count != 3 {
		t.Fatalf("the saved threshold was not applied: restricted=%v count=%d", restricted, count)
	}
	if _, remaining := f.gate(t, testMAC); remaining < 85 || remaining > 90 {
		t.Fatalf("the saved restriction duration was not applied: remaining=%d, want about 90", remaining)
	}
}

// A wait already running keeps its recorded expiry when the setting changes.
func TestIntegration_Protection_ShorteningTheSettingDoesNotShortenAWaitAlreadyRunning(t *testing.T) {
	f := newAPI(t)
	for i := 0; i < 5; i++ {
		f.fail(t, testMAC, "412", "CREDENTIAL_MISMATCH")
	}
	var before time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT expires_at FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&before); err != nil {
		t.Fatal(err)
	}

	if status, raw := f.doRaw(t, http.MethodPut, "/guest-signin-protection", map[string]any{
		"max_failed_attempts": 5, "observation_window_seconds": 60, "restriction_seconds": 30,
		"reason": "shorter waits for the conference", "password": f.password,
	}); status != http.StatusOK {
		t.Fatalf("save policy: %d %s", status, raw)
	}

	var after time.Time
	if err := f.pool.QueryRow(context.Background(),
		`SELECT expires_at FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !after.Equal(before) {
		t.Fatalf("a settings change rewrote a running restriction's expiry: %v -> %v", before, after)
	}
}

// Out-of-range policies are refused with an actionable message, not a database error.
func TestIntegration_Protection_OutOfRangeValuesAreRefused(t *testing.T) {
	f := newAPI(t)
	for _, bad := range []map[string]any{
		{"max_failed_attempts": 1, "observation_window_seconds": 60, "restriction_seconds": 60},
		{"max_failed_attempts": 99, "observation_window_seconds": 60, "restriction_seconds": 60},
		{"max_failed_attempts": 5, "observation_window_seconds": 5, "restriction_seconds": 60},
		{"max_failed_attempts": 5, "observation_window_seconds": 60, "restriction_seconds": 99999},
		{"max_failed_attempts": 0, "observation_window_seconds": 0, "restriction_seconds": 0},
	} {
		status, raw := f.doRaw(t, http.MethodPut, "/guest-signin-protection", bad)
		if status != http.StatusBadRequest {
			t.Fatalf("%v was accepted: %d %s", bad, status, raw)
		}
		if !strings.Contains(strings.ToLower(raw), "between") {
			t.Fatalf("%v was refused without telling the operator the allowed range: %s", bad, raw)
		}
	}
	_, raw := f.doRaw(t, http.MethodGet, "/guest-signin-protection", nil)
	if !strings.Contains(raw, `"max_failed_attempts":5`) {
		t.Fatalf("a refused save altered the stored policy: %s", raw)
	}
}

// Every change records the actor and the previous values.
func TestIntegration_Protection_EveryPolicyChangeRecordsWhoAndFromWhat(t *testing.T) {
	f := newAPI(t)
	if status, raw := f.doRaw(t, http.MethodPut, "/guest-signin-protection", map[string]any{
		"max_failed_attempts": 8, "observation_window_seconds": 300, "restriction_seconds": 120,
		"reason": "first change", "password": f.password,
	}); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, raw)
	}
	if status, raw := f.doRaw(t, http.MethodPut, "/guest-signin-protection", map[string]any{
		"max_failed_attempts": 20, "observation_window_seconds": 300, "restriction_seconds": 30,
		"reason": "second change, weaker", "password": f.password,
	}); status != http.StatusOK {
		t.Fatalf("save: %d %s", status, raw)
	}

	rows, err := f.pool.Query(context.Background(), `
		SELECT changed_by, COALESCE(change_reason,''), old_max_failed_attempts, new_max_failed_attempts
		  FROM iam_v2.guest_signin_protection_changes
		 WHERE tenant_id=$1 AND site_id=$2 ORDER BY changed_at`, f.tenant, f.site)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type change struct {
		by, reason string
		old        *int
		new        int
	}
	var got []change
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.by, &c.reason, &c.old, &c.new); err != nil {
			t.Fatal(err)
		}
		got = append(got, c)
	}
	if len(got) != 2 {
		t.Fatalf("%d change records, want 2", len(got))
	}
	// The first change has no previous values: the defaults were in force, with no stored row.
	if got[0].old != nil {
		t.Fatalf("the first change claimed a previous stored value: %v", *got[0].old)
	}
	if got[0].new != 8 || got[1].old == nil || *got[1].old != 8 || got[1].new != 20 {
		t.Fatalf("the change chain does not describe what happened: %+v", got)
	}
	for _, c := range got {
		if strings.TrimSpace(c.by) == "" {
			t.Fatal("a policy change was recorded with no actor")
		}
	}
	var payload string
	if err := f.pool.QueryRow(context.Background(), `
		SELECT payload::text FROM public.audit_log
		 WHERE action='guest_signin_protection.update' ORDER BY ts DESC LIMIT 1`).Scan(&payload); err != nil {
		t.Fatalf("the policy change was not audited: %v", err)
	}
	for _, want := range []string{"previous", "new", "config_version"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("the audit payload is missing %q: %s", want, payload)
		}
	}
}

// The change log is append-only.
func TestIntegration_Protection_TheChangeLogCannotBeRewritten(t *testing.T) {
	f := newAPI(t)
	if status, _ := f.doRaw(t, http.MethodPut, "/guest-signin-protection", map[string]any{
		"max_failed_attempts": 7, "observation_window_seconds": 60, "restriction_seconds": 60,
		"reason": "setup for the append-only check", "password": f.password,
	}); status != http.StatusOK {
		t.Fatal("setup save failed")
	}
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.guest_signin_protection_changes SET changed_by='somebody else' WHERE tenant_id=$1`,
		f.tenant); err == nil {
		t.Fatal("the change log accepted an UPDATE")
	}
	if _, err := f.pool.Exec(ctx,
		`DELETE FROM iam_v2.guest_signin_protection_changes WHERE tenant_id=$1`, f.tenant); err == nil {
		t.Fatal("the change log accepted a DELETE")
	}
}

func (f *apiFixture) firstRestrictionID(t *testing.T) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT id::text FROM iam_v2.guest_signin_restrictions
		  WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr`,
		f.tenant, f.site, testMAC).Scan(&id); err != nil {
		t.Fatalf("read restriction id: %v", err)
	}
	return id
}
