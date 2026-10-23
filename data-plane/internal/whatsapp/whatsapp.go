// Package whatsapp delivers a one-time sign-in code over WhatsApp, a separate channel from SMS (own licence
// module whatsapp_otp, own provider row). Business-initiated messages need an approved template, so the code
// travels only as an AUTHENTICATION template parameter, never free text. Nothing here logs a credential or
// code; logs carry the provider, HTTP status and the last digits of the destination.
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

	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
)

type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Message deliberately has no free-text body.
type Message struct {
	To         string // E.164, e.g. "+15551234567"
	Code       string // the one-time code; a template parameter, never logged
	TTLMinutes int    // how long the code is valid (for templates that show it)
	Locale     string // preferred template language; empty = the provider's configured language
}

var (
	ErrConfig      = errors.New("whatsapp: provider is not configured")
	ErrInvalidDest = errors.New("whatsapp: destination must be an E.164 number")
	ErrEmptyCode   = errors.New("whatsapp: empty code")
)

// ProviderError is a delivery the provider refused or could not accept.
type ProviderError struct {
	Provider string // "meta" | "twilio"
	Status   int    // 0 when the request never completed
	Code     string
	Message  string
	Err      error // transport error
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

// validE164 is a shape check only; normalisation happens upstream (phone.Normalize).
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

// Stub records codes to a local file for development and never contacts a provider. In a production build it
// writes only that nothing was delivered, never the code; it is also the fallback when no provider is configured.
type Stub struct {
	Path string
	mu   sync.Mutex
	// redact is buildprofile.Production unless a test overrides it.
	redact bool
}

func NewStub(path string) *Stub {
	if path == "" {
		path = "/var/log/stayconnect/otp-whatsapp.log"
	}
	return &Stub{Path: path, redact: buildprofile.Production}
}

// Send appends to the stub file; the code is never written to the process log.
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
		if s.redact {
			_, _ = fmt.Fprintf(f, "[%s] To=%s NOT DELIVERED: no WhatsApp provider is configured; code withheld (production build)\n---\n",
				time.Now().UTC().Format(time.RFC3339), Suffix(m.To))
		} else {
			_, _ = fmt.Fprintf(f, "[%s] To=%s Code=%s TTL=%dm\n---\n",
				time.Now().UTC().Format(time.RFC3339), m.To, m.Code, m.TTLMinutes)
		}
	}
	slog.Info("whatsapp.stub", "to", Suffix(m.To))
	return nil
}
