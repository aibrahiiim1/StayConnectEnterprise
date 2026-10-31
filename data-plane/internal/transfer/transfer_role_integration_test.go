//go:build integration && phase5

package transfer

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The transfer runs as the role Hotel Admin connects as, under the production Gate-P grants (the full appliance schema the harness
// builds ends with gatep-grants.sql). Before migration 0201 the same operation failed there with "permission denied": svc_edged holds
// neither EXECUTE on the entitlement state writer nor INSERT on the commerce and lineage tables.
func edgedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE svc_edged")
		return err
	}
	rp, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("role pool: %v", err)
	}
	t.Cleanup(rp.Close)
	return rp
}

func TestIntegration_F9_TransferRunsAsTheHotelAdminRoleUnderTheProductionGrants(t *testing.T) {
	p := pool(t)
	defer p.Close()
	rp := edgedPool(t)
	f := seed(t, p)
	fromEnt, session := grant(t, p, f, f.stayA)
	ctx := context.Background()

	var who string
	if err := rp.QueryRow(ctx, `SELECT current_user`).Scan(&who); err != nil || who != "svc_edged" {
		t.Fatalf("the pool must run as svc_edged, ran as %q (%v)", who, err)
	}

	s := New(rp)
	if pv, err := s.PreviewTransfer(ctx, f.tenant, f.site, f.stayA, f.stayB); err != nil || pv.Blocker != "" {
		t.Fatalf("the preview as svc_edged: %+v %v", pv, err)
	}
	out, err := s.Execute(ctx, req(f, f.stayA, f.stayB))
	if err != nil {
		t.Fatalf("the transfer as svc_edged under the production grants: %v", err)
	}
	if out.FromEntitlement != fromEnt || out.DevicesRebound != 1 || out.SessionsRebound != 1 || out.TransferID == "" || out.ToPackage == "" {
		t.Fatalf("result = %+v", out)
	}

	// Same outcome as the owner-run path: source terminated as TRANSFERRED, session moved in place AND its binding, lineage written,
	// no money moved, the operator and reason stored.
	var status, reason, openEnt, actor, opReason string
	if err := p.QueryRow(ctx, `SELECT e.status, COALESCE(e.terminal_reason,'') FROM iam_v2.entitlements e WHERE e.id=$1`, fromEnt).
		Scan(&status, &reason); err != nil || status != "TERMINATED" || reason != "TRANSFERRED" {
		t.Fatalf("source = %s/%s (%v)", status, reason, err)
	}
	if err := p.QueryRow(ctx, `SELECT entitlement_id::text FROM iam_v2.session_entitlement_bindings WHERE session_id=$1 AND bound_until IS NULL`, session).
		Scan(&openEnt); err != nil || openEnt != out.ToEntitlement {
		t.Fatalf("the open binding is %q (%v), want the destination %q", openEnt, err, out.ToEntitlement)
	}
	var sessEnt string
	if err := p.QueryRow(ctx, `SELECT entitlement_id::text FROM iam_v2.sessions WHERE id=$1`, session).Scan(&sessEnt); err != nil || sessEnt != out.ToEntitlement {
		t.Fatalf("the session row points at %q (%v)", sessEnt, err)
	}
	if err := p.QueryRow(ctx, `SELECT actor::text, operator_reason FROM iam_v2.entitlement_transfers WHERE id=$1`, out.TransferID).
		Scan(&actor, &opReason); err != nil || actor != operator || opReason != req(f, f.stayA, f.stayB).Reason {
		t.Fatalf("lineage actor/reason = %q/%q (%v)", actor, opReason, err)
	}
	var amount int64
	if err := p.QueryRow(ctx, `SELECT pu.amount_minor FROM iam_v2.purchases pu JOIN iam_v2.entitlements e ON e.purchase_id=pu.id WHERE e.id=$1`, out.ToEntitlement).Scan(&amount); err != nil || amount != 0 {
		t.Fatalf("the transfer moved money: %d (%v)", amount, err)
	}

	// The refusals still come back as the package's own errors, through the role.
	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayB)); !errors.Is(err, ErrSourceNotTransferable) && !errors.Is(err, ErrAlreadyTransferred) {
		t.Fatalf("a second transfer of the same access = %v, want a typed refusal", err)
	}
	if _, err := s.Execute(ctx, req(f, f.stayA, f.stayA2)); !errors.Is(err, ErrSameInterface) {
		t.Fatalf("a room move through the role = %v, want ErrSameInterface", err)
	}
	bad := req(f, f.stayA, f.stayB)
	bad.Reason = "x"
	if _, err := s.Execute(ctx, bad); !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("a missing reason = %v", err)
	}
}

// The other half of least privilege: the role still cannot do any of those writes directly. Only the function can.
func TestIntegration_F9_TheHotelAdminRoleStillCannotWriteTheTransferTablesDirectly(t *testing.T) {
	p := pool(t)
	defer p.Close()
	rp := edgedPool(t)
	f := seed(t, p)
	fromEnt, _ := grant(t, p, f, f.stayA)
	ctx := context.Background()

	denied := func(name, q string, args ...any) {
		t.Helper()
		tx, err := rp.Begin(ctx)
		if err != nil {
			t.Fatalf("%s: begin: %v", name, err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, q, args...)
		if err == nil {
			t.Fatalf("%s: svc_edged was allowed to write directly", name)
		}
		if !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("%s: refused for the wrong reason: %v", name, err)
		}
	}
	denied("purchases", `INSERT INTO iam_v2.purchases (tenant_id,site_id,package_revision_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,'CROSS_PMS_TRANSFER',0,'GRANTED')`, f.tenant, f.site, f.pkg, f.stayB)
	denied("entitlements", `INSERT INTO iam_v2.entitlements (tenant_id,site_id,stay_id,status) VALUES ($1,$2,$3,'PENDING')`, f.tenant, f.site, f.stayB)
	denied("entitlement_transfers", `INSERT INTO iam_v2.entitlement_transfers
		(tenant_id,site_id,from_entitlement_id,to_entitlement_id,from_stay_id,to_stay_id,actor) VALUES ($1,$2,$3,$3,$4,$5,$6)`,
		f.tenant, f.site, fromEnt, f.stayA, f.stayB, operator)
	denied("stay_links", `INSERT INTO iam_v2.stay_links (tenant_id,site_id,from_stay,to_stay,reason) VALUES ($1,$2,$3,$4,'CROSS_PMS_TRANSFER')`,
		f.tenant, f.site, f.stayA, f.stayB)
	denied("entitlement_devices", `INSERT INTO iam_v2.entitlement_devices (tenant_id,site_id,entitlement_id,device_id,status) VALUES ($1,$2,$3,$4,'AUTHORIZED')`,
		f.tenant, f.site, fromEnt, f.device)
	denied("entitlement_device_authorizations", `INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
		VALUES ($1,$2,$3,$4,1,now())`, f.tenant, f.site, fromEnt, f.device)
	denied("sessions.entitlement_id", `UPDATE iam_v2.sessions SET entitlement_id = $1 WHERE entitlement_id = $2`, fromEnt, fromEnt)
	denied("the entitlement state writer", `SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now(),'ADMIN')`, fromEnt)
	denied("the binding writer", `SELECT iam_v2.rebind_session_entitlement(gen_random_uuid(), $1, now())`, fromEnt)

	// And the one thing it may execute is that function, nothing broader: it cannot be executed by PUBLIC.
	var pub bool
	if err := p.QueryRow(ctx, `SELECT has_function_privilege('public','iam_v2.cross_pms_transfer(uuid,uuid,uuid,uuid,uuid,text,integer)','EXECUTE')`).Scan(&pub); err != nil || pub {
		t.Fatalf("PUBLIC can execute the transfer function (%v, %v)", pub, err)
	}
}
