package main

import (
	"os"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

// Asserts the production default; skipped under -tags devlicense, which intentionally permits permissive mode.
func TestProductionRequiresLicenseAndBlocksEnvBypass(t *testing.T) {
	if !buildprofile.Production {
		t.Skip("dev build: permissive allowed")
	}
	t.Setenv("SCD_LICENSE_REQUIRED", "false")

	required, attempt := resolveLicenseRequired(false)
	if !required {
		t.Fatal("production build must ALWAYS require a license, even with SCD_LICENSE_REQUIRED=false")
	}
	if attempt == "" {
		t.Fatal("production build must report the env bypass attempt")
	}
}

func TestProductionCleanNoAttempt(t *testing.T) {
	if !buildprofile.Production {
		t.Skip("dev build")
	}
	os.Unsetenv("SCD_LICENSE_REQUIRED")
	required, attempt := resolveLicenseRequired(false)
	if !required {
		t.Fatal("production build must require a license")
	}
	if attempt != "" {
		// Only fail if the box actually has a dev-mode marker (shouldn't in CI).
		if _, err := os.Stat(devModeMarker); err != nil {
			t.Fatalf("unexpected attempt on a clean production build: %q", attempt)
		}
	}
}
