package portaltls

// The shared Client Portal name against Pebble; skipped unless PORTALTLS_PEBBLE=1 (scripts/portaltls-pebble-test.sh).

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func sharedCSR(t *testing.T, names ...string) (*ecdsa.PrivateKey, string) {
	t.Helper()
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return k, csrPEM(t, k, x509.CertificateRequest{DNSNames: names})
}

func leafOf(t *testing.T, o *OrderView) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode([]byte(*o.ChainPEM))
	if b == nil {
		t.Fatal("no leaf in the chain")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// Two appliances on the shared address each obtain their OWN certificate for the shared name (own key, own
// identifier set); one on another address is refused; nobody may claim the shared name as a hotel domain; the
// readiness check gates orders; revoking one appliance leaves the other's certificate alone.
func TestPebbleSharedPortalName(t *testing.T) {
	env := pebbleEnvOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, env.pgURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// This service has a new ACME account key, so the account another test registered is forgotten too.
	for _, q := range []string{
		`DELETE FROM portal_certificate_orders`, `DELETE FROM portal_custom_domains`, `DELETE FROM portal_hostnames`,
		`UPDATE portal_tls_settings SET account_url=NULL, account_directory=NULL, caa_applied_at=NULL`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var tenant, site string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('ptls-'||substr(md5(random()::text),1,8), 'Shared portal test') RETURNING id::text`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO sites (tenant_id, code, name) VALUES ($1,'S2','Shared test site') RETURNING id::text`, tenant).Scan(&site); err != nil {
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
	appA, appB, appC := newAppliance("SHR-A"), newAppliance("SHR-B"), newAppliance("SHR-C")

	doh1, doh2 := &mutableDoH{table: map[string][]dohAnswer{}}, &mutableDoH{table: map[string][]dohAnswer{}}
	s1, s2 := httptest.NewServer(doh1), httptest.NewServer(doh2)
	defer s1.Close()
	defer s2.Close()
	svc := New(pool, Options{
		SecretsDir: t.TempDir(), DNSProvider: ProviderChalltestsrv, ChalltestsrvURL: env.ctsURL,
		DirectoryURL: env.acmeDir, ACMECAFile: env.acmeCA, DNSCheckServers: []string{env.dns},
		DoHURLs: []string{s1.URL, s2.URL}, PropagationPoll: 300 * time.Millisecond, PropagationLimit: 30 * time.Second,
		SharedHostname: DefaultSharedHostname, SharedAddress: DefaultSharedAddress,
	})
	shared := DefaultSharedHostname
	str := func(s string) *string { return &s }
	limit := 10
	if _, err := svc.UpdateSettings(ctx, SettingsUpdate{ContactEmail: str("ops@onegate.test"), WeeklyNewLimit: &limit}, "test"); err != nil {
		t.Fatal(err)
	}
	if res, err := svc.TestAccess(ctx, "test"); err != nil || res.AccountURL == "" {
		t.Fatalf("test access: %+v %v", res, err)
	}
	cts := &Challtestsrv{BaseURL: env.ctsURL}
	_ = cts.post(ctx, "/clear-caa", map[string]any{"host": "onegate.work."})

	// Public DNS: the A record only, the challenge delegation not yet published.
	for _, d := range []*mutableDoH{doh1, doh2} {
		d.set(shared, rrA, DefaultSharedAddress)
	}
	syncA, err := svc.Sync(ctx, appA, SyncRequest{PortalIP: DefaultSharedAddress})
	if err != nil {
		t.Fatal(err)
	}
	sp := syncA.SharedPortal
	if sp == nil || sp.Hostname != shared || !sp.Eligible || sp.Ready || sp.Error == nil || !strings.Contains(*sp.Error, "_acme-challenge") {
		t.Fatalf("an eligible appliance sees the name, not ready until the delegation is published: %+v", sp)
	}
	if len(sp.Records) != 2 || sp.Records[1].Value != SharedChallengeLabel+".acme.onegate.work." {
		t.Fatalf("the two fleet-wide records: %+v", sp.Records)
	}
	hostA := syncA.Hostname
	keyA, csrA := sharedCSR(t, shared, hostA)
	if _, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: shared, CSRPEM: csrA}); !isRefusal(err, 422) || err.(*Refusal).Code != "shared_not_ready" {
		t.Fatalf("no order before public DNS is ready: %v", err)
	}

	// Publish the delegation (both resolvers, and the CA's DNS server).
	for _, d := range []*mutableDoH{doh1, doh2} {
		d.set("_acme-challenge."+shared, rrCNAME, SharedChallengeName("onegate.work")+".")
	}
	if err := cts.post(ctx, "/set-cname", map[string]any{"host": dnsName("_acme-challenge." + shared), "target": SharedChallengeName("onegate.work") + "."}); err != nil {
		t.Fatal(err)
	}

	// Wrong name sets are refused.
	for _, names := range [][]string{{shared}, {shared, "zzzzzzzz.gw.onegate.work"}, {hostA}, {shared, hostA, "x.example.com"}} {
		_, bad := sharedCSR(t, names...)
		if _, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: shared, CSRPEM: bad}); !isRefusal(err, 422) {
			t.Fatalf("CSR %v for the shared name must be refused: %v", names, err)
		}
	}

	accA, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: shared, CSRPEM: csrA})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(ctx)
	oA := mustIssued(t, ctx, svc, appA, accA.OrderID, shared, &keyA.PublicKey, env)
	leafA := leafOf(t, oA)
	gotNames := append([]string{}, leafA.DNSNames...)
	sort.Strings(gotNames)
	want := []string{shared, hostA}
	sort.Strings(want)
	if strings.Join(gotNames, ",") != strings.Join(want, ",") {
		t.Fatalf("A's certificate names %v, want %v", leafA.DNSNames, want)
	}

	// Appliance B: its own key, its own identifier set; also counted as new.
	syncB, _ := svc.Sync(ctx, appB, SyncRequest{PortalIP: DefaultSharedAddress})
	keyB, csrB := sharedCSR(t, shared, syncB.Hostname)
	accB, err := svc.CreateOrder(ctx, appB, OrderRequest{Hostname: shared, CSRPEM: csrB})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(ctx)
	oB := mustIssued(t, ctx, svc, appB, accB.OrderID, shared, &keyB.PublicKey, env)
	if leafOf(t, oB).PublicKey.(*ecdsa.PublicKey).Equal(leafA.PublicKey) {
		t.Fatal("two appliances must never share a key")
	}
	var newA, newB bool
	_ = pool.QueryRow(ctx, `SELECT counts_new FROM portal_certificate_orders WHERE id::text=$1`, accA.OrderID).Scan(&newA)
	_ = pool.QueryRow(ctx, `SELECT counts_new FROM portal_certificate_orders WHERE id::text=$1`, accB.OrderID).Scan(&newB)
	if !newA || !newB {
		t.Fatalf("each appliance's first shared-name certificate is new: A %v, B %v", newA, newB)
	}

	// A's renewal is not new, and supersedes A's first certificate only.
	keyA2, csrA2 := sharedCSR(t, hostA, shared)
	accA2, err := svc.CreateOrder(ctx, appA, OrderRequest{Hostname: shared, CSRPEM: csrA2})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(ctx)
	mustIssued(t, ctx, svc, appA, accA2.OrderID, shared, &keyA2.PublicKey, env)
	var renewNew bool
	var stA1, stB string
	_ = pool.QueryRow(ctx, `SELECT counts_new FROM portal_certificate_orders WHERE id::text=$1`, accA2.OrderID).Scan(&renewNew)
	_ = pool.QueryRow(ctx, `SELECT status FROM portal_certificate_orders WHERE id::text=$1`, accA.OrderID).Scan(&stA1)
	_ = pool.QueryRow(ctx, `SELECT status FROM portal_certificate_orders WHERE id::text=$1`, accB.OrderID).Scan(&stB)
	if renewNew || stA1 != "SUPERSEDED" || stB != "ISSUED" {
		t.Fatalf("renewal: counts_new %v (want false), A1 %s (want SUPERSEDED), B %s (want ISSUED)", renewNew, stA1, stB)
	}

	// The sync reports the shared certificate.
	syncA, _ = svc.Sync(ctx, appA, SyncRequest{PortalIP: DefaultSharedAddress})
	found := false
	for _, c := range syncA.Certificates {
		if c.Hostname == shared && c.Serial != nil && *c.Serial != "" {
			found = true
		}
	}
	if !found || !syncA.SharedPortal.Ready {
		t.Fatalf("the sync reports the shared certificate and readiness: %+v %+v", syncA.Certificates, syncA.SharedPortal)
	}

	// Appliance C on another portal address: not eligible, refused.
	syncC, _ := svc.Sync(ctx, appC, SyncRequest{PortalIP: "10.254.254.1"})
	if syncC.SharedPortal == nil || syncC.SharedPortal.Eligible {
		t.Fatalf("an appliance on another address is not eligible: %+v", syncC.SharedPortal)
	}
	_, csrC := sharedCSR(t, shared, syncC.Hostname)
	if _, err := svc.CreateOrder(ctx, appC, OrderRequest{Hostname: shared, CSRPEM: csrC}); !isRefusal(err, 422) || err.(*Refusal).Code != "shared_not_eligible" {
		t.Fatalf("an appliance on another address cannot order the shared name: %v", err)
	}

	// Nobody may claim the shared name (or a name below it) as a hotel's own domain.
	for _, name := range []string{shared, "x." + shared} {
		v, _ := svc.Sync(ctx, appB, SyncRequest{PortalIP: DefaultSharedAddress, CustomDomain: str(name)})
		if v.CustomDomain == nil || v.CustomDomain.Status != "FAILED" || len(v.CustomDomain.Records) != 0 {
			t.Fatalf("%s as a hotel domain must be refused: %+v", name, v.CustomDomain)
		}
	}

	// Revoking A leaves B's certificate valid.
	if n, err := svc.RevokeAppliance(ctx, appA, "", "operator@test", "test"); err != nil || n < 1 {
		t.Fatalf("revoke A: %d %v", n, err)
	}
	svc.Tick(ctx)
	_ = pool.QueryRow(ctx, `SELECT status FROM portal_certificate_orders WHERE id::text=$1`, accB.OrderID).Scan(&stB)
	var revokedA string
	_ = pool.QueryRow(ctx, `SELECT status FROM portal_certificate_orders WHERE id::text=$1`, accA2.OrderID).Scan(&revokedA)
	if revokedA != "REVOKED" || stB != "ISSUED" {
		t.Fatalf("revocation is per appliance: A %s (want REVOKED), B %s (want ISSUED)", revokedA, stB)
	}
}
