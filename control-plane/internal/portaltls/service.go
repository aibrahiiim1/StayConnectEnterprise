package portaltls

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service is Central's portal hostname and certificate authority-of-record. One per ctrlapi process: the API
// handlers call it, and Run is its worker.
type Service struct {
	DB      *pgxpool.Pool
	Opts    Options
	Secrets Secrets
	// HTTP is used for DNS-over-HTTPS checks and the DNS provider API (not for ACME, see Options).
	HTTP *http.Client

	// NewProvider builds the DNS provider for the current settings; tests replace it.
	NewProvider func(Settings) (DNSProvider, error)

	// Overrides names the TEST-ONLY environment overrides in force (OptionsFromEnv); shown to operators.
	Overrides []string

	acmeMu sync.Mutex // one ACME conversation at a time: orders, revocations, account registration
	nudge  chan struct{}
}

// New returns a Service for the pool and options.
func New(db *pgxpool.Pool, opts Options) *Service {
	s := &Service{
		DB: db, Opts: opts, Secrets: Secrets{Dir: opts.SecretsDir},
		HTTP:  &http.Client{Timeout: 20 * time.Second},
		nudge: make(chan struct{}, 1),
	}
	s.NewProvider = s.defaultProvider
	return s
}

func (s *Service) defaultProvider(st Settings) (DNSProvider, error) {
	switch s.Opts.DNSProvider {
	case ProviderChalltestsrv:
		return &Challtestsrv{BaseURL: s.Opts.ChalltestsrvURL, Zone: st.ZoneName, NameServers: s.Opts.DNSCheckServers, HTTP: s.HTTP}, nil
	default:
		tok, err := s.Secrets.ReadToken()
		if err != nil {
			return nil, err
		}
		return &Cloudflare{Token: tok, ZoneName: st.ZoneName, ZoneID: st.ZoneID, BaseURL: s.Opts.CloudflareAPI, HTTP: s.HTTP}, nil
	}
}

// Nudge wakes the worker early (a new order, a revocation request). Never blocks.
func (s *Service) Nudge() {
	if s == nil || s.nudge == nil {
		return
	}
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Availability says whether Central can obtain certificates at all, and if not, what is missing. Appliances
// see only the boolean (available=false -> they report NOT_CONFIGURED); operators see the reason.
func (s *Service) Availability(st Settings) (bool, string) {
	if st.ContactEmail == "" {
		return false, "no ACME contact email is set"
	}
	if s.Opts.DNSProvider == ProviderChalltestsrv {
		return true, "" // TEST-ONLY provider: no token, no zone id
	}
	if !s.Secrets.TokenSet() {
		return false, "no DNS API token is stored"
	}
	if st.ZoneID == "" {
		return false, "the DNS zone has not been verified yet (Test DNS access)"
	}
	return true, ""
}

func (s *Service) available(ctx context.Context) (Settings, bool) {
	st, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return st, false
	}
	ok, _ := s.Availability(st)
	return st, ok
}

// ---------------------------------------------------------------------------------------------------------
// Refusals: what an appliance or operator request is refused with. The API layer turns them into HTTP.
// ---------------------------------------------------------------------------------------------------------

// Refusal is an expected, explained refusal (never an internal error).
type Refusal struct {
	Status  int
	Code    string
	Message string
	Extra   map[string]any
}

func (r *Refusal) Error() string { return r.Message }

func refuse(status int, code, format string, a ...any) *Refusal {
	return &Refusal{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

// ---------------------------------------------------------------------------------------------------------
// Events: the append-only audit trail of everything this package does (portal_tls_events).
// ---------------------------------------------------------------------------------------------------------

// Event kinds.
const (
	EvHostnameAllocated = "HOSTNAME_ALLOCATED"
	EvHostnameRestored  = "HOSTNAME_RESTORED"
	EvHostnameReleased  = "HOSTNAME_RELEASED"
	EvPortalIPChanged   = "PORTAL_IP_CHANGED"
	EvDNSSynced         = "DNS_SYNCED"
	EvDNSDeleted        = "DNS_DELETED"
	EvDNSError          = "DNS_ERROR"
	EvOrderQueued       = "ORDER_QUEUED"
	EvOrderDeferred     = "ORDER_DEFERRED"
	EvOrderRetry        = "ORDER_RETRY"
	EvOrderIssued       = "ORDER_ISSUED"
	EvOrderFailed       = "ORDER_FAILED"
	EvRevokeRequested   = "CERT_REVOKE_REQUESTED"
	EvCertRevoked       = "CERT_REVOKED"
	EvRevokeError       = "CERT_REVOKE_ERROR"
	EvARIUpdated        = "ARI_UPDATED"
	EvDomainRegistered  = "DOMAIN_REGISTERED"
	EvDomainVerified    = "DOMAIN_VERIFIED"
	EvDomainFailed      = "DOMAIN_FAILED"
	EvDomainRemoved     = "DOMAIN_REMOVED"
	EvSettingsChanged   = "SETTINGS_CHANGED"
	EvTokenSet          = "TOKEN_SET"
	EvAccessTested      = "DNS_ACCESS_TESTED"
	EvAccountRegistered = "ACME_ACCOUNT_REGISTERED"
	EvCAAApplied        = "CAA_APPLIED"
)

// Actor for things the worker does by itself.
const ActorSystem = "system"

// AppendEvent writes one event. detail must never hold a secret (tokens, keys) -- only names, ids, states and
// error text. A failure is logged, never returned: the audit trail must not be able to stop the work.
func AppendEvent(ctx context.Context, db DB, applianceID, hostname, kind, actor string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	raw, _ := json.Marshal(detail)
	if _, err := db.Exec(ctx, `
        INSERT INTO portal_tls_events (appliance_id, hostname, kind, actor, detail)
        VALUES (NULLIF($1,'')::uuid, NULLIF($2,''), $3, NULLIF($4,''), $5::jsonb)`,
		applianceID, hostname, kind, actor, string(raw)); err != nil {
		slog.Warn("portal tls event write failed", "kind", kind, "err", err)
	}
}

// ---------------------------------------------------------------------------------------------------------
// Appliance identity checks
// ---------------------------------------------------------------------------------------------------------

type applianceFacts struct {
	lifecycle   string
	activatedAt *time.Time
}

// requireActive refuses an appliance that is not activated. Only an assigned (activated) appliance holds a
// portal hostname; a waiting one has no customer yet, a retired one gave its name back.
func (s *Service) requireActive(ctx context.Context, applianceID string) (applianceFacts, error) {
	var f applianceFacts
	err := s.DB.QueryRow(ctx, `SELECT COALESCE(lifecycle_state,''), activated_at FROM appliances WHERE id=$1`, applianceID).
		Scan(&f.lifecycle, &f.activatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, refuse(http.StatusForbidden, "forbidden", "unknown appliance")
	}
	if err != nil {
		return f, err
	}
	if f.lifecycle != "assigned" {
		return f, refuse(http.StatusForbidden, "not_active", "only an activated appliance has a portal hostname")
	}
	return f, nil
}

// hostRow is one portal_hostnames row.
type hostRow struct {
	Label       string
	FQDN        string
	PortalIP    string
	RecordID    string
	SyncedIP    string
	SyncedAt    *time.Time
	DNSError    string
	ReleasedAt  *time.Time
	ApplianceID string
}

const hostCols = `label, fqdn, COALESCE(host(portal_ip),''), COALESCE(dns_record_id,''), COALESCE(host(dns_synced_ip),''),
       dns_synced_at, COALESCE(dns_error,''), released_at, appliance_id::text`

func scanHost(row pgx.Row) (hostRow, error) {
	var h hostRow
	err := row.Scan(&h.Label, &h.FQDN, &h.PortalIP, &h.RecordID, &h.SyncedIP, &h.SyncedAt, &h.DNSError, &h.ReleasedAt, &h.ApplianceID)
	return h, err
}

// DNSState is what the appliance is told about its A record.
func (h hostRow) DNSState() string {
	switch {
	case h.DNSError != "":
		return "ERROR"
	case h.RecordID != "" && h.PortalIP != "" && h.SyncedIP == h.PortalIP:
		return "SYNCED"
	default:
		return "PENDING"
	}
}

func isUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// allocateHostname gives the appliance its permanent hostname, once. A label is random, so a collision is
// handled by drawing again; a label that was ever allocated (its event outlives a deleted appliance row) is
// never drawn twice -- a name that once pointed at one hotel must never point at another.
func (s *Service) allocateHostname(ctx context.Context, applianceID, zone string) (hostRow, error) {
	for attempt := 0; attempt < 8; attempt++ {
		label, err := NewLabel()
		if err != nil {
			return hostRow{}, err
		}
		fqdn := HostnameFor(label, zone)
		var used bool
		_ = s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM portal_tls_events WHERE kind=$1 AND hostname=$2)`,
			EvHostnameAllocated, fqdn).Scan(&used)
		if used {
			continue
		}
		h, err := scanHost(s.DB.QueryRow(ctx, `
            INSERT INTO portal_hostnames (appliance_id, label, fqdn) VALUES ($1,$2,$3)
            ON CONFLICT DO NOTHING
            RETURNING `+hostCols, applianceID, label, fqdn))
		if err == nil {
			AppendEvent(ctx, s.DB, applianceID, fqdn, EvHostnameAllocated, ActorSystem, map[string]any{"label": label})
			return h, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return hostRow{}, err
		}
		// Nothing inserted: either a concurrent sync allocated this appliance's name (use it) or the label
		// collided (draw again).
		if h, err := scanHost(s.DB.QueryRow(ctx, `SELECT `+hostCols+` FROM portal_hostnames WHERE appliance_id=$1`, applianceID)); err == nil {
			return h, nil
		}
	}
	return hostRow{}, errors.New("could not allocate a unique portal hostname")
}

// ---------------------------------------------------------------------------------------------------------
// POST /v1/appliance/portal-tls/sync
// ---------------------------------------------------------------------------------------------------------

// SyncRequest is the appliance's report (contract §7).
type SyncRequest struct {
	PortalIP     string  `json:"portal_ip"`
	CustomDomain *string `json:"custom_domain"`
}

// Record is a DNS record the hotel must publish.
type Record struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// DomainView is the hotel domain as the appliance sees it.
type DomainView struct {
	FQDN    string   `json:"fqdn"`
	Status  string   `json:"status"`
	Records []Record `json:"records"`
	Error   *string  `json:"error"`
}

// CertView is one hostname's certificate state.
type CertView struct {
	Hostname   string     `json:"hostname"`
	Kind       string     `json:"kind"`
	OrderID    *string    `json:"order_id"`
	Status     *string    `json:"status"`
	Serial     *string    `json:"serial"`
	NotAfter   *time.Time `json:"not_after"`
	RenewAfter *time.Time `json:"renew_after"`
	Revoked    bool       `json:"revoked"`
	Error      *string    `json:"error"`
}

// SyncResponse is the answer to sync (contract §7).
type SyncResponse struct {
	Available    bool        `json:"available"`
	Hostname     string      `json:"hostname"`
	DNSState     string      `json:"dns_state"`
	CustomDomain *DomainView `json:"custom_domain"`
	Certificates []CertView  `json:"certificates"`
}

// Sync is idempotent: it allocates the hostname once, records the portal address (and publishes it when it
// changed), reconciles the hotel domain with what the appliance asks for, and reports the state of both.
func (s *Service) Sync(ctx context.Context, applianceID string, in SyncRequest) (*SyncResponse, error) {
	facts, err := s.requireActive(ctx, applianceID)
	if err != nil {
		return nil, err
	}
	ip, err := ParsePortalIP(in.PortalIP)
	if err != nil {
		return nil, refuse(http.StatusUnprocessableEntity, "invalid_portal_ip", "%s", err.Error())
	}
	st, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	available, _ := s.Availability(st)

	h, err := scanHost(s.DB.QueryRow(ctx, `SELECT `+hostCols+` FROM portal_hostnames WHERE appliance_id=$1`, applianceID))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if h, err = s.allocateHostname(ctx, applianceID, st.ZoneName); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case h.ReleasedAt != nil:
		// Released by retirement. The same appliance identity can be activated again later (re-registration
		// keeps its id): then, and only then, it gets its own name back. A box that is merely still talking
		// while it retires does not.
		if facts.activatedAt == nil || !facts.activatedAt.After(*h.ReleasedAt) {
			return nil, refuse(http.StatusForbidden, "retired", "this appliance's portal hostname was released when it was retired")
		}
		if _, err := s.DB.Exec(ctx, `UPDATE portal_hostnames SET released_at=NULL, dns_error=NULL WHERE appliance_id=$1`, applianceID); err != nil {
			return nil, err
		}
		h.ReleasedAt = nil
		AppendEvent(ctx, s.DB, applianceID, h.FQDN, EvHostnameRestored, ActorSystem, nil)
	}

	if h.PortalIP != ip.String() {
		if _, err := s.DB.Exec(ctx, `UPDATE portal_hostnames SET portal_ip=$2::inet, dns_error=NULL WHERE appliance_id=$1`,
			applianceID, ip.String()); err != nil {
			return nil, err
		}
		AppendEvent(ctx, s.DB, applianceID, h.FQDN, EvPortalIPChanged, ActorSystem, map[string]any{"from": h.PortalIP, "to": ip.String()})
		h.PortalIP, h.DNSError = ip.String(), ""
		// Publish now when we can, so the appliance hears SYNCED on this answer rather than a cycle later;
		// the worker retries whatever this does not finish.
		if available {
			if prov, perr := s.NewProvider(st); perr == nil {
				dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				h = s.syncHostDNS(dctx, prov, h)
				cancel()
			}
		}
	}

	dom, err := s.reconcileDomain(ctx, applianceID, h, in.CustomDomain, st)
	if err != nil {
		return nil, err
	}
	certs, err := s.certViews(ctx, applianceID, h, dom)
	if err != nil {
		return nil, err
	}
	return &SyncResponse{Available: available, Hostname: h.FQDN, DNSState: h.DNSState(), CustomDomain: dom, Certificates: certs}, nil
}

// syncHostDNS brings one hostname's A record in line: published at its portal address while the hostname is
// live, deleted once it is released. Returns the row as it now stands. Errors are recorded on the row (and as
// an event when the error changes, so a provider outage is one event, not one every 30 seconds).
func (s *Service) syncHostDNS(ctx context.Context, prov DNSProvider, h hostRow) hostRow {
	if h.ReleasedAt != nil {
		if h.RecordID == "" {
			return h
		}
		if err := prov.DeleteRecord(ctx, h.RecordID); err != nil {
			s.recordDNSError(ctx, &h, err)
			return h
		}
		_, _ = s.DB.Exec(ctx, `UPDATE portal_hostnames SET dns_record_id=NULL, dns_synced_ip=NULL, dns_synced_at=now(), dns_error=NULL
                                WHERE appliance_id=$1`, h.ApplianceID)
		AppendEvent(ctx, s.DB, h.ApplianceID, h.FQDN, EvDNSDeleted, ActorSystem, map[string]any{"record_id": h.RecordID})
		h.RecordID, h.SyncedIP, h.DNSError = "", "", ""
		return h
	}
	if h.PortalIP == "" || (h.RecordID != "" && h.SyncedIP == h.PortalIP && h.DNSError == "") {
		return h
	}
	id, err := prov.UpsertA(ctx, h.FQDN, h.PortalIP)
	if err != nil {
		s.recordDNSError(ctx, &h, err)
		return h
	}
	now := time.Now()
	_, _ = s.DB.Exec(ctx, `UPDATE portal_hostnames SET dns_record_id=$2, dns_synced_ip=$3::inet, dns_synced_at=now(), dns_error=NULL
                            WHERE appliance_id=$1`, h.ApplianceID, id, h.PortalIP)
	AppendEvent(ctx, s.DB, h.ApplianceID, h.FQDN, EvDNSSynced, ActorSystem, map[string]any{"ip": h.PortalIP, "record_id": id})
	h.RecordID, h.SyncedIP, h.SyncedAt, h.DNSError = id, h.PortalIP, &now, ""
	return h
}

func (s *Service) recordDNSError(ctx context.Context, h *hostRow, err error) {
	msg := truncate(err.Error(), 500)
	if msg != h.DNSError {
		AppendEvent(ctx, s.DB, h.ApplianceID, h.FQDN, EvDNSError, ActorSystem, map[string]any{"error": msg})
	}
	_, _ = s.DB.Exec(ctx, `UPDATE portal_hostnames SET dns_error=$2 WHERE appliance_id=$1`, h.ApplianceID, msg)
	h.DNSError = msg
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------------------------------------
// Hotel domains
// ---------------------------------------------------------------------------------------------------------

type domainRow struct {
	ID             string
	ApplianceID    string
	FQDN           string
	VerifyToken    string
	ChallengeLabel string
	Status         string
	LastCheckedAt  *time.Time
	LastError      string
}

const domainCols = `id::text, appliance_id::text, fqdn, verify_token, challenge_label, status, last_checked_at, COALESCE(last_error,'')`

func scanDomain(row pgx.Row) (domainRow, error) {
	var d domainRow
	err := row.Scan(&d.ID, &d.ApplianceID, &d.FQDN, &d.VerifyToken, &d.ChallengeLabel, &d.Status, &d.LastCheckedAt, &d.LastError)
	return d, err
}

func (s *Service) liveDomain(ctx context.Context, db DB, applianceID string) (*domainRow, error) {
	d, err := scanDomain(db.QueryRow(ctx, `SELECT `+domainCols+` FROM portal_custom_domains WHERE appliance_id=$1 AND status<>'REMOVED'`, applianceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// DomainRecords are the three records the hotel publishes (contract §3.1).
func DomainRecords(d domainRow, hostname, zone string) []Record {
	return []Record{
		{Type: "TXT", Name: "_onegate-verify." + d.FQDN, Value: d.VerifyToken},
		{Type: "CNAME", Name: d.FQDN, Value: hostname + "."},
		{Type: "CNAME", Name: "_acme-challenge." + d.FQDN, Value: d.ChallengeLabel + "." + ChallengeSubdomain + "." + zone + "."},
	}
}

func domainView(d domainRow, hostname, zone string) *DomainView {
	v := &DomainView{FQDN: d.FQDN, Status: d.Status, Records: DomainRecords(d, hostname, zone)}
	if d.LastError != "" {
		e := d.LastError
		v.Error = &e
	}
	return v
}

// domainCheckEvery bounds how often an appliance's sync re-checks a PENDING or FAILED domain: each check is
// eight DNS-over-HTTPS queries at two public resolvers, and an appliance syncs far more often than a hotel's
// IT publishes records.
const domainCheckEvery = 60 * time.Second

func (s *Service) reconcileDomain(ctx context.Context, applianceID string, h hostRow, want *string, st Settings) (*DomainView, error) {
	live, err := s.liveDomain(ctx, s.DB, applianceID)
	if err != nil {
		return nil, err
	}
	raw := ""
	if want != nil {
		raw = strings.TrimSpace(*want)
	}
	if raw == "" {
		if live != nil {
			if err := RemoveDomain(ctx, s.DB, *live, "removed on the appliance", ActorSystem); err != nil {
				return nil, err
			}
			s.Nudge()
		}
		return nil, nil
	}
	fqdn, verr := ValidateCustomDomain(raw, st.ZoneName)
	if verr != nil {
		// Refused without a row: the name is not something anyone can claim. Whatever was claimed before
		// stays as it was until the appliance asks for something valid (or for nothing).
		msg := verr.Error()
		return &DomainView{FQDN: NormalizeFQDN(raw), Status: "FAILED", Records: []Record{}, Error: &msg}, nil
	}
	if live != nil && live.FQDN != fqdn {
		if err := RemoveDomain(ctx, s.DB, *live, "replaced by "+fqdn, ActorSystem); err != nil {
			return nil, err
		}
		s.Nudge()
		live = nil
	}
	if live == nil {
		token, err := NewVerifyToken()
		if err != nil {
			return nil, err
		}
		var d domainRow
		for attempt := 0; attempt < 5; attempt++ {
			cl, err := NewChallengeLabel()
			if err != nil {
				return nil, err
			}
			d, err = scanDomain(s.DB.QueryRow(ctx, `
                INSERT INTO portal_custom_domains (appliance_id, fqdn, verify_token, challenge_label)
                VALUES ($1,$2,$3,$4) RETURNING `+domainCols, applianceID, fqdn, token, cl))
			if err == nil {
				break
			}
			if !isUniqueViolation(err) {
				return nil, err
			}
			// Which unique index? A claim by another appliance is final; a challenge-label collision is not.
			var other bool
			_ = s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM portal_custom_domains WHERE fqdn=$1 AND status<>'REMOVED')`, fqdn).Scan(&other)
			if other {
				msg := "this name is already claimed by another OneGate appliance"
				return &DomainView{FQDN: fqdn, Status: "FAILED", Records: []Record{}, Error: &msg}, nil
			}
			d = domainRow{}
		}
		if d.ID == "" {
			return nil, errors.New("could not register the domain")
		}
		AppendEvent(ctx, s.DB, applianceID, fqdn, EvDomainRegistered, ActorSystem, nil)
		live = &d
	}
	if live.Status != "VERIFIED" && (live.LastCheckedAt == nil || time.Since(*live.LastCheckedAt) >= domainCheckEvery) {
		s.checkAndRecordDomain(ctx, live, h.FQDN, st)
	}
	return domainView(*live, h.FQDN, st.ZoneName), nil
}

// checkAndRecordDomain runs the two-resolver check and records the outcome on the row (and in d):
//
//	PENDING  + pass -> VERIFIED        PENDING  + fail -> PENDING (records not published yet is normal)
//	VERIFIED + pass -> VERIFIED        VERIFIED + fail -> FAILED  (a delegation that broke: no renewals)
//	FAILED   + pass -> VERIFIED        FAILED   + fail -> FAILED
func (s *Service) checkAndRecordDomain(ctx context.Context, d *domainRow, hostname string, st Settings) bool {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err := CheckDomainEverywhere(cctx, s.Opts.dohURLs(), s.HTTP, DomainCheck{
		FQDN: d.FQDN, VerifyToken: d.VerifyToken, CNAMETarget: hostname,
		ChallengeAlias: d.ChallengeLabel + "." + ChallengeSubdomain + "." + st.ZoneName,
		AccountURL:     st.AccountURL,
	})
	prev := d.Status
	if err == nil {
		d.Status, d.LastError = "VERIFIED", ""
		_, _ = s.DB.Exec(ctx, `UPDATE portal_custom_domains SET status='VERIFIED', verified_at=COALESCE(CASE WHEN status='VERIFIED' THEN verified_at END, now()),
                                last_checked_at=now(), last_error=NULL WHERE id=$1 AND status<>'REMOVED'`, d.ID)
		if prev != "VERIFIED" {
			AppendEvent(ctx, s.DB, d.ApplianceID, d.FQDN, EvDomainVerified, ActorSystem, nil)
		}
		now := time.Now()
		d.LastCheckedAt = &now
		return true
	}
	msg := truncate(err.Error(), 500)
	next := prev
	if prev == "VERIFIED" {
		next = "FAILED"
	}
	_, _ = s.DB.Exec(ctx, `UPDATE portal_custom_domains SET status=$2, last_checked_at=now(), last_error=$3 WHERE id=$1 AND status<>'REMOVED'`,
		d.ID, next, msg)
	if next == "FAILED" && (prev != "FAILED" || msg != d.LastError) {
		AppendEvent(ctx, s.DB, d.ApplianceID, d.FQDN, EvDomainFailed, ActorSystem, map[string]any{"error": msg})
	}
	d.Status, d.LastError = next, msg
	now := time.Now()
	d.LastCheckedAt = &now
	return false
}

// RemoveDomain ends a hotel domain claim: REMOVED, its open orders cancelled, and every certificate issued
// for it that is still valid queued for revocation (contract §3.1 "Removal revokes its certificate").
// Idempotent.
func RemoveDomain(ctx context.Context, db DB, d domainRow, why, actor string) error {
	tag, err := db.Exec(ctx, `UPDATE portal_custom_domains SET status='REMOVED', removed_at=now() WHERE id=$1 AND status<>'REMOVED'`, d.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		AppendEvent(ctx, db, d.ApplianceID, d.FQDN, EvDomainRemoved, actor, map[string]any{"reason": why})
	}
	return requestRevocation(ctx, db, d.ApplianceID, d.FQDN, RevokeCessation, actor, why, true)
}

// ACME revocation reasons Central uses (RFC 5280 CRLReason names). keyCompromise is deliberately absent:
// Central never holds an appliance key, and Let's Encrypt accepts that reason only from the key itself.
const (
	RevokeCessation  = "cessationOfOperation"
	RevokeSuperseded = "superseded"
)

// requestRevocation marks every still-valid issued certificate of the appliance (one hostname, or all when
// hostname is "") for revocation and, when cancelOpen, cancels its open orders (the name is going away; an
// operator's revocation keeps them, since a fresh certificate is exactly what it wants). The worker performs
// the revocation.
func requestRevocation(ctx context.Context, db DB, applianceID, hostname, reason, actor, why string, cancelOpen bool) error {
	rows, err := db.Query(ctx, `
        UPDATE portal_certificate_orders
           SET revoke_requested_at=now(), revoke_reason=$3
         WHERE appliance_id=$1 AND ($2='' OR hostname=$2)
           AND status IN ('ISSUED','SUPERSEDED') AND revoke_requested_at IS NULL AND revoked_at IS NULL
           AND not_after > now()
        RETURNING id::text, hostname, COALESCE(serial,'')`, applianceID, hostname, reason)
	if err != nil {
		return err
	}
	type rv struct{ id, host, serial string }
	var marked []rv
	for rows.Next() {
		var r rv
		if rows.Scan(&r.id, &r.host, &r.serial) == nil {
			marked = append(marked, r)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range marked {
		AppendEvent(ctx, db, applianceID, r.host, EvRevokeRequested, actor, map[string]any{
			"order_id": r.id, "serial": r.serial, "reason": reason, "why": why})
	}
	if !cancelOpen {
		return nil
	}
	_, err = db.Exec(ctx, `
        UPDATE portal_certificate_orders SET status='FAILED', error=$3
         WHERE appliance_id=$1 AND ($2='' OR hostname=$2) AND status IN ('QUEUED','PROCESSING')`,
		applianceID, hostname, "cancelled: "+why)
	return err
}

// ReleaseAppliance is the portal-TLS half of the one central termination path (api.revokeApplianceBoundLicenses):
// the hostname is released (its A record is deleted by the worker and the label is never reused), every valid
// certificate is queued for revocation, open orders are cancelled and the hotel domain claim ends. Runs inside
// the caller's transaction. Idempotent.
func ReleaseAppliance(ctx context.Context, db DB, applianceID, actor string) error {
	var fqdn string
	err := db.QueryRow(ctx, `UPDATE portal_hostnames SET released_at=now() WHERE appliance_id=$1 AND released_at IS NULL RETURNING fqdn`,
		applianceID).Scan(&fqdn)
	switch {
	case err == nil:
		AppendEvent(ctx, db, applianceID, fqdn, EvHostnameReleased, actor, nil)
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if _, err := db.Exec(ctx, `
        UPDATE portal_custom_domains SET status='REMOVED', removed_at=now()
         WHERE appliance_id=$1 AND status<>'REMOVED'`, applianceID); err != nil {
		return err
	}
	return requestRevocation(ctx, db, applianceID, "", RevokeCessation, actor, "appliance retired", true)
}

// ---------------------------------------------------------------------------------------------------------
// Certificate views
// ---------------------------------------------------------------------------------------------------------

// certViews reports, per hostname the appliance should serve, the newest order (what is in progress, or why
// the last attempt failed) and the newest issued certificate (what it should have installed).
func (s *Service) certViews(ctx context.Context, applianceID string, h hostRow, dom *DomainView) ([]CertView, error) {
	type hk struct{ host, kind string }
	hosts := []hk{{h.FQDN, KindOneGate}}
	// A domain view without records is a refused name (no claim row): it has no certificate either.
	if dom != nil && len(dom.Records) > 0 {
		hosts = append(hosts, hk{dom.FQDN, KindCustom})
	}
	out := make([]CertView, 0, len(hosts))
	for _, x := range hosts {
		v := CertView{Hostname: x.host, Kind: x.kind}
		var id, status, errText string
		err := s.DB.QueryRow(ctx, `
            SELECT id::text, status, COALESCE(error,'') FROM portal_certificate_orders
             WHERE appliance_id=$1 AND hostname=$2 ORDER BY created_at DESC LIMIT 1`, applianceID, x.host).
			Scan(&id, &status, &errText)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			v.OrderID, v.Status = &id, &status
			if errText != "" {
				v.Error = &errText
			}
		}
		var serial string
		var notAfter, renewAfter *time.Time
		var revoked bool
		err = s.DB.QueryRow(ctx, `
            SELECT COALESCE(serial,''), not_after, renew_after, (revoke_requested_at IS NOT NULL OR revoked_at IS NOT NULL)
              FROM portal_certificate_orders
             WHERE appliance_id=$1 AND hostname=$2 AND issued_at IS NOT NULL AND status IN ('ISSUED','REVOKED')
             ORDER BY issued_at DESC LIMIT 1`, applianceID, x.host).Scan(&serial, &notAfter, &renewAfter, &revoked)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if err == nil {
			v.Serial, v.NotAfter, v.RenewAfter, v.Revoked = &serial, notAfter, renewAfter, revoked
		}
		out = append(out, v)
	}
	return out, nil
}
