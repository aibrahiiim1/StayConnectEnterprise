// Package whatsapp delivers a one-time sign-in code as a WhatsApp message.
//
// WHATSAPP IS NOT SMS. It is a separate identity channel with its own licence module (whatsapp_otp), its own
// Sign-in methods switch and its own provider row (notification_providers.channel = 'whatsapp').
//
// A business-initiated WhatsApp message may only be sent from a template the provider has approved. A one-time
// code therefore travels as a PARAMETER of an approved AUTHENTICATION template, never as free text: the
// adapters here take the code and fill the template, and there is no way to hand them a message body.
//
// Nothing in this package logs a credential or a code. Logs carry the provider, the HTTP status and the last
// digits of the destination only.
package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Sender delivers one code.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Message is one code to deliver. There is deliberately no free-text body.
type Message struct {
	To         string // E.164, e.g. "+15551234567"
	Code       string // the one-time code; a template parameter, never logged
	TTLMinutes int    // how long the code is valid (for templates that show it)
	Locale     string // preferred template language; empty = the provider's configured language
}

// Errors are typed so a caller can tell a configuration fault from a refused or failed delivery.
var (
	ErrConfig      = errors.New("whatsapp: provider is not configured")
	ErrInvalidDest = errors.New("whatsapp: destination must be an E.164 number")
	ErrEmptyCode   = errors.New("whatsapp: empty code")
)

// ProviderError is a delivery the provider refused or could not accept.
type ProviderError struct {
	Provider string // "meta" | "twilio"
	Status   int    // HTTP status; 0 when the request never completed
	Code     string // provider error code, when it gave one
	Message  string // provider error message, when it gave one
	Err      error  // transport error, when there was one
}

func (e *ProviderError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "whatsapp %s", e.Provider)
	if e.Status != 0 {
		fmt.Fprintf(&b, " status=%d", e.Status)
	}
	if e.Code != "" {
		fmt.Fprintf(&b, " code=%s", e.Code)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *ProviderError) Unwrap() error { return e.Err }

// validE164 is a minimal shape check: '+' then 8..15 digits. Normalisation happens upstream (phone.Normalize).
func validE164(s string) bool {
	if len(s) < 9 || len(s) > 16 || s[0] != '+' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// Suffix returns the last four characters of a destination for logs.
func Suffix(to string) string {
	if len(to) <= 4 {
		return "****"
	}
	return "…" + to[len(to)-4:]
}

func checkMessage(m Message) error {
	if !validE164(m.To) {
		return ErrInvalidDest
	}
	if strings.TrimSpace(m.Code) == "" {
		return ErrEmptyCode
	}
	return nil
}

// Stub records outgoing codes to a local file for development only. It never contacts a provider.
type Stub struct {
	Path string
	mu   sync.Mutex
}

// NewStub builds a development stub writing to path (default /var/log/stayconnect/otp-whatsapp.log).
func NewStub(path string) *Stub {
	if path == "" {
		path = "/var/log/stayconnect/otp-whatsapp.log"
	}
	return &Stub{Path: path}
}

// Send appends the message to the stub file (the development stand-in for the recipient's phone). The code is
// written to that file only, never to the process log.
func (s *Stub) Send(_ context.Context, m Message) error {
	if err := checkMessage(m); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		slog.Warn("whatsapp stub: open failed", "path", s.Path, "err", err)
	} else {
		defer f.Close()
		_, _ = fmt.Fprintf(f, "[%s] To=%s Code=%s TTL=%dm\n---\n",
			time.Now().UTC().Format(time.RFC3339), m.To, m.Code, m.TTLMinutes)
	}
	slog.Info("whatsapp.stub", "to", Suffix(m.To))
	return nil
}
