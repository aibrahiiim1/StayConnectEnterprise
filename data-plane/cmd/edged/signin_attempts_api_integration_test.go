//go:build integration

package main

// THE GUEST SIGN-IN ATTEMPTS OPERATOR API, through the real router and a real PostgreSQL.
//
// The RBAC tests next door prove which roles reach which route. They are not proof about the DATA: a
// perfectly authorized operator must still be unable to read another site's attempts, a credential read must
// still be recorded against the person who made it, and a response carrying guest credentials must still be
// uncacheable. Those are properties of the handler and the database together, so they are tested together.
//
// NO REAL GUEST DATA APPEARS HERE. Every value is invented.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// seedAttempt inserts one recorded attempt directly. scd's own suite proves the RECORDING path; what is
// under test here is what edged does with a row that exists, so writing it directly keeps the test about
// that and avoids dragging the whole guest authentication stack into an operator-API test.
func (f *apiFixture) seedAttempt(t *testing.T, room, result string) string {
	t.Helper()
	var id string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO iam_v2.sign_in_attempts
		  (tenant_id, site_id, occurred_at, guest_network_name, submitted_room, verifier_kind, result,
		   room_in_mirror, eligible_stay_candidates, pms_transport_status, mirror_age_seconds, latency_ms,
		   request_id, device_ip, device_mac)
		VALUES ($1,$2, now(), 'Guest WiFi', $3, 'FULL_NAME', $4, true, 1, 'DISCONNECTED', 3600, 42,
		        gen_random_uuid(), '10.77.9.9'::inet, '02:00:00:ab:cd:ef'::macaddr)
		RETURNING id::text`, f.tenant, f.site, room, result).Scan(&id); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	return id
}

// THE LIST CARRIES NO CREDENTIAL VALUE OF ANY KIND.
//
// Not masked — absent. A list projection that carried the values "hidden by the UI" would hand every holder
// of the weaker permission exactly what the stronger one exists to gate, and the first screen to render them
// by accident would never be noticed.
func TestIntegration_SignInAttemptsAPI_TheListCarriesNoCredentialValues(t *testing.T) {
	f := newAPI(t)
	id := f.seedAttempt(t, "412", "CREDENTIAL_MISMATCH")

	status, raw := f.doRaw(t, http.MethodGet, "/guest-signin-attempts", nil)
	if status != http.StatusOK {
		t.Fatalf("list status %d: %s", status, raw)
	}
	if !strings.Contains(raw, id) {
		t.Fatalf("the seeded attempt is missing from the list: %s", raw)
	}
	for _, forbidden := range []string{
		"submitted_verifier", "normalized_verifier", "accepted_first_name", "accepted_family_name",
		"accepted_reservation_number", "sensitive_ciphertext", "sensitive_nonce", "encryption_key_id",
	} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("the list response carries %q", forbidden)
		}
	}
	// ...and it DOES carry the plain-language reason, from the server, so the screen never shows a raw enum.
	if !strings.Contains(raw, "result_label") {
		t.Error("the list carries no plain-language reason; an operator would read the enum")
	}
}

// SITE CONFINEMENT, PROVED WITH A REAL SECOND SITE UNDER THE SAME TENANT.
//
// A tenant-only scope check passes this test's happy path and fails its point: a multi-property customer has
// several sites under one tenant, and an attempt from the hotel next door must be as unreachable as one from
// another company. The out-of-scope id must also be INDISTINGUISHABLE from an id that never existed, or the
// 404s themselves become a way to enumerate.
func TestIntegration_SignInAttemptsAPI_AnotherSitesAttemptIsUnreachableAndIndistinguishable(t *testing.T) {
	a := newAPI(t)
	b := newAPIIn(t, a.tenant) // same tenant, different site
	theirs := b.seedAttempt(t, "412", "CREDENTIAL_MISMATCH")

	// It is not in our list...
	_, raw := a.doRaw(t, http.MethodGet, "/guest-signin-attempts", nil)
	if strings.Contains(raw, theirs) {
		t.Fatal("another site's attempt appeared in this site's list")
	}
	// ...and asking for it by id answers exactly as a nonexistent id does.
	outOfScope, outBody := a.doRaw(t, http.MethodGet, "/guest-signin-attempts/"+theirs, nil)
	absent, absentBody := a.doRaw(t, http.MethodGet,
		"/guest-signin-attempts/00000000-0000-4000-8000-00000000dead", nil)
	if outOfScope != http.StatusNotFound {
		t.Fatalf("another site's attempt answered %d, want 404", outOfScope)
	}
	if outOfScope != absent || outBody != absentBody {
		t.Fatalf("an out-of-scope id is distinguishable from an absent one:\n  scope:  %d %s\n  absent: %d %s",
			outOfScope, outBody, absent, absentBody)
	}
}

// A CREDENTIAL READ IS RECORDED AGAINST THE PERSON WHO MADE IT.
//
// The audit row is written BEFORE the values are fetched, so an attempt that fails downstream is still
// recorded. Recording only successful reads would leave the interesting case — somebody walking ids — the
// one thing the log does not contain.
func TestIntegration_SignInAttemptsAPI_ACredentialReadIsAudited(t *testing.T) {
	f := newAPI(t)
	id := f.seedAttempt(t, "412", "CREDENTIAL_MISMATCH")

	before := f.auditCount(t, "guest_signin_attempt.credentials_viewed")
	// scd is not wired in this fixture, so the proxy hop fails — which is exactly the case the audit must
	// still cover. The status is unimportant here; the record is the assertion.
	f.doRaw(t, http.MethodGet, "/guest-signin-credentials/"+id, nil)
	after := f.auditCount(t, "guest_signin_attempt.credentials_viewed")
	if after != before+1 {
		t.Fatalf("credential reads recorded: %d, want one more than %d", after, before)
	}

	// Selected by the attempt it names rather than by "the newest row": the disposable fixture and the
	// production schema spell audit_log's timestamp column differently, and a test that ordered by it would
	// be asserting something about the fixture instead of about the audit trail.
	var actor, target string
	var payload map[string]any
	if err := f.pool.QueryRow(context.Background(), `
		SELECT COALESCE(actor_id,''), COALESCE(target_id,''), payload
		  FROM public.audit_log
		 WHERE tenant_id=$1 AND action='guest_signin_attempt.credentials_viewed' AND target_id=$2`,
		f.tenant, id).Scan(&actor, &target, &payload); err != nil {
		t.Fatalf("read the audit row: %v", err)
	}
	if actor != f.operator {
		t.Errorf("the audit row names actor %q, want the operator %q", actor, f.operator)
	}
	if target != id {
		t.Errorf("the audit row names attempt %q, want %q", target, id)
	}
	if fmt.Sprint(payload["site_id"]) != f.site {
		t.Errorf("the audit row does not name the site: %v", payload)
	}
	// ...and it records WHO LOOKED, never WHAT THEY SAW. An audit trail that copied the credential would
	// simply move the guest's data into a table with a longer retention and a wider readership.
	blob := fmt.Sprint(payload)
	for _, forbidden := range []string{"submitted", "verifier", "accepted", "first_name", "family_name"} {
		if strings.Contains(strings.ToLower(blob), forbidden) {
			t.Errorf("the audit payload carries %q: %v", forbidden, payload)
		}
	}
}

// SENSITIVE RESPONSES ARE NOT CACHEABLE. Without this the values can sit in a browser cache, a corporate
// proxy or a disk image long after the thirty-day purge has removed the row they came from — which would
// make the retention promise false by a mechanism nobody on the appliance can see.
func TestIntegration_SignInAttemptsAPI_CredentialResponsesAreNotCacheable(t *testing.T) {
	f := newAPI(t)
	id := f.seedAttempt(t, "412", "CREDENTIAL_MISMATCH")

	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/edge/v1/guest-signin-credentials/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.sessTok})
	resp, err := f.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	cc := resp.Header.Get("Cache-Control")
	// The header is set before the proxy hop is attempted, so it is present whether or not scd answered.
	if !strings.Contains(cc, "no-store") {
		t.Fatalf("Cache-Control = %q, want no-store", cc)
	}
}

// The DETAIL response says whether a sealed half exists, so the panel can tell "you may not see this" from
// "there is nothing to see because the key was unavailable when this was recorded". Those look identical on
// screen and mean completely different things to whoever is trying to help the guest.
func TestIntegration_SignInAttemptsAPI_DetailReportsWhetherValuesExistWithoutRevealingThem(t *testing.T) {
	f := newAPI(t)
	id := f.seedAttempt(t, "412", "ROOM_NOT_IN_MIRROR")

	status, body := f.do(t, http.MethodGet, "/guest-signin-attempts/"+id, nil)
	if status != http.StatusOK {
		t.Fatalf("detail status %d: %v", status, body)
	}
	if _, ok := body["credentials_available"]; !ok {
		t.Fatal("the detail does not say whether values exist at all")
	}
	if body["credentials_available"] != false {
		t.Fatalf("an attempt seeded with no sealed half claims values are available: %v", body["credentials_available"])
	}
	if body["result_label"] == nil || body["result_label"] == "" {
		t.Fatal("the detail carries no plain-language reason")
	}
	if body["room_in_mirror"] != true {
		t.Fatalf("room_in_mirror = %v, want the recorded value", body["room_in_mirror"])
	}
}

// auditCount counts audit rows for one action in this fixture's tenant.
func (f *apiFixture) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2`, f.tenant, action).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}
