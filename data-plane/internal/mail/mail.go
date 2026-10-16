// Package mail abstracts outbound email so OTP / notification senders can
// swap between a stub (dev), SMTP, and provider-specific clients (SES,
// SendGrid) without touching call sites.
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

// Stub writes outgoing mail to a file (default /var/log/stayconnect/otp-mail.log)
// AND to slog. Useful for dev so OTP codes can be tailed during testing.
//
// IN A PRODUCTION BUILD THE STUB WRITES NO CONTENT (audit B-13). The stub is also the fallback when a site has
// no email provider configured, so on a real appliance it used to write every verification and password-reset
// LINK, every code and every full address into a log file -- a reset link in a log is the account. A
// production build (buildprofile.Production) records only that a message was not delivered, to a masked
// recipient; the subject and body (which carry the link or code) are never written anywhere. A development
// build (-tags devlicense) keeps the full record, which is what the stub is for.
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

// MaskAddress keeps only the domain of an email address ("***@example.com"), enough for an operator to see
// which provider a message was meant for without recording who it was for.
func MaskAddress(to string) string {
	if at := strings.LastIndexByte(to, '@'); at >= 0 && at < len(to)-1 {
		return "***@" + to[at+1:]
	}
	return "***"
}
