// Package testdb gives a database integration test its own throw-away copy of a fully migrated Central
// schema.
//
// CTRLAPI_TEST_DB_URL names a database that central-migrate.sh has brought fully up to date (the template).
// Each test gets a fresh database created FROM that template, so tests in different packages -- which `go
// test ./...` runs in parallel -- never see each other's rows, and the template itself is never written to.
// Without the variable the test is skipped: these tests need a real Postgres/TimescaleDB, never a stand-in.
//
//	docker run -d --name sc-stream7-pg -e POSTGRES_USER=stayconnect -e POSTGRES_PASSWORD=stream7 \
//	    -e POSTGRES_DB=stayconnect -p 127.0.0.1:55407:5432 timescale/timescaledb:2.16.1-pg16
//	deploy/scripts/central-migrate.sh --pg-exec "docker exec -i sc-stream7-pg" --db stayconnect up
//	CTRLAPI_TEST_DB_URL=postgres://stayconnect:stream7@127.0.0.1:55407/stayconnect?sslmode=disable go test ./...
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvVar is the template database URL.
const EnvVar = "CTRLAPI_TEST_DB_URL"

// CREATE DATABASE ... TEMPLATE needs the template to have no other session; serialise the copies.
var createMu sync.Mutex

// New returns a pool on a fresh copy of the template database, dropped when the test ends.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv(EnvVar)
	if base == "" {
		t.Skip(EnvVar + " is not set: this test needs a migrated Central database")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("%s: %v", EnvVar, err)
	}
	template := strings.TrimPrefix(u.Path, "/")
	var b [6]byte
	_, _ = rand.Read(b[:])
	name := "ct_" + hex.EncodeToString(b[:])

	admin := *u
	admin.Path = "/postgres"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	createMu.Lock()
	conn, err := pgx.Connect(ctx, admin.String())
	if err != nil {
		createMu.Unlock()
		t.Fatalf("connect admin db: %v", err)
	}
	var lastErr error
	for i := 0; i < 20; i++ {
		_, lastErr = conn.Exec(ctx, `CREATE DATABASE `+name+` TEMPLATE `+pgx.Identifier{template}.Sanitize())
		if lastErr == nil || !strings.Contains(lastErr.Error(), "being accessed by other users") {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	createMu.Unlock()
	if lastErr != nil {
		_ = conn.Close(ctx)
		t.Fatalf("create test database: %v", lastErr)
	}

	dbURL := *u
	dbURL.Path = "/" + name
	pool, err := pgxpool.New(ctx, dbURL.String())
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		_, _ = conn.Exec(cctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		_ = conn.Close(cctx)
	})
	return pool
}
