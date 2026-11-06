package api

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/onegate/onegate/control-plane/internal/audit"
	"github.com/onegate/onegate/control-plane/internal/notify"
)

const (
	DeliveryImmediate = "immediate"
	DeliveryDigest    = "digest"
)

type NotificationKind struct {
	Kind            string `json:"kind"`
	Label           string `json:"label"`
	Description     string `json:"description"`
	DefaultDelivery string `json:"default_delivery"`
}

var NotificationKinds = []NotificationKind{
	{"licence_expiring", "Licence ending soon", "A licence reaches each of the three warning points before it ends.", DeliveryImmediate},
	{"licence_grace", "Licence in grace period", "A licence has ended and the appliance is running on its grace period.", DeliveryImmediate},
	{"licence_expired", "Licence and grace ended", "A licence and its grace period have both ended.", DeliveryImmediate},
	{"appliance_offline", "Appliance offline", "An activated appliance has not contacted Central for longer than the offline threshold, and when it is back.", DeliveryImmediate},
	{"security_alert", "Security alert", "Central raised a new security alert about an appliance.", DeliveryImmediate},
	{"retirement_unconfirmed", "Retirement not confirmed", "A retiring appliance did not confirm its retirement in time.", DeliveryImmediate},
	{"appliance_waiting", "Appliance waiting", "An appliance registered and is waiting for activation, or sent an offline activation request.", DeliveryDigest},
	{"portal_certificate_failed", "Portal certificate problem", "Issuing, renewing or revoking a portal certificate, or its DNS record, failed.", DeliveryImmediate},
	{"backup_problem", "Central backup problem", "Central's own database backup failed or is out of date.", DeliveryImmediate},
}

func notificationKindKnown(k string) bool {
	for _, n := range NotificationKinds {
		if n.Kind == k {
			return true
		}
	}
	return false
}

var (
	notifyFirstWarning  = settingBounds{Default: 30, Min: 2, Max: 180, Unit: "days"}
	notifySecondWarning = settingBounds{Default: 7, Min: 1, Max: 90, Unit: "days"}
	notifyFinalWarning  = settingBounds{Default: 1, Min: 0, Max: 30, Unit: "days"}
	notifyOfflineAfter  = settingBounds{Default: 1440, Min: 15, Max: 10080, Unit: "minutes"}
	notifyDigestHour    = settingBounds{Default: 7, Min: 0, Max: 23, Unit: "hour (UTC)"}
	notifyLogRetention  = settingBounds{Default: 90, Min: 7, Max: 730, Unit: "days"}
	notifyMaxAttempts   = settingBounds{Default: 8, Min: 1, Max: 20, Unit: "attempts"}
)

type notificationSettings struct {
	Enabled             bool       `json:"enabled"`
	SMTPHost            string     `json:"smtp_host"`
	SMTPPort            int        `json:"smtp_port"`
	SMTPSecurity        string     `json:"smtp_security"`
	SMTPUsername        string     `json:"smtp_username"`
	FromAddress         string     `json:"from_address"`
	FromName            string     `json:"from_name"`
	ConsoleURL          string     `json:"console_url"`
	FirstWarningDays    int        `json:"licence_first_warning_days"`
	SecondWarningDays   int        `json:"licence_second_warning_days"`
	FinalWarningDays    int        `json:"licence_final_warning_days"`
	OfflineAfterMinutes int        `json:"offline_after_minutes"`
	DigestHourUTC       int        `json:"digest_hour_utc"`
	LogRetentionDays    int        `json:"log_retention_days"`
	MaxAttempts         int        `json:"max_attempts"`
	LastSuccessAt       *time.Time `json:"last_success_at"`
	LastError           *string    `json:"last_error"`
	LastErrorAt         *time.Time `json:"last_error_at"`
	UpdatedAt           *time.Time `json:"updated_at"`
	UpdatedBy           *string    `json:"updated_by"`

	portalCursor *int64
	lastDigestAt *time.Time
}

type notificationSettingsView struct {
	notificationSettings
	PasswordSet bool                     `json:"smtp_password_set"`
	Bounds      map[string]settingBounds `json:"bounds"`
	Kinds       []NotificationKind       `json:"kinds"`
	Pending     int                      `json:"pending"`
	Failed24h   int                      `json:"failed_last_24h"`
}

type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func loadNotificationSettings(ctx context.Context, db rowQuerier) (notificationSettings, error) {
	var s notificationSettings
	var by, lastErr string
	var at time.Time
	err := db.QueryRow(ctx, `
        SELECT enabled, smtp_host, smtp_port, smtp_security, smtp_username, from_address, from_name, console_url,
               licence_first_warning_days, licence_second_warning_days, licence_final_warning_days,
               offline_after_minutes, digest_hour_utc, log_retention_days, max_attempts,
               portal_tls_event_cursor, last_digest_at, last_success_at, COALESCE(last_error,''), last_error_at,
               updated_at, COALESCE(updated_by,'')
          FROM central_notification_settings WHERE id`).Scan(&s.Enabled, &s.SMTPHost, &s.SMTPPort, &s.SMTPSecurity,
		&s.SMTPUsername, &s.FromAddress, &s.FromName, &s.ConsoleURL, &s.FirstWarningDays, &s.SecondWarningDays,
		&s.FinalWarningDays, &s.OfflineAfterMinutes, &s.DigestHourUTC, &s.LogRetentionDays, &s.MaxAttempts,
		&s.portalCursor, &s.lastDigestAt, &s.LastSuccessAt, &lastErr, &s.LastErrorAt, &at, &by)
	if err != nil {
		return s, err
	}
	s.LastError, s.UpdatedAt, s.UpdatedBy = strPtr(lastErr), &at, strPtr(by)
	return s, nil
}

func (s notificationSettings) server(secrets notify.Secrets) (notify.Server, error) {
	pw, err := secrets.ReadPassword()
	if err != nil {
		return notify.Server{}, err
	}
	return notify.Server{Host: s.SMTPHost, Port: s.SMTPPort, Security: s.SMTPSecurity, Username: s.SMTPUsername,
		Password: pw, FromAddress: s.FromAddress, FromName: s.FromName}, nil
}

func (b *Base) notifySecrets() notify.Secrets {
	if b.NotifyDir != "" {
		return notify.Secrets{Dir: b.NotifyDir}
	}
	return notify.SecretsFromEnv()
}

func (b *Base) notificationView(ctx context.Context) (notificationSettingsView, error) {
	s, err := loadNotificationSettings(ctx, b.DB)
	if err != nil {
		return notificationSettingsView{}, err
	}
	v := notificationSettingsView{notificationSettings: s, PasswordSet: b.notifySecrets().HasPassword(),
		Kinds: NotificationKinds, Bounds: map[string]settingBounds{
			"licence_first_warning_days":  notifyFirstWarning,
			"licence_second_warning_days": notifySecondWarning,
			"licence_final_warning_days":  notifyFinalWarning,
			"offline_after_minutes":       notifyOfflineAfter,
			"digest_hour_utc":             notifyDigestHour,
			"log_retention_days":          notifyLogRetention,
			"max_attempts":                notifyMaxAttempts,
		}}
	_ = b.DB.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'pending'),
                                   count(*) FILTER (WHERE status = 'failed' AND created_at > now() - interval '24 hours')
                              FROM central_notification_outbox`).Scan(&v.Pending, &v.Failed24h)
	return v, nil
}

func (b *Base) getNotificationSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	v, err := b.notificationView(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its notification settings.")
		return
	}
	WriteJSON(w, http.StatusOK, v)
}

func validConsoleURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		!strings.ContainsAny(raw, " \r\n")
}

func (b *Base) patchNotificationSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled             *bool   `json:"enabled"`
		SMTPHost            *string `json:"smtp_host"`
		SMTPPort            *int    `json:"smtp_port"`
		SMTPSecurity        *string `json:"smtp_security"`
		SMTPUsername        *string `json:"smtp_username"`
		FromAddress         *string `json:"from_address"`
		FromName            *string `json:"from_name"`
		ConsoleURL          *string `json:"console_url"`
		FirstWarningDays    *int    `json:"licence_first_warning_days"`
		SecondWarningDays   *int    `json:"licence_second_warning_days"`
		FinalWarningDays    *int    `json:"licence_final_warning_days"`
		OfflineAfterMinutes *int    `json:"offline_after_minutes"`
		DigestHourUTC       *int    `json:"digest_hour_utc"`
		LogRetentionDays    *int    `json:"log_retention_days"`
		MaxAttempts         *int    `json:"max_attempts"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	before, err := loadNotificationSettings(ctx, b.DB)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its notification settings.")
		return
	}
	after := before
	changes := map[string]any{}
	str := func(key string, p *string, cur *string, max int, label string) bool {
		if p == nil {
			return true
		}
		v := strings.TrimSpace(*p)
		if len([]rune(v)) > max || strings.ContainsAny(v, "\r\n") {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, label+" must be one line of at most "+strconv.Itoa(max)+" characters.")
			return false
		}
		if v != *cur {
			changes[key] = map[string]string{"from": *cur, "to": v}
		}
		*cur = v
		return true
	}
	if !str("smtp_host", in.SMTPHost, &after.SMTPHost, 253, "The mail server name") ||
		!str("smtp_username", in.SMTPUsername, &after.SMTPUsername, 320, "The user name") ||
		!str("from_address", in.FromAddress, &after.FromAddress, 320, "The From address") ||
		!str("from_name", in.FromName, &after.FromName, 100, "The From name") ||
		!str("console_url", in.ConsoleURL, &after.ConsoleURL, 300, "The console address") {
		return
	}
	after.ConsoleURL = strings.TrimRight(after.ConsoleURL, "/")
	if !validConsoleURL(after.ConsoleURL) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The console address must be an https:// address with no query or user name, for example https://central.example.com.")
		return
	}
	if after.FromAddress != "" {
		if _, err := notify.ParseAddress(after.FromAddress); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The From address is not a valid email address.")
			return
		}
	}
	if in.SMTPSecurity != nil {
		v := strings.TrimSpace(*in.SMTPSecurity)
		if v != notify.SecurityStartTLS && v != notify.SecurityTLS {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The connection security must be STARTTLS or TLS.")
			return
		}
		if v != after.SMTPSecurity {
			changes["smtp_security"] = map[string]string{"from": after.SMTPSecurity, "to": v}
		}
		after.SMTPSecurity = v
	}
	if in.SMTPPort != nil {
		if *in.SMTPPort < 1 || *in.SMTPPort > 65535 {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The mail server port must be between 1 and 65535.")
			return
		}
		if *in.SMTPPort != after.SMTPPort {
			changes["smtp_port"] = map[string]int{"from": after.SMTPPort, "to": *in.SMTPPort}
		}
		after.SMTPPort = *in.SMTPPort
	}
	for _, f := range []struct {
		key   string
		in    *int
		cur   *int
		b     settingBounds
		label string
	}{
		{"licence_first_warning_days", in.FirstWarningDays, &after.FirstWarningDays, notifyFirstWarning, "The first licence warning"},
		{"licence_second_warning_days", in.SecondWarningDays, &after.SecondWarningDays, notifySecondWarning, "The second licence warning"},
		{"licence_final_warning_days", in.FinalWarningDays, &after.FinalWarningDays, notifyFinalWarning, "The final licence warning"},
		{"offline_after_minutes", in.OfflineAfterMinutes, &after.OfflineAfterMinutes, notifyOfflineAfter, "The offline threshold"},
		{"digest_hour_utc", in.DigestHourUTC, &after.DigestHourUTC, notifyDigestHour, "The digest hour"},
		{"log_retention_days", in.LogRetentionDays, &after.LogRetentionDays, notifyLogRetention, "The delivery log period"},
		{"max_attempts", in.MaxAttempts, &after.MaxAttempts, notifyMaxAttempts, "The number of attempts"},
	} {
		if f.in == nil {
			continue
		}
		if *f.in < f.b.Min || *f.in > f.b.Max {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest,
				f.label+" must be between "+strconv.Itoa(f.b.Min)+" and "+strconv.Itoa(f.b.Max)+" "+f.b.Unit+".")
			return
		}
		if *f.in != *f.cur {
			changes[f.key] = map[string]int{"from": *f.cur, "to": *f.in}
		}
		*f.cur = *f.in
	}
	if !(after.FinalWarningDays < after.SecondWarningDays && after.SecondWarningDays < after.FirstWarningDays) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The licence warnings must come in order: the first earliest, then the second, then the final one.")
		return
	}
	if in.Enabled != nil {
		after.Enabled = *in.Enabled
		if after.Enabled != before.Enabled {
			changes["enabled"] = map[string]bool{"from": before.Enabled, "to": after.Enabled}
		}
	}
	if after.Enabled {
		srv, err := after.server(b.notifySecrets())
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read the saved mail server password.")
			return
		}
		if err := srv.Validate(); err != nil {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "Notifications cannot be switched on yet: "+err.Error()+".")
			return
		}
		if srv.Username != "" && srv.Password == "" {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "Notifications cannot be switched on yet: a user name is set but no password is saved.")
			return
		}
	}
	if len(changes) == 0 {
		b.getNotificationSettings(w, r)
		return
	}
	_, email := actorOf(r)
	cursorReset := after.Enabled && !before.Enabled
	if _, err := b.DB.Exec(ctx, `
        UPDATE central_notification_settings
           SET enabled=$1, smtp_host=$2, smtp_port=$3, smtp_security=$4, smtp_username=$5, from_address=$6,
               from_name=$7, console_url=$8, licence_first_warning_days=$9, licence_second_warning_days=$10,
               licence_final_warning_days=$11, offline_after_minutes=$12, digest_hour_utc=$13, log_retention_days=$14,
               max_attempts=$15, updated_at=now(), updated_by=$16,
               portal_tls_event_cursor = CASE WHEN $17 THEN (SELECT COALESCE(max(id),0) FROM portal_tls_events)
                                              ELSE portal_tls_event_cursor END,
               last_digest_at = CASE WHEN $17 THEN now() ELSE last_digest_at END
         WHERE id`, after.Enabled, after.SMTPHost, after.SMTPPort, after.SMTPSecurity, after.SMTPUsername,
		after.FromAddress, after.FromName, after.ConsoleURL, after.FirstWarningDays, after.SecondWarningDays,
		after.FinalWarningDays, after.OfflineAfterMinutes, after.DigestHourUTC, after.LogRetentionDays,
		after.MaxAttempts, email, cursorReset); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The settings could not be saved. Try again.")
		return
	}
	if cursorReset {
		_, _ = b.DB.Exec(ctx, `DELETE FROM central_notification_episodes`)
	}
	audit.Op(r.Context(), b.DB, r, "settings.notifications_updated", "settings", "notifications", changes)
	b.getNotificationSettings(w, r)
}

func (b *Base) putNotificationPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := DecodeJSON(r, &in); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, badBody(err))
		return
	}
	err := b.notifySecrets().WritePassword(in.Password)
	cleared := in.Password == ""
	in.Password = ""
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "The password could not be saved: "+err.Error()+".")
		return
	}
	action := "settings.notifications_password_set"
	if cleared {
		action = "settings.notifications_password_cleared"
	}
	audit.Op(r.Context(), b.DB, r, action, "settings", "notifications", nil)
	WriteJSON(w, http.StatusOK, map[string]any{"smtp_password_set": !cleared})
}

func (b *Base) testNotification(w http.ResponseWriter, r *http.Request) {
	_, email := actorOf(r)
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	s, err := loadNotificationSettings(ctx, b.DB)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its notification settings.")
		return
	}
	srv, err := s.server(b.notifySecrets())
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read the saved mail server password.")
		return
	}
	body := "This is a test message from OneGate Central.\n\nIf you can read it, Central can email its team through " +
		s.SMTPHost + ". Nothing else needs doing.\n"
	if s.ConsoleURL != "" {
		body += "\nNotification settings: " + s.ConsoleURL + "/system/notifications\n"
	}
	sendErr := notify.Send(ctx, srv, notify.Message{To: email, Subject: "OneGate Central test message", Text: body})
	recordNotificationHealth(context.WithoutCancel(r.Context()), b, sendErr)
	audit.Op(r.Context(), b.DB, r, "settings.notifications_tested", "settings", "notifications",
		map[string]any{"ok": sendErr == nil, "error": notify.ShortError(sendErr)})
	if sendErr != nil {
		Fail(w, r, http.StatusBadGateway, "send_failed", "The test message was not sent: "+notify.ShortError(sendErr))
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"sent_to": email})
}

func recordNotificationHealth(ctx context.Context, b *Base, err error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err == nil {
		_, _ = b.DB.Exec(ctx, `UPDATE central_notification_settings SET last_success_at = now() WHERE id`)
		return
	}
	_, _ = b.DB.Exec(ctx, `UPDATE central_notification_settings SET last_error = $1, last_error_at = now() WHERE id`,
		notify.ShortError(err))
}

type subscriptionItem struct {
	EventKind string `json:"event_kind"`
	Delivery  string `json:"delivery"`
}

func (b *Base) mySubscriptions(ctx context.Context, operatorID string) ([]subscriptionItem, error) {
	rows, err := b.DB.Query(ctx, `SELECT event_kind, delivery FROM central_notification_subscriptions
                                   WHERE operator_id::text = $1 ORDER BY event_kind`, operatorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []subscriptionItem{}
	for rows.Next() {
		var s subscriptionItem
		if err := rows.Scan(&s.EventKind, &s.Delivery); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (b *Base) getMySubscriptions(w http.ResponseWriter, r *http.Request) {
	id, email := actorOf(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	items, err := b.mySubscriptions(ctx, id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read your notification choices.")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"email": email, "items": items, "kinds": NotificationKinds})
}

func (b *Base) putMySubscriptions(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Items []subscriptionItem `json:"items"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.Items == nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "items is required")
		return
	}
	seen := map[string]bool{}
	for _, it := range in.Items {
		if !notificationKindKnown(it.EventKind) || seen[it.EventKind] {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "Unknown or repeated event: "+it.EventKind+".")
			return
		}
		if it.Delivery != DeliveryImmediate && it.Delivery != DeliveryDigest {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest, "Delivery must be immediate or digest.")
			return
		}
		seen[it.EventKind] = true
	}
	id, _ := actorOf(r)
	if id == "" {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "login required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Your choices could not be saved. Try again.")
		return
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM central_notification_subscriptions WHERE operator_id::text = $1`, id); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Your choices could not be saved. Try again.")
		return
	}
	for _, it := range in.Items {
		if _, err := tx.Exec(ctx, `INSERT INTO central_notification_subscriptions (operator_id, event_kind, delivery)
                                    VALUES ($1::uuid, $2, $3)`, id, it.EventKind, it.Delivery); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "Your choices could not be saved. Try again.")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Your choices could not be saved. Try again.")
		return
	}
	payload := map[string]any{}
	for _, it := range in.Items {
		payload[it.EventKind] = it.Delivery
	}
	audit.Op(r.Context(), b.DB, r, "notifications.subscriptions_updated", "operator", id, map[string]any{"subscriptions": payload})
	b.getMySubscriptions(w, r)
}

type notificationLogRow struct {
	ID        int64      `json:"id"`
	CreatedAt time.Time  `json:"created_at"`
	Recipient *string    `json:"recipient"`
	EventKind string     `json:"event_kind"`
	Delivery  string     `json:"delivery"`
	Subject   string     `json:"subject"`
	Status    string     `json:"status"`
	Attempts  int        `json:"attempts"`
	NextAt    *time.Time `json:"next_attempt_at"`
	SentAt    *time.Time `json:"sent_at"`
	LastError *string    `json:"last_error"`
}

func (b *Base) listNotificationLog(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "pending" && status != "sent" && status != "failed" && status != "dropped" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "status must be pending, sent, failed or dropped")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT x.id, x.created_at, o.email, x.event_kind, x.delivery, x.subject_line, x.status, x.attempts,
               CASE WHEN x.status = 'pending' THEN x.next_attempt_at END, x.sent_at, x.last_error
          FROM central_notification_outbox x
          LEFT JOIN operators o ON o.id = x.operator_id AND o.anonymised_at IS NULL
         WHERE ($1 = '' OR x.status = $1)
         ORDER BY x.created_at DESC, x.id DESC LIMIT 200`, status)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []notificationLogRow{}
	for rows.Next() {
		var x notificationLogRow
		if err := rows.Scan(&x.ID, &x.CreatedAt, &x.Recipient, &x.EventKind, &x.Delivery, &x.Subject, &x.Status,
			&x.Attempts, &x.NextAt, &x.SentAt, &x.LastError); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
			return
		}
		out = append(out, x)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"items": out})
}
