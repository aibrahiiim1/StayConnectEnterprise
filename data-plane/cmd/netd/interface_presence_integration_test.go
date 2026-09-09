//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PRESENCE IS DERIVED FROM THE LATEST SWEEP, PROVEN AGAINST A REAL POSTGRES.
//
// The SQL is the whole fix. `SELECT name FROM network_interfaces` answered "has this appliance ever seen an
// interface by this name", which let a bridge destroyed weeks earlier satisfy guest-network validation. The
// defect lives in the query, so the query is what these cases run — a fake store would simply agree with
// whatever it was handed.
//
// They run against the disposable PostgreSQL the Phase-3 gate already provisions and executes
// `-tags integration -run Integration ./cmd/netd/` against, so they run in CI rather than skipping there.

func presenceTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping the interface-presence integration")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// freshInventory gives each case an empty inventory.
//
// A REAL TABLE, NOT A TEMP ONE: the store holds a pgxpool, so consecutive statements can land on different
// connections, and a TEMP table is visible only to the session that made it. A temp table here would have
// produced a test that passes or fails on which connection the pool happened to hand out.
func freshInventory(t *testing.T, p *pgxpool.Pool) *store {
	t.Helper()
	ctx := context.Background()
	if _, err := p.Exec(ctx, `
        CREATE TABLE IF NOT EXISTS public.network_interfaces (
          id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
          name text NOT NULL UNIQUE, mac macaddr,
          role text NOT NULL DEFAULT 'unused', mode text NOT NULL DEFAULT 'auto',
          parent text, vlan_capable boolean NOT NULL DEFAULT true,
          link_state text, speed_mbps int, mtu int, driver text,
          ip_addresses jsonb NOT NULL DEFAULT '[]'::jsonb,
          is_protected boolean NOT NULL DEFAULT false,
          last_seen_at timestamptz,
          created_at timestamptz NOT NULL DEFAULT now(),
          updated_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create inventory: %v", err)
	}
	if _, err := p.Exec(ctx, `TRUNCATE public.network_interfaces`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return &store{db: p}
}

// THE DEFECT ITSELF: a row no sweep has touched for weeks is not present — and is not deleted either.
func TestIntegrationPresenceExcludesStaleRowWithoutDeletingIt(t *testing.T) {
	p := presenceTestPool(t)
	st := freshInventory(t, p)
	ctx := context.Background()

	// A destroyed bridge, recorded long ago, carrying a classification an operator chose.
	if _, err := p.Exec(ctx, `
        INSERT INTO public.network_interfaces (name, role, is_protected, last_seen_at)
        VALUES ('vguest-h','guest_access',false, now() - interval '18 days')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SyncInterfaces(ctx, []Interface{
		{Name: "ens160", LinkState: "up", MTU: 1500},
		{Name: "ens192", LinkState: "down", MTU: 1500}, // present, cable out — still an interface
	}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync: %v", err)
	}

	present, err := st.PresentIfaceSet(ctx)
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if present["vguest-h"] {
		t.Fatal("a bridge destroyed 18 days ago must NOT be present — this is the defect being fixed")
	}
	if !present["ens160"] || !present["ens192"] {
		t.Fatalf("every interface the latest sweep saw must be present, got %v", present)
	}

	// NOTHING WAS DELETED. The row survives, and so does the operator's own column.
	known, err := st.KnownIfaceSet(ctx)
	if err != nil {
		t.Fatalf("known: %v", err)
	}
	if !known["vguest-h"] {
		t.Fatal("the row must be RETAINED: deleting it would destroy operator configuration and history")
	}
	var role string
	if err := p.QueryRow(ctx, `SELECT role FROM public.network_interfaces WHERE name='vguest-h'`).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "guest_access" {
		t.Fatalf("the operator-assigned role must survive absence, got %q", role)
	}
}

// ONE SWEEP IS ONE INSTANT, so "seen by the latest sweep" is not a question about clock skew between rows.
func TestIntegrationSweepStampsOneInstant(t *testing.T) {
	p := presenceTestPool(t)
	st := freshInventory(t, p)
	ctx := context.Background()

	if err := st.SyncInterfaces(ctx, []Interface{
		{Name: "ens160"}, {Name: "ens192"}, {Name: "br-g-1"}, {Name: "ifb0"},
	}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	var distinct int
	if err := p.QueryRow(ctx, `SELECT count(DISTINCT last_seen_at) FROM public.network_interfaces`).Scan(&distinct); err != nil {
		t.Fatalf("count: %v", err)
	}
	if distinct != 1 {
		t.Fatalf("one sweep must stamp one instant; got %d distinct timestamps", distinct)
	}
}

// DISAPPEAR AND COME BACK. Absence is not sticky state: it is simply the absence of a fresh observation, so
// the next sweep that sees the interface makes it present again — with everything the operator set intact.
//
// The disappearance is aged deterministically rather than by sleeping: a test that waits for a tolerance
// window to elapse is a test that fails on a slow machine.
func TestIntegrationInterfaceReappearsWithRoleIntact(t *testing.T) {
	p := presenceTestPool(t)
	st := freshInventory(t, p)
	ctx := context.Background()

	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}, {Name: "ens224"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE public.network_interfaces SET role='guest_access' WHERE name='ens224'`); err != nil {
		t.Fatalf("classify: %v", err)
	}

	// GONE: age ens224 well beyond the tolerance, then let a sweep refresh only the interface that remains.
	if _, err := p.Exec(ctx, `UPDATE public.network_interfaces SET last_seen_at = now() - interval '3 days' WHERE name='ens224'`); err != nil {
		t.Fatalf("age: %v", err)
	}
	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync 2: %v", err)
	}
	present, err := st.PresentIfaceSet(ctx)
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if present["ens224"] {
		t.Fatal("an interface the latest sweep did not see must not be present")
	}

	// BACK AGAIN.
	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}, {Name: "ens224"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync 3: %v", err)
	}
	present, _ = st.PresentIfaceSet(ctx)
	if !present["ens224"] {
		t.Fatal("an interface that came back must be present again, immediately")
	}
	var role string
	if err := p.QueryRow(ctx, `SELECT role FROM public.network_interfaces WHERE name='ens224'`).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "guest_access" {
		t.Fatalf("the operator's classification must survive a disappearance, got %q — this is exactly why rows are not deleted", role)
	}
}

// AN EMPTY INVENTORY HAS NOTHING PRESENT. max(last_seen_at) over no rows is NULL; the query must yield
// nothing rather than fail open and call everything present.
func TestIntegrationEmptyInventoryHasNothingPresent(t *testing.T) {
	p := presenceTestPool(t)
	st := freshInventory(t, p)
	present, err := st.PresentIfaceSet(context.Background())
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if len(present) != 0 {
		t.Fatalf("an empty inventory has nothing present, got %v", present)
	}
}

// A ROW WITH NO OBSERVATION AT ALL (last_seen_at NULL) is not present. Rows can be created by other paths —
// edged assigns roles — and a NULL must never be read as "seen".
func TestIntegrationNullLastSeenIsNotPresent(t *testing.T) {
	p := presenceTestPool(t)
	st := freshInventory(t, p)
	ctx := context.Background()

	if _, err := p.Exec(ctx, `INSERT INTO public.network_interfaces (name, role) VALUES ('ens999','guest_access')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	present, err := st.PresentIfaceSet(ctx)
	if err != nil {
		t.Fatalf("present: %v", err)
	}
	if present["ens999"] {
		t.Fatal("a row that has never been observed must not be present")
	}
	known, _ := st.KnownIfaceSet(ctx)
	if !known["ens999"] {
		t.Fatal("it is still KNOWN, so validation can tell the operator it exists but is not present")
	}
}
