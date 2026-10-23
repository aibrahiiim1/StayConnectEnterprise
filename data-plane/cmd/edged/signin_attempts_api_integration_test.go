//go:build integration

package main

// Guest sign-in attempts operator API through the real router and PostgreSQL: site confinement, audited credential reads, uncacheable sensitive responses.
// No real guest data appears; every value is invented.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// seedAttempt inserts one recorded attempt directly; the recording path is scd's suite.
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

// The list carries no credential value of any kind: absent, not masked.
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

// Site confinement is proved with a real second site under the same tenant; an out-of-scope id must be indistinguishable from a nonexistent one.
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

// A credential read is recorded against the operator; the audit row is written before the values are fetched, so failed reads are recorded too.
func TestIntegration_SignInAttemptsAPI_ACredentialReadIsAudited(t *testing.T) {
	f := newAPI(t)
	id := f.seedAttempt(t, "412", "CREDENTIAL_MISMATCH")

	before := f.auditCount(t, "guest_signin_attempt.credentials_viewed")
	// scd is not wired here, so the proxy hop fails: the audit must still cover that case.
	// B8: without the operator's password nothing is revealed, and nothing is fetched.
	if st, _ := f.doRaw(t, http.MethodPost, "/guest-signin-credentials/"+id, map[string]any{"password": "not-the-password"}); st != http.StatusUnauthorized {
		t.Fatalf("a reveal with a wrong password = %d, want 401", st)
	}
	if st, _ := f.doRaw(t, http.MethodGet, "/guest-signin-credentials/"+id, nil); st != http.StatusMethodNotAllowed {
		t.Fatalf("the old GET reveal still answers: %d", st)
	}
	f.doRaw(t, http.MethodPost, "/guest-signin-credentials/"+id, map[string]any{"password": f.password})
	after := f.auditCount(t, "guest_signin_attempt.credentials_viewed")
	if after != before+1 {
		t.Fatalf("credential reads recorded: %d, want one more than %d", after, before)
	}

	// Selected by the attempt it names: the fixture and production spell audit_log's timestamp column differently.
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
	// ...and it records who looked, never what they saw.
	blob := fmt.Sprint(payload)
	for _, forbidden := range []string{"submitted", "verifier", "accepted", "first_name", "family_name"} {
		if strings.Contains(strings.ToLower(blob), forbidden) {
			t.Errorf("the audit payload carries %q: %v", forbidden, payload)
		}
	}
}

// Sensitive responses are not cacheable (a cache would outlive the retention purge).
func TestIntegration_SignInAttemptsAPI_CredentialResponsesAreNotCacheable(t *testing.T) {
	f := newAPI(t)
	id := f.seedAttempt(t, "412", "CREDENTIAL_MISMATCH")

	req, err := http.NewRequest(http.MethodPost, f.srv.URL+"/edge/v1/guest-signin-credentials/"+id,
		strings.NewReader(`{"password":`+strconv.Quote(f.password)+`}`))
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

// The detail says whether a sealed half exists, so the panel can tell "not permitted" from "nothing sealed because the key was unavailable".
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

func (f *apiFixture) auditCount(t *testing.T, action string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2`, f.tenant, action).Scan(&n); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	return n
}
