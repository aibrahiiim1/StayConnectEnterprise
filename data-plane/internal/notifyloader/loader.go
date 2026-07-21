// Package notifyloader resolves a tenant's email + SMS providers from the
// DB and returns ready-to-use mail.Mailer / sms.Sender instances.
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
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
)

// Result holds the resolved channels. Either field is always non-nil; on
// error the caller can still fall back without nil-checking everywhere.
type Result struct {
	Mailer mail.Mailer
	Sender sms.Sender

	// MailerKind / SenderKind report which implementation won (so logs
	// and the metrics layer can label by provider). "stub" when the
	// fallback was used.
	MailerKind string
	SenderKind string
}

// Load picks one row per channel (the enabled one — at most one by the
// partial unique index in migration 0016) and constructs the matching
// implementation. Returns the fallbacks unchanged on any error so scd
// always boots with a usable sender pair.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID string,
	fallbackMail mail.Mailer, fallbackSMS sms.Sender) (*Result, error) {

	out := &Result{
		Mailer: fallbackMail, MailerKind: "stub",
		Sender: fallbackSMS, SenderKind: "stub",
	}

	rows, err := db.Query(ctx, `
        SELECT channel, kind,
               COALESCE(api_key,''), COALESCE(api_user,''),
               COALESCE(from_address,''), COALESCE(from_name,''),
               COALESCE(region,'')
          FROM notification_providers
         WHERE tenant_id = $1 AND enabled = true
    `, tenantID)
	if err != nil {
		return out, fmt.Errorf("notifyloader: query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var channel, kind, apiKey, apiUser, fromAddr, fromName, region string
		if err := rows.Scan(&channel, &kind, &apiKey, &apiUser, &fromAddr, &fromName, &region); err != nil {
			slog.Warn("notifyloader: scan failed", "err", err)
			continue
		}
		_ = region // reserved for SES kind; not used yet
		switch channel {
		case "email":
			m, kind := buildMailer(kind, apiKey, fromAddr, fromName, fallbackMail)
			out.Mailer = m
			out.MailerKind = kind
		case "sms":
			s, kind := buildSender(kind, apiUser, apiKey, fromAddr, fallbackSMS)
			out.Sender = s
			out.SenderKind = kind
		}
	}
	return out, rows.Err()
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
