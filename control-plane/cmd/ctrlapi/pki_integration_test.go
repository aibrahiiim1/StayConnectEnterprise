package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

func testCAPaths(t *testing.T) caPaths {
	d := t.TempDir()
	return caPaths{
		rootCert:       filepath.Join(d, "pki", "root-ca.crt"),
		intCert:        filepath.Join(d, "pki", "intermediate-ca.crt"),
		intKey:         filepath.Join(d, "pki", "intermediate-ca.key"),
		rootKeyOffline: filepath.Join(d, "pki-offline", "root-ca.key"),
	}
}

// Audit Phase 1 H1: init-ca records the CA once; serve loads it without creating anything; a different root
// already stored in appliance_ca_versions stops the PKI and leaves the row unchanged; a missing file never
// leads to a new CA.
func TestPKIStartupFailsClosed(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()

	first := testCAPaths(t)
	ca, err := initCA(ctx, pool, first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := startPKI(ctx, pool, first); err != nil {
		t.Fatalf("serve must load the CA init-ca made: %v", err)
	}
	// A second init-ca against the same database is refused even with fresh paths.
	if _, err := initCA(ctx, pool, testCAPaths(t)); err == nil {
		t.Fatal("init-ca must refuse when the database already records a CA")
	}

	// Files of another Central (another root) on this database: refused, row untouched.
	other := testCAPaths(t)
	if _, err := initCA(ctx, nil, other); err != nil {
		t.Fatal(err)
	}
	if _, err := startPKI(ctx, pool, other); err == nil {
		t.Fatal("a CA different from the recorded one must stop the PKI")
	}
	var stored string
	if err := pool.QueryRow(ctx, `SELECT cert_pem FROM appliance_ca_versions WHERE version=0`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !samePEMCert(stored, string(ca.RootPEM())) {
		t.Fatal("the recorded root CA was overwritten")
	}

	// A lost root certificate: the PKI stays off and no file appears.
	if err := os.Remove(first.rootCert); err != nil {
		t.Fatal(err)
	}
	if _, err := startPKI(ctx, pool, first); err == nil {
		t.Fatal("a missing root certificate must stop the PKI")
	}
	if _, err := os.Stat(first.rootCert); !os.IsNotExist(err) {
		t.Fatal("serve re-created the root certificate")
	}
}
