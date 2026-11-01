//go:build integration

package main

import (
	"context"
	"net/http"
	"testing"
)

// A staff Reset of a Client Account package must stay usable: session activation admits the device that acquired the package by
// following entitlement -> purchase -> auth context, so the reset's free ADMIN_GRANT purchase keeps the original's acquisition
// lineage (review finding on PR #280). Without it the reset succeeded and its guest could not sign in.
func TestIntegration_AResetAccountPackageAdmitsTheDeviceThatAcquiredIt(t *testing.T) {
	f := newSSFixture(t)
	ctx := context.Background()
	account := f.seedAccount(t, "reset-"+randHex(3))
	ent, _, _ := f.seedEntitlementSession(t, account, f.ip(50), "02:33:00:00:"+randHex(1)+":01")
	mac := "02:33:00:00:" + randHex(1) + ":02"
	dev := f.newDeviceAcquiring(t, ent, account, mac)

	var operator string
	if err := f.owner.QueryRow(ctx, `INSERT INTO public.operators (tenant_id, email) VALUES ($1, 'reset-'||$2||'@staff.test')
		RETURNING id::text`, f.tenant, randHex(4)).Scan(&operator); err != nil {
		t.Fatalf("operator: %v", err)
	}
	var newEnt string
	if err := f.owner.QueryRow(ctx, `SELECT new_entitlement::text FROM iam_v2.admin_reset_package($1,$2,$3,$4,'guest asked at the desk',gen_random_uuid())`,
		f.tenant, f.site, ent, operator).Scan(&newEnt); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var sameChain bool
	if err := f.owner.QueryRow(ctx, `SELECT pn.auth_context_id IS NOT DISTINCT FROM po.auth_context_id AND pn.auth_context_id IS NOT NULL
		  FROM iam_v2.entitlements en JOIN iam_v2.purchases pn ON pn.id = en.purchase_id,
		       iam_v2.entitlements eo JOIN iam_v2.purchases po ON po.id = eo.purchase_id
		 WHERE en.id = $1 AND eo.id = $2`, newEnt, ent).Scan(&sameChain); err != nil || !sameChain {
		t.Fatalf("the reset purchase lost the acquisition lineage: %v %v", sameChain, err)
	}
	code, out := f.activateQuick(t, map[string]string{"entitlement_id": newEnt, "device_id": dev, "ip": f.ip(51), "mac": mac})
	if code != http.StatusOK || out["session_id"] == nil || out["session_id"] == "" {
		t.Fatalf("the device that acquired the package cannot use its reset: %d %v", code, out)
	}
}
