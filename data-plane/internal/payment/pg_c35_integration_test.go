//go:build integration

package payment

import (
	"context"
	"strings"
	"testing"
)

// Archive before cross-customer purge, failing closed. A local archive row must not authorize a purge
// (self-certification by the party deleting). Without an external verified receipt purge is impossible, which is
// intended since no receipt authority exists; the tests also show the gate opens if a real receipt existed.

func seedArchive(t *testing.T, s scope) string {
	t.Helper()
	var id string
	if err := pool(t).QueryRow(context.Background(),
		`SELECT iam_v2.p4_record_compliance_archive($1::uuid,$2::uuid,$3,$4,$5::jsonb)::text`,
		s.tenant, s.site, strings.Repeat("b", 64), "/var/backups/stayconnect/compliance/t.json",
		`{"iam_v2.purchases":3}`).Scan(&id); err != nil {
		t.Fatalf("record archive: %v", err)
	}
	return id
}

func TestIntegrationC35_LocalArchiveAloneCannotAuthorizeAPurge(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)

	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err == nil {
		t.Fatal("a tenant with no archive was cleared for purge")
	} else if !strings.Contains(err.Error(), "COMPLIANCE_ARCHIVE_MISSING") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	id := seedArchive(t, s)
	if v := scan1[bool](t, p, `SELECT receipt_verified FROM iam_v2.compliance_archives WHERE id=$1`, id); v {
		t.Fatal("a locally written archive claimed a verified receipt")
	}
	_, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant)
	if err == nil {
		t.Fatal("a local archive with no external receipt authorized a cross-customer purge")
	}
	if !strings.Contains(err.Error(), "COMPLIANCE_RECEIPT_UNVERIFIED") {
		t.Fatalf("refused, but not for the receipt: %v", err)
	}
}

// No writer may set the flag by hand, and the flag cannot be true without the external evidence.
func TestIntegrationC35_TheReceiptFlagCannotBeForged(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	id := seedArchive(t, s)

	// Even the OWNER cannot produce a receipt: the constraint requires the external evidence alongside the flag.
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.compliance_archives SET receipt_verified = true WHERE id = $1`, id); err == nil {
		t.Fatal("the receipt flag was set with no authority and no reference")
	} else if !strings.Contains(err.Error(), "ca_receipt_evidence_matches_flag") {
		t.Fatalf("refused, but not by the evidence constraint: %v", err)
	}
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.compliance_archives
		    SET receipt_verified = true, receipt_authority = '  ', receipt_reference = 'x',
		        receipt_verified_at = now() WHERE id = $1`, id); err == nil {
		t.Fatal("a blank authority satisfied the receipt evidence constraint")
	}
	if _, err := p.Exec(ctx,
		`INSERT INTO iam_v2.compliance_archives(tenant_id,site_id,manifest_sha256,receipt_verified,purpose)
		 VALUES ($1,$2,$3,true,'CROSS_CUSTOMER_PURGE')`,
		s.tenant, s.site, strings.Repeat("c", 64)); err == nil {
		t.Fatal("an archive was inserted already verified, with no authority named")
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err == nil {
		t.Fatal("the forgery attempts left the tenant cleared for purge")
	}
}

// The recorder is granted to nobody: there is no authority to receive a receipt from, and granting it would create the
// capability by the back door.
func TestIntegrationC35_NoRuntimeRoleCanRecordAReceipt(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	for _, role := range []string{"sc_payment_runtime", "sc_payment_outcome", "sc_commerce_runtime",
		"sc_financial_operator", "sc_financial_readonly"} {
		var can bool
		if err := p.QueryRow(ctx,
			`SELECT has_function_privilege($1,'iam_v2.p4_record_compliance_receipt(uuid,text,text)','EXECUTE')`,
			role).Scan(&can); err != nil {
			t.Fatal(err)
		}
		if can {
			t.Errorf("%s can record a compliance receipt; there is no authority for it to have heard from", role)
		}
	}
	var pub bool
	if err := p.QueryRow(ctx,
		`SELECT has_function_privilege('public','iam_v2.p4_record_compliance_receipt(uuid,text,text)','EXECUTE')`).
		Scan(&pub); err != nil {
		t.Fatal(err)
	}
	if pub {
		t.Fatal("PUBLIC can record a compliance receipt")
	}
}

// The gate is not "always refuse": it opens if a real authority acknowledges custody.
func TestIntegrationC35_AVerifiedReceiptWouldOpenTheGate(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	id := seedArchive(t, s)

	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err == nil {
		t.Fatal("setup: the gate was already open")
	}
	// Recorded as OWNER, standing in for a future authority; no runtime role can do this.
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_receipt($1::uuid,$2,$3)`,
		id, "test-archival-authority", "receipt-0001"); err != nil {
		t.Fatalf("recording a receipt with full evidence: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err != nil {
		t.Fatalf("the gate stayed shut with a verified receipt: %v", err)
	}
	auth := scan1[string](t, p, `SELECT receipt_authority FROM iam_v2.compliance_archives WHERE id=$1`, id)
	if auth != "test-archival-authority" {
		t.Fatalf("the receipt authority was not recorded: %q", auth)
	}
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_receipt($1::uuid,'someone-else','receipt-0002')`, id); err == nil {
		t.Fatal("a second authority overwrote the first acknowledgement")
	}
	other := seedArchive(t, seedPaidChain(t, p))
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_receipt($1::uuid,'an-authority','')`, other); err == nil {
		t.Fatal("a receipt with no reference was accepted")
	}
}

// One tenant's verified receipt must not clear a different tenant for purge.
func TestIntegrationC35_AReceiptIsScopedToItsOwnTenant(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	a := seedPaidChain(t, p)
	b := seedPaidChain(t, p)

	id := seedArchive(t, a)
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_receipt($1::uuid,'test-archival-authority','receipt-A')`,
		id); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, a.tenant); err != nil {
		t.Fatalf("tenant A should now be clear: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, b.tenant); err == nil {
		t.Fatal("tenant A's receipt cleared tenant B for purge")
	}
}
