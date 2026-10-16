package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

// Audit B-13: the stub is the fallback for a site with no WhatsApp provider, so on a production appliance it
// must not write the code anywhere.

func TestStubRedactsExactlyInAProductionBuild(t *testing.T) {
	if NewStub(filepath.Join(t.TempDir(), "wa.log")).redact != buildprofile.Production {
		t.Fatalf("stub redaction does not follow the build profile (%s)", buildprofile.Name)
	}
}

func TestStubInAProductionBuildWithholdsTheCode(t *testing.T) {
	logs := captureLogs(t)
	p := filepath.Join(t.TempDir(), "wa.log")
	st := NewStub(p)
	st.redact = true
	if err := st.Send(context.Background(), Message{To: "+15551234567", Code: testCode, TTLMinutes: 10}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), testCode) || strings.Contains(string(b), "+15551234567") {
		t.Fatalf("production stub file holds the code or the full number: %q", b)
	}
	if !strings.Contains(string(b), "NOT DELIVERED") || !strings.Contains(string(b), "4567") {
		t.Fatalf("production stub file does not record the undelivered message: %q", b)
	}
	assertNoLeak(t, logs.String())
}
