package main

// EMAIL ACCOUNTS: SELF-SERVICE REGISTRATION, VERIFICATION, SUSPENSION AND THE ONE-TIME GRACE HOUR
// (contract docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md; migration 0117).
//
// A client registers with an address and a password, is signed in at once and goes to the packages. The account
// is an iam_v2.guest_access_accounts row (the ACCOUNT subject IAM-v2 already authenticates) marked
// self_registered, site-specific, never a guest principal: an address -- verified or not -- is never an identity
// factor, never links to a Client, never joins a client group.
//
// This file holds the state and the decisions; self_service_handlers.go holds the routes.
//
// THE PIECES, AND WHERE EACH DECISION LIVES
//
//	accountSuspended   the ONE suspension predicate, pure: self-registered, verification required by the site,
//	                   policy SUSPEND, not verified, past the deadline, no live grace. The sweep, activation and
//	                   every reply use it (or its SQL twin in the sweep, which must say the same thing).
//	emailTokenHMAC     the emailed links: 256-bit random tokens, stored as HMAC-SHA256 only under a key derived
//	                   from the appliance-local resume key (domain account-email-token:v1). The token is minted
//	                   when the message is handed to the mail server, so the plaintext exists only in the email.
//	account tokens     "recently signed in with the password on THIS device": HMAC(account, MAC, expiry), 15
//	                   minutes, kept by portald in an HttpOnly cookie. Proof for status and resend; never for grace
//	                   (grace requires the password again) and never a sign-in by itself.
//	outbox             each token row is its own outbox entry; the send loop mints, sends, records, backs off.
//	sweep              every 30 s: ends the live sessions of suspended accounts through the operator-disconnect
//	                   path, queues reminders, and gives a deadline to unverified accounts that registered while
//	                   verification was not required.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
)

const (
	// Key-derivation domains under the appliance-local resume key. Distinct domains make the emailed-link index,
	// the account token and the remembered-device credential three unrelated keys although one file holds them.
	accountEmailTokenDomain   = "account-email-token:v1"
	accountSessionTokenDomain = "account-session-token:v1"

	accountTokenTTL = 15 * time.Minute
	// A VERIFY link lives until the verification deadline and at least 24 hours (contract §4); a RESET link 30
	// minutes.
	verifyTokenMinLifetime = 24 * time.Hour
	resetTokenLifetime     = 30 * time.Minute

	// Settings are read through iam_v2.self_service_settings_get and cached this long: a change in the Admin
	// Console reaches every decision within seconds, and a burst of sign-ins does not read the row each time.
	selfServiceSettingsTTL = 10 * time.Second

	// At most this many messages per send tick: a backlog after an outage drains steadily instead of handing the
	// mail server hundreds of messages at once.
	selfServiceSendBatch = 5

	// Session end reasons recorded by this file (iam_v2.sessions.end_reason, free text like ENTITLEMENT_ENDED).
	endReasonVerificationSuspended = "VERIFICATION_SUSPENDED"
	endReasonPasswordReset         = "PASSWORD_RESET"

	tokenPurposeVerify = "VERIFY"
	tokenPurposeReset  = "RESET"
)

// selfServiceSettings is iam_v2.site_self_service_settings as enforced (contract §6).
type selfServiceSettings struct {
	VerificationRequired         bool
	DeadlineHours                int
	SuspensionPolicy             string // SUSPEND | WARN_ONLY
	ReminderHoursBefore          int
	ResendCooldownSeconds        int
	ResendMaxPerDay              int
	GraceMinutes                 int
	MaxGraceExtensions           int
	RegistrationsPerDevicePerDay int
}

// defaultSelfServiceSettings are the contract defaults -- also the answer when the settings cannot be read for a
// decision that must still be made (the sweep, activation): they are the strictest values a site can have, so a
// read failure never loosens enforcement.
func defaultSelfServiceSettings() selfServiceSettings {
	return selfServiceSettings{VerificationRequired: true, DeadlineHours: 24, SuspensionPolicy: "SUSPEND",
		ReminderHoursBefore: 4, ResendCooldownSeconds: 60, ResendMaxPerDay: 5, GraceMinutes: 60,
		MaxGraceExtensions: 1, RegistrationsPerDevicePerDay: 3}
}

// selfServiceBound is one setting's default and inclusive bounds -- the table CHECKs and the setter, in Go, so a
// value read from anywhere is clamped into what the database itself would accept.
type selfServiceBound struct{ Default, Min, Max int }

var selfServiceBounds = map[string]selfServiceBound{
	"verification_deadline_hours":      {24, 1, 168},
	"reminder_hours_before":            {4, 0, 72},
	"resend_cooldown_seconds":          {60, 30, 3600},
	"resend_max_per_day":               {5, 1, 20},
	"grace_minutes":                    {60, 15, 240},
	"max_grace_extensions":             {1, 0, 3},
	"registrations_per_device_per_day": {3, 1, 10},
}

func clampSelfService(name string, v int) int {
	b := selfServiceBounds[name]
	if v < b.Min {
		return b.Min
	}
	if v > b.Max {
		return b.Max
	}
	return v
}

// normalized clamps every value into its bounds (and the reminder below the deadline). The database already
// refuses out-of-bounds values; this is the second line, for a value from a test, a fake or a future reader.
func (c selfServiceSettings) normalized() selfServiceSettings {
	c.DeadlineHours = clampSelfService("verification_deadline_hours", c.DeadlineHours)
	c.ReminderHoursBefore = clampSelfService("reminder_hours_before", c.ReminderHoursBefore)
	if c.ReminderHoursBefore >= c.DeadlineHours {
		c.ReminderHoursBefore = 0
	}
	c.ResendCooldownSeconds = clampSelfService("resend_cooldown_seconds", c.ResendCooldownSeconds)
	c.ResendMaxPerDay = clampSelfService("resend_max_per_day", c.ResendMaxPerDay)
	c.GraceMinutes = clampSelfService("grace_minutes", c.GraceMinutes)
	c.MaxGraceExtensions = clampSelfService("max_grace_extensions", c.MaxGraceExtensions)
	c.RegistrationsPerDevicePerDay = clampSelfService("registrations_per_device_per_day", c.RegistrationsPerDevicePerDay)
	if c.SuspensionPolicy != "WARN_ONLY" {
		c.SuspensionPolicy = "SUSPEND"
	}
	return c
}

// selfServiceState is the Email accounts runtime state hung on the server.
type selfServiceState struct {
	mu       sync.Mutex
	cached   selfServiceSettings
	cachedAt time.Time
	// load overrides the settings read (tests); nil reads iam_v2.self_service_settings_get.
	load func(ctx context.Context) (selfServiceSettings, error)
	// kick wakes the send loop the moment a message is queued, so a registration's email leaves at once rather
	// than at the next tick. Buffered and non-blocking: a pending kick is enough.
	kick      chan struct{}
	sendTick  time.Duration
	sweepTick time.Duration
}

// initSelfService fixes the loop cadences. SCD_SELF_SERVICE_SEND_TICK and SCD_SELF_SERVICE_SWEEP_TICK exist so an
// acceptance run does not wait on production timing; both are clamped to a floor, because a sweep that runs
// continuously is load on the session table and a send loop that spins is load on the mail server.
func (s *server) initSelfService(getenv func(string) string) {
	s.selfService.kick = make(chan struct{}, 1)
	s.selfService.sendTick = envDuration(getenv, "SCD_SELF_SERVICE_SEND_TICK", 15*time.Second, time.Second)
	s.selfService.sweepTick = envDuration(getenv, "SCD_SELF_SERVICE_SWEEP_TICK", 30*time.Second, 5*time.Second)
}

// envDuration reads a Go duration ("15s") or a bare number of seconds, clamped to min; absent or malformed is def.
func envDuration(getenv func(string) string, name string, def, min time.Duration) time.Duration {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		n, nerr := strconv.Atoi(v)
		if nerr != nil {
			slog.Warn("ignoring a malformed duration", "env", name, "value", v)
			return def
		}
		d = time.Duration(n) * time.Second
	}
	if d < min {
		return min
	}
	return d
}

func (s *server) kickSelfServiceOutbox() {
	if s.selfService.kick == nil {
		return
	}
	select {
	case s.selfService.kick <- struct{}{}:
	default:
	}
}

// selfServiceSettingsRead answers the site's settings, cached briefly. An error is returned to callers that
// must refuse (registration); callers that must still decide use selfServiceSettingsOrDefault.
func (s *server) selfServiceSettingsRead(ctx context.Context) (selfServiceSettings, error) {
	st := &s.selfService
	st.mu.Lock()
	if !st.cachedAt.IsZero() && time.Since(st.cachedAt) < selfServiceSettingsTTL {
		c := st.cached
		st.mu.Unlock()
		return c, nil
	}
	st.mu.Unlock()
	var c selfServiceSettings
	var err error
	if st.load != nil {
		c, err = st.load(ctx)
	} else if s.db == nil {
		err = errors.New("no site database")
	} else {
		err = s.db.QueryRow(ctx, `
		    SELECT verification_required, verification_deadline_hours, suspension_policy, reminder_hours_before,
		           resend_cooldown_seconds, resend_max_per_day, grace_minutes, max_grace_extensions,
		           registrations_per_device_per_day
		      FROM iam_v2.self_service_settings_get($1::uuid, $2::uuid)`, s.tenID, s.siteID).Scan(
			&c.VerificationRequired, &c.DeadlineHours, &c.SuspensionPolicy, &c.ReminderHoursBefore,
			&c.ResendCooldownSeconds, &c.ResendMaxPerDay, &c.GraceMinutes, &c.MaxGraceExtensions,
			&c.RegistrationsPerDevicePerDay)
	}
	if err != nil {
		return selfServiceSettings{}, err
	}
	c = c.normalized()
	st.mu.Lock()
	st.cached, st.cachedAt = c, time.Now()
	st.mu.Unlock()
	return c, nil
}

func (s *server) selfServiceSettingsOrDefault(ctx context.Context) selfServiceSettings {
	c, err := s.selfServiceSettingsRead(ctx)
	if err != nil {
		slog.Warn("email accounts: settings unreadable; enforcing the defaults", "err", err)
		return defaultSelfServiceSettings()
	}
	return c
}

// ---- the account and the one suspension predicate -----------------------------------------------------------

// selfServiceAccountRow is the part of iam_v2.guest_access_accounts these decisions read.
type selfServiceAccountRow struct {
	ID             string
	Username       string
	SelfRegistered bool
	Enabled        bool
	VerifiedAt     *time.Time
	Deadline       *time.Time
	GraceUntil     *time.Time
	GraceUsed      int
}

const selfServiceAccountCols = `id::text, username, self_registered, enabled, email_verified_at,
	verification_deadline, grace_until, grace_extensions_used`

func scanSelfServiceAccount(row pgx.Row) (selfServiceAccountRow, error) {
	var a selfServiceAccountRow
	err := row.Scan(&a.ID, &a.Username, &a.SelfRegistered, &a.Enabled, &a.VerifiedAt, &a.Deadline, &a.GraceUntil, &a.GraceUsed)
	return a, err
}

type pgQueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadSelfServiceAccount reads one account of THIS site by id; pgx.ErrNoRows when there is none.
func (s *server) loadSelfServiceAccount(ctx context.Context, q pgQueryRower, id string) (selfServiceAccountRow, error) {
	return scanSelfServiceAccount(q.QueryRow(ctx, `SELECT `+selfServiceAccountCols+`
	      FROM iam_v2.guest_access_accounts WHERE id = $1::uuid AND tenant_id = $2 AND site_id = $3`, id, s.tenID, s.siteID))
}

// accountSuspended is THE suspension predicate (contract §3): the account is suspended for Internet when it is
// self-registered, the site requires verification with the SUSPEND policy, the address is not verified, the
// deadline has passed, and no grace is running. A missing deadline is never past (an account registered while
// verification was not required is given one by the sweep before it can be suspended). WARN_ONLY never suspends;
// a site that does not require verification never suspends. The entitlement is not part of the question: a
// suspension lifts the moment the account is verified or granted grace, with the package exactly as it was.
func accountSuspended(a selfServiceAccountRow, set selfServiceSettings, now time.Time) bool {
	if !a.SelfRegistered || !set.VerificationRequired || set.SuspensionPolicy != "SUSPEND" {
		return false
	}
	if a.VerifiedAt != nil || a.Deadline == nil || !now.After(*a.Deadline) {
		return false
	}
	return a.GraceUntil == nil || !a.GraceUntil.After(now)
}

// graceAvailable: a suspended account that has not used its allowance (max_grace_extensions, hard maximum 3).
func graceAvailable(a selfServiceAccountRow, set selfServiceSettings, now time.Time) bool {
	return accountSuspended(a, set, now) && a.GraceUsed < set.MaxGraceExtensions
}

// resendWait answers how long until another VERIFY message may be queued: the cooldown after the newest one,
// or -- when the rolling 24-hour allowance is spent -- until the oldest of those leaves the window (limited).
func resendWait(newest, oldestInDay *time.Time, inDay int, set selfServiceSettings, now time.Time) (wait time.Duration, limited bool) {
	if inDay >= set.ResendMaxPerDay && oldestInDay != nil {
		if w := oldestInDay.Add(24 * time.Hour).Sub(now); w > 0 {
			return w, true
		}
	}
	if newest != nil {
		if w := newest.Add(time.Duration(set.ResendCooldownSeconds) * time.Second).Sub(now); w > 0 {
			return w, false
		}
	}
	return 0, false
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}

// selfServiceAccount is the account block every reply carries (contract §7). Never a password, hash or token.
type selfServiceAccount struct {
	Verified             bool       `json:"verified"`
	VerificationRequired bool       `json:"verification_required"`
	Deadline             *time.Time `json:"deadline"`
	Suspended            bool       `json:"suspended"`
	GraceUntil           *time.Time `json:"grace_until"`
	GraceAvailable       bool       `json:"grace_available"`
	ResendAvailableIn    int        `json:"resend_available_in"`
}

// tokenStats is the VERIFY history resendWait needs.
type tokenStats struct {
	newest, oldestInDay *time.Time
	inDay               int
}

func (s *server) verifyTokenStats(ctx context.Context, q pgQueryRower, accountID, purpose string) (tokenStats, error) {
	var st tokenStats
	err := q.QueryRow(ctx, `
	    SELECT max(created_at), min(created_at) FILTER (WHERE created_at > now() - interval '24 hours'),
	           count(*) FILTER (WHERE created_at > now() - interval '24 hours')
	      FROM iam_v2.account_email_tokens WHERE account_id = $1::uuid AND purpose = $2`, accountID, purpose).
		Scan(&st.newest, &st.oldestInDay, &st.inDay)
	return st, err
}

// accountView builds the reply block. The resend wait is best effort: unreadable history reads as "now", and the
// resend route itself re-checks inside its transaction.
func (s *server) accountView(ctx context.Context, a selfServiceAccountRow, set selfServiceSettings, now time.Time) *selfServiceAccount {
	v := &selfServiceAccount{
		Verified:             a.VerifiedAt != nil,
		VerificationRequired: set.VerificationRequired,
		Deadline:             a.Deadline,
		Suspended:            accountSuspended(a, set, now),
		GraceAvailable:       graceAvailable(a, set, now),
	}
	if a.GraceUntil != nil && a.GraceUntil.After(now) && a.VerifiedAt == nil {
		v.GraceUntil = a.GraceUntil
	}
	if a.VerifiedAt == nil && s.db != nil {
		if st, err := s.verifyTokenStats(ctx, s.db, a.ID, tokenPurposeVerify); err == nil {
			w, _ := resendWait(st.newest, st.oldestInDay, st.inDay, set, now)
			v.ResendAvailableIn = ceilSeconds(w)
		}
	}
	return v
}

// decorateSelfServiceResult adds the account block and the account token to a successful ACCOUNT sign-in, for a
// self-registered account only. A failure to read leaves the reply as a plain account sign-in: the sign-in
// itself succeeded, and activation applies the suspension rule on its own.
func (s *server) decorateSelfServiceResult(ctx context.Context, out *iamv2GuestResult, accountID, mac string) {
	if s.db == nil || out == nil {
		return
	}
	a, err := s.loadSelfServiceAccount(ctx, s.db, accountID)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("email accounts: account unreadable after sign-in", "err", err)
		}
		return
	}
	if !a.SelfRegistered {
		return
	}
	now := time.Now()
	out.Account = s.accountView(ctx, a, s.selfServiceSettingsOrDefault(ctx), now)
	out.AccountToken = s.issueAccountToken(a.ID, mac, now)
}

// accountSuspendedByID is the activation check: is the subject of this entitlement a suspended account?
// Read inside the activation transaction so a grace or a verification racing it is seen.
func (s *server) entitlementAccountSuspended(ctx context.Context, tx pgx.Tx, entitlementID string, set selfServiceSettings) (bool, error) {
	a, err := scanSelfServiceAccount(tx.QueryRow(ctx, `SELECT a.id::text, a.username, a.self_registered, a.enabled,
	         a.email_verified_at, a.verification_deadline, a.grace_until, a.grace_extensions_used
	    FROM iam_v2.entitlements e JOIN iam_v2.guest_access_accounts a ON a.id = e.guest_account_id
	   WHERE e.id = $1::uuid`, entitlementID))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // not an account entitlement
	}
	if err != nil {
		return false, err
	}
	return accountSuspended(a, set, time.Now()), nil
}

// ---- keys, emailed-link tokens, account tokens ----------------------------------------------------------------

// selfServiceKey derives a purpose key from the appliance-local resume key. nil when the key file is absent
// (initClientResume logged it): then no link can be minted or checked and no account token issued -- the
// account still signs in, and the messages stay queued until the key is back.
func (s *server) selfServiceKey(domain string) []byte {
	if len(s.resumeKey) < 32 {
		return nil
	}
	h := hmac.New(sha256.New, s.resumeKey)
	h.Write([]byte(domain))
	return h.Sum(nil)
}

// emailTokenHMAC is the stored blind index of an emailed token.
func emailTokenHMAC(key []byte, token string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(token))
	return h.Sum(nil)
}

// emailTokenKeyID names the key a token was indexed under (a fingerprint, never the key).
func emailTokenKeyID(key []byte) string {
	sum := sha256.Sum256(append([]byte("key-id\x00"), key...))
	return "aet1:" + hex.EncodeToString(sum[:8])
}

// newEmailToken returns 256 random bits, URL-safe.
func newEmailToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// issueAccountToken binds an account to the device that just proved its password, for 15 minutes:
// "v1.<payload>.<mac>" with payload = base64url(account|device MAC|expiry unix). "" without a key.
func (s *server) issueAccountToken(accountID, mac string, now time.Time) string {
	key := s.selfServiceKey(accountSessionTokenDomain)
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	if key == nil || err != nil || accountID == "" {
		return ""
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(accountID + "|" + hw.String() + "|" +
		strconv.FormatInt(now.Add(accountTokenTTL).Unix(), 10)))
	h := hmac.New(sha256.New, key)
	h.Write([]byte("v1." + payload))
	return "v1." + payload + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// parseAccountToken returns the account an account token names, when it is genuine, unexpired and presented
// by the device it was issued to. Any failure is the same "" -- a forged, expired or borrowed token says nothing.
func (s *server) parseAccountToken(tok, mac string, now time.Time) string {
	key := s.selfServiceKey(accountSessionTokenDomain)
	hw, err := net.ParseMAC(strings.TrimSpace(mac))
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if key == nil || err != nil || len(parts) != 3 || parts[0] != "v1" {
		return ""
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte("v1." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, h.Sum(nil)) {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	f := strings.Split(string(raw), "|")
	if len(f) != 3 || f[0] == "" {
		return ""
	}
	exp, err := strconv.ParseInt(f[2], 10, 64)
	if err != nil || !now.Before(time.Unix(exp, 0)) || f[1] != hw.String() {
		return ""
	}
	return f[0]
}

// queueEmailToken supersedes the account's unused tokens of the purpose and queues a new one for the outbox. The
// token itself is minted at send time (sendOneAccountEmail), so the row holds no secret yet.
func queueEmailToken(ctx context.Context, tx pgx.Tx, tenantID, siteID, accountID, purpose string, expires time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.account_email_tokens SET superseded_at = now()
	     WHERE account_id = $1::uuid AND purpose = $2 AND used_at IS NULL AND superseded_at IS NULL`, accountID, purpose); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.account_email_tokens (tenant_id, site_id, account_id, purpose, expires_at)
	     VALUES ($1, $2, $3::uuid, $4, $5)`, tenantID, siteID, accountID, purpose, expires)
	return err
}

// verifyTokenExpiry: until the deadline, and at least 24 hours (contract §4).
func verifyTokenExpiry(deadline *time.Time, now time.Time) time.Time {
	exp := now.Add(verifyTokenMinLifetime)
	if deadline != nil && deadline.After(exp) {
		exp = *deadline
	}
	return exp
}

// ---- the outbox -------------------------------------------------------------------------------------------------

// sendBackoff is the wait after the n-th attempt: 30 s doubling, at most 30 minutes.
func sendBackoff(attempts int) time.Duration {
	d := 30 * time.Second
	for i := 1; i < attempts && d < 30*time.Minute; i++ {
		d *= 2
	}
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
}

// accountLinkBase is where an emailed link points: https://<canonical hostname> while the portal is on HTTPS,
// otherwise http://<portal address> (contract §4). Both are the LOCAL portal, reachable on the guest Wi-Fi before
// sign-in, which is why no walled-garden entry is needed. "" when the site has neither (no portal address).
func accountLinkBase(st PortalTLSState) string {
	if st.HTTPSActive && st.Canonical != "" {
		return "https://" + st.Canonical
	}
	addr := strings.TrimSpace(st.PortalIP)
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i]
	}
	if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
		return "http://" + ip.To4().String()
	}
	return ""
}

// The link paths portald serves: a GET shows a page whose button POSTs (mail scanners prefetch links).
const (
	accountVerifyPath = "/account/verify"
	accountResetPath  = "/account/reset"
)

// accountEmailMessage is the plain-text message for a token. Short, and it says what to do if it was not you.
func accountEmailMessage(purpose, to, link string, deadline *time.Time) mail.Message {
	if purpose == tokenPurposeReset {
		return mail.Message{To: to, Subject: "Reset your Wi-Fi account password",
			Text: "Someone asked to reset the password of the Wi-Fi account for this address.\n\n" +
				"To choose a new password, open this link within 30 minutes and follow the steps:\n" + link + "\n\n" +
				"If you did not ask for this, ignore this message: your password has not changed."}
	}
	text := "Please confirm this email address for your Wi-Fi account.\n\n" +
		"Open this link and press Verify:\n" + link + "\n\n"
	if deadline != nil {
		text += "Verify before " + deadline.UTC().Format("2006-01-02 15:04 UTC") + " to keep your Internet access.\n\n"
	}
	text += "If you did not create this account, you can ignore this message."
	return mail.Message{To: to, Subject: "Verify your email for Wi-Fi access", Text: text}
}

// sendOneAccountEmail claims the next due message, mints its token, sends it and records the outcome. Returns
// false when nothing is due (or nothing could be claimed).
//
// THE ORDER IS THE POINT. The token is minted and its HMAC committed, with the attempt counted and the next
// attempt scheduled, BEFORE the mail server is called: a crash or a timeout mid-send then leaves a row that is
// retried after its backoff, never one that is resent in a tight loop, and a message the server did accept
// carries a token the database already knows. A retry mints a new token, so at most the latest one sent works.
func (s *server) sendOneAccountEmail(ctx context.Context, key []byte) bool {
	var id, purpose, to string
	var deadline *time.Time
	var token string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var attempts int
		if err := tx.QueryRow(ctx, `
		    SELECT t.id::text, t.purpose, t.send_attempts, a.username, a.verification_deadline
		      FROM iam_v2.account_email_tokens t
		      JOIN iam_v2.guest_access_accounts a ON a.id = t.account_id
		     WHERE t.tenant_id = $1 AND t.site_id = $2
		       AND t.sent_at IS NULL AND t.used_at IS NULL AND t.superseded_at IS NULL
		       AND t.expires_at > now() AND t.next_send_at <= now() AND a.enabled
		     ORDER BY t.next_send_at
		     LIMIT 1
		     FOR UPDATE OF t SKIP LOCKED`, s.tenID, s.siteID).Scan(&id, &purpose, &attempts, &to, &deadline); err != nil {
			return err
		}
		var err error
		if token, err = newEmailToken(); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE iam_v2.account_email_tokens
		     SET token_hmac = $2, key_id = $3, send_attempts = send_attempts + 1,
		         next_send_at = now() + make_interval(secs => $4)
		   WHERE id = $1::uuid`, id, emailTokenHMAC(key, token), emailTokenKeyID(key), sendBackoff(attempts+1).Seconds())
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}
	if err != nil {
		slog.Warn("email accounts: outbox claim failed", "err", err)
		return false
	}
	record := func(sendErr error) {
		if sendErr == nil {
			_, _ = s.db.Exec(ctx, `UPDATE iam_v2.account_email_tokens SET sent_at = now(), last_send_error = NULL WHERE id = $1::uuid`, id)
			return
		}
		msg := sendErr.Error()
		if len(msg) > 480 {
			msg = msg[:480]
		}
		_, _ = s.db.Exec(ctx, `UPDATE iam_v2.account_email_tokens SET last_send_error = $2 WHERE id = $1::uuid`, id, msg)
	}
	base := accountLinkBase(s.portalTLS.State())
	if base == "" {
		slog.Warn("email accounts: no portal address to link to; the message stays queued")
		record(errors.New("NO_PORTAL_ADDRESS"))
		return true
	}
	path := accountVerifyPath
	if purpose == tokenPurposeReset {
		path = accountResetPath
	}
	link := base + path + "?t=" + url.QueryEscape(token)
	mailer, _, _ := s.senders()
	if mailer == nil {
		record(errors.New("NO_EMAIL_SENDER"))
		return true
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sendErr := mailer.Send(sctx, accountEmailMessage(purpose, to, link, deadline))
	if sendErr != nil {
		// Never the address: the log is read by people who should not learn who registered.
		slog.Warn("email accounts: message not delivered; will retry", "purpose", purpose, "err", sendErr)
	}
	record(sendErr)
	return true
}

func (s *server) sendDueAccountEmails(ctx context.Context) {
	if s.db == nil || s.tenID == "" {
		return
	}
	// A message still unsent when its link expires can never be sent: retire it, so the outbox index holds
	// only rows that can still go and a long mail outage does not leave each poll skipping a growing backlog.
	// So is one for an account staff REMOVED (edged guest_accounts_manage.go: disabled, username moved to
	// "<name>.removed-<id>.invalid"); a merely disabled account keeps its queue and is skipped until re-enabled.
	if _, err := s.db.Exec(ctx, `UPDATE iam_v2.account_email_tokens t SET superseded_at = now()
	     WHERE t.tenant_id = $1 AND t.site_id = $2
	       AND t.sent_at IS NULL AND t.used_at IS NULL AND t.superseded_at IS NULL
	       AND (t.expires_at <= now() OR EXISTS (
	             SELECT 1 FROM iam_v2.guest_access_accounts a
	              WHERE a.id = t.account_id AND NOT a.enabled AND lower(a.username) LIKE '%.removed-%.invalid'))`,
		s.tenID, s.siteID); err != nil {
		slog.Warn("email accounts: retiring expired unsent messages failed", "err", err)
	}
	key := s.selfServiceKey(accountEmailTokenDomain)
	if key == nil {
		return
	}
	for i := 0; i < selfServiceSendBatch; i++ {
		if !s.sendOneAccountEmail(ctx, key) {
			return
		}
	}
}

// selfServiceSendLoop drains the outbox every send tick, and at once when a message is queued.
func (s *server) selfServiceSendLoop(ctx context.Context) {
	tick := s.selfService.sendTick
	if tick <= 0 {
		tick = 15 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		s.sendDueAccountEmails(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.selfService.kick:
		}
	}
}

// ---- the sweep ----------------------------------------------------------------------------------------------------

// selfServiceSweepLoop runs the sweep every sweep tick (30 s by default).
func (s *server) selfServiceSweepLoop(ctx context.Context) {
	tick := s.selfService.sweepTick
	if tick <= 0 {
		tick = 30 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		s.selfServiceSweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// selfServiceSweep is one pass: deadlines for accounts that have none while verification is required, reminders,
// and the end of every live session of a suspended account.
func (s *server) selfServiceSweep(ctx context.Context) {
	if s.db == nil || s.tenID == "" || s.siteID == "" {
		return
	}
	set := s.selfServiceSettingsOrDefault(ctx)
	if !set.VerificationRequired {
		return
	}
	queued := s.sweepGiveDeadlines(ctx, set)
	if s.sweepReminders(ctx, set) {
		queued = true
	}
	if queued {
		s.kickSelfServiceOutbox()
	}
	if set.SuspensionPolicy == "SUSPEND" {
		s.sweepSuspended(ctx)
	}
}

// sweepGiveDeadlines: an unverified self-registered account with no deadline registered while the site did not
// require verification. Now that it does, the account gets the full deadline from now -- never one already in
// the past -- and a verification message.
func (s *server) sweepGiveDeadlines(ctx context.Context, set selfServiceSettings) bool {
	rows, err := s.db.Query(ctx, `
	    SELECT id::text FROM iam_v2.guest_access_accounts
	     WHERE tenant_id = $1 AND site_id = $2 AND self_registered AND enabled
	       AND email_verified_at IS NULL AND verification_deadline IS NULL
	     LIMIT 100`, s.tenID, s.siteID)
	if err != nil {
		slog.Warn("email accounts: sweep (deadlines) failed", "err", err)
		return false
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	queued := false
	for _, id := range ids {
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var deadline time.Time
			if err := tx.QueryRow(ctx, `UPDATE iam_v2.guest_access_accounts
			     SET verification_deadline = now() + make_interval(hours => $2)
			   WHERE id = $1::uuid AND email_verified_at IS NULL AND verification_deadline IS NULL
			   RETURNING verification_deadline`, id, set.DeadlineHours).Scan(&deadline); err != nil {
				return err
			}
			return queueEmailToken(ctx, tx, s.tenID, s.siteID, id, tokenPurposeVerify, verifyTokenExpiry(&deadline, time.Now()))
		})
		if err == nil {
			queued = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("email accounts: could not give an account its deadline", "err", err)
		}
	}
	return queued
}

// sweepReminders queues the one reminder `reminder_hours_before` the deadline (0 = off). The reminder is a fresh
// VERIFY message; the earlier link is superseded by it, so exactly one live link exists.
func (s *server) sweepReminders(ctx context.Context, set selfServiceSettings) bool {
	if set.ReminderHoursBefore <= 0 {
		return false
	}
	rows, err := s.db.Query(ctx, `
	    SELECT id::text FROM iam_v2.guest_access_accounts
	     WHERE tenant_id = $1 AND site_id = $2 AND self_registered AND enabled
	       AND email_verified_at IS NULL AND verification_reminder_sent_at IS NULL
	       AND verification_deadline > now()
	       AND verification_deadline <= now() + make_interval(hours => $3)
	     LIMIT 100`, s.tenID, s.siteID, set.ReminderHoursBefore)
	if err != nil {
		slog.Warn("email accounts: sweep (reminders) failed", "err", err)
		return false
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	queued := false
	for _, id := range ids {
		err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
			var deadline time.Time
			if err := tx.QueryRow(ctx, `UPDATE iam_v2.guest_access_accounts SET verification_reminder_sent_at = now()
			   WHERE id = $1::uuid AND verification_reminder_sent_at IS NULL AND email_verified_at IS NULL
			   RETURNING verification_deadline`, id).Scan(&deadline); err != nil {
				return err
			}
			return queueEmailToken(ctx, tx, s.tenID, s.siteID, id, tokenPurposeVerify, verifyTokenExpiry(&deadline, time.Now()))
		})
		if err == nil {
			queued = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			slog.Warn("email accounts: could not queue a reminder", "err", err)
		}
	}
	return queued
}

// sweepSuspended ends every live session whose entitlement belongs to a suspended account. The SQL is the
// accountSuspended predicate for a site whose settings say SUSPEND (the caller checked them): self-registered,
// unverified, past the deadline, no running grace.
func (s *server) sweepSuspended(ctx context.Context) {
	rows, err := s.db.Query(ctx, `
	    SELECT se.id::text, COALESCE(host(se.ip), '')
	      FROM iam_v2.sessions se
	      JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
	      JOIN iam_v2.guest_access_accounts a ON a.id = e.guest_account_id
	     WHERE se.tenant_id = $1 AND se.site_id = $2
	       AND se.ended IS NULL AND se.state IN ('active','PENDING_ENFORCEMENT')
	       AND a.self_registered AND a.email_verified_at IS NULL
	       AND a.verification_deadline IS NOT NULL AND a.verification_deadline < now()
	       AND (a.grace_until IS NULL OR a.grace_until <= now())
	     LIMIT 200`, s.tenID, s.siteID)
	if err != nil {
		slog.Warn("email accounts: sweep (suspension) failed", "err", err)
		return
	}
	type live struct{ id, ip string }
	var due []live
	for rows.Next() {
		var l live
		if rows.Scan(&l.id, &l.ip) == nil {
			due = append(due, l)
		}
	}
	rows.Close()
	for _, l := range due {
		if err := s.endSessionNow(ctx, l.id, net.ParseIP(l.ip), endReasonVerificationSuspended); err != nil {
			slog.Warn("email accounts: could not end a suspended account's session", "err", err)
		}
	}
	if len(due) > 0 {
		slog.Info("email accounts: ended the sessions of suspended accounts", "sessions", len(due))
	}
}

// endSessionNow is the operator-disconnect path for one session: the row is ended first (so netd's convergence
// can only agree), then the address leaves the authorization set and its shaping class goes (denyAtGate, the
// same kernel steps /v1/sessions/revoke takes). Idempotent: an already-ended session changes nothing.
//
// The row is ended through the controlled session-end writer (endSessionControlled), which -- unlike a raw
// UPDATE -- may also end a session still PENDING_ENFORCEMENT.
func (s *server) endSessionNow(ctx context.Context, sessionID string, ip net.IP, reason string) error {
	ended, err := s.endSessionControlled(ctx, s.db, sessionID, reason)
	if err != nil {
		return err
	}
	if !ended {
		return nil
	}
	if ip != nil {
		s.denyAtGate(ctx, ip)
	}
	if s.met != nil && s.met.SessionsClosed != nil {
		s.met.SessionsClosed.WithLabelValues(reason).Inc()
	}
	return nil
}

// endLiveSessionsOfAccount ends every live session of an account (a completed password reset: whoever held the
// old password -- a squatter on someone else's address -- loses the connection with it).
func (s *server) endLiveSessionsOfAccount(ctx context.Context, accountID, reason string) int {
	rows, err := s.db.Query(ctx, `
	    SELECT se.id::text, COALESCE(host(se.ip), '')
	      FROM iam_v2.sessions se JOIN iam_v2.entitlements e ON e.id = se.entitlement_id
	     WHERE e.guest_account_id = $1::uuid AND se.tenant_id = $2 AND se.site_id = $3
	       AND se.ended IS NULL AND se.state IN ('active','PENDING_ENFORCEMENT')`, accountID, s.tenID, s.siteID)
	if err != nil {
		slog.Warn("email accounts: could not list an account's sessions", "err", err)
		return 0
	}
	type live struct{ id, ip string }
	var all []live
	for rows.Next() {
		var l live
		if rows.Scan(&l.id, &l.ip) == nil {
			all = append(all, l)
		}
	}
	rows.Close()
	n := 0
	for _, l := range all {
		if err := s.endSessionNow(ctx, l.id, net.ParseIP(l.ip), reason); err != nil {
			slog.Warn("email accounts: could not end a session", "err", err)
			continue
		}
		n++
	}
	return n
}
