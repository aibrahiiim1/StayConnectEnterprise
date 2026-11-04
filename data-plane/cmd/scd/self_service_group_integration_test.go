//go:build integration

package main

// A client who registers with an address in a Client Group's domain is signed in before the address is verified, so that
// sign-in carries no group; verifying must give the still-open sign-in its group, decided from the verified address only.

import (
	"context"
	"strings"
	"testing"

	"github.com/onegate/onegate/data-plane/internal/iamv2"
)

func TestIntegration_VerifyingAnEmailAccountGivesItsOpenSignInTheGroup(t *testing.T) {
	f := newSSFixture(t)
	s := f.s
	ctx := context.Background()
	domain := "corp" + randHex(3) + ".example"
	var gid string
	if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.client_groups (tenant_id, site_id, name, priority) VALUES ($1,$2,'Staff',10) RETURNING id::text`,
		f.tenant, f.site).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	f.ownerExec(t, `INSERT INTO iam_v2.client_group_rules (tenant_id, site_id, group_id, rule_type, rule_value)
		VALUES ($1,$2,$3::uuid,'EMAIL_DOMAIN',jsonb_build_object('domains', jsonb_build_array($4::text)))`, f.tenant, f.site, gid, domain)

	pinOf := func(acID string) string {
		t.Helper()
		var g *string
		if err := f.owner.QueryRow(ctx, `SELECT client_group_id::text FROM iam_v2.auth_contexts WHERE id=$1::uuid`, acID).Scan(&g); err != nil {
			t.Fatal(err)
		}
		if g == nil {
			return ""
		}
		return *g
	}

	code, out := f.post(t, s.accountRegister, map[string]string{"email": "ann@" + domain, "password": "correct horse battery",
		"ip": f.ip(40), "mac": "02:17:00:40:" + randHex(1) + ":01"})
	if code != 200 || out["auth_context_id"] == "" {
		t.Fatalf("register: %d %v", code, out)
	}
	ac := out["auth_context_id"].(string)
	if g := pinOf(ac); g != "" {
		t.Fatalf("an unverified address decided a group: %s", g)
	}
	tok := f.sendAndTakeToken(t, "/account/verify")
	if code, out := f.post(t, s.accountVerify, map[string]string{"token": tok}); code != 200 || out["verified"] != true {
		t.Fatalf("verify: %d %v", code, out)
	}
	if g := pinOf(ac); g != gid {
		t.Fatalf("after verifying, the open sign-in is in group %q, want %q", g, gid)
	}
	var evidence string
	_ = f.owner.QueryRow(ctx, `SELECT client_group_evidence::text FROM iam_v2.auth_contexts WHERE id=$1::uuid`, ac).Scan(&evidence)
	if strings.Contains(evidence, "ann@") || !strings.Contains(evidence, "EMAIL_DOMAIN") {
		t.Fatalf("the evidence must name the rule, not the address: %s", evidence)
	}
	// The package reader sees the group, so the group's packages are what the client is offered now.
	_ = iamv2.NewPgCommerceRepository(f.owner).WithTx(ctx, func(tx iamv2.CommerceTx) error {
		row, err := tx.LoadAuthContext(ctx, f.tenant, f.site, ac)
		if err != nil || row.ClientGroupID != gid {
			t.Fatalf("the commerce reader must see the group: %+v %v", row, err)
		}
		return nil
	})

	// An address outside the domain earns nothing by verifying.
	code, out = f.post(t, s.accountRegister, map[string]string{"email": "zed@elsewhere" + randHex(2) + ".example", "password": "correct horse battery",
		"ip": f.ip(41), "mac": "02:17:00:41:" + randHex(1) + ":01"})
	if code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	other := out["auth_context_id"].(string)
	tok2 := f.sendAndTakeToken(t, "/account/verify")
	if code, _ := f.post(t, s.accountVerify, map[string]string{"token": tok2}); code != 200 {
		t.Fatalf("verify: %d", code)
	}
	if g := pinOf(other); g != "" {
		t.Fatalf("an address outside the domain was put in a group: %s", g)
	}
}
