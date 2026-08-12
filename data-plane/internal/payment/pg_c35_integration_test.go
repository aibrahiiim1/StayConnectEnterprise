//go:build integration

package payment

import (
	"context"
	"strings"
	"testing"
)

// C35 — ARCHIVE BEFORE CROSS-CUSTOMER PURGE, FAILING CLOSED.
//
// The previous milestone delivered the archive, its digest and a gate. The gate was wrong in a way worth
// naming: it passed as soon as ANY archive row existed, so the local export written by the appliance that
// was about to delete the data was sufficient to authorize the deletion. That is self-certification. The
// party doing the deleting attests it kept a copy and nothing outside it agrees.
//
// These tests prove the corrected property: an archive WITHOUT an external verified receipt cannot purge,
// and no writer can get round the gate. Because no archival receipt authority exists in this product, the
// consequence is that cross-customer purge is impossible — and that is the intended state, not a defect.
// The tests therefore also prove the gate would OPEN if a real receipt existed, so "always refuses" is a
// consequence of the missing capability rather than a gate that refuses everything unconditionally.

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

// The finding itself: a local archive with no external acknowledgement must not authorize a purge.
func TestIntegrationC35_LocalArchiveAloneCannotAuthorizeAPurge(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)

	// no archive at all
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err == nil {
		t.Fatal("a tenant with no archive was cleared for purge")
	} else if !strings.Contains(err.Error(), "COMPLIANCE_ARCHIVE_MISSING") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	// an archive the appliance wrote itself, digest and all
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

// No writer may set the flag by hand, and the flag cannot be true without the evidence that makes it mean
// something. This is the half that stops the gate being routed around rather than satisfied.
func TestIntegrationC35_TheReceiptFlagCannotBeForged(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	id := seedArchive(t, s)

	// a direct UPDATE as the OWNER -- the most privileged writer there is -- still cannot produce a
	// meaningful receipt, because the constraint requires the external evidence alongside the flag.
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.compliance_archives SET receipt_verified = true WHERE id = $1`, id); err == nil {
		t.Fatal("the receipt flag was set with no authority and no reference")
	} else if !strings.Contains(err.Error(), "ca_receipt_evidence_matches_flag") {
		t.Fatalf("refused, but not by the evidence constraint: %v", err)
	}
	// nor with a blank authority
	if _, err := p.Exec(ctx,
		`UPDATE iam_v2.compliance_archives
		    SET receipt_verified = true, receipt_authority = '  ', receipt_reference = 'x',
		        receipt_verified_at = now() WHERE id = $1`, id); err == nil {
		t.Fatal("a blank authority satisfied the receipt evidence constraint")
	}
	// and an INSERT cannot be born verified either
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

// The runtime roles cannot reach the receipt recorder at all. It is granted to nobody, because there is no
// authority to receive a receipt from and granting it would create the capability by the back door.
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
	// ...and PUBLIC certainly cannot
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

// The gate is not simply "always refuse". If a real external authority ever acknowledges custody, the same
// gate opens — which is what makes the current refusal a statement about the missing capability rather
// than about the gate.
func TestIntegrationC35_AVerifiedReceiptWouldOpenTheGate(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)
	id := seedArchive(t, s)

	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err == nil {
		t.Fatal("setup: the gate was already open")
	}
	// recorded as the OWNER, standing in for the day an authority exists. No runtime role can do this.
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_receipt($1::uuid,$2,$3)`,
		id, "test-archival-authority", "receipt-0001"); err != nil {
		t.Fatalf("recording a receipt with full evidence: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err != nil {
		t.Fatalf("the gate stayed shut with a verified receipt: %v", err)
	}
	// the evidence is on the row, so a later reader can see who acknowledged what
	auth := scan1[string](t, p, `SELECT receipt_authority FROM iam_v2.compliance_archives WHERE id=$1`, id)
	if auth != "test-archival-authority" {
		t.Fatalf("the receipt authority was not recorded: %q", auth)
	}
	// and custody cannot be acknowledged twice
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_receipt($1::uuid,'someone-else','receipt-0002')`, id); err == nil {
		t.Fatal("a second authority overwrote the first acknowledgement")
	}
	// a receipt with no reference is refused outright
	other := seedArchive(t, seedPaidChain(t, p))
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_receipt($1::uuid,'an-authority','')`, other); err == nil {
		t.Fatal("a receipt with no reference was accepted")
	}
}

// The scope check: one tenant's verified receipt must not clear a different tenant for purge.
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
