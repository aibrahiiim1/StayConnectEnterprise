// Package testdb gives each test a throw-away copy of a migrated Central database, created from the template
// named by CTRLAPI_TEST_DB_URL; without it the test is skipped.
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

const EnvVar = "CTRLAPI_TEST_DB_URL"

// With these two set, the test pool connects to its copy as the Central RUNTIME role (ctrlapi_rt, migration 0063)
// instead of as the owner named in CTRLAPI_TEST_DB_URL, which still creates and drops the copies. Running the suites
// this way proves ctrlapi needs nothing more than that role holds (scripts/ci/central-restricted-role-tests.sh).
const (
	RoleEnvVar     = "CTRLAPI_TEST_DB_APP_ROLE"
	PasswordEnvVar = "CTRLAPI_TEST_DB_APP_PASSWORD"
)

// OwnerPool opens a second pool on the same copy as the owner (CTRLAPI_TEST_DB_URL's user), for fixtures and
// assertions that must read or change what the runtime role cannot. Without the role variables it is simply
// another pool on the same connection.
func OwnerPool(t *testing.T, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	u, err := url.Parse(os.Getenv(EnvVar))
	if err != nil {
		t.Fatalf("%s: %v", EnvVar, err)
	}
	u.Path = "/" + pool.Config().ConnConfig.Database
	p, err := pgxpool.New(context.Background(), u.String())
	if err != nil {
		t.Fatalf("open owner pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// CREATE DATABASE ... TEMPLATE needs the template idle, so copies are serialised.
var createMu sync.Mutex

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
	if role := os.Getenv(RoleEnvVar); role != "" {
		dbURL.User = url.UserPassword(role, os.Getenv(PasswordEnvVar))
	}
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
