// Package notifyloader resolves a tenant's email, SMS and WhatsApp providers
// from the DB and returns ready-to-use mail.Mailer / sms.Sender /
// whatsapp.Sender instances. WhatsApp is its own channel, never an SMS kind.
//
// scd's startup chain:
//  1. Load(ctx, db, tenantID, fallbacks)
//  2. Returned Mailer/Sender are wired into the server struct
//  3. If no enabled row for a channel, the matching fallback is used
//     (typically a Stub for dev / unconfigured tenants)
//
// This module also wraps the chosen provider so every Send() goes through
// the same metric instrumentation (success/failure/latency).
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

// WhatsApp provider kinds stored in notification_providers.kind (channel 'whatsapp').
const (
	KindMetaWhatsApp   = "meta_whatsapp"
	KindTwilioWhatsApp = "twilio_whatsapp"
)

// Result holds the resolved channels. Either field is always non-nil; on
// error the caller can still fall back without nil-checking everywhere.
type Result struct {
	Mailer   mail.Mailer
	Sender   sms.Sender
	WhatsApp whatsapp.Sender

	// MailerKind / SenderKind report which implementation won (so logs
	// and the metrics layer can label by provider). "stub" when the
	// fallback was used.
	MailerKind   string
	SenderKind   string
	WhatsAppKind string
}

// providerRow is one enabled notification_providers row.
type providerRow struct {
	channel, kind, apiKey, apiUser, fromAddr, fromName, region string
	extra                                                      map[string]string
}

// Load picks one row per channel (the enabled one — at most one by the
// partial unique index in migration 0016) and constructs the matching
// implementation. Returns the fallbacks unchanged on any error so scd
// always boots with a usable sender pair.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID string,
	fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) (*Result, error) {

	out := resolve(nil, fallbackMail, fallbackSMS, fallbackWA)

	rows, err := db.Query(ctx, `
        SELECT channel, kind,
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
		if err := rows.Scan(&r.channel, &r.kind, &r.apiKey, &r.apiUser, &r.fromAddr, &r.fromName, &r.region, &extra); err != nil {
			slog.Warn("notifyloader: scan failed", "err", err)
			continue
		}
		r.extra = parseExtra(extra)
		found = append(found, r)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return resolve(found, fallbackMail, fallbackSMS, fallbackWA), nil
}

// parseExtra reads the string-valued keys of the extra jsonb column; anything else is ignored.
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

// resolve picks the implementation for each channel from the enabled rows (at most one per channel by the
// partial unique index); a channel without a usable row keeps its fallback.
func resolve(rows []providerRow, fallbackMail mail.Mailer, fallbackSMS sms.Sender, fallbackWA whatsapp.Sender) *Result {
	out := &Result{
		Mailer: fallbackMail, MailerKind: "stub",
		Sender: fallbackSMS, SenderKind: "stub",
		WhatsApp: fallbackWA, WhatsAppKind: "stub",
	}
	for _, r := range rows {
		switch r.channel {
		case "email":
			out.Mailer, out.MailerKind = buildMailer(r.kind, r.apiKey, r.fromAddr, r.fromName, fallbackMail)
		case "sms":
			out.Sender, out.SenderKind = buildSender(r.kind, r.apiUser, r.apiKey, r.fromAddr, fallbackSMS)
		case "whatsapp":
			out.WhatsApp, out.WhatsAppKind = buildWhatsApp(r, fallbackWA)
		}
	}
	return out
}

// buildWhatsApp maps a whatsapp row onto its adapter. api_key is the secret (Meta access token / Twilio auth
// token); api_user is the Meta phone number ID / Twilio account SID.
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

func buildMailer(kind, apiKey, fromAddr, fromName string, fallback mail.Mailer) (mail.Mailer, string) {
	switch kind {
	case "sendgrid":
		m, err := mail.NewSendGrid(apiKey, fromAddr, fromName)
		if err != nil {
			slog.Warn("notifyloader: sendgrid construct failed; using stub", "err", err)
			return fallback, "stub"
		}
		return m, "sendgrid"
	case "stub":
		return fallback, "stub"
	}
	slog.Warn("notifyloader: unknown email kind; using stub", "kind", kind)
	return fallback, "stub"
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

// ----- Metric-instrumented wrappers -----
//
// Wrap the chosen implementation so every Send() emits:
//   scd_notification_send_total{channel,provider,result}
//   scd_notification_send_duration_seconds{channel,provider}
// and bumps last_success_at / last_error on the DB row for admin UI.

type instrumentedMailer struct {
	inner    mail.Mailer
	kind     string
	met      *metrics.Registry
	db       *pgxpool.Pool
	tenantID string
}

// WrapMailer adds metric instrumentation + DB health updates.
func WrapMailer(m mail.Mailer, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) mail.Mailer {
	return &instrumentedMailer{inner: m, kind: kind, met: met, db: db, tenantID: tenantID}
}

func (i *instrumentedMailer) Send(ctx context.Context, msg mail.Message) error {
	start := time.Now()
	err := i.inner.Send(ctx, msg)
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
	err := i.inner.Send(ctx, msg)
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

// WrapWhatsApp adds metric instrumentation + DB health updates to the WhatsApp channel.
func WrapWhatsApp(s whatsapp.Sender, kind string, met *metrics.Registry, db *pgxpool.Pool, tenantID string) whatsapp.Sender {
	return &instrumentedWhatsApp{inner: s, kind: kind, met: met, db: db, tenantID: tenantID}
}

func (i *instrumentedWhatsApp) Send(ctx context.Context, msg whatsapp.Message) error {
	start := time.Now()
	err := i.inner.Send(ctx, msg)
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

// updateHealth bumps last_success_at OR last_error / last_error_at on the
// active provider row. Best-effort; failure to write health doesn't bubble
// up to the caller (the actual send already succeeded or failed).
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
