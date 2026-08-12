//go:build integration

package payment

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

// FINAL SOFTWARE CLOSURE — the four findings that stood between Phase 4 and being eligible for WS-L.
//
// Each block below is an adversarial proof of one of them: the zero-attempt restore that could never be
// released, the marker cases the detector did not cover, cross-tenant merchant reuse, and the compliance
// archive that existed only as a table shape.

// ---------------------------------------------------------------- the zero-attempt restore

// setupZeroAttemptHold produces the exact state: a posting held by recovery, with no attempts at all,
// reconciled as CONFIRMED_NOT_COMPLETED. Before 0025 this was a dead end.
func setupZeroAttemptHold(t *testing.T, p *pgxpool.Pool, s scope) (postingID, outboxID, actor string) {
	t.Helper()
	ctx := context.Background()
	_, outboxID, _ = seedOutboxPosting(t, p, s)
	postingID = scan1[string](t, p, `SELECT posting_id::text FROM iam_v2.posting_outbox WHERE id=$1`, outboxID)
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
	simulateRestore(t, p, s)
	if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}
	actor = seedOperator(t, p, s.tenant)
	holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
	for _, h := range holds {
		if h.Kind != "POSTING_OUTBOX" || h.WorkID != outboxID {
			continue
		}
		if err := e.ResolveHold(ctx, h.ID, "CONFIRMED_NOT_COMPLETED", actor,
			"checked the folio directly; this charge was never posted"); err != nil {
			t.Fatal(err)
		}
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_attempts
		WHERE internal_posting_id=$1`, postingID); n != 0 {
		t.Fatalf("setup: the posting has %d attempts, so this is not the zero-attempt case", n)
	}
	return postingID, outboxID, actor
}

func TestIntegrationClosure_ZeroAttemptRestoreHasASafeAuditedPathOut(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	postingID, outboxID, actor := setupZeroAttemptHold(t, p, s)

	// The ordinary review path cannot help: it refuses when there is nothing to read.
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.record_posting_review_action($1::uuid,'CONFIRM_NOT_POSTED_RETRY',$2::uuid,
		   'nothing was posted',jsonb_build_object('source_type','PMS_SCREEN'),NULL,NULL)`,
		postingID, actor); err == nil {
		t.Fatal("the ordinary review path accepted a posting with no attempts")
	}
	// ...and the row is still held, which is why this was a dead end before 0025.
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
		t.Fatalf("setup: expected HELD_RECOVERY, got %s", st)
	}

	// The audited one-time authorization releases it.
	var actionID string
	if err := p.QueryRow(ctx,
		`SELECT iam_v2.p4_authorize_zero_attempt_retry($1::uuid,$2::uuid,$3,$4::jsonb)::text`,
		postingID, actor, "folio checked directly; nothing was posted, so the charge must still go out",
		`{"source_type":"PMS_SCREEN"}`).Scan(&actionID); err != nil {
		t.Fatalf("the zero-attempt authorization was refused: %v", err)
	}
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "QUEUED" {
		t.Fatalf("the authorized posting is %s, not sendable", st)
	}

	// It authorized attempt ONE, and it did NOT manufacture a history.
	if n := scan1[int](t, p, `SELECT retry_authorized_attempt_no FROM iam_v2.posting_review_state
		WHERE posting_id=$1`, postingID); n != 1 {
		t.Fatalf("expected attempt 1 to be authorized, got %d", n)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_attempts
		WHERE internal_posting_id=$1`, postingID); n != 0 {
		t.Fatalf("the authorization invented %d historical attempt(s)", n)
	}
	// and it is in the SAME append-only review ledger as every other financial decision
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_review_actions
		WHERE posting_id=$1 AND action='CONFIRM_NOT_POSTED_RETRY'`, postingID); n != 1 {
		t.Fatalf("expected exactly one audited decision, got %d", n)
	}
}

func TestIntegrationClosure_ZeroAttemptRetryRefusesEveryUnsafeCase(t *testing.T) {
	p := pool(t)
	ctx := context.Background()

	t.Run("without a reconciliation establishing it was not completed", func(t *testing.T) {
		s := recoverySite(t, p)
		_, outboxID, _ := seedOutboxPosting(t, p, s)
		postingID := scan1[string](t, p, `SELECT posting_id::text FROM iam_v2.posting_outbox WHERE id=$1`, outboxID)
		e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})
		simulateRestore(t, p, s)
		if _, err := e.ReconcileEpoch(ctx, s.tenant, s.site); err != nil {
			t.Fatal(err)
		}
		actor := seedOperator(t, p, s.tenant)
		if _, err := p.Exec(ctx,
			`SELECT iam_v2.p4_authorize_zero_attempt_retry($1::uuid,$2::uuid,$3,'{}'::jsonb)`,
			postingID, actor, "no reconciliation has been done at all"); err == nil {
			t.Fatal("an unreconciled posting was authorized for retry")
		}
		if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
			t.Fatalf("the refused posting moved to %s", st)
		}
	})

	t.Run("with a forged actor", func(t *testing.T) {
		s := recoverySite(t, p)
		postingID, outboxID, _ := setupZeroAttemptHold(t, p, s)
		if _, err := p.Exec(ctx,
			`SELECT iam_v2.p4_authorize_zero_attempt_retry($1::uuid,'11111111-2222-3333-4444-555555555555'::uuid,
			   $2,'{}'::jsonb)`,
			postingID, "an authorization attributed to nobody"); err == nil {
			t.Fatal("an invented actor authorized a retry")
		}
		if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "HELD_RECOVERY" {
			t.Fatalf("a forged authorization released the posting: %s", st)
		}
	})

	t.Run("with an evidence payload carrying a secret", func(t *testing.T) {
		s := recoverySite(t, p)
		postingID, _, actor := setupZeroAttemptHold(t, p, s)
		if _, err := p.Exec(ctx,
			`SELECT iam_v2.p4_authorize_zero_attempt_retry($1::uuid,$2::uuid,$3,$4::jsonb)`,
			postingID, actor, "folio checked; nothing was posted",
			`{"raw_body":"{\"card\":\"4111111111111111\"}"}`); err == nil {
			t.Fatal("an unsafe evidence payload entered the immutable review ledger")
		}
	})

	t.Run("when the posting does have attempts", func(t *testing.T) {
		s := recoverySite(t, p)
		postingID, _, actor := setupZeroAttemptHold(t, p, s)
		// give it a real attempt, so the ordinary path is the applicable one
		if _, err := p.Exec(ctx, `INSERT INTO iam_v2.posting_attempts
			(tenant_id,site_id,internal_posting_id,pms_interface_id,attempt_no,p_number,rn,g_number,
			 sent_at,outcome)
			SELECT $1,$2,$3,pms_interface_id,1,'99','101','7',now(),'UNKNOWN'
			  FROM iam_v2.pms_postings WHERE id=$3`,
			s.tenant, s.site, postingID); err != nil {
			t.Fatalf("staging an attempt: %v", err)
		}
		if _, err := p.Exec(ctx,
			`SELECT iam_v2.p4_authorize_zero_attempt_retry($1::uuid,$2::uuid,$3,'{}'::jsonb)`,
			postingID, actor, "trying the zero-attempt path on a posting that has one"); err == nil {
			t.Fatal("the zero-attempt path accepted a posting with attempts")
		}
	})
}

// Releasing recovery must not make the authorized retry replay by itself: the worker sends it because it is
// QUEUED and authorized, never because recovery ended.
func TestIntegrationClosure_NothingReplaysAcrossRecoveryRelease(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := recoverySite(t, p)
	postingID, outboxID, actor := setupZeroAttemptHold(t, p, s)
	e := NewEngine(liveCfg, p, NewScriptedProvider(), &fakeGranter{})

	// BEFORE release: authorized and queued, but nothing has been sent.
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_authorize_zero_attempt_retry($1::uuid,$2::uuid,$3,'{}'::jsonb)`,
		postingID, actor, "folio checked directly; nothing was posted"); err != nil {
		t.Fatal(err)
	}
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_attempts
		WHERE internal_posting_id=$1`, postingID); n != 0 {
		t.Fatal("authorizing a retry transmitted something")
	}

	// resolve whatever else is held, then release
	holds, _ := e.OpenHolds(ctx, s.tenant, s.site, 50)
	for _, h := range holds {
		if err := e.ResolveHold(ctx, h.ID, "CONFIRMED_NOT_COMPLETED", actor,
			"reconciled against the folio and the provider dashboard"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.ReleaseRecovery(ctx, s.tenant, s.site, actor,
		"every held item reconciled; the one authorized retry is queued for the worker"); err != nil {
		t.Fatalf("release: %v", err)
	}

	// AFTER release: still nothing sent. Release changes who may send, not what has been sent.
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.posting_attempts
		WHERE internal_posting_id=$1`, postingID); n != 0 {
		t.Fatal("releasing recovery transmitted a posting by itself")
	}
	if st := scan1[string](t, p, `SELECT state FROM iam_v2.posting_outbox WHERE id=$1`, outboxID); st != "QUEUED" {
		t.Fatalf("the authorized posting is %s after release", st)
	}
}

// ---------------------------------------------------------------- the four marker cases

func TestIntegrationClosure_MarkerAheadEqualBehindAndMissing(t *testing.T) {
	p := pool(t)
	ctx := context.Background()

	call := func(s scope, ident string, gen int64, present bool) string {
		var out string
		if err := p.QueryRow(ctx,
			`SELECT iam_v2.p4_reconcile_financial_epoch_v2($1::uuid,$2::uuid,$3,$4,$5)`,
			s.tenant, s.site, ident, gen, present).Scan(&out); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		return out
	}
	identOf := func() string {
		return scan1[string](t, p, `SELECT system_identifier::text FROM pg_control_system()`)
	}

	t.Run("EQUAL is the ordinary restart and changes nothing", func(t *testing.T) {
		s := seedPaidChain(t, p)
		id := identOf()
		if got := call(s, id, 0, true); got != "INITIALIZED" {
			t.Fatalf("first call: %s", got)
		}
		for i := 0; i < 3; i++ {
			if got := call(s, id, 0, true); got != "UNCHANGED" {
				t.Fatalf("restart %d reported %s", i, got)
			}
		}
	})

	t.Run("AHEAD means the database was restored", func(t *testing.T) {
		s := seedPaidChain(t, p)
		id := identOf()
		call(s, id, 0, true)
		if got := call(s, id, 1, true); got != "RECOVERY_ENTERED" {
			t.Fatalf("a marker ahead of the database reported %s", got)
		}
	})

	t.Run("BEHIND means the management partition was rolled back", func(t *testing.T) {
		// This is the case 0023 missed, and it is not hypothetical: the documented runbook tars and
		// restores /etc/stayconnect, so an old /etc carries an old marker.
		s := seedPaidChain(t, p)
		id := identOf()
		call(s, id, 5, true)
		if _, err := p.Exec(ctx, `UPDATE iam_v2.financial_epochs SET restore_generation=5
			WHERE tenant_id=$1 AND site_id=$2`, s.tenant, s.site); err != nil {
			t.Fatal(err)
		}
		if got := call(s, id, 2, true); got != "RECOVERY_ENTERED" {
			t.Fatalf("a marker BEHIND the database reported %s; the two records disagree and that is a "+
				"reason to stop", got)
		}
		if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.financial_restore_events
			WHERE tenant_id=$1 AND restored_by='MARKER_BEHIND'`, s.tenant); n != 1 {
			t.Fatal("the disagreement was not recorded as a detected event")
		}
	})

	t.Run("MISSING on a site that has a generation fails closed", func(t *testing.T) {
		s := seedPaidChain(t, p)
		id := identOf()
		call(s, id, 3, true)
		if _, err := p.Exec(ctx, `UPDATE iam_v2.financial_epochs SET restore_generation=3
			WHERE tenant_id=$1 AND site_id=$2`, s.tenant, s.site); err != nil {
			t.Fatal(err)
		}
		if got := call(s, id, 0, false); got != "RECOVERY_ENTERED" {
			t.Fatalf("a vanished marker reported %s", got)
		}
	})
}

// ---------------------------------------------------------------- C27

func TestIntegrationClosure_C27NoCrossTenantMerchantReuse(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	a := seedPaidChain(t, p)
	b := seedPaidChain(t, p) // a different tenant entirely

	ref := scan1[string](t, p, `SELECT merchant_account_ref FROM iam_v2.payment_provider_accounts WHERE id=$1`,
		a.merchant)
	if ref == "" {
		t.Fatal("setup: site A has no external merchant reference")
	}
	// The SAME external merchant account, configured under another customer. This is the hazard: one
	// customer's guests paying into another customer's account.
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.payment_provider_accounts
		(tenant_id,site_id,provider,merchant_account_ref,status,is_default)
		VALUES ($1,$2,'test-double',$3,'DISABLED',false)`, b.tenant, b.site, ref); err == nil {
		t.Fatal("the same external merchant account was configured under two tenants")
	} else if !strings.Contains(err.Error(), "ppa_merchant_ref_globally_unique") {
		t.Fatalf("refused, but not by the C27 constraint: %v", err)
	}

	// A DIFFERENT reference under the same tenant is still fine, so the constraint is about reuse rather
	// than about forbidding a second account.
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.payment_provider_accounts
		(tenant_id,site_id,provider,merchant_account_ref,status,is_default)
		VALUES ($1,$2,'test-double',$3,'DISABLED',false)`,
		b.tenant, b.site, ref+"-second"); err != nil {
		t.Fatalf("a distinct merchant account was refused: %v", err)
	}
}

// ---------------------------------------------------------------- C35

func TestIntegrationClosure_C35ArchiveGatesTheCrossCustomerPurge(t *testing.T) {
	p := pool(t)
	ctx := context.Background()
	s := seedPaidChain(t, p)

	// With no archive, the gate refuses. This is what stands between a departing customer's financial
	// record and a DELETE.
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err == nil {
		t.Fatal("the purge gate allowed a tenant with no compliance archive")
	} else if !strings.Contains(err.Error(), "COMPLIANCE_ARCHIVE_MISSING") {
		t.Fatalf("refused for the wrong reason: %v", err)
	}

	// A digest that is not a digest is refused: the record exists to identify a real artefact.
	if _, err := p.Exec(ctx,
		`SELECT iam_v2.p4_record_compliance_archive($1::uuid,$2::uuid,'not-a-digest','/tmp/x','{}'::jsonb)`,
		s.tenant, s.site); err == nil {
		t.Fatal("an archive was recorded without a real digest")
	}

	var id string
	if err := p.QueryRow(ctx,
		`SELECT iam_v2.p4_record_compliance_archive($1::uuid,$2::uuid,$3,$4,$5::jsonb)::text`,
		s.tenant, s.site, strings.Repeat("a", 64), "/var/backups/stayconnect/compliance/t.json",
		`{"iam_v2.purchases":1}`).Scan(&id); err != nil {
		t.Fatalf("recording a compliance archive: %v", err)
	}
	if _, err := p.Exec(ctx, `SELECT iam_v2.p4_assert_compliance_archived($1::uuid)`, s.tenant); err != nil {
		t.Fatalf("the gate still refuses after an archive was recorded: %v", err)
	}

	// The EXTERNAL blocker is recorded honestly rather than defaulted away: no receipt authority exists,
	// so receipt_verified is false and the row says why.
	verified := scan1[bool](t, p, `SELECT receipt_verified FROM iam_v2.compliance_archives WHERE id=$1`, id)
	if verified {
		t.Fatal("receipt_verified is true, but no external archival authority exists to have signed it")
	}
	reason := scan1[string](t, p, `SELECT coalesce(receipt_blocked_reason,'') FROM iam_v2.compliance_archives
		WHERE id=$1`, id)
	if !strings.Contains(reason, "external archival receipt authority") {
		t.Fatalf("the missing external capability is not recorded: %q", reason)
	}
}
