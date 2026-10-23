//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The History view reads provenance from the audit log in a second query whose error is deliberately discarded
// (losing the audit log must not cost the configuration), so a broken query looks like "not recorded". This test
// runs the real statement against production column types (audit_log.ts, actor_id text, operators.id uuid) in
// a schema of its own, because the shared fixture's audit_log has a different column name.
func TestIntegration_API_PmsRevisionProvenanceSQLRunsAgainstProductionColumnTypes(t *testing.T) {
	p := testPool(t)
	ctx := context.Background()

	const schema = "provenance_contract_test"
	if _, err := p.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE; CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = p.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	// Types copied from the production baseline; a uuid/text mismatch is the defect this guards.
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
		attributed = "11111111-1111-1111-1111-111111111111"
		departed   = "22222222-2222-2222-2222-222222222222"
	)
	authored := time.Now().Add(-2 * time.Hour)
	if _, err := p.Exec(ctx, `
		INSERT INTO `+schema+`.audit_log(ts, actor_type, actor_id, action, payload) VALUES
		  ($2, 'operator', $1, 'pms_interface_revision.authored',  jsonb_build_object('revision_id',$4::text)),
		  ($3, 'operator', $1, 'pms_interface.revision_published', jsonb_build_object('revision_id',$4::text,'reason_code','CONFIG_CORRECTION')),
		  ($2, 'operator', $6, 'pms_interface_revision.authored',  jsonb_build_object('revision_id',$5::text)),
		  ($3, 'operator', $6, 'pms_interface.revision_published', jsonb_build_object('revision_id',$5::text,'reason_code','INITIAL_COMMISSIONING'))`,
		operator, authored, authored.Add(time.Minute), attributed, departed,
		// Not a row in operators and not a uuid (would break a ::uuid cast): automated actions are recorded under names.
		"appliance-bootstrap"); err != nil {
		t.Fatalf("seed audit entries: %v", err)
	}

	// The real statement, retargeted at this schema, so there is no second copy to drift.
	sql := strings.ReplaceAll(pmsRevisionProvenanceSQL, "public.", schema+".")
	if sql == pmsRevisionProvenanceSQL {
		t.Fatal("the provenance query no longer qualifies its tables with public.; this test can no longer " +
			"retarget it and would silently stop proving anything")
	}

	rows, err := p.Query(ctx, sql)
	if err != nil {
		// Production discards this error, so it can only surface here.
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

	// Attribution by name proves the operator join is still present.
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

	// A version by an unknown actor still appears with its reason and no invented name.
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
