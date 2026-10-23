// Package notifyloader resolves a tenant's email, SMS and WhatsApp providers from the DB, with fallbacks.
package notifyloader

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

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
}

type providerRow struct {
	id                                                         string
	channel, kind, apiKey, apiUser, fromAddr, fromName, region string
	extra                                                      map[string]string
}

type ProviderRow struct {
	ID, Channel, Kind, Secret, APIUser, FromAddress, FromName, Region string
	Extra                                                             map[string]string
}

func BuildOne(r ProviderRow, fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) (mail.Mailer, sms.Sender, whatsapp.Sender, string) {
	row := providerRow{id: r.ID, channel: r.Channel, kind: r.Kind, apiKey: r.Secret, apiUser: r.APIUser,
		fromAddr: r.FromAddress, fromName: r.FromName, region: r.Region, extra: r.Extra}
	switch r.Channel {
	case "email":
		m, k := buildMailer(row, fallbackMail)
		return m, nil, nil, k
	case "sms":
		snd, k := buildSender(row.kind, row.apiUser, row.apiKey, row.fromAddr, fallbackSMS)
		return nil, snd, nil, k
	case "whatsapp":
		wa, k := buildWhatsApp(row, fallbackWA)
		return nil, nil, wa, k
	}
	return nil, nil, nil, "stub"
}

// Load picks the single enabled row per channel and returns the fallbacks on any error so scd always boots with a
// usable sender.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID string,
	fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) (*Result, error) {
	return LoadWithSecrets(ctx, db, tenantID, nil, fallbackMail, fallbackSMS, fallbackWA)
}

// LoadWithSecrets prefers a sealed generation over the plaintext api_key; a row with neither keeps its fallback.
func LoadWithSecrets(ctx context.Context, db *pgxpool.Pool, tenantID string, open SecretOpener,
	fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) (*Result, error) {

	out := resolve(nil, fallbackMail, fallbackSMS, fallbackWA)

	rows, err := db.Query(ctx, `
        SELECT id::text, channel, kind,
               COALESCE(api_key,''), COALESCE(api_user,''),
               COALESCE(from_address,''), COALESCE(from_name,''),
               COALESCE(region,''), COALESCE(extra,'{}'::jsonb)::text
          FROM notification_providers
         WHERE tenant_id = $1 AND enabled = true
    `, tenantID)
	if err != nil {
		return out, fmt.Errorf("notifyloader: query: %w", err)
	}
	defer rows.Close()
	var found []providerRow
	for rows.Next() {
		var r providerRow
		var extra string
		if err := rows.Scan(&r.id, &r.channel, &r.kind, &r.apiKey, &r.apiUser, &r.fromAddr, &r.fromName, &r.region, &extra); err != nil {
			slog.Warn("notifyloader: scan failed", "err", err)
			continue
		}
		r.extra = parseExtra(extra)
		found = append(found, r)
	}
	rows.Close()
	if open != nil {
		for i := range found {
			if sec, ok := open(ctx, found[i].id); ok {
				found[i].apiKey = sec
			}
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return resolve(found, fallbackMail, fallbackSMS, fallbackWA), nil
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

func resolve(rows []providerRow, fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) *Result {
	out := &Result{
		Mailer: fallbackMail, MailerKind: "stub",
		Sender: fallbackSMS, SenderKind: "stub",
		WhatsApp: fallbackWA, WhatsAppKind: "stub",
	}
	for _, r := range rows {
		switch r.channel {
		case "email":
			out.Mailer, out.MailerKind = buildMailer(r, fallbackMail)
		case "sms":
			out.Sender, out.SenderKind = buildSender(r.kind, r.apiUser, r.apiKey, r.fromAddr, fallbackSMS)
		case "whatsapp":
			out.WhatsApp, out.WhatsAppKind = buildWhatsApp(r, fallbackWA)
		}
	}
	return out
}

// api_key is the secret (Meta access token / Twilio auth token); api_user is the Meta phone number ID / Twilio account
// SID.
func buildWhatsApp(r providerRow, fallback whatsapp.Sender) (whatsapp.Sender, string) {
	switch r.kind {
	case KindMetaWhatsApp:
		s, err := whatsapp.NewMeta(r.apiKey, r.apiUser, r.extra["template_name"], r.extra["language"])
		if err != nil {
			slog.Warn("notifyloader: whatsapp meta construct failed; using stub", "err", err)
			return fallback, "stub"
		}
		return s, KindMetaWhatsApp
	case KindTwilioWhatsApp:
		s, err := whatsapp.NewTwilio(r.apiUser, r.apiKey, r.fromAddr, r.extra["content_sid"])
		if err != nil {
			slog.Warn("notifyloader: whatsapp twilio construct failed; using stub", "err", err)
			return fallback, "stub"
		}
		return s, KindTwilioWhatsApp
	case "stub":
		return fallback, "stub"
	}
	slog.Warn("notifyloader: unknown whatsapp kind; using stub", "kind", r.kind)
	return fallback, "stub"
}

func buildMailer(r providerRow, fallback mail.Mailer) (mail.Mailer, string) {
	switch r.kind {
	case "sendgrid":
		m, err := mail.NewSendGrid(r.apiKey, r.fromAddr, r.fromName)
		if err != nil {
			slog.Warn("notifyloader: sendgrid construct failed; using stub", "err", err)
			return fallback, "stub"
		}
		return m, "sendgrid"
	case KindSMTP:
		m, err := mail.NewSMTP(SMTPConfigFromRow(r.extra, r.apiKey, r.fromAddr, r.fromName))
		if err != nil {
			slog.Warn("notifyloader: smtp construct failed; using stub", "err", err)
			return fallback, "stub"
		}
		return m, KindSMTP
	case "stub":
		return fallback, "stub"
	}
	slog.Warn("notifyloader: unknown email kind; using stub", "kind", r.kind)
	return fallback, "stub"
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

func buildSender(kind, accountSID, authToken, fromNumber string, fallback sms.Sender) (sms.Sender, string) {
	switch kind {
	case "twilio":
		s, err := sms.NewTwilio(accountSID, authToken, fromNumber)
		if err != nil {
			slog.Warn("notifyloader: twilio construct failed; using stub", "err", err)
			return fallback, "stub"
		}
		return s, "twilio"
	case "stub":
		return fallback, "stub"
	}
	slog.Warn("notifyloader: unknown sms kind; using stub", "kind", kind)
	return fallback, "stub"
}

type instrumentedMailer struct {
	inner    mail.Mailer
	kind     string
	met      *metrics.Registry
	db       *pgxpool.Pool
	tenantID string
}

func WrapMailer(m mail.Mailer, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) mail.Mailer {
	return &instrumentedMailer{inner: m, kind: kind, met: met, db: db, tenantID: tenantID}
}

func (i *instrumentedMailer) Send(ctx context.Context, msg mail.Message) error {
	start := time.Now()
	err := scrubError(i.inner.Send(ctx, msg))
	dur := time.Since(start).Seconds()
	result := "ok"
	if err != nil {
		result = "failed"
	}
	if i.met != nil {
		i.met.NotifySendTotal.WithLabelValues("email", i.kind, result).Inc()
		i.met.NotifySendDuration.WithLabelValues("email", i.kind).Observe(dur)
	}
	updateHealth(ctx, i.db, i.tenantID, "email", err)
	return err
}

type instrumentedSender struct {
	inner    sms.Sender
	kind     string
	met      *metrics.Registry
	db       *pgxpool.Pool
	tenantID string
}

func WrapSender(s sms.Sender, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) sms.Sender {
	return &instrumentedSender{inner: s, kind: kind, met: met, db: db, tenantID: tenantID}
}

func (i *instrumentedSender) Send(ctx context.Context, msg sms.Message) error {
	start := time.Now()
	err := scrubError(i.inner.Send(ctx, msg))
	dur := time.Since(start).Seconds()
	result := "ok"
	if err != nil {
		result = "failed"
	}
	if i.met != nil {
		i.met.NotifySendTotal.WithLabelValues("sms", i.kind, result).Inc()
		i.met.NotifySendDuration.WithLabelValues("sms", i.kind).Observe(dur)
	}
	updateHealth(ctx, i.db, i.tenantID, "sms", err)
	return err
}

type instrumentedWhatsApp struct {
	inner    whatsapp.Sender
	kind     string
	met      *metrics.Registry
	db       *pgxpool.Pool
	tenantID string
}

func WrapWhatsApp(s whatsapp.Sender, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) whatsapp.Sender {
	return &instrumentedWhatsApp{inner: s, kind: kind, met: met, db: db, tenantID: tenantID}
}

func (i *instrumentedWhatsApp) Send(ctx context.Context, msg whatsapp.Message) error {
	start := time.Now()
	err := scrubError(i.inner.Send(ctx, msg))
	dur := time.Since(start).Seconds()
	result := "ok"
	if err != nil {
		result = "failed"
	}
	if i.met != nil {
		i.met.NotifySendTotal.WithLabelValues("whatsapp", i.kind, result).Inc()
		i.met.NotifySendDuration.WithLabelValues("whatsapp", i.kind).Observe(dur)
	}
	updateHealth(ctx, i.db, i.tenantID, "whatsapp", err)
	return err
}

// Best-effort: a failed health write never fails the send.
func updateHealth(ctx context.Context, db *pgxpool.Pool, tenantID, channel string, sendErr error) {
	if db == nil || tenantID == "" {
		return
	}
	hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var (
		errMsg sql.NullString
	)
	if sendErr != nil {
		errMsg = sql.NullString{Valid: true, String: truncate(sendErr.Error(), 500)}
	}
	if sendErr == nil {
		_, _ = db.Exec(hctx, `
            UPDATE notification_providers
               SET last_success_at = now(), last_error = NULL, last_error_at = NULL,
                   updated_at = now()
             WHERE tenant_id = $1 AND channel = $2 AND enabled = true
        `, tenantID, channel)
	} else {
		_, _ = db.Exec(hctx, `
            UPDATE notification_providers
               SET last_error = $3, last_error_at = now(), updated_at = now()
             WHERE tenant_id = $1 AND channel = $2 AND enabled = true
        `, tenantID, channel, errMsg.String)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
