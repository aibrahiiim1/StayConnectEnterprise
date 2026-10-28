package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
)

func newCSRPEM(t *testing.T) string {
	t.Helper()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "appliance"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func (f *fixture) submitFreshCSR(appID string) *httptest.ResponseRecorder {
	f.t.Helper()
	raw, _ := json.Marshal(map[string]string{"csr_pem": newCSRPEM(f.t)})
	req := httptest.NewRequest(http.MethodPost, "/v1/appliance/csr", bytes.NewReader(raw))
	req = req.WithContext(auth.ContextWithAppliance(req.Context(), &auth.ApplianceIdent{ApplianceID: appID}))
	rec := httptest.NewRecorder()
	(&CertBase{Base: f.base, CA: f.base.CA, ClientValid: 90 * 24 * time.Hour}).SubmitCSR(rec, req)
	return rec
}

// Audit Phase 1 M2: an operator's reissue that signs a waiting CSR revokes the old certificate at once; it does
// not keep the 24 h rotation overlap.
func TestReissueWithWaitingCSRRevokesOldCertificate(t *testing.T) {
	f := newFixture(t)
	f.withCA()
	cust, site := f.customerSite("Reissue")
	app := decode(t, f.register(newBox("SC-TEST-M2-1", "hwf-m2")))["appliance_id"].(string)
	f.activate(app, cust, site, 10)
	old := f.issueCert(app)
	if code := f.mtlsRequest(app, old); code != http.StatusNoContent {
		t.Fatalf("the current certificate must work before the reissue: %d", code)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliance_certificate_requests (appliance_id, csr_pem, status) VALUES ($1,$2,'pending')`,
		app, newCSRPEM(t)); err != nil {
		t.Fatal(err)
	}
	rec := f.operatorCall(f.base.reissueCertificate, http.MethodPost, app, map[string]any{"reason": "key leaked"})
	if rec.Code != http.StatusOK {
		t.Fatalf("reissue: %d %s", rec.Code, rec.Body.String())
	}
	if code := f.mtlsRequest(app, old); code != http.StatusForbidden {
		t.Fatalf("the old certificate must be refused right after an operator reissue, got %d", code)
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1 AND status='active'`, app); n != 1 {
		t.Fatalf("exactly one active certificate after reissue, got %d", n)
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificate_revocations WHERE appliance_id=$1 AND fingerprint_sha256=$2`,
		app, pki.FingerprintHex(old)); n != 1 {
		t.Fatal("the old certificate must be recorded as revoked")
	}
}

// Audit Phase 1 L8: one pending CSR per appliance; a retiring appliance is not rotated automatically.
func TestPendingCSRCapAndNoRotationWhileRetiring(t *testing.T) {
	f := newFixture(t)
	f.withCA()
	cust, site := f.customerSite("Pending")
	app := decode(t, f.register(newBox("SC-TEST-L8-1", "hwf-l8")))["appliance_id"].(string)
	for i := 0; i < 5; i++ {
		if rec := f.submitFreshCSR(app); rec.Code != http.StatusAccepted {
			t.Fatalf("waiting appliance CSR: %d %s", rec.Code, rec.Body.String())
		}
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='pending'`, app); n != 1 {
		t.Fatalf("repeated submissions must leave one pending request, got %d", n)
	}

	f.activate(app, cust, site, 10)
	f.issueCert(app)
	if rec := f.operatorCall(f.base.retire, http.MethodPost, app, map[string]any{"reason": "end"}); rec.Code != http.StatusOK {
		t.Fatalf("retire: %d %s", rec.Code, rec.Body.String())
	}
	before := f.count(`SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1`, app)
	if rec := f.submitFreshCSR(app); rec.Code != http.StatusAccepted {
		t.Fatalf("a retiring appliance's CSR must not be auto-signed: %d %s", rec.Code, rec.Body.String())
	}
	if after := f.count(`SELECT count(*) FROM appliance_certificates WHERE appliance_id=$1`, app); after != before {
		t.Fatal("a retiring appliance got a new certificate")
	}
}
