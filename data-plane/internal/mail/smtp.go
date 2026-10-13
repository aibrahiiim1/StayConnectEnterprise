package mail

// A SITE'S OWN SMTP SERVER.
//
// Email one-time codes do not have to go through a provider API: an office, a clinic or a hotel group usually
// already has a mail server or a relay (Microsoft 365, Google Workspace, an on-premise Exchange, a transactional
// relay) and the credentials to use it. This sender speaks plain SMTP with the three transport modes that cover
// those: implicit TLS (465), STARTTLS (587) and none (an internal relay that refuses TLS). Authentication is
// PLAIN or LOGIN over TLS only; CRAM-MD5 is accepted for a plaintext hop because it never sends the password.
//
// It sends ONE message per call, opens a fresh connection each time (codes are rare; a pooled connection to a
// mail server that drops idle sessions is a worse failure than a short handshake), and reports the server's
// own words on failure so an operator can act on them. The password is held in memory only.

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SMTPSecurity is the transport mode.
type SMTPSecurity string

const (
	SMTPStartTLS SMTPSecurity = "starttls" // connect in clear, upgrade with STARTTLS (587)
	SMTPTLS      SMTPSecurity = "tls"      // implicit TLS from the first byte (465)
	SMTPNone     SMTPSecurity = "none"     // no TLS at all: an internal relay only
)

// SMTPConfig is what the operator enters. The password arrives separately (sealed storage) and is never
// returned by any API.
type SMTPConfig struct {
	Host        string
	Port        int
	Security    SMTPSecurity
	Username    string
	Password    string
	FromAddress string
	FromName    string
	Timeout     time.Duration // whole send; default 20s
	// InsecureSkipVerify is deliberately NOT offered: a site that cannot present a valid certificate for its
	// relay uses SMTPNone on a trusted internal segment or fixes the certificate. Codes go to clients.
}

// ValidateSMTPConfig returns an operator-readable reason the configuration cannot work, or "".
func ValidateSMTPConfig(c SMTPConfig) string {
	host := strings.TrimSpace(c.Host)
	if host == "" || strings.ContainsAny(host, " /\\@:") {
		return "SMTP host must be a host name or address, for example smtp.office365.com"
	}
	if c.Port < 1 || c.Port > 65535 {
		return "SMTP port must be between 1 and 65535 (587 for STARTTLS, 465 for TLS)"
	}
	switch c.Security {
	case SMTPStartTLS, SMTPTLS, SMTPNone:
	default:
		return "SMTP security must be starttls, tls or none"
	}
	if c.Username != "" && c.Password == "" {
		return "a password is required when a username is set"
	}
	if c.Username == "" && c.Password != "" {
		return "a username is required when a password is set"
	}
	if _, err := mail.ParseAddress(c.FromAddress); err != nil {
		return "the from address must be a valid email address"
	}
	if c.Timeout < 0 || c.Timeout > 2*time.Minute {
		return "the timeout must be between 1 and 120 seconds"
	}
	return ""
}

// DefaultSMTPPort suggests the port that goes with a transport mode when the operator left it blank.
func DefaultSMTPPort(sec SMTPSecurity) int {
	switch sec {
	case SMTPTLS:
		return 465
	case SMTPNone:
		return 25
	}
	return 587
}

// SMTP is the Mailer.
type SMTP struct {
	cfg SMTPConfig
	// Dialer is replaceable for tests.
	Dialer func(ctx context.Context, network, addr string) (net.Conn, error)
}

// NewSMTP validates and builds the sender.
func NewSMTP(c SMTPConfig) (*SMTP, error) {
	if msg := ValidateSMTPConfig(c); msg != "" {
		return nil, errors.New("smtp: " + msg)
	}
	if c.Timeout == 0 {
		c.Timeout = 20 * time.Second
	}
	c.Host = strings.TrimSpace(c.Host)
	return &SMTP{cfg: c, Dialer: (&net.Dialer{}).DialContext}, nil
}

// Send delivers one message and returns the server's words on failure.
func (s *SMTP) Send(ctx context.Context, m Message) error {
	if _, err := mail.ParseAddress(m.To); err != nil {
		return errors.New("smtp: invalid recipient")
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprint(s.cfg.Port))
	conn, err := s.Dialer(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("smtp: connect to %s: %w", addr, err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	tlsCfg := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}
	if s.cfg.Security == SMTPTLS {
		conn = tls.Client(conn, tlsCfg)
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp: greeting: %w", err)
	}
	defer c.Close()
	if s.cfg.Security == SMTPStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: the server does not offer STARTTLS; choose tls or none")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("smtp: STARTTLS: %w", err)
		}
	}
	if s.cfg.Username != "" {
		if err := c.Auth(s.auth()); err != nil {
			return fmt.Errorf("smtp: authentication refused: %w", err)
		}
	}
	from, _ := mail.ParseAddress(s.cfg.FromAddress)
	if err := c.Mail(from.Address); err != nil {
		return fmt.Errorf("smtp: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return fmt.Errorf("smtp: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp: DATA: %w", err)
	}
	if _, err := w.Write([]byte(s.render(m))); err != nil {
		_ = w.Close()
		return fmt.Errorf("smtp: body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtp: %w", err)
	}
	return c.Quit()
}

// auth picks a mechanism the transport allows: PLAIN/LOGIN only under TLS, CRAM-MD5 otherwise.
func (s *SMTP) auth() smtp.Auth {
	if s.cfg.Security == SMTPNone {
		return smtp.CRAMMD5Auth(s.cfg.Username, s.cfg.Password)
	}
	return &loginOrPlain{user: s.cfg.Username, pass: s.cfg.Password, host: s.cfg.Host}
}

// loginOrPlain answers PLAIN when offered (the common case) and the older LOGIN dialogue otherwise
// (Office 365 relays and some appliances). Both only ever run after TLS is established.
type loginOrPlain struct{ user, pass, host string }

func (a *loginOrPlain) Start(si *smtp.ServerInfo) (string, []byte, error) {
	if !si.TLS {
		return "", nil, errors.New("password authentication requires TLS")
	}
	for _, m := range si.Auth {
		if m == "PLAIN" {
			return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
		}
	}
	for _, m := range si.Auth {
		if m == "LOGIN" {
			return "LOGIN", nil, nil
		}
	}
	return "", nil, errors.New("the server offers neither PLAIN nor LOGIN authentication")
}

func (a *loginOrPlain) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(fromServer))) {
	case "username:":
		return []byte(a.user), nil
	case "password:":
		return []byte(a.pass), nil
	}
	return nil, errors.New("unexpected server challenge")
}

// render writes a minimal RFC 5322 message: text, plus HTML as a multipart alternative when given.
func (s *SMTP) render(m Message) string {
	var b strings.Builder
	from := s.cfg.FromAddress
	if s.cfg.FromName != "" {
		from = mime.QEncoding.Encode("utf-8", s.cfg.FromName) + " <" + s.cfg.FromAddress + ">"
	}
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + m.To + "\r\n")
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	if m.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
		b.WriteString(crlf(m.Text))
		return b.String()
	}
	const boundary = "=_onegate_alt_boundary_"
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")
	b.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n" + crlf(m.Text) + "\r\n")
	b.WriteString("--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\n\r\n" + crlf(m.HTML) + "\r\n")
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

func crlf(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
