//go:build integration

package main

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The timeline's SQL runs as the production role: every table, column and grant it needs must hold for svc_edged.
func TestIntegration_ClientActivityRunsAsTheProductionRole(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const tenant, site = "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2"
	now := time.Now()
	for _, hotel := range []bool{true, false} {
		sc := clientActivityScopeFor([]string{"site_admin"}, hotel)
		f := clientActivityFilter{From: now.Add(-30 * 24 * time.Hour), To: now, Q: "a", Limit: 50}
		base, args := clientActivitySQL(f, sc, tenant, site, true)
		args = append(args, f.Limit, f.Offset)
		lim, off := "$"+strconv.Itoa(len(args)-1), "$"+strconv.Itoa(len(args))
		rows, err := pool.Query(ctx, base+` SELECT `+clientActivityCols+`, count(*) OVER () FROM u ORDER BY at DESC, rid LIMIT `+lim+` OFFSET `+off, args...)
		if err != nil {
			t.Fatalf("hotel=%v: the timeline does not run as svc_edged: %v", hotel, err)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("hotel=%v: %v", hotel, err)
		}
		sumBase, sumArgs := clientActivitySQL(f, sc, tenant, site, false)
		if _, err := pool.Exec(ctx, sumBase+` SELECT cat, count(*) FROM u GROUP BY cat`, sumArgs...); err != nil {
			t.Fatalf("hotel=%v: the summary does not run as svc_edged: %v", hotel, err)
		}
	}
}
