package assignment_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment/assignmenttest"
)

func TestResolveLocal(t *testing.T) {
	t.Run("verified and granting", func(t *testing.T) {
		f := assignmenttest.New(t)
		f.WriteAssignment(t, f.Doc(assignment.StateAssigned, 1))
		r, appl := assignment.ResolveLocal(f.Paths, f.IdentityDir, time.Now(), nil)
		if r.Outcome != assignment.OutcomeGranted || r.TenantID != assignmenttest.TenantID || appl != f.ApplianceID {
			t.Fatalf("want granted, got %+v appl=%q", r, appl)
		}
	})
	t.Run("rogue signer is unverifiable, never a scope", func(t *testing.T) {
		f := assignmenttest.New(t)
		f.WriteAssignment(t, f.Rogue(1))
		r, _ := assignment.ResolveLocal(f.Paths, f.IdentityDir, time.Now(), nil)
		if r.Outcome != assignment.OutcomeUnverifiable || r.TenantID != "" {
			t.Fatalf("want unverifiable with no tenant, got %+v", r)
		}
		if assignment.EnvFallbackAllowed(r) {
			t.Fatal("a present assignment must never permit an env fallback")
		}
	})
	t.Run("bound to another appliance", func(t *testing.T) {
		f := assignmenttest.New(t)
		d := f.Doc(assignment.StateAssigned, 1)
		d.ApplianceID = "someone-else"
		assignment.Sign(f.SignerPriv, d)
		f.WriteAssignment(t, d)
		if r, _ := assignment.ResolveLocal(f.Paths, f.IdentityDir, time.Now(), nil); r.Assigned() {
			t.Fatalf("an assignment bound elsewhere must not grant: %+v", r)
		}
	})
	t.Run("revoked verifies and grants nothing", func(t *testing.T) {
		f := assignmenttest.New(t)
		f.WriteAssignment(t, f.Doc(assignment.StateRevoked, 2))
		r, _ := assignment.ResolveLocal(f.Paths, f.IdentityDir, time.Now(), nil)
		if r.Outcome != assignment.OutcomeNotGranting || r.State != assignment.StateRevoked {
			t.Fatalf("want not_granting/revoked, got %+v", r)
		}
	})
	t.Run("assignment without a registered identity is refused", func(t *testing.T) {
		f := assignmenttest.New(t)
		f.WriteAssignment(t, f.Doc(assignment.StateAssigned, 1))
		if err := os.RemoveAll(f.IdentityDir); err != nil {
			t.Fatal(err)
		}
		r, _ := assignment.ResolveLocal(f.Paths, f.IdentityDir, time.Now(), nil)
		if r.Outcome != assignment.OutcomeUnverifiable {
			t.Fatalf("want unverifiable, got %v", r.Outcome)
		}
	})
	t.Run("factory clean is absent", func(t *testing.T) {
		dir := t.TempDir()
		p := assignment.Paths{Dir: filepath.Join(dir, "a"), TrustPath: filepath.Join(dir, "t.json"),
			RegistryPath: filepath.Join(dir, "r.json"), RegistryRootPath: filepath.Join(dir, "root.pub")}
		r, appl := assignment.ResolveLocal(p, filepath.Join(dir, "identity"), time.Now(), nil)
		if r.Outcome != assignment.OutcomeAbsent || appl != "" {
			t.Fatalf("want absent, got %+v", r)
		}
		// The default build is the production licensing profile: even "absent" never permits env scope.
		if assignment.EnvFallbackAllowed(r) {
			t.Fatal("the default (production) build must never use an env tenant/site")
		}
	})
}
