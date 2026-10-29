package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/activation"
	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
)

// A package minted by activationPackage carries the registry Central serves to appliances, and that registry verifies against Central's registry
// root key; with no registry signed yet the field is left out and the package is exactly the pre-field shape.
func TestActivationPackageCarriesTheSignedRegistry(t *testing.T) {
	f := newFixture(t)
	f.withCA()
	vendorPath := f.t.TempDir() + "/vendor.key"
	vendorPub, vk, _ := ed25519.GenerateKey(rand.Reader)
	if err := os.WriteFile(vendorPath, vk, 0o600); err != nil {
		t.Fatal(err)
	}
	off := NewOfflineBase(f.base, vendorPath, "", "https://central.example.invalid")
	if off == nil {
		t.Fatal("offline base")
	}
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	reg := &RegistryBase{Base: f.base, RootKey: rootPriv}

	mint := func(serial string) activation.Package {
		t.Helper()
		req, _ := signedRequest(t, serial)
		f.header = map[string]string{"X-Activation-Code": codeOfPub(t, req.PublicKey)}
		if rec := f.operatorCall(off.importRequest, http.MethodPost, "", req); rec.Code != http.StatusOK {
			t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
		}
		f.header = nil
		var appID string
		if err := f.pool.QueryRow(f.ctx, `SELECT id::text FROM appliances WHERE serial=$1`, serial).Scan(&appID); err != nil {
			t.Fatal(err)
		}
		cust, site := f.customerSite("Reg-" + serial)
		f.activate(appID, cust, site, 5)
		rec := f.operatorCall(off.activationPackage, http.MethodPost, appID, map[string]any{})
		if rec.Code != http.StatusCreated {
			t.Fatalf("package: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Package activation.Package `json:"package"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if !activation.VerifyPackage(vendorPub, &out.Package) {
			t.Fatal("the minted package must verify under the vendor key, registry field included")
		}
		return out.Package
	}

	// No registry signed yet: the field is absent.
	if p := mint("SC-TEST-REG-A"); len(p.AssignmentRegistry) != 0 {
		t.Fatalf("with no signed registry the package must not carry one, got %s", p.AssignmentRegistry)
	}

	// Registry signed: the package carries the current one, verifying against the root, equal to what Serve would send.
	if _, err := reg.Rebuild(f.ctx, "test"); err != nil {
		t.Fatal(err)
	}
	p := mint("SC-TEST-REG-B")
	if len(p.AssignmentRegistry) == 0 {
		t.Fatal("the package must carry the current signed registry")
	}
	var sr assignment.SignedRegistry
	if err := json.Unmarshal(p.AssignmentRegistry, &sr); err != nil {
		t.Fatal(err)
	}
	if reason := assignment.VerifyRegistry(rootPub, &sr, time.Now()); reason != "" {
		t.Fatalf("the carried registry must verify against Central's registry root: %s", reason)
	}
	env, err := currentRegistryEnvelope(f.ctx, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	var served assignment.SignedRegistry
	if err := json.Unmarshal(env, &served); err != nil {
		t.Fatal(err)
	}
	if served.RegistryVersion != sr.RegistryVersion || served.Signature != sr.Signature {
		t.Fatal("the carried registry must be the one Central serves")
	}
	if len(sr.Keys) == 0 {
		t.Fatal("the registry must list the assignment signing key")
	}
}
