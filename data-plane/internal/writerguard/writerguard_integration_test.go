//go:build integration

package writerguard

// The startup boundary check, against a real PostgreSQL 16 carrying the real Phase-3 schema.
//
// Every case here is a way a service could come up believing it is protected when it is not. The check exists
// because all of them look identical from inside the process: writes succeed either way.

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ownerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping writer-boundary integration")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// probeConn connects as a NON-OWNER role — the shape a Gate-P runtime actually has. Running the check as the
// schema owner would pass the trigger assertions while proving nothing about whether they constrain anyone.
func probeConn(t *testing.T, owner *pgxpool.Pool) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	// The EXECUTE grant is part of what "the shape a Gate-P runtime has" MEANS. A service that writes a
	// capability-scoped family must be able to open that family's scope, or every one of its authoritative
	// writes is refused — so a probe without it would be testing a role that could not do the job at all.
	//
	// This is a test-only role, so it does not touch the dark-privilege invariant the migration lifecycle gate
	// asserts: the named runtime service roles (scd, edged, portald, acctd, pmsd) still hold ZERO iam_v2
	// function EXECUTE while Phase 3 is dark. They receive this grant at Gate-P, alongside the rest of their
	// least-privilege set, and Verify runs only where the Phase-3 surface is actually mounted.
	if _, err := owner.Exec(ctx, `
		DO $$ BEGIN CREATE ROLE p3_guard_probe LOGIN PASSWORD 'x' NOSUPERUSER NOCREATEDB NOCREATEROLE;
		EXCEPTION WHEN duplicate_object THEN NULL; END $$;
		GRANT USAGE ON SCHEMA iam_v2 TO p3_guard_probe;
		GRANT EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) TO p3_guard_probe;`); err != nil {
		t.Fatalf("create the probe role: %v", err)
	}
	u, err := url.Parse(os.Getenv("PHASE3_TEST_DSN"))
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.UserPassword("p3_guard_probe", "x")
	c, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect as the probe role: %v", err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// The real requirement set, checked by a role that is NOT the writer, must pass. If this fails, either the
// boundary is not installed or the check is asserting something the schema does not actually promise — and
// both are worth knowing before a service refuses to start in production.
func TestIntegration_BoundaryHoldsForANonOwnerRuntime(t *testing.T) {
	owner := ownerPool(t)
	probe := probeConn(t, owner)
	if err := Verify(context.Background(), probe, Phase3Requirements()); err != nil {
		t.Fatalf("the installed Phase-3 boundary was rejected for an ordinary runtime role: %v", err)
	}
}

// THE MIXED-MODE REFUSAL. A process connected as the controlled operations' owner satisfies every guard
// trivially: it can write any Phase-3 table raw, and nothing in the schema would ever object. That is the
// legacy/raw writer mode, and it must stop the service rather than run "protected" in name only.
func TestIntegration_OwnerConnectionIsRefused(t *testing.T) {
	owner := ownerPool(t)
	err := Verify(context.Background(), owner, Phase3Requirements())
	if err == nil {
		t.Fatal("a process connected as the controlled-writer owner was allowed to start")
	}
	if !strings.Contains(err.Error(), "bypass every controlled operation") {
		t.Fatalf("the refusal did not name the mixed-writer problem: %v", err)
	}
}

// A missing operation and an unguarded table are the two ways an out-of-date or hand-repaired schema silently
// drops the boundary while every Phase-3 write keeps succeeding.
func TestIntegration_MissingPiecesAreRefused(t *testing.T) {
	owner := ownerPool(t)
	probe := probeConn(t, owner)
	ctx := context.Background()

	err := Verify(ctx, probe, []Requirement{{
		Family:   "entitlement",
		Function: "iam_v2.apply_entitlement_transition(uuid,text)", // wrong arity: not the approved operation
		Tables:   []string{"entitlements"},
	}})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("a missing controlled operation was accepted: %v", err)
	}

	// iam_v2.devices is a real Phase-3 table with no controlled-writer guard. Naming it here stands in for a
	// schema where a guard was never applied to a table that needs one.
	err = Verify(ctx, probe, []Requirement{{
		Family:   "accounting",
		Function: "iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)",
		Tables:   []string{"devices"},
	}})
	if err == nil || !strings.Contains(err.Error(), "no ENABLED controlled-writer guard") {
		t.Fatalf("an unguarded table was accepted: %v", err)
	}
}

// A SECURITY DEFINER function is only safe with a pinned search_path and without PUBLIC EXECUTE. Both defects
// are invisible at runtime — the function works perfectly — so they have to be caught at startup.
func TestIntegration_UnsafeFunctionShapesAreRefused(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
	// These live in their own schema so the shared Phase-3 objects are never touched.
	if _, err := owner.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS wg_probe;
		CREATE OR REPLACE FUNCTION wg_probe.no_path() RETURNS void
		  LANGUAGE sql SECURITY DEFINER AS $$ SELECT $$;
		REVOKE EXECUTE ON FUNCTION wg_probe.no_path() FROM PUBLIC;
		CREATE OR REPLACE FUNCTION wg_probe.public_exec() RETURNS void
		  LANGUAGE sql SECURITY DEFINER SET search_path = iam_v2, pg_temp AS $$ SELECT $$;
		CREATE OR REPLACE FUNCTION wg_probe.not_definer() RETURNS void
		  LANGUAGE sql SET search_path = iam_v2, pg_temp AS $$ SELECT $$;
		REVOKE EXECUTE ON FUNCTION wg_probe.not_definer() FROM PUBLIC;
		DO $$ BEGIN CREATE ROLE p3_guard_probe LOGIN PASSWORD 'x' NOSUPERUSER NOCREATEDB NOCREATEROLE;
		EXCEPTION WHEN duplicate_object THEN NULL; END $$;
		-- The check reads pg_proc THROUGH to_regprocedure, which needs USAGE on the schema to resolve a name
		-- at all. Without it the probe gets "permission denied" instead of the shape verdict under test.
		GRANT USAGE ON SCHEMA wg_probe TO p3_guard_probe;`); err != nil {
		t.Fatalf("seed the probe functions: %v", err)
	}
	t.Cleanup(func() { _, _ = owner.Exec(context.Background(), `DROP SCHEMA IF EXISTS wg_probe CASCADE`) })
	probe := probeConn(t, owner)

	cases := []struct {
		fn   string
		want string
	}{
		{"wg_probe.not_definer()", "is not SECURITY DEFINER"},
		{"wg_probe.no_path()", "does not pin its search_path"},
		{"wg_probe.public_exec()", "PUBLIC holds EXECUTE"},
	}
	for _, c := range cases {
		err := Verify(ctx, probe, []Requirement{{Family: "probe", Function: c.fn}})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("%s: expected %q, got %v", c.fn, c.want, err)
		}
	}
}

// A runtime that cannot OPEN a capability-scoped family's operation is refused at startup rather than at the
// first write. The difference matters more than it looks: the first Stay write is the first PMS event of the
// day, and the first commerce write is a guest standing in the lobby. Both are terrible places to discover a
// missing grant, and both are places where the pressure is to disable the check rather than fix the grant.
func TestIntegration_ARuntimeThatCannotOpenAScopeIsRefusedAtStartup(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		DO $$ BEGIN CREATE ROLE p3_noexec_probe LOGIN PASSWORD 'x' NOSUPERUSER NOCREATEDB NOCREATEROLE;
		EXCEPTION WHEN duplicate_object THEN NULL; END $$;
		GRANT USAGE ON SCHEMA iam_v2 TO p3_noexec_probe;
		REVOKE EXECUTE ON FUNCTION iam_v2.begin_controlled_operation(text) FROM p3_noexec_probe;`); err != nil {
		t.Fatalf("create the probe role: %v", err)
	}
	u, err := url.Parse(os.Getenv("PHASE3_TEST_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("p3_noexec_probe", "x")
	c, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatalf("connect as the probe role: %v", err)
	}
	defer func() { _ = c.Close(context.Background()) }()

	err = Verify(ctx, c, Phase3Requirements())
	if err == nil {
		t.Fatal("a runtime with no EXECUTE on the operation opener was accepted")
	}
	if !strings.Contains(err.Error(), "cannot open") {
		t.Fatalf("the refusal does not name the missing capability: %v", err)
	}
}

// The two tiers must stay distinguishable in the recorded contract. If every family drifted to the weaker
// tier the package would still compile, every test above would still pass, and the boundary would quietly be
// a different, weaker thing than the one that was reviewed.
func TestPhase3RequirementsRecordBothTiers(t *testing.T) {
	var owned, scoped int
	seen := map[string]bool{}
	for _, r := range Phase3Requirements() {
		if seen[r.Family] {
			t.Fatalf("family %q is listed twice", r.Family)
		}
		seen[r.Family] = true
		if len(r.Tables) == 0 {
			t.Fatalf("family %q guards no tables", r.Family)
		}
		switch r.Tier {
		case TierOperationOwned:
			owned++
			if r.Capability != "" {
				t.Fatalf("operation-owned family %q names a capability", r.Family)
			}
		case TierCapabilityScoped:
			scoped++
			if r.Capability == "" {
				t.Fatalf("capability-scoped family %q names no capability", r.Family)
			}
		}
	}
	// The five families whose corruption is silent — entitlement, grace config, accounting, class generation
	// and the offer set — are the ones that must stay operation-owned. This is a floor, not an equality: new
	// families may be added at either tier, but these five may not be downgraded without this failing.
	if owned < 6 {
		t.Fatalf("only %d operation-owned families remain; the strong tier is being eroded", owned)
	}
	if scoped == 0 {
		t.Fatal("no capability-scoped families are recorded, so verifyCapability is never exercised")
	}
}
