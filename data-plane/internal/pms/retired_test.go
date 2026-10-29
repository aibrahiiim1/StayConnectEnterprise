package pms

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// Audit AD-39: the legacy in-process PMS provider layer is retired. pmsd owns every PMS connection, and a second FIAS client
// from scd would open a second link to a PMS that accepts only one. This keeps it retired: nothing named like a provider
// comes back into this package, and the loader and guard packages stay gone.
func TestTheLegacyProviderLayerStaysRetired(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	banned := map[string]bool{"Provider": true, "Registry": true, "NewRegistry": true, "ProtelFIAS": true, "Mews": true,
		"Apaleo": true, "Stub": true, "NewStub": true, "NewProtelFIAS": true, "NewMews": true, "NewApaleo": true}
	for _, p := range pkgs {
		for name, f := range p.Files {
			for id, obj := range f.Scope.Objects {
				if banned[id] && obj != nil {
					t.Errorf("%s declares %s: the legacy provider layer must not return (pmsd and internal/pmsprovider are the runtime)", name, id)
				}
			}
		}
	}
	for _, dir := range []string{"../pmsloader", "../pmsguard"} {
		if _, err := os.Stat(filepath.Clean(dir)); err == nil {
			t.Errorf("%s exists again; it was retired with the legacy provider layer", dir)
		}
	}
}
