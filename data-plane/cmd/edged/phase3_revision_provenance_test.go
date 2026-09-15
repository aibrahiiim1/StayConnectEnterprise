//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// A SILENT FAILURE NEEDS A TEST THAT LOOKS FOR SILENCE.
//
// The History view tells an operator when each saved PMS configuration was put in use, by whom, and for what
// recorded reason. None of that is in the revisions table -- it is in the audit log -- so it is read by a
// second query whose error is deliberately discarded, because losing the audit log must never cost the
// operator the configuration itself.
//
// That tolerance hid a real defect in production. The join compared operators.id (uuid) to
// audit_log.actor_id (text); PostgreSQL has no such operator and refused the statement; the discarded error
// meant the endpoint still answered 200, with every version reading "How this version came to be saved was
// not recorded". The feature was entirely dead and nothing failed, because the fallback for "no audit log"
// is indistinguishable from the fallback for "the query is broken".
//
// No existing test could have caught it. The disposable fixture builds public.audit_log with a created_at
// column, while the appliance's own 0001 migration names it ts -- so on the fixture this query fails for a
// second, unrelated reason, and asserting empty provenance there would have asserted the bug.
//
// This test therefore builds the PRODUCTION column types in a schema of its own and runs the real statement
// text against them. It is about the shape of the data, not the plumbing: what it proves is that the query
// composes with the types it will actually meet.
func TestIntegration_API_PmsRevisionProvenanceSQLRunsAgainstProductionColumnTypes(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()

	// A schema of this test's own: public.audit_log already exists in the shared fixture database with the
	// wrong column name, and must not be disturbed for the tests that rely on it.
	const schema = "provenance_contract_test"
	if _, err := p.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE; CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	// THE TYPES ARE THE POINT. Copied from migrations/baseline/0000_production_baseline.sql: audit_log.ts
	// (not created_at), audit_log.actor_id text, operators.id uuid. Mismatching either of the last two is
	// exactly the defect this guards, so they are spelled out rather than inherited from a helper.
	if _, err := p.Exec(ctx, `
		CREATE TABLE `+schema+`.audit_log (
			ts        timestamptz NOT NULL DEFAULT now(),
			tenant_id uuid,
			actor_type text NOT NULL,
			actor_id  text,
			action    text NOT NULL,
			target_type text, target_id text,
			payload   jsonb NOT NULL DEFAULT '{}'::jsonb);
		CREATE TABLE `+schema+`.operators (
			id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
			email text NOT NULL,
			display_name text)`); err != nil {
		t.Fatalf("provision production-shaped tables: %v", err)
	}

	var operator string
	if err := p.QueryRow(ctx, `INSERT INTO `+schema+`.operators(email, display_name)
		VALUES ('site-admin@test.local','Site Admin') RETURNING id::text`).Scan(&operator); err != nil {
		t.Fatalf("seed operator: %v", err)
	}

	const (
		attributed = "11111111-1111-1111-1111-111111111111" // saved by an operator still on this appliance
		departed   = "22222222-2222-2222-2222-222222222222" // saved by one who is not
	)
	authored := time.Now().Add(-2 * time.Hour)
	if _, err := p.Exec(ctx, `
		INSERT INTO `+schema+`.audit_log(ts, actor_type, actor_id, action, payload) VALUES
		  ($2, 'operator', $1, 'pms_interface_revision.authored',  jsonb_build_object('revision_id',$4::text)),
		  ($3, 'operator', $1, 'pms_interface.revision_published', jsonb_build_object('revision_id',$4::text,'reason_code','CONFIG_CORRECTION')),
		  ($2, 'operator', $6, 'pms_interface_revision.authored',  jsonb_build_object('revision_id',$5::text)),
		  ($3, 'operator', $6, 'pms_interface.revision_published', jsonb_build_object('revision_id',$5::text,'reason_code','INITIAL_COMMISSIONING'))`,
		operator, authored, authored.Add(time.Minute), attributed, departed,
		// An actor who is NOT a row in operators, and -- the detail that would break a ::uuid cast -- one
		// whose recorded identity is not a uuid at all. actor_id is text for this reason: the appliance's
		// own automated actions are recorded under names, not ids.
		"appliance-bootstrap"); err != nil {
		t.Fatalf("seed audit entries: %v", err)
	}

	// The REAL statement, retargeted at this schema. Any future edit to the production text is executed here
	// verbatim; there is no second copy to drift.
	sql := strings.ReplaceAll(pmsRevisionProvenanceSQL, "public.", schema+".")
	if sql == pmsRevisionProvenanceSQL {
		t.Fatal("the provenance query no longer qualifies its tables with public.; this test can no longer " +
			"retarget it and would silently stop proving anything")
	}

	rows, err := p.Query(ctx, sql)
	if err != nil {
		// THE ASSERTION THAT MATTERS. In production this error is discarded on purpose, so it can only ever
		// surface here.
		t.Fatalf("the provenance query does not run against production column types: %v", err)
	}
	defer rows.Close()

	type prov struct {
		authoredAt, publishedAt *time.Time
		actorID, label, reason  string
	}
	got := map[string]prov{}
	for rows.Next() {
		var id string
		var e prov
		if err := rows.Scan(&id, &e.authoredAt, &e.publishedAt, &e.actorID, &e.label, &e.reason); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = e
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("expected provenance for both versions, got %d: %+v", len(got), got)
	}

	// A version saved by a known operator is attributed to them BY NAME. Asserting only that the query runs
	// would still pass if the operator join were dropped entirely.
	a := got[attributed]
	if a.label != "Site Admin" {
		t.Errorf("version saved by a known operator should name them, got %q", a.label)
	}
	if a.reason != "CONFIG_CORRECTION" {
		t.Errorf("recorded reason = %q, want CONFIG_CORRECTION", a.reason)
	}
	if a.authoredAt == nil || a.publishedAt == nil {
		t.Errorf("both timestamps should be recorded, got authored=%v published=%v", a.authoredAt, a.publishedAt)
	}
	if !a.publishedAt.After(*a.authoredAt) {
		t.Errorf("published %v should follow authored %v", a.publishedAt, a.authoredAt)
	}

	// And a version whose actor is not a uuid and not an operator still APPEARS, with its reason intact and
	// no invented name. Dropping it would hide configuration that was genuinely in force; naming it would be
	// a fabrication.
	d := got[departed]
	if d.reason != "INITIAL_COMMISSIONING" {
		t.Errorf("a version by an unknown actor keeps its reason, got %q", d.reason)
	}
	if d.label != "" {
		t.Errorf("an actor absent from operators must not be given a name, got %q", d.label)
	}
	if d.actorID != "appliance-bootstrap" {
		t.Errorf("the recorded actor identity should survive verbatim, got %q", d.actorID)
	}
}
