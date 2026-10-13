package portaltls

// END-TO-END against Pebble + pebble-challtestsrv + a migrated Central database. Skipped unless
// PORTALTLS_PEBBLE=1; scripts/portaltls-pebble-test.sh starts everything and sets the variables.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type pebbleEnv struct {
	pgURL, acmeDir, acmeCA, pebbleMgmt, ctsURL, dns string
}

func pebbleEnvOrSkip(t *testing.T) pebbleEnv {
	if os.Getenv("PORTALTLS_PEBBLE") != "1" {
		t.Skip("PORTALTLS_PEBBLE=1 not set (run scripts/portaltls-pebble-test.sh)")
	}
	return pebbleEnv{
		pgURL: os.Getenv("PORTALTLS_PG_URL"), acmeDir: os.Getenv("PORTALTLS_ACME_DIR"), acmeCA: os.Getenv("PORTALTLS_ACME_CA"),
		pebbleMgmt: os.Getenv("PORTALTLS_PEBBLE_MGMT"), ctsURL: os.Getenv("PORTALTLS_CTS_URL"), dns: os.Getenv("PORTALTLS_DNS"),
	}
}

// mutableDoH is a DNS-over-HTTPS JSON endpoint whose answers the test sets as the "hotel" publishes records.
type mutableDoH struct {
	mu    sync.Mutex
	table map[string][]dohAnswer
}

func (m *mutableDoH) set(name string, typ int, data string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.table[name+"|"+fmt.Sprint(typ)] = append(m.table[name+"|"+fmt.Sprint(typ)], dohAnswer{Name: name + ".", Type: typ, Data: data})
}

func (m *mutableDoH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := strings.TrimSuffix(r.URL.Query().Get("name"), ".") + "|" + r.URL.Query().Get("type")
	_ = json.NewEncoder(w).Encode(dohResponse{Status: 0, Answer: m.table[key]})
}

func newCSR(t *testing.T, host string) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return k, csrPEM(t, k, x509.CertificateRequest{DNSNames: []string{host}})
}

func TestPebbleEndToEnd(t *testing.T) {
	env := pebbleEnvOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, env.pgURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// A clean slate in the portal tables (events are append-only: a fresh database per run, see the script).
	for _, q := range []string{
		`DELETE FROM portal_certificate_orders`, `DELETE FROM portal_custom_domains`, `DELETE FROM portal_hostnames`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var tenant, site string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('ptls-'||substr(md5(random()::text),1,8), 'Portal TLS test') RETURNING id::text`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sites (tenant_id, code, name) VALUES ($1,'S1','Nile test site') RETURNING id::text`, tenant).Scan(&site); err != nil {
		t.Fatal(err)
	}
	newAppliance := func(serial string) string {
		var id string
		if err := pool.QueryRow(ctx, `
            INSERT INTO appliances (tenant_id, site_id, serial, lifecycle_state, activated_at)
            VALUES ($1,$2,$3,'assigned', now()) RETURNING id::text`, tenant, site, serial+"-"+fmt.Sprint(time.Now().UnixNano())).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	appA, appB, appC := newAppliance("PTLS-A"), newAppliance("PTLS-B"), newAppliance("PTLS-C")

	doh1, doh2 := &mutableDoH{table: map[string][]dohAnswer{}}, &mutableDoH{table: map[string][]dohAnswer{}}
	s1, s2 := httptest.NewServer(doh1), httptest.NewServer(doh2)
	defer s1.Close()
	defer s2.Close()

	svc := New(pool, Options{
		SecretsDir: t.TempDir(), DNSProvider: ProviderChalltestsrv, ChalltestsrvURL: env.ctsURL,
		DirectoryURL: env.acmeDir, ACMECAFile: env.acmeCA, DNSCheckServers: []string{env.dns},
		DoHURLs: []string{s1.URL, s2.URL}, PropagationPoll: 300 * time.Millisecond, PropagationLimit: 30 * time.Second,
	})
	str := func(s string) *string { return &s }
	limit := 2
	if _, err := svc.UpdateSettings(ctx, SettingsUpdate{ContactEmail: str("ops@onegate.test"), WeeklyNewLimit: &limit}, "test"); err != nil {
		t.Fatal(err)
	}
	// Only the TEST provider may be available without a token.
	st, _ := LoadSettings(ctx, pool)
	if ok, why := svc.Availability(st); !ok {
		t.Fatalf("challtestsrv needs no token: %s", why)
	}
	res, err := svc.TestAccess(ctx, "test")
	if err != nil || res.AccountURL == "" {
		t.Fatalf("test access / account registration: %+v %v", res, err)
	}
	if _, err := svc.ApplyCAA(ctx, "test"); err != nil {
		t.Fatalf("apply CAA: %v", err)
	}
	// Pebble's CA identity is pebble.letsencrypt.org, not letsencrypt.org: take the policy away again so it
	// cannot stand between the test CA and the rest of the run.
	_ = (&Challtestsrv{BaseURL: env.ctsURL}).post(ctx, "/clear-caa", map[string]any{"host": "onegate.work."})

	// ---- 1. Hostname allocation and its A record.
	sync1, err := svc.Sync(ctx, appA, SyncRequest{PortalIP: "10.255.255.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !sync1.Available || !strings.HasSuffix(sync1.Hostname, ".gw.onegate.work") || !IsLabel(strings.Split(sync1.Hostname, ".")[0]) {
		t.Fatalf("sync: %+v", sync1)
	}
	if sync1.DNSState != "SYNCED" {
		t.Fatalf("the A record is published during the sync that reports the address: %s", sync1.DNSState)
	}
	again, _ := svc.Sync(ctx, appA, SyncRequest{PortalIP: "10.255.255.1"})
	if again.Hostname != sync1.Hostname {
		t.Fatal("the hostname is permanent")
	}
	if _, err := svc.Sync(ctx, appA, SyncRequest{PortalIP: "8.8.8.8"}); err == nil {
		t.Fatal("a public portal address is refused")
	}
	hostA := sync1.Hostname

	// ---- 2. Order -> ISSUED, a valid chain for the CSR's key.
	keyA1, csrA1 := newCSR(t, hostA)
	acc, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: hostA, CSRPEM: csrA1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: hostA, CSRPEM: csrA1}); !isRefusal(err, 409) {
		t.Fatalf("a second open order for the same hostname is 409: %v", err)
	}
	if _, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: "other.gw.onegate.work", CSRPEM: csrA1}); !isRefusal(err, 422) {
		t.Fatalf("a CSR for another name is 422: %v", err)
	}
	svc.Tick(ctx)
	o1 := mustIssued(t, ctx, svc, appA, acc.OrderID, hostA, &keyA1.PublicKey, env)
	if o1.RenewAfter == nil || !o1.RenewAfter.After(time.Now()) || !o1.RenewAfter.Before(*o1.NotAfter) {
		t.Fatalf("renew_after must lie inside the validity: %v", o1.RenewAfter)
	}
	var countsNew bool
	var dir string
	_ = pool.QueryRow(ctx, `SELECT counts_new, acme_directory_url FROM portal_certificate_orders WHERE id::text=$1`, acc.OrderID).Scan(&countsNew, &dir)
	if !countsNew || dir != env.acmeDir {
		t.Fatalf("a first certificate counts as new and records its directory: %v %q", countsNew, dir)
	}

	// ---- 3. Renewal: same hostname, new key -> not new; the previous one SUPERSEDED.
	keyA2, csrA2 := newCSR(t, hostA)
	acc2, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: hostA, CSRPEM: csrA2})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(ctx)
	mustIssued(t, ctx, svc, appA, acc2.OrderID, hostA, &keyA2.PublicKey, env)
	var prevStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM portal_certificate_orders WHERE id::text=$1`, acc.OrderID).Scan(&prevStatus)
	_ = pool.QueryRow(ctx, `SELECT counts_new FROM portal_certificate_orders WHERE id::text=$1`, acc2.OrderID).Scan(&countsNew)
	if prevStatus != "SUPERSEDED" || countsNew {
		t.Fatalf("renewal: previous %s (want SUPERSEDED), counts_new %v (want false)", prevStatus, countsNew)
	}

	// ---- 4. Weekly cap (limit 2): B's first certificate is the second new one; C's must wait.
	syncB, _ := svc.Sync(ctx, appB, SyncRequest{PortalIP: "10.254.254.1"})
	syncC, _ := svc.Sync(ctx, appC, SyncRequest{PortalIP: "10.253.253.1"})
	keyB, csrB := newCSR(t, syncB.Hostname)
	accB, err := svc.CreateOrder(ctx, appB, OrderRequest{Hostname: syncB.Hostname, CSRPEM: csrB})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(ctx)
	mustIssued(t, ctx, svc, appB, accB.OrderID, syncB.Hostname, &keyB.PublicKey, env)
	_, csrC := newCSR(t, syncC.Hostname)
	accC, err := svc.CreateOrder(ctx, appC, OrderRequest{Hostname: syncC.Hostname, CSRPEM: csrC})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(ctx)
	var cStatus string
	var cNext time.Time
	_ = pool.QueryRow(ctx, `SELECT status, next_attempt_at FROM portal_certificate_orders WHERE id::text=$1`, accC.OrderID).Scan(&cStatus, &cNext)
	if cStatus != "QUEUED" || cNext.Before(time.Now().Add(6*24*time.Hour)) {
		t.Fatalf("over the weekly cap the order waits QUEUED until a slot frees (~7 days): %s %v", cStatus, cNext)
	}
	// Raising the limit lets it through at the next due time.
	limit = 3
	_, _ = svc.UpdateSettings(ctx, SettingsUpdate{WeeklyNewLimit: &limit}, "test")
	_, _ = pool.Exec(ctx, `UPDATE portal_certificate_orders SET next_attempt_at=now() WHERE id::text=$1`, accC.OrderID)
	st, _ = LoadSettings(ctx, pool)
	svc.Tick(ctx)
	_ = pool.QueryRow(ctx, `SELECT status FROM portal_certificate_orders WHERE id::text=$1`, accC.OrderID).Scan(&cStatus)
	if cStatus != "ISSUED" {
		t.Fatalf("under the raised cap the order issues: %s", cStatus)
	}

	// ---- 5. Operator revocation: the CA confirms, and the appliance hears "revoked".
	n, err := svc.RevokeAppliance(ctx, appA, "", "operator@test", "suspected misuse")
	if err != nil || n < 1 {
		t.Fatalf("revoke: %d %v", n, err)
	}
	svc.Tick(ctx)
	var revokedSerial string
	_ = pool.QueryRow(ctx, `SELECT serial FROM portal_certificate_orders WHERE id::text=$1 AND status='REVOKED' AND revoked_at IS NOT NULL`,
		acc2.OrderID).Scan(&revokedSerial)
	if revokedSerial == "" {
		t.Fatal("the current certificate must be REVOKED")
	}
	if got := pebbleCertStatus(t, env, revokedSerial); got != "Revoked" {
		t.Fatalf("Pebble reports serial %s as %q", revokedSerial, got)
	}
	syncAfter, _ := svc.Sync(ctx, appA, SyncRequest{PortalIP: "10.255.255.1"})
	if len(syncAfter.Certificates) == 0 || !syncAfter.Certificates[0].Revoked {
		t.Fatalf("sync tells the appliance its certificate is revoked: %+v", syncAfter.Certificates)
	}

	// ---- 6. Hotel domain, verified through two resolvers, issued through its delegated challenge name.
	const hotel = "wifi.nilehotel.test"
	dsync, err := svc.Sync(ctx, appB, SyncRequest{PortalIP: "10.254.254.1", CustomDomain: str(hotel)})
	if err != nil || dsync.CustomDomain == nil || dsync.CustomDomain.Status != "PENDING" || len(dsync.CustomDomain.Records) != 3 {
		t.Fatalf("domain registration: %+v %v", dsync.CustomDomain, err)
	}
	if other, _ := svc.Sync(ctx, appC, SyncRequest{PortalIP: "10.253.253.1", CustomDomain: str(hotel)}); other.CustomDomain == nil ||
		other.CustomDomain.Status != "FAILED" || len(other.CustomDomain.Records) != 0 {
		t.Fatalf("a name claimed by another appliance is refused: %+v", other.CustomDomain)
	}
	if bad, _ := svc.Sync(ctx, appC, SyncRequest{PortalIP: "10.253.253.1", CustomDomain: str("nilehotel.test")}); bad.CustomDomain == nil || bad.CustomDomain.Status != "FAILED" {
		t.Fatalf("an apex is refused: %+v", bad.CustomDomain)
	}
	_, _ = svc.Sync(ctx, appC, SyncRequest{PortalIP: "10.253.253.1"})
	recs := dsync.CustomDomain.Records
	for _, d := range []*mutableDoH{doh1, doh2} {
		d.set(recs[0].Name, rrTXT, `"`+recs[0].Value+`"`)
		d.set(recs[1].Name, rrCNAME, recs[1].Value)
		d.set(recs[2].Name, rrCNAME, recs[2].Value)
	}
	// The hotel's real DNS (challtestsrv here) carries the delegation the CA follows.
	cts := &Challtestsrv{BaseURL: env.ctsURL}
	if err := cts.post(ctx, "/set-cname", map[string]any{"host": dnsName(recs[2].Name), "target": recs[2].Value}); err != nil {
		t.Fatal(err)
	}
	_, _ = pool.Exec(ctx, `UPDATE portal_custom_domains SET last_checked_at=NULL WHERE fqdn=$1`, hotel)
	dsync, _ = svc.Sync(ctx, appB, SyncRequest{PortalIP: "10.254.254.1", CustomDomain: str(hotel)})
	if dsync.CustomDomain.Status != "VERIFIED" {
		t.Fatalf("all three records published at both resolvers: %+v", dsync.CustomDomain)
	}
	keyH, csrH := newCSR(t, hotel)
	accH, err := svc.CreateOrder(ctx, appB, OrderRequest{Hostname: hotel, CSRPEM: csrH})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(ctx)
	mustIssued(t, ctx, svc, appB, accH.OrderID, hotel, &keyH.PublicKey, env)
	var kind string
	_ = pool.QueryRow(ctx, `SELECT kind, counts_new FROM portal_certificate_orders WHERE id::text=$1`, accH.OrderID).Scan(&kind, &countsNew)
	if kind != KindCustom || countsNew {
		t.Fatalf("a hotel-domain certificate is CUSTOM and never counts against the OneGate cap: %s %v", kind, countsNew)
	}

	// ---- 7. The lifecycle release: B is retired -> hostname released, A record deleted, certificates revoked.
	tx, _ := pool.Begin(ctx)
	if err := ReleaseAppliance(ctx, tx, appB, ActorSystem); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseAppliance(ctx, tx, appB, ActorSystem); err != nil {
		t.Fatal("ReleaseAppliance is idempotent")
	}
	_ = tx.Commit(ctx)
	if err := svc.FinalizeRelease(ctx, appB); err != nil {
		t.Fatalf("finalize release: %v", err)
	}
	var live int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM portal_certificate_orders WHERE appliance_id=$1 AND status IN ('ISSUED','SUPERSEDED')`, appB).Scan(&live)
	var recID *string
	var released *time.Time
	_ = pool.QueryRow(ctx, `SELECT dns_record_id, released_at FROM portal_hostnames WHERE appliance_id=$1`, appB).Scan(&recID, &released)
	var domStatus string
	_ = pool.QueryRow(ctx, `SELECT status FROM portal_custom_domains WHERE appliance_id=$1 ORDER BY created_at DESC LIMIT 1`, appB).Scan(&domStatus)
	if live != 0 || recID != nil || released == nil || domStatus != "REMOVED" {
		t.Fatalf("after release: %d valid certificates, record %v, released %v, domain %s", live, recID, released, domStatus)
	}
	if _, err := svc.Sync(ctx, appB, SyncRequest{PortalIP: "10.254.254.1"}); !isRefusal(err, 403) {
		t.Fatalf("a retired appliance does not get its hostname back by syncing: %v", err)
	}

	// The fleet view tells staging, production and test-CA certificates apart.
	fleet, err := svc.Fleet(ctx)
	if err != nil || len(fleet) < 3 {
		t.Fatalf("fleet: %d %v", len(fleet), err)
	}
	for _, r := range fleet {
		for _, c := range r.Certificates {
			if c.DirectoryURL == nil || c.Directory != "other" {
				t.Errorf("fleet certificate %s: directory %v %q", c.Hostname, c.DirectoryURL, c.Directory)
			}
		}
	}
	evs, _ := svc.Events(ctx, appA, 100)
	kinds := map[string]bool{}
	for _, e := range evs {
		kinds[e.Kind] = true
		if strings.Contains(string(e.Detail), "BEGIN") {
			t.Errorf("an event carries PEM material: %s", e.Kind)
		}
	}
	for _, k := range []string{EvHostnameAllocated, EvDNSSynced, EvOrderQueued, EvOrderIssued, EvRevokeRequested, EvCertRevoked} {
		if !kinds[k] {
			t.Errorf("no %s event for appliance A", k)
		}
	}
}

func isRefusal(err error, status int) bool {
	r, ok := err.(*Refusal)
	return ok && r.Status == status
}

// mustIssued fetches the order as the appliance would and checks the chain: it names exactly host, carries
// the CSR's key, chains to Pebble's root, and is currently valid.
func mustIssued(t *testing.T, ctx context.Context, svc *Service, appID, orderID, host string, pub *ecdsa.PublicKey, env pebbleEnv) *OrderView {
	t.Helper()
	o, err := svc.GetOrder(ctx, appID, orderID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != "ISSUED" || o.ChainPEM == nil {
		var e string
		if o.Error != nil {
			e = *o.Error
		}
		t.Fatalf("order %s for %s: status %s, error %q", orderID, host, o.Status, e)
	}
	var certs []*x509.Certificate
	rest := []byte(*o.ChainPEM)
	for {
		var b *pem.Block
		b, rest = pem.Decode(rest)
		if b == nil {
			break
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		certs = append(certs, c)
	}
	if len(certs) < 2 {
		t.Fatalf("the chain must carry the leaf and its intermediate, got %d", len(certs))
	}
	leaf := certs[0]
	if !pub.Equal(leaf.PublicKey) {
		t.Fatal("the leaf does not carry the CSR's key")
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pebbleRoot(t, env))
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter, Roots: roots}); err != nil {
		t.Fatalf("the chain does not verify for %s: %v", host, err)
	}
	if o.Serial == nil || *o.Serial != fmt.Sprintf("%x", leaf.SerialNumber) {
		t.Fatal("the recorded serial is the leaf's")
	}
	return o
}

func pebbleMgmtClient(t *testing.T, env pebbleEnv) *http.Client {
	ca, err := os.ReadFile(env.acmeCA)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}}
}

func pebbleRoot(t *testing.T, env pebbleEnv) []byte {
	resp, err := pebbleMgmtClient(t, env).Get(env.pebbleMgmt + "/roots/0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b
}

func pebbleCertStatus(t *testing.T, env pebbleEnv, serialHex string) string {
	resp, err := pebbleMgmtClient(t, env).Get(env.pebbleMgmt + "/cert-status-by-serial/" + serialHex)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var v struct {
		Status string `json:"Status"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&v)
	return v.Status
}
