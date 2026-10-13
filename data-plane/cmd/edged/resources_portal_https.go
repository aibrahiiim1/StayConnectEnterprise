package main

// PORTAL HTTPS — the Client Portal's own address, its hostnames and its certificates
// (docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §2.1, §3.1, §4, §8).
//
// What this surface OWNS is small: the two operator settings in public.portal_https_settings (the portal
// address and the hotel portal domain), each change recorded in portal_https_settings_changes. Everything
// else it shows -- the OneGate hostname, the hotel domain's verification state, which hostname is canonical,
// whether HTTPS is active or the approved HTTP fallback is in force, the certificates -- is what scd last
// learnt from Central and installed. edged reads that (public.portal_tls_state, public.portal_certificates)
// and never writes it; it asks scd to re-sync or renew over scd's local socket.
//
// Permissions: resource key "portal-https". READ is portal_https.read; WRITE is portal_https.manage. The three
// changes that move the portal or its certificates -- the address, the hotel domain, Renew now -- additionally
// require the operator's password (step-up), the same re-authentication every sensitive setting uses here.
//
// No key material is ever read or returned: the private keys are files under /etc/stayconnect/portal-tls that
// edged cannot read, and the certificate rows carry metadata only.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// The scd and netd endpoints this surface depends on. Named once so a test and a reader find them together.
const (
	scdPortalTLSKick  = "/v1/portal/tls/kick"  // POST, no body -> 202: re-sync with Central now
	scdPortalTLSRenew = "/v1/portal/tls/renew" // POST, no body -> 202: order new certificates now
	scdPortalTLSState = "/v1/portal/tls-state" // GET -> {https_active, canonical_hostname, ...}
	netdPortalIPCheck = "/v1/portal-ip/validate"
)

// portalHistoryLimit bounds the change history the page shows. Presentation only.
const portalHistoryLimit = 50

// portalCheckWait is how long POST /check waits for scd to finish the re-sync it was asked for before
// answering with whatever state is current. A sync is one or two HTTPS round trips to Central.
const portalCheckWait = 5 * time.Second

type portalHTTPSSettings struct {
	PortalIP     string    `json:"portal_ip"`
	CustomDomain *string   `json:"custom_domain"`
	UpdatedAt    time.Time `json:"updated_at"`
	UpdatedBy    string    `json:"updated_by"`
	// Configured is false only when migration 0116 could not choose a default because every candidate overlapped
	// a guest network: the site must choose a portal address before HTTPS can work.
	Configured bool `json:"configured"`
}

type portalDNSRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type portalTLSState struct {
	CentralAvailable    bool              `json:"central_available"`
	OneGateHostname     string            `json:"onegate_hostname"`
	DNSState            string            `json:"dns_state"`
	CustomDomain        string            `json:"custom_domain"`
	CustomDomainStatus  string            `json:"custom_domain_status"`
	CustomDomainRecords []portalDNSRecord `json:"custom_domain_records"`
	CustomDomainError   string            `json:"custom_domain_error"`
	CanonicalHostname   string            `json:"canonical_hostname"`
	HTTPSActive         bool              `json:"https_active"`
	FallbackReason      string            `json:"fallback_reason"`
	FallbackSince       *time.Time        `json:"fallback_since"`
	LastSyncAt          *time.Time        `json:"last_sync_at"`
	LastSyncError       string            `json:"last_sync_error"`
	UpdatedAt           *time.Time        `json:"updated_at"`
}

type portalCertificate struct {
	Hostname       string     `json:"hostname"`
	Kind           string     `json:"kind"`
	Serial         string     `json:"serial"`
	NotBefore      *time.Time `json:"not_before"`
	NotAfter       *time.Time `json:"not_after"`
	RenewAfter     *time.Time `json:"renew_after"`
	Fingerprint    string     `json:"fingerprint"`
	InstalledAt    *time.Time `json:"installed_at"`
	PendingOrderID *string    `json:"pending_order_id"`
	PendingSince   *time.Time `json:"pending_since"`
	LastError      string     `json:"last_error"`
	LastAttemptAt  *time.Time `json:"last_attempt_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type portalSettingsChange struct {
	ID        int64     `json:"id"`
	ChangedAt time.Time `json:"changed_at"`
	ChangedBy string    `json:"changed_by"`
	Field     string    `json:"field"`
	OldValue  *string   `json:"old_value"`
	NewValue  *string   `json:"new_value"`
	SourceIP  *string   `json:"source_ip"`
}

type portalCallbackURL struct {
	Hostname  string `json:"hostname"`
	Kind      string `json:"kind"` // ONEGATE | CUSTOM
	URL       string `json:"url"`
	Canonical bool   `json:"canonical"`
}

type portalHTTPSDoc struct {
	Settings     portalHTTPSSettings    `json:"settings"`
	State        portalTLSState         `json:"state"`
	Certificates []portalCertificate    `json:"certificates"`
	CallbackURLs []portalCallbackURL    `json:"callback_urls"`
	History      []portalSettingsChange `json:"history"`
	Health       portalHTTPSCheck       `json:"health"`
}

func (s *server) portalHTTPSRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.portalHTTPSGet)
	r.Put("/address", s.portalHTTPSPutAddress)
	r.Put("/custom-domain", s.portalHTTPSPutDomain)
	r.Delete("/custom-domain", s.portalHTTPSDeleteDomain)
	r.Post("/check", s.portalHTTPSCheckNow)
	r.Post("/renew", s.portalHTTPSRenew)
	return r
}

// ---------------------------------------------------------------------------------------------------------
// validation (pure)
// ---------------------------------------------------------------------------------------------------------

var rfc1918 = []*net.IPNet{
	mustCIDR("10.0.0.0/8"), mustCIDR("172.16.0.0/12"), mustCIDR("192.168.0.0/16"),
}

func mustCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		panic(err)
	}
	return n
}

// validatePortalIP is the save-time half of the portal_https_settings CHECK: one private (RFC 1918) IPv4
// address, written without a prefix (or as /32). It returns the canonical dotted form, or an operator-readable
// reason. Whether the address collides with anything on this appliance is netd's question, asked next.
func validatePortalIP(raw string) (string, string) {
	v := strings.TrimSpace(raw)
	v = strings.TrimSuffix(v, "/32")
	if v == "" {
		return "", "enter the portal address, for example 10.255.255.1"
	}
	ip := net.ParseIP(v)
	if ip == nil || ip.To4() == nil || strings.Contains(v, ":") {
		return "", "the portal address must be an IPv4 address such as 10.255.255.1"
	}
	ip4 := ip.To4()
	private := false
	for _, n := range rfc1918 {
		if n.Contains(ip4) {
			private = true
			break
		}
	}
	if !private {
		return "", "the portal address must be a private address (10.0.0.0/8, 172.16.0.0/12 or 192.168.0.0/16); " +
			"it exists only inside the appliance and is never routed"
	}
	if ip4[3] == 0 || ip4[3] == 255 {
		return "", "the portal address must not end in .0 or .255"
	}
	return ip4.String(), ""
}

// The DB CHECK on portal_https_settings.custom_domain, verbatim.
var portalDomainRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// normalisePortalDomain lowercases, trims and drops one trailing dot.
func normalisePortalDomain(raw string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
}

// validatePortalDomain applies the same rules as the database CHECK, plus the contract's "a subdomain, never
// an apex" (§2) in the only form edged can check without a public-suffix list: at least three labels. Central
// re-checks the name against the public suffix list and refuses an apex there.
func validatePortalDomain(fqdn string) string {
	if fqdn == "" {
		return "enter the hotel portal domain, for example wifi.yourhotel.com"
	}
	if len(fqdn) < 4 || len(fqdn) > 253 || !portalDomainRe.MatchString(fqdn) {
		return "the domain must be a host name such as wifi.yourhotel.com (letters, digits and hyphens only)"
	}
	if fqdn == "onegate.work" || strings.HasSuffix(fqdn, ".onegate.work") {
		return "the hotel portal domain must be a domain your hotel owns, not one under onegate.work"
	}
	if strings.Count(fqdn, ".") < 2 {
		return "use a subdomain such as wifi." + fqdn + ", not the domain itself"
	}
	return ""
}

// ---------------------------------------------------------------------------------------------------------
// reads
// ---------------------------------------------------------------------------------------------------------

func (s *server) loadPortalHTTPSSettings(ctx context.Context) (portalHTTPSSettings, error) {
	var st portalHTTPSSettings
	var by *string
	err := s.db.QueryRow(ctx, `SELECT host(portal_ip), custom_domain, updated_at, updated_by
	                             FROM public.portal_https_settings WHERE id`).
		Scan(&st.PortalIP, &st.CustomDomain, &st.UpdatedAt, &by)
	if isNoRows(err) {
		return portalHTTPSSettings{}, nil
	}
	if err != nil {
		return st, err
	}
	st.Configured = true
	if by != nil {
		st.UpdatedBy = *by
	}
	return st, nil
}

func (s *server) loadPortalTLSState(ctx context.Context) (portalTLSState, error) {
	var st portalTLSState
	var host, cdom, cstatus, cerr, canon, reason, syncErr *string
	var records []byte
	err := s.db.QueryRow(ctx, `
	    SELECT central_available, onegate_hostname, dns_state, custom_domain, custom_domain_status,
	           custom_domain_records::text, custom_domain_error, canonical_hostname, https_active,
	           fallback_reason, fallback_since, last_sync_at, last_sync_error, updated_at
	      FROM public.portal_tls_state WHERE id`).
		Scan(&st.CentralAvailable, &host, &st.DNSState, &cdom, &cstatus, &records, &cerr, &canon, &st.HTTPSActive,
			&reason, &st.FallbackSince, &st.LastSyncAt, &syncErr, &st.UpdatedAt)
	if isNoRows(err) {
		// The migration inserts the row; a missing one reads as "not configured", never as active.
		return portalTLSState{DNSState: "UNKNOWN", FallbackReason: "NOT_CONFIGURED", CustomDomainRecords: []portalDNSRecord{}}, nil
	}
	if err != nil {
		return st, err
	}
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	st.OneGateHostname, st.CustomDomain, st.CustomDomainStatus = deref(host), deref(cdom), deref(cstatus)
	st.CustomDomainError, st.CanonicalHostname, st.FallbackReason = deref(cerr), deref(canon), deref(reason)
	st.LastSyncError = deref(syncErr)
	st.CustomDomainRecords = []portalDNSRecord{}
	if len(records) > 0 {
		_ = json.Unmarshal(records, &st.CustomDomainRecords) // unreadable records show as none
	}
	return st, nil
}

func (s *server) loadPortalCertificates(ctx context.Context) ([]portalCertificate, error) {
	rows, err := s.db.Query(ctx, `
	    SELECT hostname, kind, COALESCE(serial,''), not_before, not_after, renew_after, COALESCE(fingerprint,''),
	           installed_at, pending_order_id::text, pending_since, COALESCE(last_error,''), last_attempt_at, updated_at
	      FROM public.portal_certificates ORDER BY kind, hostname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []portalCertificate{}
	for rows.Next() {
		var c portalCertificate
		if err := rows.Scan(&c.Hostname, &c.Kind, &c.Serial, &c.NotBefore, &c.NotAfter, &c.RenewAfter, &c.Fingerprint,
			&c.InstalledAt, &c.PendingOrderID, &c.PendingSince, &c.LastError, &c.LastAttemptAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *server) loadPortalHistory(ctx context.Context) ([]portalSettingsChange, error) {
	rows, err := s.db.Query(ctx, `
	    SELECT id, changed_at, changed_by, field, old_value, new_value, host(source_ip)
	      FROM public.portal_https_settings_changes ORDER BY id DESC LIMIT $1`, portalHistoryLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []portalSettingsChange{}
	for rows.Next() {
		var c portalSettingsChange
		if err := rows.Scan(&c.ID, &c.ChangedAt, &c.ChangedBy, &c.Field, &c.OldValue, &c.NewValue, &c.SourceIP); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// portalHostnames are the hostnames a client could reach the portal by, as scd last established them: the
// OneGate hostname and, once one is registered and not removed, the hotel portal domain. They are what a
// social provider's callback can be registered on.
func portalHostnames(st portalTLSState) []portalCallbackURL {
	out := []portalCallbackURL{}
	if h := strings.ToLower(strings.TrimSpace(st.OneGateHostname)); h != "" {
		out = append(out, portalCallbackURL{Hostname: h, Kind: "ONEGATE", URL: socialCallbackURL(h),
			Canonical: st.HTTPSActive && h == st.CanonicalHostname})
	}
	if h := strings.ToLower(strings.TrimSpace(st.CustomDomain)); h != "" && st.CustomDomainStatus != "REMOVED" {
		out = append(out, portalCallbackURL{Hostname: h, Kind: "CUSTOM", URL: socialCallbackURL(h),
			Canonical: st.HTTPSActive && h == st.CanonicalHostname})
	}
	return out
}

func socialCallbackURL(host string) string { return "https://" + host + socialCallbackPath }

func (s *server) portalHTTPSDocument(ctx context.Context) (portalHTTPSDoc, error) {
	var doc portalHTTPSDoc
	var err error
	if doc.Settings, err = s.loadPortalHTTPSSettings(ctx); err != nil {
		return doc, err
	}
	if doc.State, err = s.loadPortalTLSState(ctx); err != nil {
		return doc, err
	}
	if doc.Certificates, err = s.loadPortalCertificates(ctx); err != nil {
		return doc, err
	}
	if doc.History, err = s.loadPortalHistory(ctx); err != nil {
		return doc, err
	}
	doc.CallbackURLs = portalHostnames(doc.State)
	doc.Health = evaluatePortalHTTPS(doc.State, doc.Certificates, time.Now())
	return doc, nil
}

func (s *server) portalHTTPSGet(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	doc, err := s.portalHTTPSDocument(ctx)
	if err != nil {
		slog.Warn("portal https read failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal HTTPS state could not be read")
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// ---------------------------------------------------------------------------------------------------------
// writes
// ---------------------------------------------------------------------------------------------------------

// portalStepUp re-authenticates the operator and returns the name the change is recorded under.
func (s *server) portalStepUp(w http.ResponseWriter, r *http.Request, password string) (string, bool) {
	if !s.reauth(r, password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return "", false
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return "", false
	}
	return actor, true
}

// sourceIPOrNil is the caller's address when it is one the inet column accepts.
func sourceIPOrNil(r *http.Request) any {
	if ip := net.ParseIP(clientIP(r)); ip != nil {
		return ip.String()
	}
	return nil
}

type portalIPIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// validatePortalIPWithNetd asks netd whether the address collides with anything on this appliance (a guest
// subnet, the WAN or management subnet, a Docker bridge). It FAILS CLOSED: an unreachable or unreadable netd
// is a refusal, never an approval -- an address nobody checked must not be saved.
func (s *server) validatePortalIPWithNetd(ctx context.Context, ip string) (bool, []portalIPIssue, string) {
	if s.netd == nil {
		return false, nil, "the network service is unavailable, so the address could not be checked"
	}
	st, raw, err := s.netd.call(ctx, http.MethodPost, netdPortalIPCheck, map[string]string{"portal_ip": ip})
	if err != nil {
		return false, nil, "the network service is unavailable, so the address could not be checked"
	}
	var out struct {
		OK     bool            `json:"ok"`
		Issues []portalIPIssue `json:"issues"`
	}
	if st != http.StatusOK || json.Unmarshal(raw, &out) != nil {
		return false, nil, "the network service could not check the address"
	}
	if !out.OK && len(out.Issues) == 0 {
		out.Issues = []portalIPIssue{{Code: "refused", Message: "the network service refused the address"}}
	}
	return out.OK, out.Issues, ""
}

func (s *server) portalHTTPSPutAddress(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PortalIP string `json:"portal_ip"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ip, msg := validatePortalIP(in.PortalIP)
	if msg != "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation", "message": msg,
			"issues": []portalIPIssue{{Code: "invalid_address", Message: msg}}})
		return
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	if !ok {
		return
	}
	ok, issues, unavailable := s.validatePortalIPWithNetd(r.Context(), ip)
	if unavailable != "" {
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", unavailable)
		return
	}
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "validation",
			"message": "the portal address cannot be used on this appliance", "issues": issues})
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	old, changed, err := s.savePortalSetting(ctx, r, actor, "portal_ip", &ip)
	if err != nil {
		slog.Warn("portal address save failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal address could not be saved")
		return
	}
	if !changed {
		writeJSON(w, http.StatusOK, map[string]any{"portal_ip": ip, "changed": false, "apply_required": false})
		return
	}
	s.audit(r, "portal_https.address_changed", "portal_https_settings", "portal_ip",
		map[string]any{"old": old, "new": ip})
	// A portal address change is a NETWORK change (contract §2.1): netd puts it on ogportal0 and re-renders the
	// guest-side rules only through validate -> apply -> confirm, which the Client networks screen drives.
	writeJSON(w, http.StatusOK, map[string]any{
		"portal_ip": ip, "changed": true, "apply_required": true, "apply_href": "/network",
		"message": "Saved. Apply the network configuration to start answering on the new address.",
	})
}

func (s *server) portalHTTPSPutDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FQDN     string `json:"fqdn"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	fqdn := normalisePortalDomain(in.FQDN)
	if msg := validatePortalDomain(fqdn); msg != "" {
		jsonErr(w, http.StatusUnprocessableEntity, "validation", msg)
		return
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	if !ok {
		return
	}
	s.portalDomainChange(w, r, actor, &fqdn)
}

func (s *server) portalHTTPSDeleteDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	// An empty body is not malformed: it reaches the step-up, which refuses it with the answer the UI expects.
	if err := decodeJSON(r, &in); err != nil && !errors.Is(err, io.EOF) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	if !ok {
		return
	}
	s.portalDomainChange(w, r, actor, nil)
}

func (s *server) portalDomainChange(w http.ResponseWriter, r *http.Request, actor string, fqdn *string) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	old, changed, err := s.savePortalSetting(ctx, r, actor, "custom_domain", fqdn)
	if errors.Is(err, errPortalNotConfigured) {
		jsonErr(w, http.StatusConflict, "portal_address_required",
			"choose a portal address first; the hotel domain points at it")
		return
	}
	if err != nil {
		slog.Warn("portal domain save failed", "err", err)
		jsonErr(w, http.StatusInternalServerError, "internal", "the hotel portal domain could not be saved")
		return
	}
	action := "portal_https.custom_domain_set"
	if fqdn == nil {
		action = "portal_https.custom_domain_removed"
	}
	syncRequested := false
	if changed {
		s.audit(r, action, "portal_https_settings", "custom_domain", map[string]any{"old": old, "new": fqdn})
		// Central registers, re-checks or removes the domain on the next sync; ask for that sync now.
		syncRequested = s.kickPortalTLS(r.Context())
	}
	doc, derr := s.portalHTTPSDocument(ctx)
	if derr != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal HTTPS state could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changed": changed, "sync_requested": syncRequested, "portal": doc})
}

var errPortalNotConfigured = errors.New("portal_https_settings has no row")

// savePortalSetting writes one field and its history row in ONE transaction, so the setting and its record can
// never disagree. It returns the previous value and whether anything changed (saving the same value is not a
// change and records nothing). A missing settings row is created only by a portal_ip write: a domain needs an
// address to point at.
func (s *server) savePortalSetting(ctx context.Context, r *http.Request, actor, field string, value *string) (*string, bool, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var old *string
	col := "host(portal_ip)"
	if field == "custom_domain" {
		col = "custom_domain"
	}
	err = tx.QueryRow(ctx, `SELECT `+col+` FROM public.portal_https_settings WHERE id FOR UPDATE`).Scan(&old)
	missing := isNoRows(err)
	if err != nil && !missing {
		return nil, false, err
	}
	if missing && field != "portal_ip" {
		return nil, false, errPortalNotConfigured
	}
	if !missing && strDeref(old) == strDeref(value) && (old == nil) == (value == nil) {
		return old, false, nil
	}
	switch {
	case missing:
		_, err = tx.Exec(ctx, `INSERT INTO public.portal_https_settings (portal_ip, updated_by) VALUES ($1::inet, $2)`,
			*value, actor)
	case field == "portal_ip":
		_, err = tx.Exec(ctx, `UPDATE public.portal_https_settings SET portal_ip=$1::inet, updated_at=now(), updated_by=$2 WHERE id`,
			*value, actor)
	default:
		_, err = tx.Exec(ctx, `UPDATE public.portal_https_settings SET custom_domain=$1, updated_at=now(), updated_by=$2 WHERE id`,
			value, actor)
	}
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO public.portal_https_settings_changes
	        (changed_by, field, old_value, new_value, source_ip) VALUES ($1, $2, $3, $4, $5::inet)`,
		actor, field, old, value, sourceIPOrNil(r)); err != nil {
		return nil, false, err
	}
	return old, true, tx.Commit(ctx)
}

// kickPortalTLS asks scd to re-sync with Central now. Best-effort: scd re-syncs on its own cycle anyway, so a
// refused kick delays the change, it does not lose it.
func (s *server) kickPortalTLS(ctx context.Context) bool {
	if s.scd == nil {
		return false
	}
	kctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	st, _, err := s.scd.call(kctx, http.MethodPost, scdPortalTLSKick, nil)
	if err != nil || st >= 300 {
		slog.Warn("scd portal tls kick failed", "status", st, "err", err)
		return false
	}
	return true
}

// portalHTTPSCheckNow re-syncs with Central and answers with the state that follows, waiting a few seconds for
// scd to record the sync. Nothing is changed by it, so it needs manage but not step-up.
func (s *server) portalHTTPSCheckNow(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), portalCheckWait+5*time.Second)
	defer cancel()
	var before time.Time
	if err := s.db.QueryRow(ctx, `SELECT now()`).Scan(&before); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the site database could not be read")
		return
	}
	if !s.kickPortalTLS(ctx) {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the appliance's session controller did not accept the check; try again shortly")
		return
	}
	s.audit(r, "portal_https.check_requested", "portal_https_settings", "", nil)
	refreshed := s.waitPortalSync(ctx, before, portalCheckWait)
	doc, err := s.portalHTTPSDocument(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal HTTPS state could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"refreshed": refreshed, "portal": doc})
}

// waitPortalSync polls the persisted tls-state until scd records a sync newer than `since`, or the wait ends.
// It reads the database row rather than scd's in-memory answer because the row is what the page shows.
func (s *server) waitPortalSync(ctx context.Context, since time.Time, max time.Duration) bool {
	deadline := time.Now().Add(max)
	for {
		var lastSync, updated *time.Time
		if err := s.db.QueryRow(ctx, `SELECT last_sync_at, updated_at FROM public.portal_tls_state WHERE id`).
			Scan(&lastSync, &updated); err == nil {
			if (lastSync != nil && lastSync.After(since)) || (updated != nil && updated.After(since)) {
				return true
			}
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *server) portalHTTPSRenew(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if _, ok := s.portalStepUp(w, r, in.Password); !ok {
		return
	}
	if s.scd == nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the appliance's session controller is unavailable")
		return
	}
	st, raw, err := s.scd.call(r.Context(), http.MethodPost, scdPortalTLSRenew, nil)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", "the appliance's session controller is unavailable")
		return
	}
	if st >= 300 {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = "the renewal could not be started"
		}
		jsonErr(w, http.StatusBadGateway, "renew_refused", e.Message)
		return
	}
	s.audit(r, "portal_https.renew_requested", "portal_https_settings", "", nil)
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "renewal_requested",
		"message": "New certificates are being ordered. This usually takes a minute or two."})
}
