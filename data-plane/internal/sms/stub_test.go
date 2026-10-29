package sms

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

// Audit B-13: the stub SMS sender is the fallback for a site with no SMS provider. On a production appliance it
// wrote every code and full phone number to a log file.

const (
	stubTo   = "+15550001234"
	stubCode = "739104"
)

func sendStub(t *testing.T, redact bool) (file, logs string) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	p := filepath.Join(t.TempDir(), "sms.log")
	st := NewStub(p)
	st.redact = redact
	err := st.Send(context.Background(), Message{To: stubTo, Text: "Your code is " + stubCode})
	// AD-35: a production stub delivers nothing and says so; a development stub is a successful local sink.
	if redact && !errors.Is(err, ErrNotDelivered) {
		t.Fatalf("a production stub reported %v, want ErrNotDelivered", err)
	}
	if !redact && err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	return string(b), buf.String()
}

func TestStubRedactsExactlyInAProductionBuild(t *testing.T) {
	if NewStub(filepath.Join(t.TempDir(), "s.log")).redact != buildprofile.Production {
		t.Fatalf("stub redaction does not follow the build profile (%s)", buildprofile.Name)
	}
}

func TestStubInAProductionBuildWritesNoCodeAndNoNumber(t *testing.T) {
	file, logs := sendStub(t, true)
	for _, leak := range []string{stubCode, stubTo, "5550001"} {
		if strings.Contains(file, leak) || strings.Contains(logs, leak) {
			t.Fatalf("production stub leaked %q\nfile: %s\nlogs: %s", leak, file, logs)
		}
	}
	if !strings.Contains(file, "NOT DELIVERED") || !strings.Contains(file, "1234") {
		t.Fatalf("production stub file does not record the undelivered message: %q", file)
	}
}

func TestStubInADevelopmentBuildKeepsTheFullRecord(t *testing.T) {
	file, _ := sendStub(t, false)
	if !strings.Contains(file, stubCode) || !strings.Contains(file, stubTo) {
		t.Fatalf("development stub file lost the message: %q", file)
	}
}
