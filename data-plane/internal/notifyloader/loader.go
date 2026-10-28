// Package notifyloader resolves a tenant's email, SMS and WhatsApp providers from the DB, with fallbacks.
package notifyloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
)

const (
	KindMetaWhatsApp   = "meta_whatsapp"
	KindTwilioWhatsApp = "twilio_whatsapp"
	KindSMTP           = "smtp"
	kindStub           = "stub"
)

// SecretOpener returns the sealed secret of a provider row, or ok=false when none is stored or it cannot be opened; nil
// means plaintext api_key only.
type SecretOpener func(ctx context.Context, providerID string) (secret string, ok bool)

type Result struct {
	Mailer   mail.Mailer
	Sender   sms.Sender
	WhatsApp whatsapp.Sender

	MailerKind   string
	SenderKind   string
	WhatsAppKind string

	// BuildErrors names each enabled provider (by id) that could not be built and fell back to the stub.
	BuildErrors map[string]error
}

// ProviderRow is one notification_providers row; Secret is the opened sealed secret or the plaintext api_key. Region is
// read by callers that display it; no sender uses it.
type ProviderRow struct {
	ID, Channel, Kind, Secret, APIUser, FromAddress, FromName, Region string
	Extra                                                             map[string]string
}

func BuildOne(r ProviderRow, fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) (mail.Mailer, sms.Sender, whatsapp.Sender, string) {
	switch r.Channel {
	case "email":
		m, k, _ := buildMailer(r, fallbackMail)
		return m, nil, nil, k
	case "sms":
		snd, k, _ := buildSender(r, fallbackSMS)
		return nil, snd, nil, k
	case "whatsapp":
		wa, k, _ := buildWhatsApp(r, fallbackWA)
		return nil, nil, wa, k
	}
	return nil, nil, nil, kindStub
}

// Load picks the single enabled row per channel and returns the fallbacks on any error so scd always boots with a
// usable sender.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID string,
	fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) (*Result, error) {
	return LoadWithSecrets(ctx, db, tenantID, nil, fallbackMail, fallbackSMS, fallbackWA)
}

// LoadWithSecrets prefers a sealed generation over the plaintext api_key; a row with neither keeps its fallback. Rows
// are read newest first and the first enabled row per channel wins, so the choice does not depend on scan order. A row
// that cannot be built records the reason as its last_error, so the provider screen never shows it as working.
func LoadWithSecrets(ctx context.Context, db *pgxpool.Pool, tenantID string, open SecretOpener,
	fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) (*Result, error) {

	out := resolve(nil, fallbackMail, fallbackSMS, fallbackWA)

	rows, err := db.Query(ctx, `
        SELECT id::text, channel, kind,
               COALESCE(api_key,''), COALESCE(api_user,''),
               COALESCE(from_address,''), COALESCE(from_name,''),
               COALESCE(extra,'{}'::jsonb)::text
          FROM notification_providers
         WHERE tenant_id = $1 AND enabled = true
         ORDER BY updated_at DESC, id
    `, tenantID)
	if err != nil {
		return out, fmt.Errorf("notifyloader: query: %w", err)
	}
	defer rows.Close()
	var found []ProviderRow
	for rows.Next() {
		var r ProviderRow
		var extra string
		if err := rows.Scan(&r.ID, &r.Channel, &r.Kind, &r.Secret, &r.APIUser, &r.FromAddress, &r.FromName, &extra); err != nil {
			slog.Warn("notifyloader: scan failed", "err", err)
			continue
		}
		r.Extra = parseExtra(extra)
		found = append(found, r)
	}
	rows.Close()
	if open != nil {
		for i := range found {
			if sec, ok := open(ctx, found[i].ID); ok {
				found[i].Secret = sec
			}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	res := resolve(found, fallbackMail, fallbackSMS, fallbackWA)
	for id, berr := range res.BuildErrors {
		recordBuildError(ctx, db, tenantID, id, berr)
	}
	return res, nil
}

func recordBuildError(ctx context.Context, db *pgxpool.Pool, tenantID, providerID string, berr error) {
	if db == nil {
		return
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	msg := Truncate(ScrubContacts("the sender could not be built: "+berr.Error()), 500)
	if _, err := db.Exec(hctx, `UPDATE notification_providers SET last_error = $3, last_error_at = now()
	     WHERE id = $1::uuid AND tenant_id = $2`, providerID, tenantID, msg); err != nil {
		slog.Warn("notifyloader: could not record a provider build failure", "provider", providerID, "err", err)
	}
}

func parseExtra(raw string) map[string]string {
	out := map[string]string{}
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return out
	}
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

// resolve keeps the first row per channel (rows arrive newest first).
func resolve(rows []ProviderRow, fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) *Result {
	out := &Result{
		Mailer: fallbackMail, MailerKind: kindStub,
		Sender: fallbackSMS, SenderKind: kindStub,
		WhatsApp: fallbackWA, WhatsAppKind: kindStub,
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.Channel] {
			slog.Warn("notifyloader: more than one enabled provider for a channel; using the newest", "channel", r.Channel, "ignored", r.ID)
			continue
		}
		var berr error
		switch r.Channel {
		case "email":
			out.Mailer, out.MailerKind, berr = buildMailer(r, fallbackMail)
		case "sms":
			out.Sender, out.SenderKind, berr = buildSender(r, fallbackSMS)
		case "whatsapp":
			out.WhatsApp, out.WhatsAppKind, berr = buildWhatsApp(r, fallbackWA)
		default:
			continue
		}
		seen[r.Channel] = true
		if berr != nil && r.ID != "" {
			if out.BuildErrors == nil {
				out.BuildErrors = map[string]error{}
			}
			out.BuildErrors[r.ID] = berr
		}
	}
	return out
}

// fallbackOn logs a provider that could not be built and returns the stub; the error is recorded on the row.
func fallbackOn[T any](fallback T, channel, kind string, err error) (T, string, error) {
	slog.Warn("notifyloader: provider construct failed; using stub", "channel", channel, "kind", kind, "err", err)
	return fallback, kindStub, err
}

var errUnknownKind = errors.New("unknown provider kind")

// api_key is the secret (Meta access token / Twilio auth token); api_user is the Meta phone number ID / Twilio account
// SID.
func buildWhatsApp(r ProviderRow, fallback whatsapp.Sender) (whatsapp.Sender, string, error) {
	switch r.Kind {
	case KindMetaWhatsApp:
		s, err := whatsapp.NewMeta(r.Secret, r.APIUser, r.Extra["template_name"], r.Extra["language"])
		if err != nil {
			return fallbackOn(fallback, "whatsapp", r.Kind, err)
		}
		return s, KindMetaWhatsApp, nil
	case KindTwilioWhatsApp:
		s, err := whatsapp.NewTwilio(r.APIUser, r.Secret, r.FromAddress, r.Extra["content_sid"])
		if err != nil {
			return fallbackOn(fallback, "whatsapp", r.Kind, err)
		}
		return s, KindTwilioWhatsApp, nil
	case kindStub:
		return fallback, kindStub, nil
	}
	return fallbackOn(fallback, "whatsapp", r.Kind, fmt.Errorf("%w %q", errUnknownKind, r.Kind))
}

func buildMailer(r ProviderRow, fallback mail.Mailer) (mail.Mailer, string, error) {
	switch r.Kind {
	case "sendgrid":
		m, err := mail.NewSendGrid(r.Secret, r.FromAddress, r.FromName)
		if err != nil {
			return fallbackOn(fallback, "email", r.Kind, err)
		}
		return m, "sendgrid", nil
	case KindSMTP:
		m, err := mail.NewSMTP(SMTPConfigFromRow(r.Extra, r.Secret, r.FromAddress, r.FromName))
		if err != nil {
			return fallbackOn(fallback, "email", r.Kind, err)
		}
		return m, KindSMTP, nil
	case kindStub:
		return fallback, kindStub, nil
	}
	return fallbackOn(fallback, "email", r.Kind, fmt.Errorf("%w %q", errUnknownKind, r.Kind))
}

// extra keys: host, port, security (starttls|tls|none), username, timeout_seconds.
func SMTPConfigFromRow(extra map[string]string, password, fromAddr, fromName string) mail.SMTPConfig {
	sec := mail.SMTPSecurity(extra["security"])
	if sec == "" {
		sec = mail.SMTPStartTLS
	}
	port := atoi(extra["port"])
	if port == 0 {
		port = mail.DefaultSMTPPort(sec)
	}
	timeout := time.Duration(atoi(extra["timeout_seconds"])) * time.Second
	return mail.SMTPConfig{Host: extra["host"], Port: port, Security: sec, Username: extra["username"],
		Password: password, FromAddress: fromAddr, FromName: fromName, Timeout: timeout}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 {
			return 0
		}
	}
	return n
}

// The SMS provider's account SID is api_user, its auth token the secret.
func buildSender(r ProviderRow, fallback sms.Sender) (sms.Sender, string, error) {
	switch r.Kind {
	case "twilio":
		s, err := sms.NewTwilio(r.APIUser, r.Secret, r.FromAddress)
		if err != nil {
			return fallbackOn(fallback, "sms", r.Kind, err)
		}
		return s, "twilio", nil
	case kindStub:
		return fallback, kindStub, nil
	}
	return fallbackOn(fallback, "sms", r.Kind, fmt.Errorf("%w %q", errUnknownKind, r.Kind))
}

// instrument times one send, counts it, and records the provider's health. A send through the stub is not the
// enabled provider's doing, so it never marks that provider as working (or failing).
type instrument struct {
	channel, kind string
	met           *metrics.Registry
	db            *pgxpool.Pool
	tenantID      string
}

func (i instrument) send(ctx context.Context, do func() error) error {
	start := time.Now()
	err := scrubError(do())
	dur := time.Since(start).Seconds()
	result := "ok"
	if err != nil {
		result = "failed"
	}
	if i.met != nil {
		i.met.NotifySendTotal.WithLabelValues(i.channel, i.kind, result).Inc()
		i.met.NotifySendDuration.WithLabelValues(i.channel, i.kind).Observe(dur)
	}
	if i.kind != kindStub {
		updateHealth(ctx, i.db, i.tenantID, i.channel, err)
	}
	return err
}

type instrumentedMailer struct {
	inner mail.Mailer
	instrument
}

func WrapMailer(m mail.Mailer, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) mail.Mailer {
	return &instrumentedMailer{inner: m, instrument: instrument{channel: "email", kind: kind, met: met, db: db, tenantID: tenantID}}
}

func (i *instrumentedMailer) Send(ctx context.Context, msg mail.Message) error {
	return i.send(ctx, func() error { return i.inner.Send(ctx, msg) })
}

type instrumentedSender struct {
	inner sms.Sender
	instrument
}

func WrapSender(s sms.Sender, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) sms.Sender {
	return &instrumentedSender{inner: s, instrument: instrument{channel: "sms", kind: kind, met: met, db: db, tenantID: tenantID}}
}

func (i *instrumentedSender) Send(ctx context.Context, msg sms.Message) error {
	return i.send(ctx, func() error { return i.inner.Send(ctx, msg) })
}

type instrumentedWhatsApp struct {
	inner whatsapp.Sender
	instrument
}

func WrapWhatsApp(s whatsapp.Sender, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) whatsapp.Sender {
	return &instrumentedWhatsApp{inner: s, instrument: instrument{channel: "whatsapp", kind: kind, met: met, db: db, tenantID: tenantID}}
}

func (i *instrumentedWhatsApp) Send(ctx context.Context, msg whatsapp.Message) error {
	return i.send(ctx, func() error { return i.inner.Send(ctx, msg) })
}

// healthExec is the database write updateHealth makes; tests replace it.
var healthExec = func(ctx context.Context, db *pgxpool.Pool, sql string, args ...any) error {
	_, err := db.Exec(ctx, sql, args...)
	return err
}

// Best-effort: a failed health write never fails the send. Its context does not end with the send's, so a send that
// failed because its own context ended still records why.
func updateHealth(ctx context.Context, db *pgxpool.Pool, tenantID, channel string, sendErr error) {
	if db == nil || tenantID == "" {
		return
	}
	hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	var err error
	if sendErr == nil {
		err = healthExec(hctx, db, `
            UPDATE notification_providers
               SET last_success_at = now(), last_error = NULL, last_error_at = NULL,
                   updated_at = now()
             WHERE tenant_id = $1 AND channel = $2 AND enabled = true
        `, tenantID, channel)
	} else {
		err = healthExec(hctx, db, `
            UPDATE notification_providers
               SET last_error = $3, last_error_at = now(), updated_at = now()
             WHERE tenant_id = $1 AND channel = $2 AND enabled = true
        `, tenantID, channel, Truncate(sendErr.Error(), 500))
	}
	if err != nil {
		slog.Warn("notifyloader: provider health not recorded", "channel", channel, "err", err)
	}
}

// Truncate cuts s to at most n bytes without splitting a UTF-8 character, adding an ellipsis when it cut.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
