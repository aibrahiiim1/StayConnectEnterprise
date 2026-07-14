// Package sms abstracts outbound SMS so OTP / notification senders can
// swap between a stub (dev), Twilio, MessageBird, and others without
// touching call sites. Mirrors the design of internal/mail.
package sms

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

type Sender interface {
	Send(ctx context.Context, msg Message) error
}

type Message struct {
	To   string // E.164 (e.g. "+15551234567")
	From string // E.164 sender id, optional (provider-decided when empty)
	Text string // SMS body (plain text)
}

// Stub appends outgoing SMS to a file (default /var/log/stayconnect/otp-sms.log)
// AND to slog. Useful for dev so the flow can be exercised without a real
// SMS account; replace with TwilioSender / MessageBirdSender in production.
type Stub struct {
	Path string
	mu   sync.Mutex
}

func NewStub(path string) *Stub {
	if path == "" {
		path = "/var/log/stayconnect/otp-sms.log"
	}
	return &Stub{Path: path}
}

func (s *Stub) Send(_ context.Context, m Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		slog.Warn("sms stub: open log failed", "path", s.Path, "err", err)
	} else {
		defer f.Close()
		_, _ = fmt.Fprintf(f, "[%s] To=%s From=%s\n%s\n---\n",
			time.Now().UTC().Format(time.RFC3339), m.To, m.From, m.Text)
	}
	slog.Info("sms.stub", "to", m.To)
	return nil
}
