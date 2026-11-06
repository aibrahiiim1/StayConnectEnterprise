package api

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/onegate/onegate/control-plane/internal/auth"
	"github.com/onegate/onegate/control-plane/internal/notify"
)

const (
	notifyLockKey         = int64(0x6f6e65676174656e)
	notifyOneShotLifetime = 24 * time.Hour
	notifySendBatch       = 500
)

type notice struct {
	Kind    string
	Subject string
	Stage   int
	OneShot bool
	Title   string
	Body    string
	Link    string
}

type episode struct {
	stage      int
	notifiedAt *time.Time
}

func licenceStage(s notificationSettings, left time.Duration) int {
	day := 24 * time.Hour
	switch {
	case left <= time.Duration(s.FinalWarningDays)*day:
		return 3
	case left <= time.Duration(s.SecondWarningDays)*day:
		return 2
	case left <= time.Duration(s.FirstWarningDays)*day:
		return 1
	}
	return 0
}

func daysLeftText(left time.Duration) string {
	d := int(math.Ceil(left.Hours() / 24))
	switch {
	case d <= 0:
		return "today"
	case d == 1:
		return "in 1 day"
	}
	return "in " + strconv.Itoa(d) + " days"
}

func where(a ApplianceRow) string {
	parts := []string{}
	if n := deref(a.CustomerName); n != "" {
		parts = append(parts, "customer "+n)
	}
	if n := deref(a.SiteName); n != "" {
		parts = append(parts, "site "+n)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

func applianceNotices(s notificationSettings, apps []ApplianceRow, now time.Time) []notice {
	var out []notice
	offlineAfter := time.Duration(s.OfflineAfterMinutes) * time.Minute
	for _, a := range apps {
		link := "/appliances/" + a.ID
		name := "appliance " + a.Serial
		at := where(a)
		switch a.Activation {
		case ActivationRetired:
			continue
		case ActivationWaiting:
			out = append(out, notice{Kind: "appliance_waiting", Subject: a.ID + "|waiting", Link: link,
				Title: "Appliance " + a.Serial + " is waiting for activation",
				Body:  "Appliance " + a.Serial + " registered with Central on " + a.RegisteredAt.UTC().Format("2006-01-02 15:04 UTC") + " and is waiting for activation."})
			continue
		}
		if a.OfflineRequestPending {
			out = append(out, notice{Kind: "appliance_waiting", Subject: a.ID + "|offline-request", Link: link,
				Title: "Appliance " + a.Serial + " sent an offline activation request",
				Body:  "An offline activation request for " + name + at + " was imported and is waiting for its activation file."})
		}
		if a.terminalDelivery == "terminal_delivery_failed" {
			out = append(out, notice{Kind: "retirement_unconfirmed", Subject: a.ID, Link: link,
				Title: "Appliance " + a.Serial + " did not confirm its retirement",
				Body:  "The retirement of " + name + at + " was not confirmed by the appliance in time. Its credentials are still valid until it confirms or is retired again."})
		}
		if lv := a.License.ValidUntil; lv != nil {
			key := a.ID + "|" + strconv.FormatInt(lv.Unix(), 10)
			ends := lv.UTC().Format("2006-01-02")
			switch a.License.State {
			case LicenseActive, LicenseExpiring:
				left := lv.Sub(now)
				if st := licenceStage(s, left); st > 0 {
					grace := ""
					if g := a.License.GracePeriodDays; g != nil && *g > 0 {
						grace = " After that it has " + strconv.Itoa(*g) + " grace days."
					}
					out = append(out, notice{Kind: "licence_expiring", Subject: key, Stage: st, Link: link,
						Title: "Licence for appliance " + a.Serial + " ends " + daysLeftText(left),
						Body:  "The licence for " + name + at + " ends on " + ends + "." + grace + " Renew it in Central before then."})
				}
			case LicenseGrace:
				ge := ""
				if a.License.GraceEndsAt != nil {
					ge = " until " + a.License.GraceEndsAt.UTC().Format("2006-01-02")
				}
				out = append(out, notice{Kind: "licence_grace", Subject: key, Link: link,
					Title: "Licence for appliance " + a.Serial + " has ended and is in grace",
					Body:  "The licence for " + name + at + " ended on " + ends + ". The appliance keeps working on its grace period" + ge + ". Renew it in Central."})
			case LicenseExpired:
				out = append(out, notice{Kind: "licence_expired", Subject: key, Link: link,
					Title: "Licence and grace for appliance " + a.Serial + " have ended",
					Body:  "The licence for " + name + at + " ended on " + ends + " and its grace period is over. The appliance no longer admits new guests until it is renewed."})
			}
		}
		if a.Activation != ActivationWaiting && a.LastSeenAt != nil && now.Sub(*a.LastSeenAt) > offlineAfter {
			out = append(out, notice{Kind: "appliance_offline", Subject: a.ID, Link: link,
				Title: "Appliance " + a.Serial + " is offline",
				Body:  "Appliance " + a.Serial + at + " has not contacted Central since " + a.LastSeenAt.UTC().Format("2006-01-02 15:04 UTC") + "."})
		}
	}
	return out
}

func (b *Base) securityNotices(ctx context.Context, apps []ApplianceRow) ([]notice, error) {
	alerts, err := b.openAlerts(ctx, "")
	if err != nil {
		return nil, err
	}
	byID := map[string]ApplianceRow{}
	for _, a := range apps {
		byID[a.ID] = a
	}
	var out []notice
	for _, al := range alerts {
		at := ""
		if a, ok := byID[al.applianceID]; ok {
			at = where(a)
		}
		serial := al.serial
		if serial == "" {
			serial = "unknown"
		}
		out = append(out, notice{Kind: "security_alert", Subject: al.id, Link: "/system/security-alerts",
			Title: "Security alert for appliance " + serial + ": " + strings.ReplaceAll(al.kind, "_", " "),
			Body: "Central raised a security alert (" + al.kind + ") for appliance " + serial + at + " at " +
				al.at.UTC().Format("2006-01-02 15:04 UTC") + ". Review it on the Security alerts page."})
	}
	return out, nil
}

func (b *Base) portalNotices(ctx context.Context, s notificationSettings) ([]notice, int64, error) {
	var cursor int64
	if s.portalCursor != nil {
		cursor = *s.portalCursor
	}
	rows, err := b.DB.Query(ctx, `
        SELECT e.id, COALESCE(e.appliance_id::text,''), COALESCE(e.hostname,''), e.kind, e.at,
               COALESCE(e.detail->>'error', e.detail->>'reason', ''), COALESCE(a.serial,'')
          FROM portal_tls_events e LEFT JOIN appliances a ON a.id = e.appliance_id
         WHERE e.id > $1 AND e.kind = ANY($2)
         ORDER BY e.id LIMIT 1000`, cursor,
		[]string{"ORDER_FAILED", "DNS_ERROR", "DOMAIN_FAILED", "CERT_REVOKE_ERROR"})
	if err != nil {
		return nil, cursor, err
	}
	defer rows.Close()
	var out []notice
	next := cursor
	for rows.Next() {
		var id int64
		var appID, host, kind, detail, serial string
		var at time.Time
		if err := rows.Scan(&id, &appID, &host, &kind, &at, &detail, &serial); err != nil {
			return nil, cursor, err
		}
		next = id
		what := map[string]string{"ORDER_FAILED": "a portal certificate order failed",
			"DNS_ERROR": "a portal DNS record could not be updated", "DOMAIN_FAILED": "a custom portal domain failed verification",
			"CERT_REVOKE_ERROR": "a portal certificate could not be revoked"}[kind]
		target := host
		if serial != "" {
			target = "appliance " + serial + " (" + host + ")"
		}
		if target == "" {
			target = "Central"
		}
		body := "For " + target + ", " + what + " at " + at.UTC().Format("2006-01-02 15:04 UTC") + "."
		if detail != "" {
			body += " Reason given: " + notify.ShortError(fmt.Errorf("%s", detail)) + "."
		}
		subj := "portal|" + appID + "|" + host + "|" + kind
		out = append(out, notice{Kind: "portal_certificate_failed", Subject: subj, OneShot: true, Link: "/system/portal-certificates",
			Title: "Portal certificate problem for " + target, Body: body})
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, err
	}
	if s.portalCursor == nil {
		_ = b.DB.QueryRow(ctx, `SELECT COALESCE(max(id),0) FROM portal_tls_events`).Scan(&next)
		return nil, next, nil
	}
	return out, next, nil
}

func backupNotices(now time.Time) []notice {
	h := dbBackupHealth(now)
	if h.Healthy {
		return nil
	}
	return []notice{{Kind: "backup_problem", Subject: "database", Link: "/system/backup-health",
		Title: "Central database backup problem", Body: deref(h.Problem)}}
}

type subscriber struct {
	operatorID string
	delivery   string
}

func (b *Base) subscribers(ctx context.Context) (map[string][]subscriber, error) {
	rows, err := b.DB.Query(ctx, `
        SELECT s.event_kind, s.operator_id::text, s.delivery
          FROM central_notification_subscriptions s
          JOIN operators o ON o.id = s.operator_id
         WHERE o.status = 'active' AND o.anonymised_at IS NULL
           AND EXISTS (SELECT 1 FROM operator_roles r WHERE r.operator_id = o.id AND r.tenant_id IS NULL
                          AND r.role = ANY($1))`, auth.PlatformRoles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]subscriber{}
	for rows.Next() {
		var kind string
		var s subscriber
		if err := rows.Scan(&kind, &s.operatorID, &s.delivery); err != nil {
			return nil, err
		}
		out[kind] = append(out[kind], s)
	}
	return out, rows.Err()
}

func composeBody(s notificationSettings, n notice) string {
	body := n.Body
	if s.ConsoleURL != "" && n.Link != "" {
		body += "\n\nOpen in Central: " + s.ConsoleURL + n.Link
	}
	return body
}

func backOnlineNotice(apps map[string]ApplianceRow, subject string, now time.Time) (notice, bool) {
	a, ok := apps[subject]
	if !ok || a.Activation == ActivationRetired || a.LastSeenAt == nil || now.Sub(*a.LastSeenAt) > ConnectedWithin {
		return notice{}, false
	}
	return notice{Kind: "appliance_offline", Subject: a.ID, Link: "/appliances/" + a.ID,
		Title: "Appliance " + a.Serial + " is back online",
		Body:  "Appliance " + a.Serial + where(a) + " contacted Central again at " + a.LastSeenAt.UTC().Format("2006-01-02 15:04 UTC") + "."}, true
}

func NotificationPass(ctx context.Context, b *Base, now time.Time) error {
	conn, err := b.DB.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, notifyLockKey).Scan(&locked); err != nil || !locked {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, notifyLockKey)

	s, err := loadNotificationSettings(ctx, b.DB)
	if err != nil {
		return err
	}
	if _, err := b.DB.Exec(ctx, `DELETE FROM central_notification_outbox
                                   WHERE status <> 'pending' AND created_at < now() - make_interval(days => $1)`,
		s.LogRetentionDays); err != nil {
		return err
	}
	if !s.Enabled {
		return nil
	}
	if err := b.evaluateNotices(ctx, s, now); err != nil {
		return err
	}
	return b.deliverNotices(ctx, s, now)
}

func (b *Base) evaluateNotices(ctx context.Context, s notificationSettings, now time.Time) error {
	apps, err := b.queryAppliances(ctx, "", "")
	if err != nil {
		return err
	}
	current := applianceNotices(s, apps, now)
	sec, err := b.securityNotices(ctx, apps)
	if err != nil {
		return err
	}
	current = append(current, sec...)
	portal, cursor, err := b.portalNotices(ctx, s)
	if err != nil {
		return err
	}
	current = append(current, portal...)
	current = append(current, backupNotices(now)...)

	eps := map[[2]string]episode{}
	rows, err := b.DB.Query(ctx, `SELECT event_kind, subject, stage, notified_at FROM central_notification_episodes`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k, subj string
		var e episode
		if err := rows.Scan(&k, &subj, &e.stage, &e.notifiedAt); err != nil {
			rows.Close()
			return err
		}
		eps[[2]string{k, subj}] = e
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	subs, err := b.subscribers(ctx)
	if err != nil {
		return err
	}
	byID := map[string]ApplianceRow{}
	for _, a := range apps {
		byID[a.ID] = a
	}

	tx, err := b.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	enqueue := func(n notice) error {
		for _, sub := range subs[n.Kind] {
			if _, err := tx.Exec(ctx, `INSERT INTO central_notification_outbox
                                         (operator_id, event_kind, delivery, subject_line, body)
                                       VALUES ($1::uuid, $2, $3, $4, $5)`,
				sub.operatorID, n.Kind, sub.delivery, n.Title, composeBody(s, n)); err != nil {
				return err
			}
		}
		return nil
	}
	present := map[[2]string]bool{}
	for _, n := range current {
		key := [2]string{n.Kind, n.Subject}
		if present[key] {
			continue
		}
		present[key] = true
		e, had := eps[key]
		switch {
		case !had:
			if _, err := tx.Exec(ctx, `INSERT INTO central_notification_episodes (event_kind, subject, stage, notified_at)
                                       VALUES ($1, $2, $3, now()) ON CONFLICT (event_kind, subject) DO NOTHING`,
				n.Kind, n.Subject, n.Stage); err != nil {
				return err
			}
			if err := enqueue(n); err != nil {
				return err
			}
		case n.Stage > e.stage:
			if _, err := tx.Exec(ctx, `UPDATE central_notification_episodes SET stage = $3, notified_at = now()
                                        WHERE event_kind = $1 AND subject = $2`, n.Kind, n.Subject, n.Stage); err != nil {
				return err
			}
			if err := enqueue(n); err != nil {
				return err
			}
		}
	}
	for key, e := range eps {
		if present[key] {
			continue
		}
		if key[0] == "portal_certificate_failed" && e.notifiedAt != nil && now.Sub(*e.notifiedAt) < notifyOneShotLifetime {
			continue
		}
		if _, err := tx.Exec(ctx, `DELETE FROM central_notification_episodes WHERE event_kind = $1 AND subject = $2`,
			key[0], key[1]); err != nil {
			return err
		}
		if key[0] == "appliance_offline" {
			if n, ok := backOnlineNotice(byID, key[1], now); ok {
				if err := enqueue(n); err != nil {
					return err
				}
			}
		}
	}
	if cursor != deref64(s.portalCursor) || s.portalCursor == nil {
		if _, err := tx.Exec(ctx, `UPDATE central_notification_settings SET portal_tls_event_cursor = $1 WHERE id`, cursor); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func digestDue(s notificationSettings, now time.Time) bool {
	u := now.UTC()
	slot := time.Date(u.Year(), u.Month(), u.Day(), s.DigestHourUTC, 0, 0, 0, time.UTC)
	if u.Before(slot) {
		slot = slot.Add(-24 * time.Hour)
	}
	return s.lastDigestAt == nil || s.lastDigestAt.Before(slot)
}

func retryDelay(attempts int) time.Duration {
	steps := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour}
	if attempts-1 < len(steps) {
		return steps[max(attempts-1, 0)]
	}
	return 4 * time.Hour
}

type outboxRow struct {
	id       int64
	operator string
	kind     string
	delivery string
	subject  string
	body     string
	attempts int
}

func (b *Base) deliverNotices(ctx context.Context, s notificationSettings, now time.Time) error {
	digest := digestDue(s, now)
	rows, err := b.DB.Query(ctx, `
        SELECT id, COALESCE(operator_id::text,''), event_kind, delivery, subject_line, body, attempts
          FROM central_notification_outbox
         WHERE status = 'pending' AND next_attempt_at <= now() AND (delivery = 'immediate' OR $1)
         ORDER BY id LIMIT $2`, digest, notifySendBatch)
	if err != nil {
		return err
	}
	groups := map[string][]outboxRow{}
	var order []string
	for rows.Next() {
		var x outboxRow
		if err := rows.Scan(&x.id, &x.operator, &x.kind, &x.delivery, &x.subject, &x.body, &x.attempts); err != nil {
			rows.Close()
			return err
		}
		key := x.operator + "|" + x.delivery
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if digest {
		if _, err := b.DB.Exec(ctx, `UPDATE central_notification_settings SET last_digest_at = $1 WHERE id`, now); err != nil {
			return err
		}
	}
	if len(order) == 0 {
		return nil
	}
	srv, srvErr := s.server(b.notifySecrets())
	if srvErr == nil {
		srvErr = srv.Validate()
	}
	var lastErr error
	sentAny := false
	for _, key := range order {
		items := groups[key]
		ids := make([]int64, len(items))
		for i, x := range items {
			ids[i] = x.id
		}
		email, ok := b.recipientEmail(ctx, items[0].operator)
		if !ok {
			if _, err := b.DB.Exec(ctx, `UPDATE central_notification_outbox SET status = 'dropped',
                                            last_error = 'the recipient is no longer an active Central team member'
                                          WHERE id = ANY($1)`, ids); err != nil {
				return err
			}
			continue
		}
		subject, body := groupMessage(items, s)
		err := srvErr
		if err == nil {
			err = notify.Send(ctx, srv, notify.Message{To: email, Subject: subject, Text: body})
		}
		if err == nil {
			sentAny = true
			if _, e := b.DB.Exec(ctx, `UPDATE central_notification_outbox
                                          SET status = 'sent', sent_at = now(), attempts = attempts + 1, last_error = NULL
                                        WHERE id = ANY($1)`, ids); e != nil {
				return e
			}
			continue
		}
		lastErr = err
		attempts := items[0].attempts + 1
		status := "pending"
		if attempts >= s.MaxAttempts {
			status = "failed"
		}
		if _, e := b.DB.Exec(ctx, `UPDATE central_notification_outbox
                                      SET attempts = attempts + 1, last_error = $2, status = $3,
                                          next_attempt_at = now() + make_interval(secs => $4)
                                    WHERE id = ANY($1)`, ids, notify.ShortError(err), status,
			retryDelay(attempts).Seconds()); e != nil {
			return e
		}
	}
	if sentAny {
		recordNotificationHealth(ctx, b, nil)
	}
	if lastErr != nil {
		recordNotificationHealth(ctx, b, lastErr)
	}
	return nil
}

func (b *Base) recipientEmail(ctx context.Context, operatorID string) (string, bool) {
	if operatorID == "" {
		return "", false
	}
	var email string
	err := b.DB.QueryRow(ctx, `
        SELECT o.email FROM operators o
         WHERE o.id::text = $1 AND o.status = 'active' AND o.anonymised_at IS NULL
           AND EXISTS (SELECT 1 FROM operator_roles r WHERE r.operator_id = o.id AND r.tenant_id IS NULL
                          AND r.role = ANY($2))`, operatorID, auth.PlatformRoles).Scan(&email)
	if err != nil {
		return "", false
	}
	if _, err := notify.ParseAddress(email); err != nil {
		return "", false
	}
	return email, true
}

func groupMessage(items []outboxRow, s notificationSettings) (string, string) {
	footer := "\n\n--\nYou receive this because of your notification choices in OneGate Central."
	if s.ConsoleURL != "" {
		footer += " Change them at " + s.ConsoleURL + "/system/notifications"
	}
	if len(items) == 1 && items[0].delivery == DeliveryImmediate {
		return items[0].subject, items[0].body + footer + "\n"
	}
	sorted := append([]outboxRow(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].kind < sorted[j].kind })
	var sb strings.Builder
	n := strconv.Itoa(len(items))
	subject := "OneGate Central: " + n + " new notices"
	intro := "Central has " + n + " new notices for you.\n"
	if items[0].delivery == DeliveryDigest {
		subject = "OneGate Central daily digest: " + n + " notices"
		intro = "Your daily digest from Central has " + n + " notices.\n"
		if len(items) == 1 {
			subject = "OneGate Central daily digest: 1 notice"
			intro = "Your daily digest from Central has 1 notice.\n"
		}
	}
	sb.WriteString(intro)
	for i, x := range sorted {
		sb.WriteString("\n" + strconv.Itoa(i+1) + ". " + x.subject + "\n" + x.body + "\n")
	}
	return subject, sb.String() + footer + "\n"
}
