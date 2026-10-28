package licstate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	lic "github.com/stayconnect/enterprise/license"
)

func licenceServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lastCloudValidation(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		LastCloudValidation string `json:"last_cloud_validation"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	if st.LastCloudValidation == "0001-01-01T00:00:00Z" {
		return ""
	}
	return st.LastCloudValidation
}

// P2 L24 / M4 step 4: a null envelope over the non-mTLS client does not reset cloud staleness.
func TestNullEnvelopeOffMTLSDoesNotMarkCloudValidated(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	srv := licenceServer(t, map[string]any{"license_id": "", "envelope": nil, "revoked": []string{}})
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := m.FetchFromCloud(context.Background(), srv.URL, "44444444-4444-4444-4444-444444444444", priv); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := lastCloudValidation(t, f.dir); got != "" {
		t.Fatalf("null envelope over plain HTTPS marked cloud validation at %s", got)
	}

	// The same answer over the mutual-TLS transport still counts.
	m.SetMTLSTransport(srv.Client(), srv.URL)
	if err := m.FetchFromCloud(context.Background(), srv.URL, "44444444-4444-4444-4444-444444444444", priv); err != nil {
		t.Fatalf("fetch over mTLS transport: %v", err)
	}
	if got := lastCloudValidation(t, f.dir); got == "" {
		t.Fatal("null envelope over the mTLS transport must mark cloud validation")
	}
}

// P2 M4 step 3: a revocation that cannot be stored fails the fetch instead of being dropped.
func TestRevocationSaveFailureFailsFetch(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "revoked.json"), []byte("[\"x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := licenceServer(t, map[string]any{"license_id": "", "envelope": nil,
		"revoked": []string{"11111111-1111-1111-1111-111111111111"}})
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := m.FetchFromCloud(context.Background(), srv.URL, "44444444-4444-4444-4444-444444444444", priv); err == nil {
		t.Fatal("fetch must fail when the revocation cannot be stored")
	}
}

// P2 L14: an unchanged evaluation does not rewrite the limit rows; a new licence does.
func TestUnchangedEvaluationSkipsLimitSync(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	var syncs int
	m.syncLimitsFn = func(context.Context, *lic.Document) error { syncs++; return nil }
	m.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "aaaa", Serial: "SC-1"})
	if _, err := m.Install(context.Background(), f.envelope(t, "aaaa", 1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		m.Load(context.Background())
	}
	if syncs != 1 {
		t.Fatalf("limit sync ran %d times for one unchanged licence, want 1", syncs)
	}
	if _, err := m.Install(context.Background(), f.envelope(t, "aaaa", 2)); err != nil {
		t.Fatal(err)
	}
	if syncs != 2 {
		t.Fatalf("limit sync ran %d times after a new licence, want 2", syncs)
	}
}

// P2 L14: Load, Install and the fetch's identity write do not race (run with -race).
func TestConcurrentLoadInstallFetchNoRace(t *testing.T) {
	f := newFixture(t)
	m := New(nil, "", f.dir, f.pubPath, true)
	m.syncLimitsFn = func(context.Context, *lic.Document) error { return nil }
	m.SetLocalIdentity(lic.LocalIdentity{IdentityKeyFingerprint: "aaaa", Serial: "SC-1"})
	srv := licenceServer(t, map[string]any{"license_id": "", "envelope": nil, "revoked": []string{}})
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(3)
		go func() { defer wg.Done(); m.Load(context.Background()) }()
		go func(v int64) {
			defer wg.Done()
			_, _ = m.Install(context.Background(), f.envelope(t, "aaaa", v))
		}(int64(i + 1))
		go func() {
			defer wg.Done()
			_ = m.FetchFromCloud(context.Background(), srv.URL, "44444444-4444-4444-4444-444444444444", priv)
		}()
	}
	wg.Wait()
}
