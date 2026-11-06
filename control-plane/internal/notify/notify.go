package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	SecurityStartTLS = "starttls"
	SecurityTLS      = "tls"
	passwordFile     = "smtp-password"
	sendTimeout      = 30 * time.Second
)

type Secrets struct{ Dir string }

func SecretsFromEnv() Secrets {
	if d := strings.TrimSpace(os.Getenv("CTRLAPI_NOTIFY_DIR")); d != "" {
		return Secrets{Dir: d}
	}
	return Secrets{Dir: "/etc/stayconnect/notify"}
}

func (s Secrets) path() string { return filepath.Join(s.Dir, passwordFile) }

func (s Secrets) HasPassword() bool {
	fi, err := os.Stat(s.path())
	return err == nil && fi.Size() > 0
}

func (s Secrets) ReadPassword() (string, error) {
	raw, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(raw), "\r\n"), nil
}

func (s Secrets) WritePassword(pw string) error {
	if len(pw) > 1024 || strings.ContainsAny(pw, "\r\n") {
		return errors.New("the password must be one line of at most 1024 characters")
	}
	if pw == "" {
		err := os.Remove(s.path())
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	_ = os.Chmod(s.Dir, 0o700)
	f, err := os.CreateTemp(s.Dir, "."+passwordFile+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.WriteString(pw + "\n"); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path())
}

type Server struct {
	Host        string
	Port        int
	Security    string
	Username    string
	Password    string
	FromAddress string
	FromName    string
}

type Message struct {
	To      string
	Subject string
	Text    string
}

func (s Server) Validate() error {
	host := strings.TrimSpace(s.Host)
	if host == "" {
		return errors.New("the mail server name is not set")
	}
	if strings.ContainsAny(host, " /\\@\r\n") {
		return errors.New("the mail server name is not valid")
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("the mail server port must be between 1 and 65535")
	}
	if s.Security != SecurityStartTLS && s.Security != SecurityTLS {
		return errors.New("the connection security must be STARTTLS or TLS")
	}
	if _, err := ParseAddress(s.FromAddress); err != nil {
		return errors.New("the From address is not a valid email address")
	}
	if strings.ContainsAny(s.FromName, "\r\n") {
		return errors.New("the From name must be one line")
	}
	if strings.ContainsAny(s.Username, "\r\n") {
		return errors.New("the user name must be one line")
	}
	return nil
}

func ParseAddress(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" || strings.ContainsAny(addr, "\r\n<>,;\"") {
		return "", errors.New("invalid address")
	}
	a, err := mail.ParseAddress(addr)
	if err != nil || a.Address != addr || !strings.Contains(addr, "@") {
		return "", errors.New("invalid address")
	}
	return a.Address, nil
}

func oneLine(s string) string {
	s = strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
	return strings.TrimSpace(s)
}

func Compose(s Server, m Message, now time.Time) ([]byte, error) {
	to, err := ParseAddress(m.To)
	if err != nil {
		return nil, fmt.Errorf("recipient: %w", err)
	}
	from, err := ParseAddress(s.FromAddress)
	if err != nil {
		return nil, fmt.Errorf("sender: %w", err)
	}
	idRaw := make([]byte, 12)
	if _, err := rand.Read(idRaw); err != nil {
		return nil, err
	}
	domain := from[strings.LastIndex(from, "@")+1:]
	fromHeader := (&mail.Address{Name: oneLine(s.FromName), Address: from}).String()
	var buf bytes.Buffer
	h := func(k, v string) { buf.WriteString(k + ": " + v + "\r\n") }
	h("From", fromHeader)
	h("To", (&mail.Address{Address: to}).String())
	h("Subject", mime.QEncoding.Encode("utf-8", oneLine(m.Subject)))
	h("Date", now.UTC().Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(idRaw)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="utf-8"`)
	h("Content-Transfer-Encoding", "quoted-printable")
	h("Auto-Submitted", "auto-generated")
	buf.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&buf)
	body := strings.ReplaceAll(strings.ReplaceAll(m.Text, "\r\n", "\n"), "\n", "\r\n")
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Send(ctx context.Context, s Server, m Message) error {
	if err := s.Validate(); err != nil {
		return err
	}
	raw, err := Compose(s, m, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	host := strings.TrimSpace(s.Host)
	addr := net.JoinHostPort(host, strconv.Itoa(s.Port))
	tlsCfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	d := &net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	if s.Security == SecurityTLS {
		conn, err = (&tls.Dialer{NetDialer: d, Config: tlsCfg}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("could not reach the mail server: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("the mail server did not answer: %w", err)
	}
	defer c.Close()
	if s.Security == SecurityStartTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("the mail server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("the secure connection failed: %w", err)
		}
	}
	if s.Username != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errors.New("the mail server does not accept sign-in")
		}
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, host)); err != nil {
			return fmt.Errorf("the mail server refused the sign-in: %w", err)
		}
	}
	from, _ := ParseAddress(s.FromAddress)
	to, _ := ParseAddress(m.To)
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("the mail server refused the sender: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("the mail server refused the recipient: %w", err)
	}
	wc, err := c.Data()
	if err != nil {
		return fmt.Errorf("the mail server refused the message: %w", err)
	}
	if _, err := wc.Write(raw); err != nil {
		wc.Close()
		return fmt.Errorf("the message could not be sent: %w", err)
	}
	if err := wc.Close(); err != nil {
		return fmt.Errorf("the mail server refused the message: %w", err)
	}
	return c.Quit()
}

func ShortError(err error) string {
	if err == nil {
		return ""
	}
	s := oneLine(err.Error())
	if r := []rune(s); len(r) > 300 {
		s = string(r[:300]) + "..."
	}
	return s
}
