package main

// Tenant and site scope must come from the appliance (s.tenantID / s.siteID), never the request: the
// SECURITY DEFINER reader iam_v2.p2_package_current_conditions is protected only by the scope it is handed, so a
// request-influenced value would allow cross-site reads. This inspects the handlers' source because the
// property is about the code path, which no single crafted request could prove.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

var scopeHandlers = []string{
	"getCommercialPackageCurrent",
	"listCommercialPackages",
	"listServicePlans",
}

func TestCommerceReadsScopeFromTheAppliance(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "resources_commerce.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse resources_commerce.go: %v", err)
	}

	found := map[string]bool{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		want := false
		for _, h := range scopeHandlers {
			if h == name {
				want = true
			}
		}
		if !want {
			continue
		}
		found[name] = true

		var body strings.Builder
		ast.Inspect(fn, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					body.WriteString(id.Name + "." + sel.Sel.Name + " ")
				}
			}
			return true
		})
		src := body.String()

		for _, pinned := range []string{"s.tenantID", "s.siteID"} {
			if !strings.Contains(src, pinned) {
				t.Fatalf("%s does not use %s; a commerce read that does not scope by the appliance's own "+
					"identity can be aimed at another property", name, pinned)
			}
		}
		// Nothing request-derived (form values, query, headers) may reach the scope.
		for _, hostile := range []string{"r.URL", "r.Form", "r.PostForm", "r.Header", "r.Host"} {
			if strings.Contains(src, hostile) {
				t.Fatalf("%s reads %s; tenant and site scope must not be influenced by the request", name, hostile)
			}
		}
	}

	for _, h := range scopeHandlers {
		if !found[h] {
			t.Fatalf("handler %s was not found — this guard must be re-pointed rather than silently passing", h)
		}
	}
}

// The package id may come from the URL: it names which package, never whose; the reader refuses packages
// outside the pinned scope.
func TestPackageIdentityMayComeFromTheURLButScopeMayNot(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "resources_commerce.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var uses bool
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "getCommercialPackageCurrent" {
			return true
		}
		ast.Inspect(fn, func(m ast.Node) bool {
			if sel, ok := m.(*ast.SelectorExpr); ok && sel.Sel.Name == "URLParam" {
				uses = true
			}
			return true
		})
		return false
	})
	if !uses {
		t.Fatal("getCommercialPackageCurrent no longer takes the package id from the URL path")
	}
}
