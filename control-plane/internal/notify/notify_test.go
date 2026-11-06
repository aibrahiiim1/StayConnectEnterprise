package notify

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func server() Server {
	return Server{Host: "smtp.example.test", Port: 587, Security: SecurityStartTLS, FromAddress: "central@example.test", FromName: "OneGate Central"}
}

func TestComposeKeepsHeadersOnOneLine(t *testing.T) {
	s := server()
	s.FromName = "Central\r\nBcc: victim@example.test"
	raw, err := Compose(s, Message{To: "ops@example.test", Subject: "Hi\r\nBcc: x@example.test", Text: "line one\nline two"}, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	head, body, ok := strings.Cut(string(raw), "\r\n\r\n")
	if !ok {
		t.Fatal("no header/body separator")
	}
	for _, line := range strings.Split(head, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") {
			t.Fatalf("an injected header survived: %q", line)
		}
	}
	if !strings.Contains(head, "Auto-Submitted: auto-generated") || !strings.Contains(head, "To: <ops@example.test>") {
		t.Fatalf("missing headers:\n%s", head)
	}
	if !strings.Contains(body, "line one\r\nline two") {
		t.Fatalf("body not normalised to CRLF: %q", body)
	}
}

func TestComposeRefusesBadRecipient(t *testing.T) {
	for _, to := range []string{"", "no-at-sign", "a@example.test, b@example.test", "x@example.test\r\nBcc: y@example.test", "Name <a@example.test>"} {
		if _, err := Compose(server(), Message{To: to, Subject: "s", Text: "t"}, time.Now()); err == nil {
			t.Errorf("recipient %q was accepted", to)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := server().Validate(); err != nil {
		t.Fatalf("a good server was refused: %v", err)
	}
	for name, mut := range map[string]func(*Server){
		"no host":       func(s *Server) { s.Host = "" },
		"host with @":   func(s *Server) { s.Host = "user@smtp.example.test" },
		"bad port":      func(s *Server) { s.Port = 0 },
		"no encryption": func(s *Server) { s.Security = "none" },
		"bad from":      func(s *Server) { s.FromAddress = "central" },
		"two-line name": func(s *Server) { s.FromName = "a\nb" },
	} {
		s := server()
		mut(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSecretsRoundTripAndPermissions(t *testing.T) {
	s := Secrets{Dir: filepath.Join(t.TempDir(), "notify")}
	if s.HasPassword() {
		t.Fatal("a password exists before one was written")
	}
	if pw, err := s.ReadPassword(); err != nil || pw != "" {
		t.Fatalf("missing file: %q %v", pw, err)
	}
	if err := s.WritePassword("s3cret pass"); err != nil {
		t.Fatal(err)
	}
	if pw, _ := s.ReadPassword(); pw != "s3cret pass" {
		t.Fatalf("read back %q", pw)
	}
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(s.Dir, passwordFile))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("password file mode %v", fi.Mode().Perm())
		}
	}
	if err := s.WritePassword("two\nlines"); err == nil {
		t.Fatal("a two-line password was accepted")
	}
	if err := s.WritePassword(""); err != nil || s.HasPassword() {
		t.Fatalf("clearing failed: %v", err)
	}
}

func TestShortError(t *testing.T) {
	long := strings.Repeat("x", 400)
	if got := ShortError(os.ErrInvalid); got != "invalid argument" {
		t.Fatalf("got %q", got)
	}
	if got := ShortError(&os.PathError{Op: "open", Path: long, Err: os.ErrNotExist}); len([]rune(got)) != 303 {
		t.Fatalf("not shortened: %d", len([]rune(got)))
	}
}
