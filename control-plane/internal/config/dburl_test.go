package config

import (
	"strings"
	"testing"
)

// Audit Phase 10 X3: production refuses to start on the built-in development database address.
func TestProductionRefusesTheDefaultDatabaseURL(t *testing.T) {
	t.Setenv("CTRLAPI_DB_URL", "")
	t.Setenv("CTRLAPI_ADDR", "")
	t.Setenv("CTRLAPI_COOKIE_SECURE", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !c.DBURLDefaulted {
		t.Fatal("an unset CTRLAPI_DB_URL must be recorded as defaulted")
	}
	c.Env = "production"
	if errs, _ := c.Validate(); len(errs) != 1 || !strings.Contains(errs[0], "CTRLAPI_DB_URL") {
		t.Fatalf("production on the default database URL must refuse to start, got %v", errs)
	}
	c.Env = "dev"
	if errs, warns := c.Validate(); len(errs) != 0 || len(warns) != 1 {
		t.Fatalf("development only warns: %v %v", errs, warns)
	}
	t.Setenv("CTRLAPI_DB_URL", "postgres://ctrlapi:s3cret@127.0.0.1:5432/stayconnect")
	c, _ = Load()
	c.Env = "production"
	if errs, _ := c.Validate(); len(errs) != 0 {
		t.Fatalf("a configured database URL starts: %v", errs)
	}
}
