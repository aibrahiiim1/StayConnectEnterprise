// Package mail abstracts outbound email so OTP / notification senders can
// swap between a stub (dev), SMTP, and provider-specific clients (SES,
// SendGrid) without touching call sites.
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

// Stub writes outgoing mail to a file (default /var/log/stayconnect/otp-mail.log)
// AND to slog. Useful for dev so OTP codes can be tailed during testing.
type Stub struct {
	Path string
	mu   sync.Mutex
}

func NewStub(path string) *Stub {
	if path == "" {
		path = "/var/log/stayconnect/otp-mail.log"
	}
	return &Stub{Path: path}
}

func (s *Stub) Send(ctx context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		// Log-only — don't block auth flow on a missing log path in dev.
		slog.Warn("mail stub: open log failed", "path", s.Path, "err", err)
	} else {
		defer f.Close()
		_, _ = fmt.Fprintf(f, "[%s] To=%s Subject=%q\n%s\n---\n",
			time.Now().UTC().Format(time.RFC3339), m.To, m.Subject, m.Text)
	}
	slog.Info("mail.stub", "to", m.To, "subject", m.Subject)
	return nil
}
