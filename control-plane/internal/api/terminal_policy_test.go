package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// These tests pin the retirement policy in the code itself: an appliance's credentials are revoked only after
// it acknowledged its terminal assignment, or on an explicit emergency — never by a path that is merely
// retiring it. Revoking first makes the retirement undeliverable: the assignment channel (strictMTLSSelf)
// refuses a revoked certificate, so the box never learns it was retired and keeps serving.

// callersOf maps each top-level function in the package to the calls it makes to target.
func callersOf(t *testing.T, target string) map[string][]*ast.CallExpr {
	t.Helper()
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	out := map[string][]*ast.CallExpr{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == target {
						out[fn.Name.Name] = append(out[fn.Name.Name], c)
					}
				}
				return true
			})
		}
	}
	return out
}

func TestCredentialsAreRevokedOnlyAfterAckOrOnEmergency(t *testing.T) {
	var got []string
	for fn := range callersOf(t, "phase2ShutCredentials") {
		got = append(got, fn)
	}
	sort.Strings(got)
	want := []string{"AckHandler", "beginTerminalDelivery"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phase2ShutCredentials is called from %v; only %v may revoke retirement credentials", got, want)
	}
}

func TestEmergencyIsTheOnlyImmediateRevocationInBeginTerminalDelivery(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "terminal_delivery.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "beginTerminalDelivery" {
			continue
		}
		// Every phase2ShutCredentials call must sit inside `if emergency { ... }`.
		var guarded, total int
		var walk func(n ast.Node, inEmergency bool)
		walk = func(n ast.Node, inEmergency bool) {
			ast.Inspect(n, func(m ast.Node) bool {
				if ifs, ok := m.(*ast.IfStmt); ok {
					if id, ok := ifs.Cond.(*ast.Ident); ok && id.Name == "emergency" {
						walk(ifs.Body, true)
						if ifs.Else != nil {
							walk(ifs.Else, inEmergency)
						}
						return false
					}
				}
				if c, ok := m.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "phase2ShutCredentials" {
						total++
						if inEmergency {
							guarded++
						}
					}
				}
				return true
			})
		}
		walk(fn.Body, false)
		if total == 0 || guarded != total {
			t.Fatalf("beginTerminalDelivery revokes credentials outside `if emergency` (%d of %d guarded)", guarded, total)
		}
		return
	}
	t.Fatal("beginTerminalDelivery not found")
}

// Replacement completion retires the old appliance through the normal, NON-emergency terminal delivery and
// never revokes its certificate itself.
func TestReplacementUsesAcknowledgedRetirement(t *testing.T) {
	for _, forbidden := range []string{"phase2ShutCredentials", "revokeActiveCertificates", "issueAssignment"} {
		if calls := callersOf(t, forbidden)["completeReplacementIfPending"]; len(calls) > 0 {
			t.Errorf("completeReplacementIfPending calls %s", forbidden)
		}
	}
	calls := callersOf(t, "beginTerminalDelivery")["completeReplacementIfPending"]
	if len(calls) != 1 {
		t.Fatalf("completeReplacementIfPending must start exactly one terminal delivery, found %d", len(calls))
	}
	args := calls[0].Args
	if last, ok := args[len(args)-1].(*ast.Ident); !ok || last.Name != "false" {
		t.Fatal("the replacement's terminal delivery must be the normal (emergency=false) two-phase flow")
	}
	if sel, ok := args[3].(*ast.SelectorExpr); !ok || sel.Sel.Name != "StateDecommissioned" {
		t.Fatal("a replaced appliance is decommissioned (normal retirement), not revoked")
	}
}
