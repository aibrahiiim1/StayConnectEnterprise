package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
)

func (f *fixture) withCA() {
	f.t.Helper()
	ca, err := pki.LoadOrCreate(filepath.Join(f.t.TempDir(), "ca.key"), 1)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliance_ca_versions (version, cert_pem, subject, key_fingerprint, active)
        VALUES (1,$1,$2,$3,true) ON CONFLICT (version) DO NOTHING`, string(ca.CertPEM()), ca.Subject(), ca.KeyFingerprint()); err != nil {
		f.t.Fatal(err)
	}
	f.base.CA = ca
}

func (f *fixture) issueCert(appID string) *x509.Certificate {
	f.t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "appliance"}}, key)
	csr := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der})
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliance_certificate_requests (appliance_id, csr_pem, status) VALUES ($1,$2,'pending')`, appID, string(csr)); err != nil {
		f.t.Fatal(err)
	}
	cb := &CertBase{Base: f.base, CA: f.base.CA, ClientValid: 90 * 24 * time.Hour}
	sc, err := cb.issueCertForAppliance(f.ctx, httptest.NewRequest(http.MethodPost, "/", nil), appID, "test", false)
	if err != nil {
		f.t.Fatal(err)
	}
	block, _ := pem.Decode(sc.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		f.t.Fatal(err)
	}
	return cert
}

func (f *fixture) mtlsRequest(appID string, cert *x509.Certificate) int {
	h := mtlsCertBinding(f.pool)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	req := httptest.NewRequest(http.MethodGet, "/v1/appliance/hello", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	req = req.WithContext(auth.ContextWithAppliance(req.Context(), &auth.ApplianceIdent{ApplianceID: appID}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func (f *fixture) ack(appID string, bx box, cert *x509.Certificate, a assignment.Ack) *httptest.ResponseRecorder {
	assignment.SignAck(bx.priv, &a)
	raw, _ := json.Marshal(a)
	req := httptest.NewRequest(http.MethodPost, "/v1/appliance/assignment/ack", bytes.NewReader(raw))
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}
	req = req.WithContext(auth.ContextWithAppliance(req.Context(), &auth.ApplianceIdent{ApplianceID: appID}))
	rec := httptest.NewRecorder()
	f.base.AckHandler(rec, req)
	return rec
}

// Retirement is atomic: if the terminal assignment cannot be signed, the licence is not revoked and nothing is recorded.
func TestRetirementIsAtomic(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Atomic")
	app := decode(t, f.register(newBox("SC-TEST-0006", "hwf-6")))["appliance_id"].(string)
	f.activate(app, cust, site, 30)

	if _, err := f.pool.Exec(f.ctx, `UPDATE assignment_signing_keys SET state='verify_only'`); err != nil {
		t.Fatal(err)
	}
	rec := f.operatorCall(f.base.retire, http.MethodPost, app, map[string]any{"reason": "end of contract"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("retire with an unsignable terminal document: want 503, got %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM licenses WHERE $1::uuid = ANY(appliance_ids) AND status='active'`, app); n != 1 {
		t.Fatal("a retirement that did not start must not have revoked the licence")
	}
	if n := f.count(`SELECT count(*) FROM appliance_terminal_delivery WHERE appliance_id=$1`, app); n != 0 {
		t.Fatal("a retirement that did not start must leave no terminal delivery record")
	}

	if _, err := f.pool.Exec(f.ctx, `UPDATE assignment_signing_keys SET state='active'`); err != nil {
		t.Fatal(err)
	}
	if rec = f.operatorCall(f.base.retire, http.MethodPost, app, map[string]any{"reason": "end of contract"}); rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM licenses WHERE $1::uuid = ANY(appliance_ids) AND status='revoked'`, app); n != 1 {
		t.Fatal("the licence must be revoked")
	}
	if n := f.count(`SELECT count(*) FROM appliance_terminal_delivery WHERE appliance_id=$1 AND delivery_state='terminal_delivery_pending'`, app); n != 1 {
		t.Fatal("the terminal delivery must be pending")
	}
	if n := f.count(`SELECT count(*) FROM appliance_signed_assignments WHERE appliance_id=$1 AND state='decommissioned'`, app); n != 1 {
		t.Fatal("a signed decommissioned assignment must be current")
	}
	if rec = f.operatorCall(f.base.retire, http.MethodPost, app, map[string]any{"reason": "again"}); rec.Code != http.StatusConflict {
		t.Fatalf("a second normal retirement must be refused, got %d", rec.Code)
	}
}

func TestUnregisteredAssignmentKeyDoesNotSign(t *testing.T) {
	f := newFixture(t)
	cust, site := f.customerSite("Guard")
	app := decode(t, f.register(newBox("SC-TEST-0007", "hwf-7")))["appliance_id"].(string)
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM assignment_signing_keys`); err != nil {
		t.Fatal(err)
	}
	rec := f.operatorCall(f.base.activate, http.MethodPost, app, map[string]any{
		"customer_id": cust, "site_id": site, "reason": "x", "activation_code": f.codeFor(app),
		"license": map[string]any{"max_concurrent_online_guests": 5}})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not registered") {
		t.Fatalf("an unregistered signing key must not sign: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND lifecycle_state='pending_approval'`, app); n != 1 {
		t.Fatal("nothing may be activated when the assignment cannot be signed")
	}
}

func TestSupersededCertificatesAndAckRules(t *testing.T) {
	f := newFixture(t)
	f.withCA()
	cust, site := f.customerSite("Certs")
	bx := newBox("SC-TEST-0008", "hwf-8")
	app := decode(t, f.register(bx))["appliance_id"].(string)
	f.activate(app, cust, site, 30)
	first := f.issueCert(app)
	second := f.issueCert(app) // rotation

	if c := f.mtlsRequest(app, second); c != http.StatusNoContent {
		t.Fatalf("the current certificate: %d", c)
	}
	if c := f.mtlsRequest(app, first); c != http.StatusNoContent {
		t.Fatalf("a just-rotated certificate is still accepted during the overlap: %d", c)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE appliance_certificates SET superseded_at = now() - interval '2 days' WHERE status='superseded'`); err != nil {
		t.Fatal(err)
	}
	if c := f.mtlsRequest(app, first); c != http.StatusForbidden {
		t.Fatalf("a certificate superseded two days ago must be refused, got %d", c)
	}
	if n, err := RevokeSupersededCertificates(f.ctx, f.base); err != nil || n != 1 {
		t.Fatalf("housekeeping must revoke the one superseded certificate: %d %v", n, err)
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1 AND status='revoked'`, app); n != 1 {
		t.Fatal("the superseded certificate must now be revoked")
	}

	var ver int64
	_ = f.pool.QueryRow(f.ctx, `SELECT version FROM appliance_signed_assignments WHERE appliance_id=$1`, app).Scan(&ver)
	rec := f.ack(app, bx, second, assignment.Ack{ApplianceID: app, Version: ver, TerminalState: "decommissioned", AdoptedAt: time.Now().Unix()})
	if rec.Code != http.StatusConflict {
		t.Fatalf("an ack without a retirement must be refused, got %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND lifecycle_state='assigned'`, app); n != 1 {
		t.Fatal("a refused ack must not retire the appliance")
	}

	if rec = f.operatorCall(f.base.retire, http.MethodPost, app, map[string]any{"reason": "returned"}); rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	var signed []byte
	_ = f.pool.QueryRow(f.ctx, `SELECT version, signed_doc FROM appliance_signed_assignments WHERE appliance_id=$1`, app).Scan(&ver, &signed)
	var doc assignment.Document
	_ = json.Unmarshal(signed, &doc)
	rec = f.ack(app, bx, second, assignment.Ack{ApplianceID: app, Version: ver, TerminalState: doc.State,
		Fingerprint: assignment.DocFingerprint(&doc), AdoptedAt: time.Now().Unix()})
	if rec.Code != http.StatusOK {
		t.Fatalf("ack: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances WHERE id=$1 AND lifecycle_state='decommissioned'`, app); n != 1 {
		t.Fatal("the acknowledged appliance must be decommissioned")
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1 AND status <> 'revoked'`, app); n != 0 {
		t.Fatal("every certificate of the retired appliance must be revoked")
	}
}
