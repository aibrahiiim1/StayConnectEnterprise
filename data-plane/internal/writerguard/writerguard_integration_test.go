//go:build integration

package writerguard

// Each case is a way a service could start believing it is protected when it is not; all look identical from inside the process.

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

// probeConn connects as a non-owner role, the runtime's real shape; the schema owner would pass while proving nothing about who is constrained.
func probeConn(t *testing.T, owner *pgxpool.Pool) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	// The EXECUTE grant is part of the runtime's shape: a service writing a capability-scoped family must be able
	// to open its scope. This test-only role does not touch the dark-privilege invariant on the real service roles.
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

// The real requirement set, checked by a non-writer role, must pass.
func TestIntegration_BoundaryHoldsForANonOwnerRuntime(t *testing.T) {
	owner := ownerPool(t)
	probe := probeConn(t, owner)
	if err := Verify(context.Background(), probe, Phase3Requirements()); err != nil {
		t.Fatalf("the installed Phase-3 boundary was rejected for an ordinary runtime role: %v", err)
	}
}

// A process connected as the controlled operations' owner satisfies every guard trivially (raw writes), so it must be refused.
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

// A missing operation and an unguarded table silently drop the boundary while writes keep succeeding.
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

	// iam_v2.devices has no controlled-writer guard, standing in for a table that needed one.
	err = Verify(ctx, probe, []Requirement{{
		Family:   "accounting",
		Function: "iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)",
		Tables:   []string{"devices"},
	}})
	if err == nil || !strings.Contains(err.Error(), "no ENABLED controlled-writer guard") {
		t.Fatalf("an unguarded table was accepted: %v", err)
	}
}

// A SECURITY DEFINER function needs a pinned search_path and no PUBLIC EXECUTE; both defects are invisible at runtime, so catch them at startup.
func TestIntegration_UnsafeFunctionShapesAreRefused(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
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

// A runtime that cannot open a capability-scoped family's operation is refused at startup, not at the first write (a PMS event or a guest purchase).
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

// The two tiers must stay distinguishable: if every family drifted to the weaker tier, everything else would still pass.
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
	// Families whose corruption is silent (entitlement, grace config, accounting, class generation, offer set) must stay operation-owned; a floor, not an equality.
	if owned < 6 {
		t.Fatalf("only %d operation-owned families remain; the strong tier is being eroded", owned)
	}
	if scoped == 0 {
		t.Fatal("no capability-scoped families are recorded, so verifyCapability is never exercised")
	}
}
