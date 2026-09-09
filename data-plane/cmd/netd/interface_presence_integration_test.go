//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WHAT THE INVENTORY IS FOR, NOW THAT IT IS NOT THE PRESENCE ORACLE.
//
// Presence is observed live at the moment of the decision (see applier.presenceSets and
// interface_presence_test.go). This table answers a different question — what has this appliance EVER
// recorded — and holds the operator's own columns. These cases pin the properties that question depends on,
// against a real PostgreSQL, because they are properties of the SQL.
//
// They run against the disposable PostgreSQL the Phase-3 gate provisions and executes
// `-tags integration -run Integration ./cmd/netd/` against.

func presenceTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping the interface-inventory integration")
	}
	p, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// A REAL TABLE, NOT A TEMP ONE: the store holds a pool, so consecutive statements can land on different
// connections and a TEMP table is visible only to the session that made it.
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

// NOTHING IS EVER DELETED, and the operator's own column survives an interface going away. This is why the
// fix does not prune: role, is_protected and mode are chosen by a person, and an interface that came back
// after a prune would return silently declassified.
func TestIntegrationInventoryRetainsVanishedInterfaceAndItsRole(t *testing.T) {
	p := presenceTestPool(t)
	st := freshInventory(t, p)
	ctx := context.Background()

	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}, {Name: "ens224"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync 1: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE public.network_interfaces SET role='guest_access' WHERE name='ens224'`); err != nil {
		t.Fatalf("classify: %v", err)
	}

	// ens224 is gone: a later sweep does not see it.
	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync 2: %v", err)
	}

	known, err := st.KnownIfaceSet(ctx)
	if err != nil {
		t.Fatalf("known: %v", err)
	}
	if !known["ens224"] {
		t.Fatal("the row must be RETAINED: the inventory records what this appliance has ever had")
	}
	var role string
	if err := p.QueryRow(ctx, `SELECT role FROM public.network_interfaces WHERE name='ens224'`).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "guest_access" {
		t.Fatalf("the operator-assigned role must survive absence, got %q", role)
	}

	// And it comes back with that classification intact.
	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}, {Name: "ens224"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync 3: %v", err)
	}
	if err := p.QueryRow(ctx, `SELECT role FROM public.network_interfaces WHERE name='ens224'`).Scan(&role); err != nil {
		t.Fatalf("read role: %v", err)
	}
	if role != "guest_access" {
		t.Fatalf("reappearance must not reset the operator's classification, got %q", role)
	}
}

// ONE SWEEP IS ONE INSTANT, so anything that legitimately reads this table reads a coherent snapshot rather
// than a spread of per-row timestamps.
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

// THE KNOWN SET IS EVERY ROW, whatever its observation state — including one never observed at all, which
// other paths (edged assigning a role) can create.
func TestIntegrationKnownSetIncludesNeverObservedRows(t *testing.T) {
	p := presenceTestPool(t)
	st := freshInventory(t, p)
	ctx := context.Background()

	if _, err := p.Exec(ctx, `INSERT INTO public.network_interfaces (name, role) VALUES ('ens999','guest_access')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := st.SyncInterfaces(ctx, []Interface{{Name: "ens160"}}, "ens160", "ens160"); err != nil {
		t.Fatalf("sync: %v", err)
	}
	known, err := st.KnownIfaceSet(ctx)
	if err != nil {
		t.Fatalf("known: %v", err)
	}
	if !known["ens999"] || !known["ens160"] {
		t.Fatalf("the known set is every recorded row, got %v", known)
	}
}
