// Package assignmenttest builds an on-disk appliance identity + trust registry + signed assignment for tests of
// the daemons that resolve their scope from them. Test-only: nothing in a binary imports it.
package assignmenttest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
)

// Fixture is one appliance's assignment chain on disk.
type Fixture struct {
	Root        string
	IdentityDir string
	Paths       assignment.Paths
	ApplianceID string
	Serial      string
	IdentityFpr string // hex, as the assignment carries it
	SignerPriv  ed25519.PrivateKey
	Identity    *identity.Identity
}

const (
	TenantID = "36f3ba78-f8d8-41b6-aa2e-50d797e2d9c8"
	SiteID   = "0d40f7c8-4a98-428a-a2f5-74f0cabf5d42"
)

// New writes a registered identity and a legacy plain trust registry holding one active assignment-signing key.
// No assignment is written; call WriteAssignment.
func New(t *testing.T) *Fixture {
	t.Helper()
	root := t.TempDir()
	f := &Fixture{Root: root, IdentityDir: filepath.Join(root, "identity"), ApplianceID: "appl-test-1", Serial: "SC-TEST-1"}
	f.Paths = assignment.Paths{
		Dir:              filepath.Join(root, "assignment"),
		RegistryPath:     filepath.Join(root, "assignment", "registry.json"),
		RegistryRootPath: filepath.Join(root, "registry-root.pub"),
		TrustPath:        filepath.Join(root, "assignment-trust.json"),
	}
	st := &identity.Store{Dir: f.IdentityDir}
	id, err := st.EnsureLocalKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AdoptApplianceID(f.ApplianceID); err != nil {
		t.Fatal(err)
	}
	// Pin the serial so the binding is deterministic regardless of the host running the test.
	id.ApplianceID, id.Serial = f.ApplianceID, f.Serial
	b, _ := json.Marshal(id)
	if err := os.WriteFile(filepath.Join(f.IdentityDir, "identity.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	f.Identity = id
	raw, _ := base64.RawStdEncoding.DecodeString(id.PublicKeyB64)
	f.IdentityFpr = applianceauth.KeyID(ed25519.PublicKey(raw))

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f.SignerPriv = priv
	reg := &assignment.Registry{Keys: []assignment.TrustedKey{{
		KeyID: assignment.KeyID(pub), PublicKey: base64.StdEncoding.EncodeToString(pub), State: assignment.KeyActive,
	}}}
	if err := reg.Save(f.Paths.TrustPath); err != nil {
		t.Fatal(err)
	}
	return f
}

// Doc returns a correctly bound document in the given state, signed by the trusted key.
func (f *Fixture) Doc(state string, version int64) *assignment.Document {
	d := &assignment.Document{
		AssignmentID: "asg-1", ApplianceID: f.ApplianceID, IdentityKeyFpr: f.IdentityFpr, Serial: f.Serial,
		Version: version, State: state, IssuedAt: time.Now().Unix(),
		TenantName: "Test Customer", SiteName: "Test Site",
	}
	if assignment.Grants(state) {
		d.TenantID, d.SiteID = TenantID, SiteID
	}
	assignment.Sign(f.SignerPriv, d)
	return d
}

// Rogue returns a document signed by a key the appliance does NOT trust.
func (f *Fixture) Rogue(version int64) *assignment.Document {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	d := f.Doc(assignment.StateAssigned, version)
	assignment.Sign(priv, d)
	return d
}

// WriteAssignment persists d as the appliance's assignment.json (as scd's agent would), with no verification.
func (f *Fixture) WriteAssignment(t *testing.T, d *assignment.Document) {
	t.Helper()
	if err := (&assignment.Store{Dir: f.Paths.Dir}).Adopt(d); err != nil {
		t.Fatal(err)
	}
}
