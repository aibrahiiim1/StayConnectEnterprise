package mail

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

// Audit B-13: the stub mailer is the fallback for a site with no email provider. On a production appliance it
// wrote every verification/reset link and full address to a log file.

const (
	stubTo   = "guest.example@example.org"
	stubLink = "https://portal.example.net/account/reset?t=Zx81QmT0kLr2"
)

func captureStubLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func sendStub(t *testing.T, redact bool) (file, logs string) {
	t.Helper()
	l := captureStubLogs(t)
	p := filepath.Join(t.TempDir(), "mail.log")
	st := NewStub(p)
	st.redact = redact
	if err := st.Send(context.Background(), Message{To: stubTo, Subject: "Reset your password", Text: "Open " + stubLink}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	return string(b), l.String()
}

func TestStubRedactsExactlyInAProductionBuild(t *testing.T) {
	if NewStub(filepath.Join(t.TempDir(), "m.log")).redact != buildprofile.Production {
		t.Fatalf("stub redaction does not follow the build profile (%s)", buildprofile.Name)
	}
}

func TestStubInAProductionBuildWritesNoLinkAndNoAddress(t *testing.T) {
	file, logs := sendStub(t, true)
	for _, leak := range []string{stubLink, "Zx81QmT0kLr2", stubTo, "guest.example", "Reset your password"} {
		if strings.Contains(file, leak) || strings.Contains(logs, leak) {
			t.Fatalf("production stub leaked %q\nfile: %s\nlogs: %s", leak, file, logs)
		}
	}
	if !strings.Contains(file, "NOT DELIVERED") || !strings.Contains(file, "***@example.org") {
		t.Fatalf("production stub file does not record the undelivered message: %q", file)
	}
}

func TestStubInADevelopmentBuildKeepsTheFullRecord(t *testing.T) {
	file, _ := sendStub(t, false)
	if !strings.Contains(file, stubLink) || !strings.Contains(file, stubTo) {
		t.Fatalf("development stub file lost the message: %q", file)
	}
}

func TestMaskAddress(t *testing.T) {
	for in, want := range map[string]string{"a@b.example": "***@b.example", "nope": "***", "x@": "***", "": "***"} {
		if got := MaskAddress(in); got != want {
			t.Fatalf("MaskAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
