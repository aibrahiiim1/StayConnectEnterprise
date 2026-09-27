package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/activation"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

type offlineRig struct {
	s          *server
	vendorPriv ed25519.PrivateKey
	asgPriv    ed25519.PrivateKey
	ident      *identity.Identity
	req        activation.Request
	asgDir     string
}

func newOfflineRig(t *testing.T) *offlineRig {
	t.Helper()
	root := t.TempDir()
	idDir := filepath.Join(root, "identity")
	asgDir := filepath.Join(root, "assignment")
	t.Setenv("SCD_IDENTITY_DIR", idDir)
	t.Setenv("SCD_ASSIGNMENT_DIR", asgDir)
	t.Setenv("SCD_ASSIGNMENT_REGISTRY", filepath.Join(asgDir, "registry.json"))
	t.Setenv("SCD_ASSIGNMENT_REGISTRY_ROOT", filepath.Join(root, "no-root.pub"))
	t.Setenv("SCD_ASSIGNMENT_TRUST", filepath.Join(root, "assignment-trust.json"))
	t.Setenv("SCD_CERT_DIR", filepath.Join(root, "certs"))

	vpub, vpriv, _ := ed25519.GenerateKey(rand.Reader)
	vpath := filepath.Join(root, "vendor.pub")
	if err := os.WriteFile(vpath, vpub, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCD_VENDOR_PUB", vpath)

	apub, apriv, _ := ed25519.GenerateKey(rand.Reader)
	reg := &assignment.Registry{Keys: []assignment.TrustedKey{{KeyID: assignment.KeyID(apub),
		PublicKey: base64.StdEncoding.EncodeToString(apub), State: assignment.KeyActive}}}
	if err := reg.Save(filepath.Join(root, "assignment-trust.json")); err != nil {
		t.Fatal(err)
	}

	store := &identity.Store{Dir: idDir}
	id, err := store.EnsureLocalKeypair()
	if err != nil {
		t.Fatal(err)
	}
	s := &server{idStore: store, serial: "SC-OFF-1"}
	rig := &offlineRig{s: s, vendorPriv: vpriv, asgPriv: apriv, ident: id, asgDir: asgDir,
		req: activation.Request{SchemaVersion: activation.SchemaVersion, RequestID: "req-1", Serial: "SC-OFF-1",
			PublicKey: id.PublicKeyB64, CreatedAt: time.Now().Unix(), Nonce: "nonce-1"}}
	activation.SignRequest(id.PrivateKey(), &rig.req)
	b, _ := json.Marshal(rig.req)
	if err := os.WriteFile(s.activationRequestPath(), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return rig
}

func (r *offlineRig) pkg(t *testing.T, signAssignmentWith ed25519.PrivateKey) []byte {
	t.Helper()
	raw, _ := base64.RawStdEncoding.DecodeString(r.ident.PublicKeyB64)
	doc := &assignment.Document{AssignmentID: "asg-1", ApplianceID: "appl-off-1", Serial: "SC-OFF-1",
		IdentityKeyFpr: applianceauthKeyIDFromB64(r.ident.PublicKeyB64),
		TenantID:       "36f3ba78-f8d8-41b6-aa2e-50d797e2d9c8", SiteID: "0d40f7c8-4a98-428a-a2f5-74f0cabf5d42",
		Version: 1, State: assignment.StateAssigned, IssuedAt: time.Now().Unix()}
	assignment.Sign(signAssignmentWith, doc)
	asg, _ := json.Marshal(doc)
	p := &activation.Package{PackageID: "pkg-1", RequestID: r.req.RequestID, RequestNonce: r.req.Nonce,
		ApplianceID: "appl-off-1", Serial: "SC-OFF-1", IdentityKeyFpr: activation.KeyID(ed25519.PublicKey(raw)),
		TenantID: doc.TenantID, SiteID: doc.SiteID, Assignment: asg, LicenseEnvelope: json.RawMessage("null"),
		Entitlements: json.RawMessage("{}"), IssuedAt: time.Now().Unix(), ExpiresAt: time.Now().Add(time.Hour).Unix(),
		Nonce: "pkg-nonce-1"}
	activation.SignPackage(r.vendorPriv, p)
	b, _ := json.Marshal(p)
	return b
}

// F8: the signed assignment inside a first-activation package is verified exactly as boot verifies it, BEFORE
// anything is written. A package whose vendor signature is fine but whose assignment was signed by a key the
// appliance does not trust is refused, and the appliance is left exactly as it was.
func TestOfflineActivationVerifiesAssignmentBeforeAdopting(t *testing.T) {
	restartAfterActivation = func() {}
	rig := newOfflineRig(t)
	_, rogue, _ := ed25519.GenerateKey(rand.Reader)

	w := httptest.NewRecorder()
	rig.s.centralOfflinePackage(w, httptest.NewRequest(http.MethodPost, "/v1/central/offline-package",
		bytes.NewReader(rig.pkg(t, rogue))))
	if w.Code != http.StatusForbidden {
		t.Fatalf("a package carrying an unverifiable assignment must be refused, got %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(rig.asgDir, "assignment.json")); !os.IsNotExist(err) {
		t.Fatal("nothing may be adopted from a refused package")
	}
	if _, err := os.Stat(rig.s.activationJournalPath()); !os.IsNotExist(err) {
		t.Fatal("a refused package must not even be journalled")
	}
	if id, _ := rig.s.idStore.LoadBound(); id != nil {
		t.Fatal("a refused package must not bind an appliance id")
	}

	// The same package with a trusted assignment signer is applied.
	w = httptest.NewRecorder()
	rig.s.centralOfflinePackage(w, httptest.NewRequest(http.MethodPost, "/v1/central/offline-package",
		bytes.NewReader(rig.pkg(t, rig.asgPriv))))
	if w.Code != http.StatusOK {
		t.Fatalf("a verifiable package must apply, got %d %s", w.Code, w.Body)
	}
	id, _ := rig.s.idStore.LoadBound()
	if id == nil || id.ApplianceID != "appl-off-1" {
		t.Fatal("the appliance id must be adopted after a verified activation")
	}
	// And what was adopted survives the boot-time verification.
	r := assignment.Resolve(assignmentPaths(), assignment.ApplianceBinding{ApplianceID: id.ApplianceID,
		Serial: "SC-OFF-1", PublicKeyB64: id.PublicKeyB64}, time.Now(), nil)
	if !r.Assigned() {
		t.Fatalf("the adopted assignment must verify at boot, got %v", r.Outcome)
	}
}
