//go:build integration && phase5

package main

// Operator post-stay API against the real router, RBAC middleware and database.
// Revoke ends post-stay access for the stay episode and is terminal. There is no PIN reset (AD-36).
// Each guard (permission, step-up, bounded reason, session-derived audit actor) is tested by removing it.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

func newPostStayAPI(t *testing.T, roles ...string) *apiFixture {
	t.Helper()
	f := newAPI(t, roles...)
	if len(roles) == 0 {
		roles = []string{"site_admin"}
	}
	s := &server{db: f.pool, sessions: newSessionStore(2 * time.Hour), tenantID: f.tenant, siteID: f.site}
	f.sessTok = s.sessions.create(&session{OperatorID: f.operator, Email: "op@test.local", Roles: roles})
	r := chi.NewRouter()
	r.Route("/edge/v1", func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(s.requireAuth)
			mountResource(r, s, "post-stay-profiles", s.postStayProfilesRoutes)
		})
	})
	// Assigning Config.Handler after listening has no effect; replace the server (cleanup closes f.srv).
	f.srv.Close()
	f.srv = httptest.NewServer(r)
	return f
}

// seedProfile builds a CHECKED_OUT stay with a post-stay profile via the controlled operation, the only write path.
func seedProfile(t *testing.T, f *apiFixture) (stay, profile string) {
	t.Helper()
	ctx := context.Background()
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `WITH
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  st AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,lifecycle_version,effective_checkout_at,normalized_room_number)
	         SELECT gen_random_uuid(),$1,$2,pi.id,'RES-PS','PS','CHECKED_OUT',1, now(),'412' FROM pi RETURNING id)
	SELECT id::text FROM st`, f.tenant, f.site).Scan(&stay)
	}); err != nil {
		t.Fatalf("seed stay: %v", err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.p5_begin_controlled_operation('post_stay_identity')`); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.post_stay_profiles
		(tenant_id,site_id,origin_stay_id,origin_lifecycle_version,pin_hash,valid_until,issued_via,pin_revealed_at)
		VALUES ($1,$2,$3,1,'$argon2id$v=19$m=65536,t=1,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNo',
		        now()+interval '1 day','GUEST_AUTHENTICATED_SESSION', now())
		RETURNING id::text`, f.tenant, f.site, stay).Scan(&profile); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return stay, profile
}

func TestIntegration_PostStayAPI_ListAndGetNeverExposeASecret(t *testing.T) {
	f := newPostStayAPI(t)
	_, profile := seedProfile(t, f)

	code, raw := f.doRaw(t, http.MethodGet, "/post-stay-profiles/", nil)
	if code != http.StatusOK {
		t.Fatalf("list: %d %s", code, raw)
	}
	// The operator screen must not carry the hash or any plaintext.
	for _, forbidden := range []string{"pin_hash", "argon2id", "\"pin\""} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the list response contains %q: %s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"authenticable"`) {
		t.Fatalf("the list response does not say whether the profile can authenticate: %s", raw)
	}
	code, raw = f.doRaw(t, http.MethodGet, "/post-stay-profiles/"+profile, nil)
	if code != http.StatusOK {
		t.Fatalf("get: %d %s", code, raw)
	}
	for _, forbidden := range []string{"pin_hash", "argon2id"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the detail response contains %q", forbidden)
		}
	}
}

// Each guard removed in turn.
func TestIntegration_PostStayAPI_MutationsRequireTheFullWeight(t *testing.T) {
	f := newPostStayAPI(t)
	_, profile := seedProfile(t, f)
	base := "/post-stay-profiles/" + profile

	for _, tc := range []struct {
		name string
		path string
		body map[string]any
		want int
	}{
		{"revoke with a too-short reason", base + "/revoke",
			map[string]any{"password": f.password, "reason": "x"}, http.StatusBadRequest},
		{"revoke with NO password", base + "/revoke",
			map[string]any{"reason": "guest asked"}, http.StatusUnauthorized},
		{"revoke with no reason", base + "/revoke",
			map[string]any{"password": f.password}, http.StatusBadRequest},
		{"revoke with the wrong password", base + "/revoke",
			map[string]any{"password": "nope", "reason": "guest asked"}, http.StatusUnauthorized},
	} {
		code, raw := f.doRaw(t, http.MethodPost, tc.path, tc.body)
		if code != tc.want {
			t.Fatalf("%s: status %d (want %d): %s", tc.name, code, tc.want, raw)
		}
		if strings.Contains(raw, "\"pin\"") {
			t.Fatalf("%s: a refused mutation returned a PIN", tc.name)
		}
	}
	// ...and the profile is untouched by every one of those refusals.
	var status string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status FROM iam_v2.post_stay_profiles WHERE id=$1`, profile).Scan(&status); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "ACTIVE" {
		t.Fatalf("a refused mutation changed the profile: status=%s", status)
	}
}

// A role without the permission cannot reach the surface at all.
func TestIntegration_PostStayAPI_RBAC(t *testing.T) {
	f := newPostStayAPI(t, "voucher_operator")
	_, profile := seedProfile(t, f)
	code, _ := f.doRaw(t, http.MethodGet, "/post-stay-profiles/", nil)
	if code != http.StatusForbidden {
		t.Fatalf("a role with no post-stay permission read the list: %d", code)
	}
	code, _ = f.doRaw(t, http.MethodPost, "/post-stay-profiles/"+profile+"/revoke",
		map[string]any{"password": f.password, "reason": "should never happen"})
	if code != http.StatusForbidden {
		t.Fatalf("a role with no post-stay permission revoked a profile: %d", code)
	}

	// A read-only role reads and cannot act.
	g := newPostStayAPI(t, "site_viewer")
	_, prof2 := seedProfile(t, g)
	if code, _ := g.doRaw(t, http.MethodGet, "/post-stay-profiles/", nil); code != http.StatusOK {
		t.Fatalf("a viewer could not read the list: %d", code)
	}
	if code, _ := g.doRaw(t, http.MethodPost, "/post-stay-profiles/"+prof2+"/revoke",
		map[string]any{"password": g.password, "reason": "viewer should not revoke"}); code != http.StatusForbidden {
		t.Fatalf("a viewer revoked a profile: %d", code)
	}
}

// There is no reset route: a PIN is not issued or rotated any more, so the path does not exist.
func TestIntegration_PostStayAPI_ThereIsNoPINReset(t *testing.T) {
	f := newPostStayAPI(t)
	_, profile := seedProfile(t, f)
	code, raw := f.doRaw(t, http.MethodPost, "/post-stay-profiles/"+profile+"/reset",
		map[string]any{"password": f.password, "reason": "guest lost the printout"})
	if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("a PIN reset route still answers: %d %s", code, raw)
	}
	if strings.Contains(raw, "\"pin\"") {
		t.Fatalf("a PIN was returned: %s", raw)
	}
}

// REVOKE IS TERMINAL. It cannot be undone.
func TestIntegration_PostStayAPI_RevokeIsTerminal(t *testing.T) {
	f := newPostStayAPI(t)
	_, profile := seedProfile(t, f)
	base := "/post-stay-profiles/" + profile

	if code, body := f.do(t, http.MethodPost, base+"/revoke",
		map[string]any{"password": f.password, "reason": "guest asked us to end it"}); code != http.StatusOK {
		t.Fatalf("revoke: %d %v", code, body)
	}
	var status string
	var reason *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT status, revoke_reason FROM iam_v2.post_stay_profiles WHERE id=$1`, profile).
		Scan(&status, &reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "REVOKED" || reason == nil || *reason == "" {
		t.Fatalf("revoke left status=%s reason=%v", status, reason)
	}

	// ...and revoking twice is refused rather than silently repeated, so the audit shows ONE termination.
	if code, _ := f.doRaw(t, http.MethodPost, base+"/revoke",
		map[string]any{"password": f.password, "reason": "again"}); code != http.StatusConflict {
		t.Fatalf("a second revoke was accepted: %d", code)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM public.audit_log WHERE target_id=$1 AND action='poststay.revoke'`, profile).
		Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	if n != 1 {
		t.Fatalf("the audit shows %d revocations for one profile", n)
	}
}

// An out-of-scope profile is indistinguishable from an absent one (no cross-site enumeration oracle).
func TestIntegration_PostStayAPI_CrossSiteIsIndistinguishable(t *testing.T) {
	f := newPostStayAPI(t)
	other := newPostStayAPI(t)
	_, foreign := seedProfile(t, other)

	codeForeign, rawForeign := f.doRaw(t, http.MethodGet, "/post-stay-profiles/"+foreign, nil)
	codeAbsent, rawAbsent := f.doRaw(t, http.MethodGet,
		"/post-stay-profiles/00000000-0000-0000-0000-0000000000ff", nil)
	if codeForeign != codeAbsent || rawForeign != rawAbsent {
		t.Fatalf("a foreign profile is distinguishable from an absent one:\n foreign: %d %s\n absent:  %d %s",
			codeForeign, rawForeign, codeAbsent, rawAbsent)
	}
}
