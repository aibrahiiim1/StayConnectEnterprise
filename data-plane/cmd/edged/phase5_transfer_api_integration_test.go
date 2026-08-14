//go:build integration && phase5

package main

// THE CROSS-PMS TRANSFER API, against the real router, the real RBAC middleware and a real database.
//
// The transfer package's own suite proves the operation. What this proves is the SURFACE around it:
//
//   * the review signal is read-only, says out loud that it is not evidence, and authorizes nothing;
//   * the preview writes nothing and takes no locks, so an operator can look before they commit;
//   * execute is unreachable without permission, step-up and a bounded reason — each removed in turn;
//   * a refused execute leaves the guest exactly as they were.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func newTransferAPI(t *testing.T, roles ...string) *apiFixture {
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
			mountResource(r, s, "stay-transfers", s.stayTransfersRoutes)
		})
	})
	f.srv.Close()
	f.srv = httptest.NewServer(r)
	return f
}

type xferFx struct{ stayA, stayB, stayA2, entA string }

// seedTransferFixture builds two interfaces, an IN_HOUSE Stay on each, a second Stay on the first (the
// room-move shape), and live access on the source.
func seedTransfer(t *testing.T, f *apiFixture) xferFx {
	t.Helper()
	ctx := context.Background()
	var x xferFx
	var pkg string
	if err := f.pool.QueryRow(ctx, `WITH
	  ia AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  ib AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state)
	         VALUES (gen_random_uuid(),$1,$2,'protel-fias','ACTIVE') RETURNING id),
	  sa AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,normalized_room_number)
	         SELECT gen_random_uuid(),$1,$2,ia.id,'RES-A','SA','IN_HOUSE','101' FROM ia RETURNING id),
	  sa2 AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	            external_stay_identity,status,normalized_room_number)
	          SELECT gen_random_uuid(),$1,$2,ia.id,'RES-A2','SA2','IN_HOUSE','102' FROM ia RETURNING id),
	  sb AS (INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,
	           external_stay_identity,status,normalized_room_number)
	         SELECT gen_random_uuid(),$1,$2,ib.id,'RES-B','SB','IN_HOUSE','201' FROM ib RETURNING id),
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code)
	         VALUES (gen_random_uuid(),$1,$2,'XF-PLAN') RETURNING id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,
	            max_concurrent_devices,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(),$1,$2,sp.id,1,4,'VALIDITY_WINDOW',1000000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system,active)
	         VALUES (gen_random_uuid(),$1,$2,'XF-GRACE',true,true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,
	            service_plan_revision_id,package_type,price_minor,settlement_methods)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'CHECKOUT_GRACE',0,ARRAY['NOT_REQUIRED']::text[]
	            FROM ip, spr RETURNING id)
	SELECT (SELECT id FROM sa)::text,(SELECT id FROM sb)::text,(SELECT id FROM sa2)::text,(SELECT id FROM ipr)::text`,
		f.tenant, f.site).Scan(&x.stayA, &x.stayB, &x.stayA2, &pkg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, pkg); err != nil {
		t.Fatalf("current revision: %v", err)
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var purchase string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		f.tenant, f.site, pkg, x.stayA).Scan(&purchase); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,window_ends_at,status)
		SELECT $1,$2,$3,$4,'{}'::jsonb, ipr.service_plan_revision_id,$5,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		       now()+interval '4 hours','PENDING' FROM iam_v2.internet_package_revisions ipr WHERE ipr.id=$5
		RETURNING id::text`, f.tenant, f.site, x.stayA, purchase, pkg).Scan(&x.entA); err != nil {
		t.Fatalf("entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now(),NULL)`, x.entA); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return x
}

// The review signal is a signal, and says so.
func TestIntegration_TransferAPI_ReviewSignalAuthorizesNothing(t *testing.T) {
	f := newTransferAPI(t)
	code, raw := f.doRaw(t, http.MethodGet, "/stay-transfers/review-signals", nil)
	if code != http.StatusOK {
		t.Fatalf("review signals: %d %s", code, raw)
	}
	low := strings.ToLower(raw)
	if !strings.Contains(low, "not transfers") || !strings.Contains(low, "never evidence") {
		t.Fatalf("the review-signal payload does not say what it is not: %s", raw)
	}
	// It must not offer a transfer, an id to act on, or anything that looks like an authorization.
	for _, forbidden := range []string{"transfer_id", "authorized", "confirm"} {
		if strings.Contains(low, forbidden) {
			t.Fatalf("the review-signal payload contains %q", forbidden)
		}
	}
}

// The preview writes nothing. An operator can look at a transfer they will not perform.
func TestIntegration_TransferAPI_PreviewIsReadOnly(t *testing.T) {
	f := newTransferAPI(t)
	x := seedTransfer(t, f)
	ctx := context.Background()

	var before int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlement_transfers
		WHERE from_stay_id=$1`, x.stayA).Scan(&before); err != nil {
		t.Fatalf("count: %v", err)
	}
	code, body := f.do(t, http.MethodPost, "/stay-transfers/preview",
		map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayB})
	if code != http.StatusOK {
		t.Fatalf("preview: %d %v", code, body)
	}
	if body["blocker"] != nil && body["blocker"] != "" {
		t.Fatalf("preview blocked a transfer that should succeed: %v", body["blocker"])
	}
	if body["from_room"] != "101" || body["to_room"] != "201" {
		t.Fatalf("preview does not show which rooms are involved: %v", body)
	}
	var after int
	var status string
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM iam_v2.entitlement_transfers WHERE from_stay_id=$2),
		(SELECT status FROM iam_v2.entitlements WHERE id=$1)`, x.entA, x.stayA).Scan(&after, &status); err != nil {
		t.Fatalf("count: %v", err)
	}
	if after != before || status != "ACTIVE" {
		t.Fatalf("the preview changed state: transfers %d->%d, entitlement %s", before, after, status)
	}

	// A room move is named as one BEFORE the operator types a password.
	_, blocked := f.do(t, http.MethodPost, "/stay-transfers/preview",
		map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayA2})
	if b, _ := blocked["blocker"].(string); !strings.Contains(b, "room move") {
		t.Fatalf("the preview did not name the room-move blocker: %v", blocked)
	}
}

// Each guard removed in turn, and the guest untouched by every refusal.
func TestIntegration_TransferAPI_ExecuteRequiresTheFullWeight(t *testing.T) {
	f := newTransferAPI(t)
	x := seedTransfer(t, f)

	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"no reason", map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayB,
			"password": f.password}, http.StatusBadRequest},
		{"a reason too short to mean anything", map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayB,
			"password": f.password, "reason": "x"}, http.StatusBadRequest},
		{"the wrong password", map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayB,
			"password": "nope", "reason": "guest moved properties"}, http.StatusUnauthorized},
		{"no password", map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayB,
			"reason": "guest moved properties"}, http.StatusUnauthorized},
		{"no destination", map[string]any{"from_stay_id": x.stayA,
			"password": f.password, "reason": "guest moved properties"}, http.StatusBadRequest},
		{"a room move", map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayA2,
			"password": f.password, "reason": "guest moved properties"}, http.StatusConflict},
		{"a destination that does not exist", map[string]any{"from_stay_id": x.stayA,
			"to_stay_id": "00000000-0000-0000-0000-0000000000ff",
			"password":   f.password, "reason": "guest moved properties"}, http.StatusConflict},
	} {
		code, raw := f.doRaw(t, http.MethodPost, "/stay-transfers/execute", tc.body)
		if code != tc.want {
			t.Fatalf("%s: status %d (want %d): %s", tc.name, code, tc.want, raw)
		}
	}
	// The guest is exactly as they were after every one of those.
	var status string
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT (SELECT status FROM iam_v2.entitlements WHERE id=$1),
		        (SELECT count(*) FROM iam_v2.entitlement_transfers WHERE from_stay_id=$2)`,
		x.entA, x.stayA).Scan(&status, &n); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if status != "ACTIVE" || n != 0 {
		t.Fatalf("a refused transfer disturbed state: entitlement=%s transfers=%d", status, n)
	}
}

func TestIntegration_TransferAPI_ExecuteMovesAccessAndAudits(t *testing.T) {
	f := newTransferAPI(t)
	x := seedTransfer(t, f)
	code, body := f.do(t, http.MethodPost, "/stay-transfers/execute",
		map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayB,
			"password": f.password, "reason": "guest moved to the sister property"})
	if code != http.StatusOK {
		t.Fatalf("execute: %d %v", code, body)
	}
	if body["transfer_id"] == nil || body["transfer_id"] == "" {
		t.Fatalf("no transfer id: %v", body)
	}
	ctx := context.Background()
	var action, payload string
	if err := f.pool.QueryRow(ctx,
		`SELECT action, payload::text FROM public.audit_log WHERE target_id=$1 ORDER BY id DESC LIMIT 1`,
		x.stayA).Scan(&action, &payload); err != nil {
		t.Fatalf("audit: %v", err)
	}
	if action != "stay.cross_pms_transfer" {
		t.Fatalf("audit action = %q", action)
	}
	if !strings.Contains(payload, "sister property") || !strings.Contains(payload, x.stayB) {
		t.Fatalf("the audit does not record what was done and why: %s", payload)
	}
	// ...and the lineage is readable afterwards, which is what makes the record worth keeping.
	_, list := f.do(t, http.MethodGet, "/stay-transfers/", nil)
	rows, _ := list["transfers"].([]any)
	if len(rows) != 1 {
		t.Fatalf("lineage list has %d row(s)", len(rows))
	}
}

func TestIntegration_TransferAPI_RBAC(t *testing.T) {
	f := newTransferAPI(t, "voucher_operator")
	x := seedTransfer(t, f)
	if code, _ := f.doRaw(t, http.MethodGet, "/stay-transfers/", nil); code != http.StatusForbidden {
		t.Fatalf("a role with no transfer permission read the lineage: %d", code)
	}
	if code, _ := f.doRaw(t, http.MethodPost, "/stay-transfers/execute",
		map[string]any{"from_stay_id": x.stayA, "to_stay_id": x.stayB,
			"password": f.password, "reason": "should never happen"}); code != http.StatusForbidden {
		t.Fatalf("a role with no transfer permission executed a transfer: %d", code)
	}

	g := newTransferAPI(t, "site_viewer")
	y := seedTransfer(t, g)
	if code, _ := g.doRaw(t, http.MethodGet, "/stay-transfers/review-signals", nil); code != http.StatusOK {
		t.Fatalf("a viewer could not read the review signals: %d", code)
	}
	if code, _ := g.doRaw(t, http.MethodPost, "/stay-transfers/execute",
		map[string]any{"from_stay_id": y.stayA, "to_stay_id": y.stayB,
			"password": g.password, "reason": "viewer should not transfer"}); code != http.StatusForbidden {
		t.Fatalf("a viewer executed a transfer: %d", code)
	}
	// A viewer must not be able to PREVIEW either — the preview reveals another reservation's room number,
	// and read on the lineage is not read on arbitrary stay pairs.
	if code, _ := g.doRaw(t, http.MethodPost, "/stay-transfers/preview",
		map[string]any{"from_stay_id": y.stayA, "to_stay_id": y.stayB}); code != http.StatusForbidden {
		t.Fatalf("a viewer previewed a transfer: %d", code)
	}
}
