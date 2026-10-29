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
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/activation"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// A never-online appliance: the registry root key is pinned, NO registry is on disk and the legacy plain trust file is absent. The signed registry
// reaches it only inside the activation package.

type regRig struct {
	*offlineRig
	rootPriv ed25519.PrivateKey
	regPath  string
}

func newRegRig(t *testing.T) *regRig {
	t.Helper()
	restartAfterActivation = func() {}
	rig := newOfflineRig(t)
	rootPub, rootPriv, _ := ed25519.GenerateKey(rand.Reader)
	root := t.TempDir()
	rootPath := filepath.Join(root, "assignment-registry-root.pub")
	if err := os.WriteFile(rootPath, rootPub, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCD_ASSIGNMENT_REGISTRY_ROOT", rootPath)
	// Factory-clean: no legacy plain trust file either.
	if err := os.Remove(assignmentTrustPath()); err != nil {
		t.Fatal(err)
	}
	return &regRig{offlineRig: rig, rootPriv: rootPriv, regPath: assignmentRegistryPath()}
}

func (r *regRig) signedRegistry(version int64, signer ed25519.PrivateKey, keys ...ed25519.PublicKey) *assignment.SignedRegistry {
	sr := &assignment.SignedRegistry{RegistryVersion: version, IssuedAt: time.Now().Unix(), NotBefore: time.Now().Add(-5 * time.Minute).Unix()}
	for _, k := range keys {
		sr.Keys = append(sr.Keys, assignment.TrustedKey{KeyID: assignment.KeyID(k), PublicKey: base64.StdEncoding.EncodeToString(k),
			State: assignment.KeyActive, AddedAt: time.Now().UTC().Format(time.RFC3339)})
	}
	assignment.SignRegistry(signer, sr)
	return sr
}

// pkgWith builds a vendor-signed package whose assignment is signed by asgPriv and which carries registryRaw (nil = no registry).
func (r *regRig) pkgWith(t *testing.T, registryRaw []byte) []byte {
	t.Helper()
	var p activation.Package
	if err := json.Unmarshal(r.pkg(t, r.asgPriv), &p); err != nil {
		t.Fatal(err)
	}
	p.AssignmentRegistry = registryRaw
	activation.SignPackage(r.vendorPriv, &p)
	b, _ := json.Marshal(&p)
	return b
}

func (r *regRig) post(body []byte) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r.s.centralOfflinePackage(w, httptest.NewRequest(http.MethodPost, "/v1/central/offline-package", bytes.NewReader(body)))
	return w
}

func (r *regRig) assertNothingChanged(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected the package to be refused (403), got %d %s", w.Code, w.Body)
	}
	if _, err := os.Stat(filepath.Join(r.asgDir, "assignment.json")); !os.IsNotExist(err) {
		t.Fatal("nothing may be adopted from a refused package")
	}
	if _, err := os.Stat(r.s.activationJournalPath()); !os.IsNotExist(err) {
		t.Fatal("a refused package must not even be journalled")
	}
	if _, err := os.Stat(r.s.registryUndoPath()); !os.IsNotExist(err) {
		t.Fatal("a refused package must leave no registry undo record")
	}
	if id, _ := r.s.idStore.LoadBound(); id != nil {
		t.Fatal("a refused package must not bind an appliance id")
	}
}

func (r *regRig) asgPub() ed25519.PublicKey { return r.asgPriv.Public().(ed25519.PublicKey) }

func (r *regRig) heldVersion() int64 {
	return registryStoreOnDisk().CurrentVersion()
}

func TestFactoryCleanApplianceActivatesWithRegistryInPackage(t *testing.T) {
	rig := newRegRig(t)
	if _, err := os.Stat(rig.regPath); !os.IsNotExist(err) {
		t.Fatal("precondition: no registry on disk")
	}
	raw, _ := json.Marshal(rig.signedRegistry(1, rig.rootPriv, rig.asgPub()))
	w := rig.post(rig.pkgWith(t, raw))
	if w.Code != http.StatusOK {
		t.Fatalf("a genuine package carrying the registry must apply on a factory-clean appliance, got %d %s", w.Code, w.Body)
	}
	if rig.heldVersion() != 1 {
		t.Fatalf("the registry must be persisted, held version %d", rig.heldVersion())
	}
	if !registryStoreOnDisk().EverAdopted() {
		t.Fatal("adoption marker must be written like an online adoption")
	}
	id, _ := rig.s.idStore.LoadBound()
	if id == nil || id.ApplianceID != "appl-off-1" {
		t.Fatal("appliance id must be adopted")
	}
	// The boot-time resolution (registry anchored on the pinned root) accepts what was adopted.
	res := assignment.Resolve(assignmentPaths(), assignment.ApplianceBinding{ApplianceID: id.ApplianceID, Serial: "SC-OFF-1",
		PublicKeyB64: id.PublicKeyB64}, time.Now(), nil)
	if !res.Assigned() {
		t.Fatalf("the adopted assignment must verify at boot, got %v", res.Outcome)
	}
	for _, p := range []string{rig.s.activationJournalPath(), rig.s.registryUndoPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s must be gone after a completed activation", p)
		}
	}
	// Replay: the same file again is refused (the appliance is assigned now) and changes nothing.
	if w2 := rig.post(rig.pkgWith(t, raw)); w2.Code == http.StatusOK {
		t.Fatal("a package must not apply twice")
	}
}

func TestPackageWithoutRegistryAndNoneOnDiskStillRefused(t *testing.T) {
	rig := newRegRig(t)
	w := rig.post(rig.pkgWith(t, nil))
	rig.assertNothingChanged(t, w)
	if !strings.Contains(w.Body.String(), "no assignment trust registry") {
		t.Fatalf("expected the registry reason, got %s", w.Body)
	}
}

func TestForgedRegistryInPackageIsRefused(t *testing.T) {
	rig := newRegRig(t)
	_, otherRoot, _ := ed25519.GenerateKey(rand.Reader)
	raw, _ := json.Marshal(rig.signedRegistry(1, otherRoot, rig.asgPub()))
	w := rig.post(rig.pkgWith(t, raw))
	rig.assertNothingChanged(t, w)
	if _, err := os.Stat(rig.regPath); !os.IsNotExist(err) {
		t.Fatal("a forged registry must not be persisted")
	}
}

func TestRegistryEditedAfterRegistrySigningIsRefused(t *testing.T) {
	rig := newRegRig(t)
	_, rogue, _ := ed25519.GenerateKey(rand.Reader)
	sr := rig.signedRegistry(1, rig.rootPriv, rogue.Public().(ed25519.PublicKey))
	// The attacker swaps in their own key after the root signed: the registry signature no longer matches.
	sr.Keys = append(sr.Keys, assignment.TrustedKey{KeyID: assignment.KeyID(rig.asgPub()), PublicKey: base64.StdEncoding.EncodeToString(rig.asgPub()), State: assignment.KeyActive})
	raw, _ := json.Marshal(sr)
	w := rig.post(rig.pkgWith(t, raw))
	rig.assertNothingChanged(t, w)
	if !strings.Contains(w.Body.String(), "registry") {
		t.Fatalf("expected a registry refusal, got %s", w.Body)
	}
}

func TestRegistryEditedAfterPackageSigningIsRefusedBySignature(t *testing.T) {
	rig := newRegRig(t)
	raw, _ := json.Marshal(rig.signedRegistry(1, rig.rootPriv, rig.asgPub()))
	var p activation.Package
	if err := json.Unmarshal(rig.pkgWith(t, raw), &p); err != nil {
		t.Fatal(err)
	}
	p.AssignmentRegistry = json.RawMessage(strings.Replace(string(p.AssignmentRegistry), `"registry_version":1`, `"registry_version":2`, 1))
	b, _ := json.Marshal(&p)
	w := rig.post(b)
	rig.assertNothingChanged(t, w)
	if !strings.Contains(w.Body.String(), "signature invalid") {
		t.Fatalf("expected the package signature refusal, got %s", w.Body)
	}
}

func TestPackageRegistryNeedsAPinnedRootKey(t *testing.T) {
	rig := newRegRig(t)
	t.Setenv("SCD_ASSIGNMENT_REGISTRY_ROOT", filepath.Join(t.TempDir(), "absent.pub"))
	raw, _ := json.Marshal(rig.signedRegistry(1, rig.rootPriv, rig.asgPub()))
	rig.assertNothingChanged(t, rig.post(rig.pkgWith(t, raw)))
}

func (r *regRig) holdRegistry(t *testing.T, version int64) {
	t.Helper()
	if ok, reason := registryStoreOnDisk().Adopt(r.signedRegistry(version, r.rootPriv, r.asgPub()), time.Now()); !ok {
		t.Fatalf("could not hold registry v%d: %s", version, reason)
	}
}

func TestRegistryOlderThanHeldIsRefused(t *testing.T) {
	rig := newRegRig(t)
	rig.holdRegistry(t, 5)
	raw, _ := json.Marshal(rig.signedRegistry(3, rig.rootPriv, rig.asgPub()))
	w := rig.post(rig.pkgWith(t, raw))
	rig.assertNothingChanged(t, w)
	if rig.heldVersion() != 5 {
		t.Fatalf("the held registry must be untouched, got v%d", rig.heldVersion())
	}
}

func TestNewerRegistryReplacesHeldAndOldIsKeptAsPrevious(t *testing.T) {
	rig := newRegRig(t)
	rig.holdRegistry(t, 5)
	raw, _ := json.Marshal(rig.signedRegistry(7, rig.rootPriv, rig.asgPub()))
	if w := rig.post(rig.pkgWith(t, raw)); w.Code != http.StatusOK {
		t.Fatalf("a newer registry must apply, got %d %s", w.Code, w.Body)
	}
	if rig.heldVersion() != 7 {
		t.Fatalf("held version %d, want 7", rig.heldVersion())
	}
	if rec := registryStoreOnDisk().Load(); rec == nil || rec.Previous == nil || rec.Previous.RegistryVersion != 5 {
		t.Fatal("the outgoing registry must be kept as previous")
	}
}

func TestPackageWithoutRegistryUsesTheRegistryOnDisk(t *testing.T) {
	rig := newRegRig(t)
	rig.holdRegistry(t, 2)
	if w := rig.post(rig.pkgWith(t, nil)); w.Code != http.StatusOK {
		t.Fatalf("a registry-less package must still apply against the registry on disk, got %d %s", w.Code, w.Body)
	}
	if rig.heldVersion() != 2 {
		t.Fatalf("the held registry must be unchanged, got v%d", rig.heldVersion())
	}
}

func TestFailedApplyRestoresRegistryExactly(t *testing.T) {
	for _, held := range []bool{false, true} {
		rig := newRegRig(t)
		var before []byte
		if held {
			rig.holdRegistry(t, 2)
			before, _ = os.ReadFile(rig.regPath)
		}
		// Make step 3 (trust bundle install) fail, after the registry and the assignment were written.
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("SCD_CERT_DIR", filepath.Join(blocker, "certs"))
		var p activation.Package
		raw, _ := json.Marshal(rig.signedRegistry(9, rig.rootPriv, rig.asgPub()))
		if err := json.Unmarshal(rig.pkgWith(t, raw), &p); err != nil {
			t.Fatal(err)
		}
		p.CABundlePEM = "CA"
		activation.SignPackage(rig.vendorPriv, &p)
		b, _ := json.Marshal(&p)
		if w := rig.post(b); w.Code != http.StatusBadRequest {
			t.Fatalf("held=%v: expected the activation to fail and roll back, got %d %s", held, w.Code, w.Body)
		}
		after, err := os.ReadFile(rig.regPath)
		if held {
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("the registry held before must be restored byte for byte")
			}
		} else if !os.IsNotExist(err) {
			t.Fatal("a rolled-back activation must leave no new registry behind")
		}
		if !held && registryStoreOnDisk().EverAdopted() {
			t.Fatal("a rolled-back activation must not leave the adoption marker")
		}
		if _, err := os.Stat(filepath.Join(rig.asgDir, "assignment.json")); err == nil && rig.s.assignmentStore().Doc() != nil && assignment.Grants(rig.s.assignmentStore().Doc().State) {
			t.Fatal("the assignment must be rolled back")
		}
		if _, err := os.Stat(rig.s.registryUndoPath()); !os.IsNotExist(err) {
			t.Fatal("the undo record must be removed once the registry is restored")
		}
	}
}

// A crash after the registry and the assignment were written but before the identity was adopted: the next boot rolls everything back.
func TestInterruptedActivationRecoveryRemovesTheRegistry(t *testing.T) {
	for _, held := range []bool{false, true} {
		rig := newRegRig(t)
		var before []byte
		if held {
			rig.holdRegistry(t, 2)
			before, _ = os.ReadFile(rig.regPath)
		}
		raw, _ := json.Marshal(rig.signedRegistry(4, rig.rootPriv, rig.asgPub()))
		var pkg activation.Package
		if err := json.Unmarshal(rig.pkgWith(t, raw), &pkg); err != nil {
			t.Fatal(err)
		}
		jb, _ := json.Marshal(&pkg)
		if err := writeFileSync(rig.s.activationJournalPath(), jb, 0o600); err != nil {
			t.Fatal(err)
		}
		vr, reason := vetPackageRegistry(&pkg, time.Now())
		if reason != "" {
			t.Fatal(reason)
		}
		if _, err := rig.s.adoptPackageRegistry(vr); err != nil {
			t.Fatal(err)
		}
		var doc assignment.Document
		_ = json.Unmarshal(pkg.Assignment, &doc)
		if err := rig.s.assignmentStore().Adopt(&doc); err != nil {
			t.Fatal(err)
		}
		if rig.heldVersion() != 4 {
			t.Fatal("precondition: the interrupted activation wrote the registry")
		}
		// "Reboot": the identity was never adopted (s.applID is empty), so the activation is incomplete.
		rig.s.recoverInterruptedActivation()
		after, err := os.ReadFile(rig.regPath)
		if held {
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("recovery must restore the previously held registry")
			}
		} else if !os.IsNotExist(err) || registryStoreOnDisk().EverAdopted() {
			t.Fatal("recovery must leave no registry or marker behind")
		}
		if d := rig.s.assignmentStore().Doc(); d != nil && assignment.Grants(d.State) {
			t.Fatal("recovery must unassign")
		}
		for _, p := range []string{rig.s.activationJournalPath(), rig.s.registryUndoPath()} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("%s must be gone after recovery", p)
			}
		}
	}
}
