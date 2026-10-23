// Package sms abstracts outbound SMS so senders can swap providers without touching call sites.
package sms

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

type Sender interface {
	Send(ctx context.Context, msg Message) error
}

type Message struct {
	To   string // E.164
	From string // E.164 sender id, optional
	Text string
}

// Stub is the fallback for a site with no SMS provider. A production build writes no content, only that a
// message was not delivered and the last four digits of the number, because the text carries the OTP code;
// a development build (-tags devlicense) keeps the full record.
type Stub struct {
	Path string
	mu   sync.Mutex
	// redact is buildprofile.Production unless a test overrides it.
	redact bool
}

func NewStub(path string) *Stub {
	if path == "" {
		path = "/var/log/stayconnect/otp-sms.log"
	}
	return &Stub{Path: path, redact: buildprofile.Production}
}

func (s *Stub) Send(_ context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		slog.Warn("sms stub: open log failed", "path", s.Path, "err", err)
	} else {
		defer f.Close()
		if s.redact {
			_, _ = fmt.Fprintf(f, "[%s] To=%s NOT DELIVERED: no SMS provider is configured; content withheld (production build)\n---\n",
				time.Now().UTC().Format(time.RFC3339), MaskNumber(m.To))
		} else {
			_, _ = fmt.Fprintf(f, "[%s] To=%s From=%s\n%s\n---\n",
				time.Now().UTC().Format(time.RFC3339), m.To, m.From, m.Text)
		}
	}
	if s.redact {
		slog.Warn("sms.stub: message not delivered, no SMS provider is configured", "to", MaskNumber(m.To))
		return nil
	}
	slog.Info("sms.stub", "to", m.To)
	return nil
}

func MaskNumber(to string) string {
	if len(to) <= 4 {
		return "****"
	}
	return "…" + to[len(to)-4:]
}
