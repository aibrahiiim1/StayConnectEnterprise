package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/offline"
	lic "github.com/stayconnect/enterprise/license"
)

// AD-18(a): the single-use ledger row is written together with a successful licence install. Needs a Postgres
// reachable at SCD_TEST_DB_URL (any empty database); skipped without it.
func TestOfflineImportLedgerRowIsWrittenOnlyWithASuccessfulInstall(t *testing.T) {
	url := os.Getenv("SCD_TEST_DB_URL")
	if url == "" {
		t.Skip("SCD_TEST_DB_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS edge_offline_packages;
        CREATE TABLE edge_offline_packages (package_id uuid PRIMARY KEY, nonce text UNIQUE,
            consumed_at timestamptz NOT NULL DEFAULT now(), reconciled_at timestamptz)`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	vpub, vpriv, _ := ed25519.GenerateKey(rand.Reader)
	vpath := filepath.Join(root, "vendor.pub")
	if err := os.WriteFile(vpath, vpub, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCD_VENDOR_PUB", vpath)
	t.Setenv("SCD_ASSIGNMENT_DIR", filepath.Join(root, "assignment"))

	const idFpr = "aaaaaaaaaaaaaaaa"
	m := licstate.New(nil, "", filepath.Join(root, "license"), vpath, true)
	m.SetLocalIdentity(lic.LocalIdentity{ApplianceID: "appl-1", IdentityKeyFingerprint: idFpr, Serial: "SC-1"})
	s := &server{db: pool, lic: m, applID: "appl-1", serial: "SC-1", identityKeyFpr: idFpr}

	signer := lic.NewSigner(vpriv)
	envelope := func(forAppliance string, version int64) json.RawMessage {
		now := time.Now().UTC()
		env, err := signer.Sign(&lic.Document{LicenseID: "11111111-1111-1111-1111-111111111111", Status: lic.DocActive,
			TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
			ApplianceID: forAppliance, ApplianceIDs: []string{forAppliance}, IdentityKeyFingerprint: idFpr, ApplianceSerial: "SC-1",
			IssuedAt: now.Add(-time.Hour), ValidUntil: now.AddDate(0, 6, 0), SchemaVersion: lic.CurrentSchemaVersion,
			Modules: lic.Modules{}, LicenseVersion: version})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := env.Encode()
		return json.RawMessage(raw)
	}
	pkg := func(id string, env json.RawMessage) []byte {
		p := &offline.Package{PackageID: id, ApplianceID: "appl-1", Serial: "SC-1", IdentityKeyFpr: idFpr,
			TenantID: "22222222-2222-2222-2222-222222222222", SiteID: "33333333-3333-3333-3333-333333333333",
			LicenseEnvelope: env, Entitlements: json.RawMessage(`{}`), IssuedAt: time.Now().Unix(),
			ExpiresAt: time.Now().Add(time.Hour).Unix(), Nonce: "nonce-" + id}
		offline.Sign(vpriv, p)
		b, _ := json.Marshal(p)
		return b
	}
	post := func(body []byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.setupOfflineImport(w, httptest.NewRequest(http.MethodPost, "/setup/offline-import", bytes.NewReader(body)))
		return w
	}
	ledger := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM edge_offline_packages`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// A licence for ANOTHER appliance fails to install: the package is not burned.
	bad := pkg("00000000-0000-4000-8000-000000000001", envelope("appl-OTHER", 1))
	if w := post(bad); w.Code != http.StatusBadRequest {
		t.Fatalf("a licence for another appliance must fail the import, got %d %s", w.Code, w.Body)
	}
	if n := ledger(); n != 0 {
		t.Fatalf("a failed install must not write the ledger row, got %d", n)
	}
	// The same kind of package with a good licence succeeds and records exactly one row.
	good := pkg("00000000-0000-4000-8000-000000000002", envelope("appl-1", 1))
	if w := post(good); w.Code != http.StatusOK {
		t.Fatalf("a good package must import, got %d %s", w.Code, w.Body)
	}
	if n := ledger(); n != 1 {
		t.Fatalf("one consumed row expected, got %d", n)
	}
	if w := post(good); w.Code != http.StatusConflict {
		t.Fatalf("a replayed package must be refused as consumed, got %d %s", w.Code, w.Body)
	}
	// No database: the import is refused rather than skipping the single-use ledger.
	s.db = nil
	if w := post(pkg("00000000-0000-4000-8000-000000000003", envelope("appl-1", 2))); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("an import without the ledger database must be refused, got %d %s", w.Code, w.Body)
	}
}
