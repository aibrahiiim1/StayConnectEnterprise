package main

// Portal certificate agent decisions with a fake Central, fake clock, in-memory database and test CA.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaltls"
	"github.com/stayconnect/enterprise/data-plane/internal/portaltls/portaltlstest"
)

const (
	ogHost     = "ab12cd34.gw.onegate.work"
	customHost = "wifi.nilehotel.com"
)

type memRepo struct {
	set   ptSettings
	state ptState
	certs map[string]*ptCertRow
	saves int
}

func newMemRepo() *memRepo {
	return &memRepo{
		set:   ptSettings{PortalIP: "10.255.255.1"},
		state: ptState{DNSState: "UNKNOWN", FallbackReason: ptReasonNotConfigured},
		certs: map[string]*ptCertRow{},
	}
}

func (m *memRepo) Settings(context.Context) (ptSettings, error) { return m.set, nil }
func (m *memRepo) LoadState(context.Context) (ptState, error)   { return m.state, nil }
func (m *memRepo) SaveState(_ context.Context, st ptState) error {
	if st.HTTPSActive != (st.FallbackReason == "") || (st.HTTPSActive && st.Canonical == "") {
		return fmt.Errorf("the row would violate migration 0116's CHECKs: %+v", st)
	}
	m.state = st
	m.saves++
	return nil
}
func (m *memRepo) Certs(context.Context) (map[string]*ptCertRow, error) {
	out := map[string]*ptCertRow{}
	for k, v := range m.certs {
		c := *v
		out[k] = &c
	}
	return out, nil
}
func (m *memRepo) UpsertCert(_ context.Context, c *ptCertRow) error {
	cp := *c
	m.certs[c.Hostname] = &cp
	return nil
}
func (m *memRepo) DeleteCert(_ context.Context, h string) error { delete(m.certs, h); return nil }

type fakeOrder struct {
	host   string
	csr    []byte
	status string
	chain  []byte
}

type fakeCentral struct {
	ca  *portaltlstest.CA
	now func() time.Time

	unreachable bool
	available   bool
	hostname    string
	domain      *ptCustomDomain
	revoked     map[string]bool // serial -> revoked
	renewAfter  map[string]time.Time

	createErr  error
	conflictID string
	orders     map[string]*fakeOrder
	seq        int
	syncs      []ptSyncReq
	creates    int
	// lifetime of issued certificates
	lifetime time.Duration
	// autoIssue issues on the first GetOrder.
	autoIssue bool
	failNext  bool
}

func newFakeCentral(ca *portaltlstest.CA, now func() time.Time) *fakeCentral {
	return &fakeCentral{ca: ca, now: now, available: true, hostname: ogHost, orders: map[string]*fakeOrder{},
		revoked: map[string]bool{}, renewAfter: map[string]time.Time{}, lifetime: 90 * 24 * time.Hour, autoIssue: true}
}

func (f *fakeCentral) Sync(_ context.Context, req ptSyncReq) (*ptSyncResp, error) {
	f.syncs = append(f.syncs, req)
	if f.unreachable {
		return nil, errors.New("dial tcp: connection refused")
	}
	r := &ptSyncResp{Available: f.available, Hostname: f.hostname, DNSState: "SYNCED"}
	if req.CustomDomain != nil && f.domain != nil {
		d := *f.domain
		r.CustomDomain = &d
	}
	for serial := range f.revoked {
		r.Certificates = append(r.Certificates, ptCertStatus{Hostname: ogHost, Serial: serial, Revoked: true})
	}
	for host, ra := range f.renewAfter {
		ra := ra
		r.Certificates = append(r.Certificates, ptCertStatus{Hostname: host, RenewAfter: &ra, Serial: ""})
	}
	return r, nil
}

func (f *fakeCentral) CreateOrder(_ context.Context, host string, csr []byte) (string, bool, error) {
	f.creates++
	if f.createErr != nil {
		return "", false, f.createErr
	}
	if f.conflictID != "" {
		return f.conflictID, true, nil
	}
	f.seq++
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
	f.orders[id] = &fakeOrder{host: host, csr: csr, status: "QUEUED"}
	return id, false, nil
}

func (f *fakeCentral) GetOrder(_ context.Context, id string) (*ptOrder, error) {
	o, ok := f.orders[id]
	if !ok {
		return nil, &ptHTTPError{Code: http.StatusNotFound}
	}
	if f.failNext {
		f.failNext = false
		o.status = "FAILED"
		return &ptOrder{Status: "FAILED", Error: "dns-01 challenge failed"}, nil
	}
	if f.autoIssue && o.status == "QUEUED" {
		now := f.now()
		chain, err := f.ca.IssueForCSR(o.csr, now.Add(-time.Minute), now.Add(f.lifetime))
		if err != nil {
			return nil, err
		}
		o.status, o.chain = "ISSUED", chain
	}
	return &ptOrder{Status: o.status, ChainPEM: string(o.chain)}, nil
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

type agentFixture struct {
	a       *portalTLSAgent
	repo    *memRepo
	central *fakeCentral
	store   *portaltls.Store
	clk     *clock
	ca      *portaltlstest.CA
	addrUp  bool
}

func newAgentFixture(t *testing.T) *agentFixture {
	t.Helper()
	clk := &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	ca, err := portaltlstest.New(clk.t)
	if err != nil {
		t.Fatal(err)
	}
	store := &portaltls.Store{
		Dir: t.TempDir(), LookupGID: func(string) (int, bool) { return 1, true },
		Chown: func(string, int, int) error { return nil }, Now: clk.now, Roots: ca.Pool(),
	}
	f := &agentFixture{repo: newMemRepo(), clk: clk, ca: ca, store: store, addrUp: true}
	f.central = newFakeCentral(ca, clk.now)
	f.a = newPortalTLSAgent(f.repo, f.central, store, "")
	f.a.now = clk.now
	f.a.addrUp = func(ip string) bool { return f.addrUp && ip == f.repo.set.PortalIP }
	return f
}

func (f *agentFixture) cycle(t *testing.T, force bool) PortalTLSState {
	t.Helper()
	if err := f.a.cycle(context.Background(), force); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	return f.a.State()
}

// toActive runs the two cycles that take a fresh appliance to HTTPS: order, then collect + install.
func (f *agentFixture) toActive(t *testing.T) PortalTLSState {
	t.Helper()
	st := f.cycle(t, false)
	if st.HTTPSActive || st.FallbackReason != ptReasonNoCertificate {
		t.Fatalf("first cycle: %+v", st)
	}
	if f.repo.certs[ogHost] == nil || f.repo.certs[ogHost].PendingOrderID == "" || !f.store.HasPending(ogHost) {
		t.Fatalf("the first cycle must order with a pending key: %+v", f.repo.certs[ogHost])
	}
	st = f.cycle(t, false)
	if !st.HTTPSActive || st.Canonical != ogHost {
		t.Fatalf("second cycle must install and go HTTPS: %+v (row %+v)", st, f.repo.certs[ogHost])
	}
	return st
}

func TestNoCertificateOrdersThenTheIssuedCertificateIsInstalledAndHTTPSActivates(t *testing.T) {
	f := newAgentFixture(t)
	st := f.toActive(t)
	row := f.repo.certs[ogHost]
	if row.PendingOrderID != "" || row.Serial == "" || row.InstalledAt == nil || row.NotAfter == nil || row.RenewAfter == nil || row.Fingerprint == "" {
		t.Fatalf("the row must describe the installed certificate: %+v", row)
	}
	if f.store.HasPending(ogHost) {
		t.Fatal("the pending key must be gone once installed")
	}
	if f.repo.state.Canonical != ogHost || !f.repo.state.HTTPSActive || f.repo.state.FallbackReason != "" || f.repo.state.FallbackSince != nil {
		t.Fatalf("persisted state: %+v", f.repo.state)
	}
	if len(st.Hostnames) != 1 || st.Hostnames[0] != ogHost || st.PortalIP != "10.255.255.1" {
		t.Fatalf("view: %+v", st)
	}
	if req := f.central.syncs[0]; req.PortalIP != "10.255.255.1" || req.CustomDomain != nil {
		t.Fatalf("sync body: %+v", req)
	}
	// A third cycle changes nothing and orders nothing.
	creates := f.central.creates
	f.cycle(t, false)
	if f.central.creates != creates {
		t.Fatal("a valid certificate must not be re-ordered")
	}
}

func TestRenewAfterPassedOrdersANewKey(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	oldSerial := f.repo.certs[ogHost].Serial
	oldKey, _ := os.ReadFile(f.store.KeyPath(ogHost))

	f.clk.t = f.clk.t.Add(61 * 24 * time.Hour) // past 2/3 of 90 days
	st := f.cycle(t, false)
	if !st.HTTPSActive {
		t.Fatal("renewal is due, the certificate is still valid: HTTPS stays on")
	}
	row := f.repo.certs[ogHost]
	if row.PendingOrderID == "" || !f.store.HasPending(ogHost) {
		t.Fatalf("renew_after passed: an order with a new pending key: %+v", row)
	}
	f.cycle(t, false)
	row = f.repo.certs[ogHost]
	newKey, _ := os.ReadFile(f.store.KeyPath(ogHost))
	if row.Serial == oldSerial || string(newKey) == string(oldKey) {
		t.Fatal("the renewal must install a new certificate on a NEW key")
	}
}

func TestCentralRenewAfterOverridesTheLocalDefault(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	serial := f.repo.certs[ogHost].Serial
	ra := f.clk.t.Add(24 * time.Hour)
	f.central.renewAfter[ogHost] = ra
	// Central's statement applies only to the serial installed; a status without a serial is ignored.
	f.cycle(t, false)
	if got := f.repo.certs[ogHost].RenewAfter; got.Equal(ra) {
		t.Fatal("a renew_after for an unnamed serial must not be applied")
	}
	delete(f.central.renewAfter, ogHost)
	f.central.revoked = map[string]bool{}
	// Use a serial-carrying status this time.
	f.a.central = &serialRenewCentral{fakeCentral: f.central, serial: serial, ra: ra}
	f.cycle(t, false)
	if got := f.repo.certs[ogHost].RenewAfter; got == nil || !got.Equal(ra) {
		t.Fatalf("renew_after from Central: %v", got)
	}
	f.clk.t = ra.Add(time.Minute)
	f.cycle(t, false)
	if f.repo.certs[ogHost].PendingOrderID == "" {
		t.Fatal("past Central's renew_after a new order is placed")
	}
}

type serialRenewCentral struct {
	*fakeCentral
	serial string
	ra     time.Time
}

func (s *serialRenewCentral) Sync(ctx context.Context, req ptSyncReq) (*ptSyncResp, error) {
	r, err := s.fakeCentral.Sync(ctx, req)
	if err == nil {
		ra := s.ra
		r.Certificates = append(r.Certificates, ptCertStatus{Hostname: ogHost, Serial: s.serial, RenewAfter: &ra})
	}
	return r, err
}

func TestARevokedCertificateIsRemovedAndReplaced(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	serial := f.repo.certs[ogHost].Serial
	f.central.revoked[serial] = true
	st := f.cycle(t, false)
	if st.HTTPSActive || st.FallbackReason != ptReasonNoCertificate {
		t.Fatalf("a revoked certificate is not served: %+v", st)
	}
	if _, err := os.Stat(f.store.CertPath(ogHost)); err == nil {
		t.Fatal("the revoked certificate's file must be removed")
	}
	if f.repo.certs[ogHost].PendingOrderID == "" {
		t.Fatal("a replacement must be ordered at once")
	}
	delete(f.central.revoked, serial)
	if st = f.cycle(t, false); !st.HTTPSActive || f.repo.certs[ogHost].Serial == serial {
		t.Fatalf("replacement installed: %+v", st)
	}
}

func TestAFailedOrderDropsThePendingKeyAndBacksOff(t *testing.T) {
	f := newAgentFixture(t)
	f.cycle(t, false)
	f.central.failNext = true
	st := f.cycle(t, false)
	row := f.repo.certs[ogHost]
	if row.PendingOrderID != "" || f.store.HasPending(ogHost) || row.LastError == "" {
		t.Fatalf("after FAILED: %+v", row)
	}
	if st.FallbackReason != ptReasonNoCertificate {
		t.Fatalf("reason %q", st.FallbackReason)
	}
	creates := f.central.creates
	f.clk.t = f.clk.t.Add(30 * time.Minute)
	f.cycle(t, false)
	if f.central.creates != creates {
		t.Fatal("within the hour after a failed order no new order is placed")
	}
	f.clk.t = f.clk.t.Add(31 * time.Minute)
	f.cycle(t, false)
	if f.central.creates != creates+1 {
		t.Fatal("after the backoff the order is retried")
	}
}

func TestRefusedOrdersBackOffAnHour(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusUnprocessableEntity} {
		f := newAgentFixture(t)
		f.central.createErr = &ptHTTPError{Code: code, Msg: "limit"}
		f.cycle(t, false)
		if f.repo.certs[ogHost].LastError == "" {
			t.Fatalf("%d: the refusal must be recorded", code)
		}
		f.cycle(t, false)
		if f.central.creates != 1 {
			t.Fatalf("%d: retried within the backoff (%d orders)", code, f.central.creates)
		}
		f.central.createErr = nil
		f.clk.t = f.clk.t.Add(ptOrderBackoff + time.Minute)
		f.cycle(t, false)
		if f.central.creates != 2 || f.repo.certs[ogHost].PendingOrderID == "" {
			t.Fatalf("%d: after the backoff: %d orders, row %+v", code, f.central.creates, f.repo.certs[ogHost])
		}
	}
}

func TestA409AdoptsTheOpenOrderAndReusesThePendingKey(t *testing.T) {
	f := newAgentFixture(t)
	// A pending key exists with no recorded order: the process stopped right after Central accepted an order.
	csr, err := f.store.NewPendingKey(ogHost)
	if err != nil {
		t.Fatal(err)
	}
	id := "00000000-0000-4000-8000-000000000099"
	f.central.orders[id] = &fakeOrder{host: ogHost, csr: csr, status: "QUEUED"}
	f.central.conflictID = id
	f.cycle(t, false)
	if got := f.repo.certs[ogHost].PendingOrderID; got != id {
		t.Fatalf("adopted order %q, want %q", got, id)
	}
	if st := f.cycle(t, false); !st.HTTPSActive {
		t.Fatalf("the adopted order's certificate matches the reused key and installs: %+v %+v", st, f.repo.certs[ogHost])
	}
}

func TestAnIssuedCertificateForTheWrongKeyIsRefused(t *testing.T) {
	f := newAgentFixture(t)
	f.cycle(t, false)
	id := f.repo.certs[ogHost].PendingOrderID
	other, _ := portaltlstest.New(f.clk.t)
	// Central "issues" a chain from an authority the appliance does not trust.
	chain, _ := other.IssueForCSR(f.central.orders[id].csr, f.clk.t.Add(-time.Minute), f.clk.t.Add(24*time.Hour))
	f.central.orders[id].status, f.central.orders[id].chain = "ISSUED", chain
	st := f.cycle(t, false)
	if st.HTTPSActive {
		t.Fatal("an untrusted chain must never be installed")
	}
	row := f.repo.certs[ogHost]
	if row.PendingOrderID != "" || f.store.HasPending(ogHost) || row.LastError == "" {
		t.Fatalf("refused install: %+v", row)
	}
}

func TestOperatorRenewOrdersANewKeyForAValidCertificate(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	creates := f.central.creates
	f.cycle(t, true)
	if f.central.creates != creates+1 || f.repo.certs[ogHost].PendingOrderID == "" {
		t.Fatal("Renew now must order a new certificate")
	}
	// A second forced cycle with the order still open places no second order.
	f.central.autoIssue = false
	f.cycle(t, true)
	if f.central.creates != creates+1 {
		t.Fatal("an open order is collected, not duplicated")
	}
}

func TestAMissingFileOrAMismatchedKeyIsReordered(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	_ = os.Remove(f.store.CertPath(ogHost))
	st := f.cycle(t, false)
	if st.HTTPSActive || f.repo.certs[ogHost].PendingOrderID == "" || f.repo.certs[ogHost].Serial != "" {
		t.Fatalf("missing file: %+v %+v", st, f.repo.certs[ogHost])
	}
}

// A valid certificate keeps the portal on HTTPS whatever Central says.
func TestAnOutageKeepsHTTPSWhileTheCertificateIsValid(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	f.central.unreachable = true
	for i := 0; i < 3; i++ {
		f.clk.t = f.clk.t.Add(24 * time.Hour)
		st := f.cycle(t, false)
		if !st.HTTPSActive || st.Canonical != ogHost {
			t.Fatalf("day %d of an outage: %+v", i+1, st)
		}
	}
	if f.repo.state.LastSyncError == "" {
		t.Fatal("the failed sync is recorded")
	}
	// A nil Central (an appliance that does not contact Central) likewise.
	f.a.central = nil
	if st := f.cycle(t, false); !st.HTTPSActive {
		t.Fatalf("no Central at all, valid certificate: %+v", st)
	}
}

func TestCentralUnreachableOnlyWhenTheCertificateIsMissingOrExpired(t *testing.T) {
	f := newAgentFixture(t)
	f.central.unreachable = true
	if st := f.cycle(t, false); st.FallbackReason != ptReasonCentralUnreachable {
		t.Fatalf("fresh appliance, Central down: %+v", st)
	}
	f.central.unreachable = false
	f.toActive(t)
	f.central.unreachable = true
	f.clk.t = f.clk.t.Add(91 * 24 * time.Hour) // expired, and no renewal could be ordered
	if st := f.cycle(t, false); st.HTTPSActive || st.FallbackReason != ptReasonCentralUnreachable {
		t.Fatalf("expired + Central down: %+v", st)
	}
	f.central.unreachable = false
	f.central.autoIssue = false
	if st := f.cycle(t, false); st.FallbackReason != ptReasonExpired {
		t.Fatalf("expired, Central reachable, order open: %+v", st)
	}
}

func TestACertificateWithinFiveMinutesOfExpiryIsNotServed(t *testing.T) {
	f := newAgentFixture(t)
	f.central.lifetime = time.Hour
	f.toActive(t)
	f.central.unreachable = true
	f.clk.t = f.clk.t.Add(56 * time.Minute) // 3 minutes left
	if st := f.cycle(t, false); st.HTTPSActive || st.FallbackReason != ptReasonCentralUnreachable {
		t.Fatalf("3 minutes left: %+v", st)
	}
}

func TestNotConfiguredAndNoHostname(t *testing.T) {
	f := newAgentFixture(t)
	f.central.available = false
	f.central.hostname = ""
	if st := f.cycle(t, false); st.FallbackReason != ptReasonNotConfigured || f.central.creates != 0 {
		t.Fatalf("Central not configured: %+v (%d orders)", st, f.central.creates)
	}
	f.central.available = true
	if st := f.cycle(t, false); st.FallbackReason != ptReasonNoHostname {
		t.Fatalf("available, no hostname yet: %+v", st)
	}
	// No Central client at all, nothing installed.
	g := newAgentFixture(t)
	g.a.central = nil
	if st := g.cycle(t, false); st.FallbackReason != ptReasonNotConfigured {
		t.Fatalf("no Central: %+v", st)
	}
}

func TestThePortalAddressMustBeUpOnOgportal0(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	f.addrUp = false
	st := f.cycle(t, false)
	if st.HTTPSActive || st.FallbackReason != ptReasonAddressNotActive || st.Canonical != ogHost {
		t.Fatalf("address down: %+v", st)
	}
	if f.repo.state.FallbackSince == nil || !f.repo.state.FallbackSince.Equal(f.clk.t) {
		t.Fatalf("entering the fallback stamps fallback_since: %+v", f.repo.state.FallbackSince)
	}
	since := *f.repo.state.FallbackSince
	f.clk.t = f.clk.t.Add(time.Minute)
	f.cycle(t, false)
	if !f.repo.state.FallbackSince.Equal(since) {
		t.Fatal("fallback_since is the moment the fallback began, not the latest cycle")
	}
	f.addrUp = true
	if st := f.cycle(t, false); !st.HTTPSActive || f.repo.state.FallbackSince != nil {
		t.Fatalf("recovery is automatic: %+v", st)
	}
	f.repo.set.PortalIP = ""
	if st := f.cycle(t, false); st.FallbackReason != ptReasonAddressNotActive {
		t.Fatalf("no portal address: %+v", st)
	}
}

func TestAVerifiedHotelDomainBecomesCanonicalAndItsRemovalCleansUp(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	f.repo.set.CustomDomain = customHost
	f.central.domain = &ptCustomDomain{FQDN: customHost, Status: "PENDING",
		Records: []ptRecord{{Type: "TXT", Name: "_onegate-verify." + customHost, Value: "tok"}}}
	st := f.cycle(t, false)
	if st.Canonical != ogHost || f.repo.certs[customHost] != nil {
		t.Fatalf("a PENDING domain is neither ordered nor canonical: %+v", st)
	}
	if f.repo.state.CustomDomainStatus != "PENDING" || string(f.repo.state.CustomDomainRecords) == "[]" {
		t.Fatalf("the records to publish are kept for the Admin Console: %+v", f.repo.state)
	}
	if req := f.central.syncs[len(f.central.syncs)-1]; req.CustomDomain == nil || *req.CustomDomain != customHost {
		t.Fatalf("the sync carries the requested domain: %+v", req)
	}

	f.central.domain.Status = "VERIFIED"
	f.cycle(t, false) // order
	st = f.cycle(t, false)
	if !st.HTTPSActive || st.Canonical != customHost {
		t.Fatalf("a verified domain with a valid certificate is canonical: %+v", st)
	}
	hs := append([]string{}, st.Hostnames...)
	sort.Strings(hs)
	if len(hs) != 2 {
		t.Fatalf("both hostnames are served: %v", hs)
	}

	// The site removes the domain; even with Central unreachable it stops being served now.
	f.repo.set.CustomDomain = ""
	f.central.unreachable = true
	st = f.cycle(t, false)
	if st.Canonical != ogHost || !st.HTTPSActive {
		t.Fatalf("after removal the OneGate hostname is canonical: %+v", st)
	}
	if f.repo.certs[customHost] != nil {
		t.Fatal("the removed domain's row must be deleted")
	}
	for _, p := range []string{f.store.CertPath(customHost), f.store.KeyPath(customHost), f.store.PendingKeyPath(customHost)} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s must be removed", p)
		}
	}
	if f.repo.state.CustomDomain != "" {
		t.Fatalf("state still names the removed domain: %+v", f.repo.state)
	}
}

func TestAnEmptyHostnameAnswerNeverErasesTheKnownOne(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	f.central.hostname = ""
	f.central.available = false
	if st := f.cycle(t, false); !st.HTTPSActive || st.OneGateHostname != ogHost {
		t.Fatalf("Central mid-configuration must not take a valid certificate down: %+v", st)
	}
	if _, err := os.Stat(f.store.CertPath(ogHost)); err != nil {
		t.Fatal("the certificate must stay")
	}
}

func TestTheTLSStateEndpoint(t *testing.T) {
	f := newAgentFixture(t)
	f.toActive(t)
	s := &server{portalTLS: f.a}
	rec := httptest.NewRecorder()
	s.portalTLSState(rec, httptest.NewRequest(http.MethodGet, "/v1/portal/tls-state", nil))
	var got map[string]any
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for k, want := range map[string]any{"https_active": true, "canonical_hostname": ogHost, "onegate_hostname": ogHost,
		"portal_ip": "10.255.255.1", "fallback_reason": "", "cert_dir": f.store.Dir, "trust": "system", "custom_domain": ""} {
		if got[k] != want {
			t.Errorf("%s = %v, want %v", k, got[k], want)
		}
	}

	// A nil agent answers the fallback, never an error and never "active".
	rec = httptest.NewRecorder()
	(&server{}).portalTLSState(rec, httptest.NewRequest(http.MethodGet, "/v1/portal/tls-state", nil))
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got["https_active"] != false || got["fallback_reason"] != ptReasonNotConfigured {
		t.Fatalf("nil agent: %d %s", rec.Code, rec.Body.String())
	}
	// Kick and renew coalesce and answer 202; without an agent, 503.
	for _, h := range []http.HandlerFunc{s.portalTLSKick, s.portalTLSRenew} {
		for i := 0; i < 3; i++ {
			rec = httptest.NewRecorder()
			h(rec, httptest.NewRequest(http.MethodPost, "/", nil))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("kick/renew: %d", rec.Code)
			}
		}
	}
	rec = httptest.NewRecorder()
	(&server{}).portalTLSKick(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("no agent: %d", rec.Code)
	}
}

func TestTestRootsAreReportedAsSuch(t *testing.T) {
	a := newPortalTLSAgent(nil, nil, &portaltls.Store{Dir: "/x"}, ptTrustTestRoots)
	if st := a.State(); st.Trust != "test-roots" || st.HTTPSActive {
		t.Fatalf("%+v", st)
	}
}

func TestSameSerial(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"0aBc", "abc", true}, {"0a:bc", "ABC", true}, {"abc", "abd", false}, {"", "", false}} {
		if sameSerial(c.a, c.b) != c.want {
			t.Errorf("%q %q", c.a, c.b)
		}
	}
}
