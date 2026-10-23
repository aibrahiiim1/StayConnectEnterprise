// Package mail abstracts outbound email so senders can swap between a stub, SMTP and provider clients.
package mail

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
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

// Stub writes outgoing mail to a file and slog for development. In a production build it writes no content (it is
// also the no-provider fallback, and a reset link or code in a log is the account): only that a message was not
// delivered, to a masked recipient.
type Stub struct {
	Path string
	mu   sync.Mutex
	// redact is buildprofile.Production unless a test overrides it.
	redact bool
}

func NewStub(path string) *Stub {
	if path == "" {
		path = "/var/log/stayconnect/otp-mail.log"
	}
	return &Stub{Path: path, redact: buildprofile.Production}
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
		if s.redact {
			_, _ = fmt.Fprintf(f, "[%s] To=%s NOT DELIVERED: no email provider is configured; content withheld (production build)\n---\n",
				time.Now().UTC().Format(time.RFC3339), MaskAddress(m.To))
		} else {
			_, _ = fmt.Fprintf(f, "[%s] To=%s Subject=%q\n%s\n---\n",
				time.Now().UTC().Format(time.RFC3339), m.To, m.Subject, m.Text)
		}
	}
	if s.redact {
		slog.Warn("mail.stub: message not delivered, no email provider is configured", "to", MaskAddress(m.To))
		return nil
	}
	slog.Info("mail.stub", "to", m.To, "subject", m.Subject)
	return nil
}

// MaskAddress keeps only the domain ("***@example.com"): which provider, not who.
func MaskAddress(to string) string {
	if at := strings.LastIndexByte(to, '@'); at >= 0 && at < len(to)-1 {
		return "***@" + to[at+1:]
	}
	return "***"
}
