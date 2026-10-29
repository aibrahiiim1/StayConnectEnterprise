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

	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// installation is one installation of an appliance: the identity key that signs its requests (box) and the separate mTLS key
// its CSR carries. A reinstall of the same hardware keeps the serial and hardware fingerprint and changes both keys.
type installation struct {
	box  box
	mtls ed25519.PublicKey
	csr  string
}

func newInstallation(serial, hwf string) installation {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "appliance"}}, priv)
	return installation{box: newBox(serial, hwf), mtls: pub,
		csr: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))}
}

func (f *fixture) certBase() *CertBase {
	return &CertBase{Base: f.base, CA: f.base.CA, ClientValid: f.base.ClientValid}
}

func (f *fixture) applianceCall(h http.HandlerFunc, method, appID string, body any) *httptest.ResponseRecorder {
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, "/v1/appliance/x", bytes.NewReader(raw))
	req = req.WithContext(auth.ContextWithAppliance(req.Context(), &auth.ApplianceIdent{ApplianceID: appID}))
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func (f *fixture) submitCSR(appID string, in installation) *httptest.ResponseRecorder {
	return f.applianceCall(f.certBase().SubmitCSR, http.MethodPost, appID, map[string]string{"csr_pem": in.csr})
}

func (f *fixture) certStatus(appID string) map[string]any {
	f.t.Helper()
	return decode(f.t, f.applianceCall(f.certBase().FetchCertificate, http.MethodGet, appID, nil))
}

// activeCertKey returns the public key of the appliance's active certificate, or nil.
func (f *fixture) activeCertKey(appID string) ed25519.PublicKey {
	f.t.Helper()
	var certPEM string
	if err := f.pool.QueryRow(f.ctx, `SELECT cert_pem FROM appliance_certificates WHERE appliance_id=$1 AND status='active'`, appID).
		Scan(&certPEM); err != nil {
		return nil
	}
	block, _ := pem.Decode([]byte(certPEM))
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.PublicKey.(ed25519.PublicKey)
}

// F1, end to end: an appliance installed, registered and filed its CSR; it was reinstalled before activation (same hardware, new
// identity and mTLS keys). The first installation's request is voided when the identity is replaced, the new installation is told
// "none" so it files its own, and Activate signs THAT one. Before the fix Activate signed the first request, and the appliance
// could never use the certificate.
func TestReinstallBeforeActivationNeverSignsTheEarlierRequest(t *testing.T) {
	f := newFixture(t)
	f.withCA()
	cust, site := f.customerSite("F1 Hotel")

	first := newInstallation("SC-TEST-F1", "hwf-f1")
	id := decode(t, f.register(first.box))["appliance_id"].(string)
	if rec := f.submitCSR(id, first); rec.Code != http.StatusAccepted {
		t.Fatalf("first CSR: %d %s", rec.Code, rec.Body.String())
	}
	if st := f.certStatus(id)["status"]; st != "pending" {
		t.Fatalf("a waiting appliance's own CSR reads pending, got %v", st)
	}

	second := newInstallation("SC-TEST-F1", "hwf-f1")
	// AD-1 supersedes "the reinstall adopts the waiting row": a waiting row's key is never replaced. The operator deletes
	// the waiting row and the reinstalled box registers as a NEW row.
	rec := f.register(second.box)
	if rec.Code != http.StatusForbidden || decode(t, rec)["error"] != "identity_replacement_refused" {
		t.Fatalf("the reinstall must not take over the waiting row: %d %s", rec.Code, rec.Body.String())
	}
	if rec = f.operatorCall(f.base.deleteAppliance, http.MethodDelete, id,
		map[string]any{"confirm_serial": "SC-TEST-F1", "reason": "reinstalled before activation"}); rec.Code >= 300 {
		t.Fatalf("delete the waiting row: %d %s", rec.Code, rec.Body.String())
	}
	if rec = f.register(second.box); rec.Code != http.StatusOK {
		t.Fatalf("the reinstalled box registers as new: %d %s", rec.Code, rec.Body.String())
	}
	id = decode(t, rec)["appliance_id"].(string)
	if st := f.certStatus(id)["status"]; st != "none" {
		t.Fatalf("the new installation must be told none so it files its own CSR, got %v", st)
	}

	if rec := f.submitCSR(id, second); rec.Code != http.StatusAccepted {
		t.Fatalf("second CSR: %d %s", rec.Code, rec.Body.String())
	}
	f.activate(id, cust, site, 10)
	key := f.activeCertKey(id)
	if key == nil {
		t.Fatal("Activate must issue the waiting installation's certificate")
	}
	if !key.Equal(second.mtls) {
		t.Fatal("Activate signed a request from the replaced installation (F1)")
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='signed'`, id); n != 1 {
		t.Fatalf("exactly the current installation's request is signed, got %d", n)
	}
	if st := f.certStatus(id)["status"]; st != "issued" {
		t.Fatalf("after Activate the certificate is issued, got %v", st)
	}
}

// A Central that ran the old code may already hold a request older than the current identity (never voided). It is not reported
// as pending, Activate and Reissue do not sign it, and the appliance's own CSR is signed on arrival.
func TestRequestOlderThanTheIdentityIsNeverSigned(t *testing.T) {
	f := newFixture(t)
	f.withCA()
	cust, site := f.customerSite("Legacy Hotel")

	current := newInstallation("SC-TEST-F1L", "hwf-f1l")
	id := decode(t, f.register(current.box))["appliance_id"].(string)
	stale := newInstallation("SC-TEST-F1L", "hwf-f1l")
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO appliance_certificate_requests (appliance_id, csr_pem, status, requested_at)
        SELECT $1, $2, 'pending', registered_at - interval '1 hour' FROM appliances WHERE id=$1`, id, stale.csr); err != nil {
		t.Fatal(err)
	}
	if st := f.certStatus(id)["status"]; st != "none" {
		t.Fatalf("a request older than the identity must not read as pending, got %v", st)
	}

	f.activate(id, cust, site, 10)
	if f.activeCertKey(id) != nil {
		t.Fatal("Activate signed a request older than the current identity")
	}
	rec := f.operatorCall(f.base.reissueCertificate, http.MethodPost, id, map[string]any{"reason": "test"})
	if rec.Code != http.StatusOK || f.activeCertKey(id) != nil {
		t.Fatalf("Reissue must not sign the stale request: %d %s", rec.Code, rec.Body.String())
	}

	if rec := f.submitCSR(id, current); rec.Code != http.StatusCreated {
		t.Fatalf("an activated appliance's own CSR is signed on arrival: %d %s", rec.Code, rec.Body.String())
	}
	if key := f.activeCertKey(id); key == nil || !key.Equal(current.mtls) {
		t.Fatal("the certificate must carry the current installation's key")
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='pending'`, id); n != 0 {
		t.Fatalf("no request may stay pending after issuance, got %d", n)
	}
	if n := f.count(`SELECT count(*) FROM appliance_certificate_requests WHERE appliance_id=$1 AND status='rejected' AND csr_pem=$2`, id, stale.csr); n != 1 {
		t.Fatalf("the stale request is closed as rejected, got %d", n)
	}
}
