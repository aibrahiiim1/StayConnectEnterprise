package main

// THE TENANT AND SITE A PACKAGE READ IS SCOPED BY MUST COME FROM THE APPLIANCE, NEVER FROM THE REQUEST.
//
// iam_v2.p2_package_current_conditions is a SECURITY DEFINER reader: it runs with the rights that own the
// package-conditions tables, and its only protection against reading another property's packages is the
// tenant and site it is handed. If any of that could be influenced by a query parameter, a header or a JSON
// body, the definer would become a way to read across sites — which is precisely the failure a definer
// function has to be designed against.
//
// edged pins both ids at startup from its own assignment (s.tenantID / s.siteID). This asserts the handler
// uses those and nothing else, by reading the handler's own source: the property is "no request-derived
// value reaches the scope arguments", and that is a statement about the code path rather than about any one
// request, so a single crafted request could not prove it and its absence could not disprove it.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// scopeHandlers are the commerce read handlers whose scope arguments must be appliance-pinned.
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

		// The pinned identity must be what the handler passes.
		for _, pinned := range []string{"s.tenantID", "s.siteID"} {
			if !strings.Contains(src, pinned) {
				t.Fatalf("%s does not use %s; a commerce read that does not scope by the appliance's own "+
					"identity can be aimed at another property", name, pinned)
			}
		}
		// ...and nothing request-derived may appear near the scope. These are the ways a value could arrive
		// from outside: parsed form values, query parameters, headers and decoded bodies.
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

// The package id IS taken from the URL, and that is correct and safe: it names WHICH package, never whose.
// The reader refuses any package that is not in the pinned tenant and site, so an id from another property
// returns no rows rather than another property's conditions.
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
