package main

// THE PORTAL'S HTTPS CERTIFICATES, AND THE ONE ANSWER TO "IS THE PORTAL ON HTTPS RIGHT NOW".
//
// docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md sections 3, 4 and 7. Central owns the hostname, the
// DNS record and the ACME order; THIS APPLIANCE owns the private key (generated here, never sent anywhere) and the
// decision whether what Central issued is safe to serve. The agent below runs every five minutes and on a kick:
//
//	sync     POST /v1/appliance/portal-tls/sync  -- tell Central the portal address and the hotel domain the site
//	         asked for; learn the OneGate hostname, the domain's verification state and each certificate's status
//	order    when a hostname has no usable certificate (missing, expired, revoked, past renew_after, or not
//	         matching its key): a NEW P-256 key, a CSR, POST /v1/appliance/portal-tls/orders
//	collect  GET /v1/appliance/portal-tls/orders/{id} until ISSUED; verify, install atomically
//	decide   canonical hostname, https_active and the fallback reason (contract section 4), persisted in
//	         public.portal_tls_state and served to portald on GET /v1/portal/tls-state
//
// HTTPS NEVER DEPENDS ON CENTRAL BEING REACHABLE. Once a valid certificate is installed the portal stays on HTTPS
// through an internet outage: the decision reads the certificate files and the portal address, not the last sync.
// Central's reachability matters only when there is no usable certificate (CENTRAL_UNREACHABLE).
//
// THE FALLBACK IS THE APPROVED HTTP PORTAL (contract section 1, decision 6), not an error state: manual codes keep
// working, emails carry the code alone, social sign-in is hidden, and recovery is automatic.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/portaltls"
)

// Fallback reasons (migration 0116's CHECK list, contract section 4).
const (
	ptReasonNotConfigured      = "NOT_CONFIGURED"
	ptReasonNoHostname         = "NO_HOSTNAME"
	ptReasonNoCertificate      = "NO_CERTIFICATE"
	ptReasonExpired            = "CERTIFICATE_EXPIRED"
	ptReasonAddressNotActive   = "PORTAL_ADDRESS_NOT_ACTIVE"
	ptReasonCentralUnreachable = "CENTRAL_UNREACHABLE"
)

const (
	ptKindOneGate = "ONEGATE"
	ptKindCustom  = "CUSTOM"

	// portalInterface is the dummy interface netd puts the portal address on (contract section 2.1).
	portalInterface = "ogportal0"

	// ptMinRemaining: a certificate with less than this left is treated as expired. It is the agent's own
	// interval, so a certificate can never be served past its expiry between two cycles.
	ptMinRemaining = 5 * time.Minute
	// ptOrderBackoff: after Central refuses an order (422, 429) or an order fails, the next order for that
	// hostname waits at least this long. Central's own limits are daily; hammering it gains nothing.
	ptOrderBackoff = time.Hour

	ptTrustSystem    = "system"
	ptTrustTestRoots = "test-roots"
)

// The agent's cadence. Vars so tests can shorten them.
var (
	portalTLSEvery = 5 * time.Minute
	// While an order is open the agent looks again sooner: issuance takes a minute or two, and a guest network
	// waiting for its first certificate should not wait a further five.
	portalTLSPendingEvery = 30 * time.Second
	// portalTLSFastPollFor bounds how long an open order is polled at portalTLSPendingEvery.
	portalTLSFastPollFor = 15 * time.Minute
)

// PortalTLSState is what the rest of scd (OTP links; the social sign-in redirect URIs) and portald learn about
// the portal's HTTPS state. It carries no secret.
type PortalTLSState struct {
	HTTPSActive        bool   `json:"https_active"`
	Canonical          string `json:"canonical_hostname"`
	OneGateHostname    string `json:"onegate_hostname"`
	CustomDomain       string `json:"custom_domain"`
	CustomDomainStatus string `json:"custom_domain_status"`
	PortalIP           string `json:"portal_ip"`
	// PortalIPActive: the portal address is configured AND up on this appliance, so a guest can reach the portal
	// at http://<portal_ip>/ whatever the HTTPS state. portald uses it as the HTTP fallback's canonical origin.
	PortalIPActive bool       `json:"portal_ip_active"`
	FallbackReason string     `json:"fallback_reason"`
	FallbackSince  *time.Time `json:"fallback_since"`
	// Hostnames are the portal hostnames portald may serve a certificate for: those with an installed, valid
	// certificate. The canonical hostname is one of them whenever it is set.
	Hostnames []string `json:"portal_hostnames"`
	CertDir   string   `json:"cert_dir"`
	// Trust is "system", or "test-roots" when SCD_PORTAL_TLS_EXTRA_ROOTS is in force (staging or a test CA) --
	// shown by the Admin Console so a test configuration is never mistaken for production.
	Trust string `json:"trust"`
}

// ---- persistence ---------------------------------------------------------------------------------------------

type ptSettings struct {
	PortalIP     string // "" when the site has no portal address (every default candidate overlapped)
	CustomDomain string // "" when none
}

// ptState is public.portal_tls_state.
type ptState struct {
	CentralAvailable    bool
	OneGateHostname     string
	DNSState            string
	CustomDomain        string
	CustomDomainStatus  string
	CustomDomainRecords json.RawMessage
	CustomDomainError   string
	Canonical           string
	HTTPSActive         bool
	FallbackReason      string
	FallbackSince       *time.Time
	LastSyncAt          *time.Time
	LastSyncError       string
}

// ptCertRow is one row of public.portal_certificates.
type ptCertRow struct {
	Hostname       string
	Kind           string
	Serial         string
	NotBefore      *time.Time
	NotAfter       *time.Time
	RenewAfter     *time.Time
	Fingerprint    string
	InstalledAt    *time.Time
	PendingOrderID string
	PendingSince   *time.Time
	LastError      string
	LastAttemptAt  *time.Time
}

type ptRepo interface {
	Settings(ctx context.Context) (ptSettings, error)
	LoadState(ctx context.Context) (ptState, error)
	SaveState(ctx context.Context, st ptState) error
	Certs(ctx context.Context) (map[string]*ptCertRow, error)
	UpsertCert(ctx context.Context, row *ptCertRow) error
	DeleteCert(ctx context.Context, host string) error
}

// ---- Central (contract section 7) ----------------------------------------------------------------------------

type ptSyncReq struct {
	PortalIP     string  `json:"portal_ip"`
	CustomDomain *string `json:"custom_domain"`
}

type ptRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ptCustomDomain struct {
	FQDN    string     `json:"fqdn"`
	Status  string     `json:"status"`
	Records []ptRecord `json:"records"`
	Error   string     `json:"error"`
}

type ptCertStatus struct {
	Hostname   string     `json:"hostname"`
	Kind       string     `json:"kind"`
	OrderID    string     `json:"order_id"`
	Status     string     `json:"status"`
	Serial     string     `json:"serial"`
	NotAfter   *time.Time `json:"not_after"`
	RenewAfter *time.Time `json:"renew_after"`
	Revoked    bool       `json:"revoked"`
	Error      string     `json:"error"`
}

type ptSyncResp struct {
	Available    bool            `json:"available"`
	Hostname     string          `json:"hostname"`
	DNSState     string          `json:"dns_state"`
	CustomDomain *ptCustomDomain `json:"custom_domain"`
	Certificates []ptCertStatus  `json:"certificates"`
}

type ptOrder struct {
	Status     string     `json:"status"`
	ChainPEM   string     `json:"chain_pem"`
	Serial     string     `json:"serial"`
	NotAfter   *time.Time `json:"not_after"`
	RenewAfter *time.Time `json:"renew_after"`
	Error      string     `json:"error"`
}

// ptCentral is the three calls. The HTTP implementation is ptHTTPCentral; tests supply a fake.
type ptCentral interface {
	Sync(ctx context.Context, req ptSyncReq) (*ptSyncResp, error)
	// CreateOrder returns the order id. adopted is true when Central answered 409 (an order was already open
	// for the hostname) and the id is that order's.
	CreateOrder(ctx context.Context, host string, csrPEM []byte) (orderID string, adopted bool, err error)
	GetOrder(ctx context.Context, id string) (*ptOrder, error)
}

// ptHTTPError is Central answering a portal-tls call with a non-success status. It unwraps to httpStatusErr so
// the Central contact record and describeCentralErr read it like every other Central call.
type ptHTTPError struct {
	Code int
	Msg  string // Central's {"error": ...}, bounded; never a URL or a key
}

func (e *ptHTTPError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("HTTP %d: %s", e.Code, e.Msg)
	}
	return fmt.Sprintf("HTTP %d", e.Code)
}
func (e *ptHTTPError) Unwrap() error { return &httpStatusErr{code: e.Code} }

// ---- the agent -----------------------------------------------------------------------------------------------

type portalTLSAgent struct {
	repo    ptRepo
	central ptCentral // nil: this appliance does not contact Central (no Central configured, or removed)
	store   *portaltls.Store
	now     func() time.Time
	// addrUp reports whether the portal address is assigned to ogportal0 and the interface is up.
	addrUp func(ip string) bool
	trust  string

	kick  chan struct{}
	renew chan struct{}

	// retryAt holds the per-hostname order backoff. Only the agent goroutine touches it.
	retryAt map[string]time.Time
	// pendingAny: an order was open at the end of the last cycle (the loop then looks again sooner).
	pendingAny bool

	mu   sync.RWMutex
	snap PortalTLSState
}

func newPortalTLSAgent(repo ptRepo, central ptCentral, store *portaltls.Store, trust string) *portalTLSAgent {
	if trust == "" {
		trust = ptTrustSystem
	}
	a := &portalTLSAgent{
		repo: repo, central: central, store: store, now: time.Now, addrUp: portalAddressUp, trust: trust,
		kick: make(chan struct{}, 1), renew: make(chan struct{}, 1), retryAt: map[string]time.Time{},
	}
	a.snap = a.defaultState()
	return a
}

// defaultState is the answer before the first cycle, and whenever nothing better is known: the approved HTTP
// fallback. Never "active" by default.
func (a *portalTLSAgent) defaultState() PortalTLSState {
	st := PortalTLSState{FallbackReason: ptReasonNotConfigured, CertDir: portaltls.DefaultDir, Trust: ptTrustSystem, Hostnames: []string{}}
	if a != nil {
		if a.store != nil && a.store.Dir != "" {
			st.CertDir = a.store.Dir
		}
		st.Trust = a.trust
	}
	return st
}

// State is the current portal HTTPS state. Safe on a nil agent (tests, or an scd without a database): the
// fallback.
func (a *portalTLSAgent) State() PortalTLSState {
	if a == nil {
		return (*portalTLSAgent)(nil).defaultState()
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	st := a.snap
	st.Hostnames = append([]string{}, a.snap.Hostnames...)
	return st
}

// Kick asks for a cycle now (a settings change, Check now). Renew asks for a cycle that orders a new key and
// certificate for every hostname without an open order. Both coalesce.
func (a *portalTLSAgent) Kick() {
	select {
	case a.kick <- struct{}{}:
	default:
	}
}

func (a *portalTLSAgent) Renew() {
	select {
	case a.renew <- struct{}{}:
	default:
	}
}

func (a *portalTLSAgent) run(ctx context.Context) {
	force := false
	for {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if err := a.cycle(cctx, force); err != nil {
			slog.Warn("portal-tls: cycle did not complete", "err", err)
		}
		cancel()
		force = false
		wait := portalTLSEvery
		if a.pendingAny {
			wait = portalTLSPendingEvery
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-a.kick:
		case <-a.renew:
			force = true
		}
		t.Stop()
	}
}

// centralMode is what this cycle learnt about Central.
type centralMode int

const (
	centralSynced centralMode = iota
	centralFailed
	centralAbsent
	centralNoAddress
)

var dnsStates = map[string]bool{"UNKNOWN": true, "PENDING": true, "SYNCED": true, "ERROR": true}
var domainStatuses = map[string]bool{"PENDING": true, "VERIFIED": true, "FAILED": true, "REMOVED": true}

// cycle is one pass: sync, order/collect/install per hostname, clean up, decide, persist.
func (a *portalTLSAgent) cycle(ctx context.Context, forceRenew bool) error {
	now := a.now()
	set, err := a.repo.Settings(ctx)
	if err != nil {
		return fmt.Errorf("read portal_https_settings: %w", err)
	}
	prev, err := a.repo.LoadState(ctx)
	if err != nil {
		return fmt.Errorf("read portal_tls_state: %w", err)
	}
	st := prev

	// 1. SYNC.
	mode := centralSynced
	certStatus := map[string]ptCertStatus{}
	switch {
	case set.PortalIP == "":
		mode = centralNoAddress
		st.LastSyncError = "no portal address is configured"
	case a.central == nil:
		mode = centralAbsent
		st.LastSyncError = "this appliance does not contact OneGate Central"
	default:
		req := ptSyncReq{PortalIP: set.PortalIP}
		if set.CustomDomain != "" {
			cd := set.CustomDomain
			req.CustomDomain = &cd
		}
		resp, err := a.central.Sync(ctx, req)
		if err != nil {
			mode = centralFailed
			st.LastSyncError = describeCentralErr(err)
			break
		}
		st.CentralAvailable = resp.Available
		// A OneGate hostname is permanent (contract section 2). An answer without one never erases the one
		// already known -- that would take down a valid certificate over a Central that is mid-configuration.
		if h := strings.ToLower(strings.TrimSpace(resp.Hostname)); portaltls.ValidHostname(h) {
			st.OneGateHostname = h
		}
		st.DNSState = "UNKNOWN"
		if dnsStates[resp.DNSState] {
			st.DNSState = resp.DNSState
		}
		st.CustomDomain, st.CustomDomainStatus, st.CustomDomainError = "", "", ""
		st.CustomDomainRecords = json.RawMessage("[]")
		if set.CustomDomain != "" {
			st.CustomDomain = set.CustomDomain
			if cd := resp.CustomDomain; cd != nil && strings.EqualFold(strings.TrimSpace(cd.FQDN), set.CustomDomain) {
				if domainStatuses[cd.Status] {
					st.CustomDomainStatus = cd.Status
				}
				st.CustomDomainError = boundText(cd.Error, 500)
				if cd.Records != nil {
					if b, err := json.Marshal(cd.Records); err == nil {
						st.CustomDomainRecords = b
					}
				}
			}
		}
		for _, c := range resp.Certificates {
			certStatus[strings.ToLower(c.Hostname)] = c
		}
		t := now
		st.LastSyncAt = &t
		st.LastSyncError = ""
	}
	// The site removed or changed its hotel domain and Central has not confirmed it yet: the LOCAL setting is
	// authoritative for what this appliance serves. A removed domain stops being served now, not at the next
	// successful sync.
	if mode != centralSynced && st.CustomDomain != set.CustomDomain {
		st.CustomDomain, st.CustomDomainStatus, st.CustomDomainError = set.CustomDomain, "", ""
		st.CustomDomainRecords = json.RawMessage("[]")
	}

	// 2. WHICH HOSTNAMES. Orders are placed for the OneGate hostname and for a VERIFIED hotel domain; a hotel
	// domain that is still configured but not verified (PENDING, or FAILED after a broken delegation) keeps
	// whatever it has but is neither renewed nor canonical.
	targets := map[string]string{}
	if st.OneGateHostname != "" {
		targets[st.OneGateHostname] = ptKindOneGate
	}
	customVerified := set.CustomDomain != "" && st.CustomDomain == set.CustomDomain && st.CustomDomainStatus == "VERIFIED"
	if customVerified {
		targets[set.CustomDomain] = ptKindCustom
	}
	keep := map[string]bool{}
	for h := range targets {
		keep[h] = true
	}
	if set.CustomDomain != "" && st.CustomDomain == set.CustomDomain && st.CustomDomainStatus != "REMOVED" {
		keep[set.CustomDomain] = true
	}

	// 3. PER HOSTNAME.
	rows, rowsErr := a.repo.Certs(ctx)
	if rowsErr != nil {
		slog.Warn("portal-tls: could not read portal_certificates; certificates are not renewed this cycle", "err", rowsErr)
		rows = nil
	}
	pendingAny := false
	if rowsErr == nil {
		for _, host := range sortedKeys(targets) {
			row := rows[host]
			if row == nil {
				row = &ptCertRow{Hostname: host}
			}
			row.Kind = targets[host]
			var cs *ptCertStatus
			if c, ok := certStatus[host]; ok {
				cs = &c
			}
			a.reconcileHost(ctx, row, cs, mode == centralSynced && st.CentralAvailable, forceRenew, now)
			// Quick polling only while the order is young: one Central holds QUEUED (its weekly limit) is
			// collected at the normal cadence rather than every 30 seconds for days.
			if row.PendingOrderID != "" && row.PendingSince != nil && now.Sub(*row.PendingSince) < portalTLSFastPollFor {
				pendingAny = true
			}
			if err := a.repo.UpsertCert(ctx, row); err != nil {
				slog.Warn("portal-tls: could not record a certificate", "host", host, "err", err)
			}
		}
		// 4. CLEAN UP hostnames this appliance no longer serves (a removed hotel domain): files and rows.
		for host := range rows {
			if keep[host] {
				continue
			}
			if err := a.store.Remove(host); err != nil {
				slog.Warn("portal-tls: could not remove the files of a hostname no longer served", "host", host, "err", err)
			}
			if err := a.repo.DeleteCert(ctx, host); err != nil {
				slog.Warn("portal-tls: could not remove a certificate record", "host", host, "err", err)
			}
			delete(a.retryAt, host)
			slog.Info("portal-tls: stopped serving a hostname", "host", host)
		}
	}
	if hosts, err := a.store.Hosts(); err == nil {
		for _, host := range hosts {
			if !keep[host] {
				_ = a.store.Remove(host)
			}
		}
	}
	a.pendingAny = pendingAny

	// 5. DECIDE (contract section 4).
	view := a.decide(set, &st, mode, customVerified, now)
	if !st.HTTPSActive {
		if prev.HTTPSActive || prev.FallbackSince == nil || prev.FallbackReason == "" {
			t := now
			st.FallbackSince = &t
		} else {
			st.FallbackSince = prev.FallbackSince
		}
	} else {
		st.FallbackSince = nil
	}
	view.FallbackSince = st.FallbackSince
	a.logTransition(prev, st)

	a.mu.Lock()
	a.snap = view
	a.mu.Unlock()
	if err := a.repo.SaveState(ctx, st); err != nil {
		return fmt.Errorf("write portal_tls_state: %w", err)
	}
	return nil
}

// usable returns the installed certificate of host if it can be served for at least ptMinRemaining more.
func (a *portalTLSAgent) usable(host string, now time.Time) (*portaltls.Installed, bool) {
	if host == "" {
		return nil, false
	}
	inst, err := a.store.Inspect(host)
	if err != nil {
		return nil, false
	}
	return inst, !now.Before(inst.NotBefore) && now.Add(ptMinRemaining).Before(inst.NotAfter)
}

func (a *portalTLSAgent) decide(set ptSettings, st *ptState, mode centralMode, customVerified bool, now time.Time) PortalTLSState {
	canonical := ""
	if customVerified {
		if _, ok := a.usable(set.CustomDomain, now); ok {
			canonical = set.CustomDomain
		}
	}
	if canonical == "" {
		if _, ok := a.usable(st.OneGateHostname, now); ok {
			canonical = st.OneGateHostname
		}
	}
	reason := ""
	if canonical != "" {
		if set.PortalIP == "" || a.addrUp == nil || !a.addrUp(set.PortalIP) {
			reason = ptReasonAddressNotActive
		}
	} else {
		anyExpired := false
		for _, h := range []string{st.OneGateHostname, set.CustomDomain} {
			if h == "" {
				continue
			}
			if inst, err := a.store.Inspect(h); err == nil && !now.Add(ptMinRemaining).Before(inst.NotAfter) {
				anyExpired = true
			}
		}
		switch {
		case set.PortalIP == "":
			reason = ptReasonAddressNotActive
		case mode == centralFailed:
			// No usable certificate AND Central could not be asked for one. With a usable certificate an
			// unreachable Central changes nothing -- that is the branch above.
			reason = ptReasonCentralUnreachable
		case mode == centralAbsent || !st.CentralAvailable:
			reason = ptReasonNotConfigured
		case st.OneGateHostname == "" && !customVerified:
			reason = ptReasonNoHostname
		case anyExpired:
			reason = ptReasonExpired
		default:
			reason = ptReasonNoCertificate
		}
	}
	st.Canonical = canonical
	st.HTTPSActive = reason == ""
	st.FallbackReason = reason

	var hosts []string
	for _, h := range []string{st.OneGateHostname, set.CustomDomain} {
		if h == "" || (h == set.CustomDomain && st.CustomDomain != set.CustomDomain) {
			continue
		}
		if inst, err := a.store.Inspect(h); err == nil && !now.Before(inst.NotBefore) && now.Before(inst.NotAfter) {
			hosts = append(hosts, h)
		}
	}
	if hosts == nil {
		hosts = []string{}
	}
	return PortalTLSState{
		HTTPSActive: st.HTTPSActive, Canonical: canonical, OneGateHostname: st.OneGateHostname,
		CustomDomain: set.CustomDomain, CustomDomainStatus: st.CustomDomainStatus, PortalIP: set.PortalIP,
		PortalIPActive: set.PortalIP != "" && a.addrUp != nil && a.addrUp(set.PortalIP),
		FallbackReason: reason, Hostnames: hosts, CertDir: a.defaultState().CertDir, Trust: a.trust,
	}
}

func (a *portalTLSAgent) logTransition(prev, st ptState) {
	if prev.HTTPSActive == st.HTTPSActive && prev.FallbackReason == st.FallbackReason && prev.Canonical == st.Canonical {
		return
	}
	if st.HTTPSActive {
		slog.Info("portal-tls: the Client Portal is on HTTPS", "canonical", st.Canonical)
		return
	}
	slog.Warn("portal-tls: the Client Portal is on the HTTP fallback (manual codes keep working; emails carry the code only)",
		"reason", st.FallbackReason, "previous_reason", prev.FallbackReason, "was_https", prev.HTTPSActive)
}

// reconcileHost brings one hostname's certificate forward by at most one step: collect an open order, or place
// a new one when the installed certificate is not good enough. canOrder is false while Central is unreachable
// or not configured for portal certificates -- ordering would only fail.
func (a *portalTLSAgent) reconcileHost(ctx context.Context, row *ptCertRow, cs *ptCertStatus, canOrder, forceRenew bool, now time.Time) {
	host := row.Hostname
	inst, ierr := a.store.Inspect(host)
	if ierr == nil && row.Serial != inst.Serial {
		// The row follows the file: the file is what portald serves.
		nb, na := inst.NotBefore, inst.NotAfter
		row.Serial, row.NotBefore, row.NotAfter, row.Fingerprint = inst.Serial, &nb, &na, inst.Fingerprint
		if row.InstalledAt == nil {
			t := now
			row.InstalledAt = &t
		}
		row.RenewAfter = defaultRenewAfter(inst)
	}
	if ierr != nil && row.Serial != "" {
		// The file is gone (or no longer matches its key): the row must not keep describing it.
		row.Serial, row.NotBefore, row.NotAfter, row.Fingerprint, row.InstalledAt, row.RenewAfter = "", nil, nil, "", nil, nil
	}
	// Central re-checks ARI daily and may move renew_after; it applies to the certificate actually installed.
	if cs != nil && ierr == nil && sameSerial(cs.Serial, inst.Serial) && cs.RenewAfter != nil {
		ra := *cs.RenewAfter
		row.RenewAfter = &ra
	}
	// A REVOKED CERTIFICATE IS NOT SERVED. Central revokes on decommission, domain removal or by an operator;
	// keeping it on disk would let the portal present it until it expired.
	if cs != nil && cs.Revoked && ierr == nil && (cs.Serial == "" || sameSerial(cs.Serial, inst.Serial)) {
		slog.Warn("portal-tls: Central reports the installed certificate revoked; removing it and ordering a new one",
			"host", host, "serial", inst.Serial)
		if err := a.store.RemoveInstalled(host); err != nil {
			slog.Warn("portal-tls: could not remove the revoked certificate", "host", host, "err", err)
		}
		row.Serial, row.NotBefore, row.NotAfter, row.Fingerprint, row.InstalledAt, row.RenewAfter = "", nil, nil, "", nil, nil
		inst, ierr = nil, portaltls.ErrNotInstalled
	}

	// An order already open is collected first, whatever the installed certificate's state.
	if row.PendingOrderID != "" {
		if !a.store.HasPending(host) {
			row.PendingOrderID, row.PendingSince = "", nil
			row.LastError = "the key of the open order was lost; a new order will be placed"
		} else {
			a.collect(ctx, row, now)
			return
		}
	}

	why := ""
	switch {
	case errors.Is(ierr, portaltls.ErrKeyMismatch):
		why = "key_mismatch"
	case ierr != nil:
		why = "missing"
	case !now.Before(inst.NotAfter):
		why = "expired"
	case row.RenewAfter != nil && now.After(*row.RenewAfter):
		why = "renew_after"
	case forceRenew:
		why = "operator"
	}
	if why == "" || !canOrder || a.central == nil {
		return
	}
	if t, ok := a.retryAt[host]; ok && now.Before(t) && !forceRenew {
		return
	}

	// EVERY ORDER IS FOR A NEW KEY -- except that a pending key with no recorded order is reused. That key may
	// belong to an order Central accepted just before this process stopped; reusing it is what lets the 409
	// answer below adopt that order and install its certificate.
	var csr []byte
	var err error
	if a.store.HasPending(host) && !forceRenew {
		csr, err = a.store.PendingCSR(host)
	} else {
		csr, err = a.store.NewPendingKey(host)
	}
	if err != nil {
		row.LastError = "could not create a key and request: " + err.Error()
		slog.Error("portal-tls: could not create a key and request", "host", host, "err", err)
		return
	}
	t := now
	row.LastAttemptAt = &t
	id, adopted, err := a.central.CreateOrder(ctx, host, csr)
	if err != nil {
		var he *ptHTTPError
		if errors.As(err, &he) && (he.Code == http.StatusUnprocessableEntity || he.Code == http.StatusTooManyRequests) {
			a.retryAt[host] = now.Add(ptOrderBackoff)
		}
		row.LastError = "certificate order not accepted: " + boundText(err.Error(), 300)
		slog.Warn("portal-tls: certificate order not accepted", "host", host, "why", why, "err", err)
		return
	}
	row.PendingOrderID, row.PendingSince, row.LastError = id, &t, ""
	delete(a.retryAt, host)
	slog.Info("portal-tls: certificate ordered", "host", host, "why", why, "order_id", id, "adopted_open_order", adopted)
}

// collect asks Central where an open order stands and installs the certificate once issued.
func (a *portalTLSAgent) collect(ctx context.Context, row *ptCertRow, now time.Time) {
	host := row.Hostname
	if a.central == nil {
		return
	}
	o, err := a.central.GetOrder(ctx, row.PendingOrderID)
	if err != nil {
		var he *ptHTTPError
		if errors.As(err, &he) && he.Code == http.StatusNotFound {
			// Central does not know the order: it can never issue. Start again with a new key.
			_ = a.store.DropPending(host)
			row.PendingOrderID, row.PendingSince = "", nil
			row.LastError = "Central no longer knows the open order; a new one will be placed"
			return
		}
		row.LastError = "order status unavailable: " + boundText(describeCentralErr(err), 300)
		return
	}
	switch strings.ToUpper(o.Status) {
	case "ISSUED":
		inst, err := a.store.Install(host, []byte(o.ChainPEM))
		if err != nil {
			// What Central delivered is not safe to serve (wrong key, wrong name, untrusted chain...). The
			// pending key is dropped so the next order uses a new one.
			_ = a.store.DropPending(host)
			row.PendingOrderID, row.PendingSince = "", nil
			row.LastError = "the issued certificate was refused: " + boundText(err.Error(), 300)
			a.retryAt[host] = now.Add(ptOrderBackoff)
			slog.Error("portal-tls: the issued certificate was refused and not installed", "host", host, "err", err)
			return
		}
		nb, na, t := inst.NotBefore, inst.NotAfter, now
		row.Serial, row.NotBefore, row.NotAfter, row.Fingerprint, row.InstalledAt = inst.Serial, &nb, &na, inst.Fingerprint, &t
		row.RenewAfter = defaultRenewAfter(inst)
		if o.RenewAfter != nil {
			ra := *o.RenewAfter
			row.RenewAfter = &ra
		}
		row.PendingOrderID, row.PendingSince, row.LastError = "", nil, ""
		delete(a.retryAt, host)
		slog.Info("portal-tls: certificate installed", "host", host, "serial", inst.Serial,
			"not_after", inst.NotAfter.UTC().Format(time.RFC3339))
	case "FAILED", "REVOKED", "CANCELLED", "CANCELED", "EXPIRED", "REFUSED", "INVALID":
		_ = a.store.DropPending(host)
		row.PendingOrderID, row.PendingSince = "", nil
		msg := boundText(o.Error, 300)
		if msg == "" {
			msg = strings.ToLower(o.Status)
		}
		row.LastError = "certificate order failed: " + msg
		a.retryAt[host] = now.Add(ptOrderBackoff)
		slog.Warn("portal-tls: certificate order failed", "host", host, "status", o.Status, "err", o.Error)
	default:
		// QUEUED / PENDING / PROCESSING: Central is working on it.
	}
}

// defaultRenewAfter is two thirds of the lifetime -- the same rule Central applies when the CA offers no ARI.
// Used only until Central states its own renew_after.
func defaultRenewAfter(inst *portaltls.Installed) *time.Time {
	t := inst.NotBefore.Add(inst.NotAfter.Sub(inst.NotBefore) * 2 / 3)
	return &t
}

// sameSerial compares two serial numbers written as hex, ignoring case, separators and leading zeros.
func sameSerial(a, b string) bool {
	norm := func(s string) string {
		s = strings.ToLower(strings.NewReplacer(":", "", "-", "", " ", "").Replace(strings.TrimSpace(s)))
		return strings.TrimLeft(s, "0")
	}
	return a != "" && b != "" && norm(a) == norm(b)
}

func boundText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// portalAddressUp: the portal address is assigned to ogportal0 and the interface is up. netd configures it; a
// missing or down interface means guests could not reach the HTTPS portal, so HTTPS must not be advertised.
func portalAddressUp(ip string) bool {
	want := net.ParseIP(ip)
	if want == nil {
		return false
	}
	ifc, err := net.InterfaceByName(portalInterface)
	if err != nil || ifc.Flags&net.FlagUp == 0 {
		return false
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.Equal(want) {
			return true
		}
	}
	return false
}

// ---- the HTTP client to Central ------------------------------------------------------------------------------

// ptHTTPCentral rides the same mutual-TLS transport and appliance JWT as every other /v1/appliance/ call
// (central_retry.go): s.centralMTLS for the client certificate, applianceauth.SignRequest for the bearer.
type ptHTTPCentral struct{ s *server }

var orderIDRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (c ptHTTPCentral) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	s := c.s
	if s.idPriv == nil || s.applID == "" {
		return 0, nil, errNoCentralTransport
	}
	cl, base, ok := s.centralMTLS()
	if !ok || base == "" {
		return 0, nil, errNoCentralTransport
	}
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, method, path, body)
	if err != nil {
		return 0, nil, err
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		s.central.record(err, time.Now())
		return 0, nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 500 {
		s.central.record(&httpStatusErr{code: resp.StatusCode}, time.Now())
	} else {
		s.central.record(nil, time.Now())
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		// Central refused the client certificate: let the certificate agent check it (appliancecert.Reconcile).
		select {
		case s.certReconcile <- struct{}{}:
		default:
		}
	}
	return resp.StatusCode, out, nil
}

func ptErrorFrom(code int, body []byte) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return &ptHTTPError{Code: code, Msg: boundText(e.Error, 200)}
}

func (c ptHTTPCentral) Sync(ctx context.Context, req ptSyncReq) (*ptSyncResp, error) {
	body, _ := json.Marshal(req)
	code, out, err := c.do(ctx, http.MethodPost, "/v1/appliance/portal-tls/sync", body)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, ptErrorFrom(code, out)
	}
	var resp ptSyncResp
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("sync answer is not valid JSON: %w", err)
	}
	return &resp, nil
}

func (c ptHTTPCentral) CreateOrder(ctx context.Context, host string, csrPEM []byte) (string, bool, error) {
	body, _ := json.Marshal(map[string]string{"hostname": host, "csr_pem": string(csrPEM)})
	code, out, err := c.do(ctx, http.MethodPost, "/v1/appliance/portal-tls/orders", body)
	if err != nil {
		return "", false, err
	}
	var r struct {
		OrderID string `json:"order_id"`
	}
	_ = json.Unmarshal(out, &r)
	switch {
	case (code == http.StatusAccepted || code == http.StatusOK || code == http.StatusCreated) && orderIDRE.MatchString(r.OrderID):
		return r.OrderID, false, nil
	case code == http.StatusConflict && orderIDRE.MatchString(r.OrderID):
		return r.OrderID, true, nil
	}
	return "", false, ptErrorFrom(code, out)
}

func (c ptHTTPCentral) GetOrder(ctx context.Context, id string) (*ptOrder, error) {
	if !orderIDRE.MatchString(id) {
		return nil, &ptHTTPError{Code: http.StatusNotFound, Msg: "malformed order id"}
	}
	code, out, err := c.do(ctx, http.MethodGet, "/v1/appliance/portal-tls/orders/"+strings.ToLower(id), nil)
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, ptErrorFrom(code, out)
	}
	var o ptOrder
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, fmt.Errorf("order answer is not valid JSON: %w", err)
	}
	return &o, nil
}

// ---- the database --------------------------------------------------------------------------------------------

type ptPGRepo struct{ db *pgxpool.Pool }

func (r ptPGRepo) Settings(ctx context.Context) (ptSettings, error) {
	var s ptSettings
	var ip, cd *string
	err := r.db.QueryRow(ctx, `SELECT host(portal_ip), custom_domain FROM public.portal_https_settings WHERE id`).Scan(&ip, &cd)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if ip != nil {
		s.PortalIP = *ip
	}
	if cd != nil {
		s.CustomDomain = strings.ToLower(*cd)
	}
	return s, nil
}

func nz(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (r ptPGRepo) LoadState(ctx context.Context) (ptState, error) {
	var st ptState
	var og, cd, cds, cde, can, fr, lse *string
	err := r.db.QueryRow(ctx, `
	  SELECT central_available, onegate_hostname, dns_state, custom_domain, custom_domain_status,
	         custom_domain_records, custom_domain_error, canonical_hostname, https_active, fallback_reason,
	         fallback_since, last_sync_at, last_sync_error
	    FROM public.portal_tls_state WHERE id`).Scan(&st.CentralAvailable, &og, &st.DNSState, &cd, &cds,
		&st.CustomDomainRecords, &cde, &can, &st.HTTPSActive, &fr, &st.FallbackSince, &st.LastSyncAt, &lse)
	if errors.Is(err, pgx.ErrNoRows) {
		return ptState{DNSState: "UNKNOWN", FallbackReason: ptReasonNotConfigured, CustomDomainRecords: json.RawMessage("[]")}, nil
	}
	if err != nil {
		return st, err
	}
	st.OneGateHostname, st.CustomDomain, st.CustomDomainStatus, st.CustomDomainError = nz(og), nz(cd), nz(cds), nz(cde)
	st.Canonical, st.FallbackReason, st.LastSyncError = nz(can), nz(fr), nz(lse)
	return st, nil
}

func (r ptPGRepo) SaveState(ctx context.Context, st ptState) error {
	recs := st.CustomDomainRecords
	if len(recs) == 0 {
		recs = json.RawMessage("[]")
	}
	dns := st.DNSState
	if !dnsStates[dns] {
		dns = "UNKNOWN"
	}
	_, err := r.db.Exec(ctx, `
	  INSERT INTO public.portal_tls_state (id, central_available, onegate_hostname, dns_state, custom_domain,
	         custom_domain_status, custom_domain_records, custom_domain_error, canonical_hostname, https_active,
	         fallback_reason, fallback_since, last_sync_at, last_sync_error, updated_at)
	  VALUES (true, $1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10, $11, $12, $13, now())
	  ON CONFLICT (id) DO UPDATE SET central_available=EXCLUDED.central_available,
	         onegate_hostname=EXCLUDED.onegate_hostname, dns_state=EXCLUDED.dns_state,
	         custom_domain=EXCLUDED.custom_domain, custom_domain_status=EXCLUDED.custom_domain_status,
	         custom_domain_records=EXCLUDED.custom_domain_records, custom_domain_error=EXCLUDED.custom_domain_error,
	         canonical_hostname=EXCLUDED.canonical_hostname, https_active=EXCLUDED.https_active,
	         fallback_reason=EXCLUDED.fallback_reason, fallback_since=EXCLUDED.fallback_since,
	         last_sync_at=EXCLUDED.last_sync_at, last_sync_error=EXCLUDED.last_sync_error, updated_at=now()`,
		st.CentralAvailable, nullIfEmpty(st.OneGateHostname), dns, nullIfEmpty(st.CustomDomain),
		nullIfEmpty(st.CustomDomainStatus), string(recs), nullIfEmpty(st.CustomDomainError),
		nullIfEmpty(st.Canonical), st.HTTPSActive, nullIfEmpty(st.FallbackReason), st.FallbackSince,
		st.LastSyncAt, nullIfEmpty(st.LastSyncError))
	return err
}

func (r ptPGRepo) Certs(ctx context.Context) (map[string]*ptCertRow, error) {
	rows, err := r.db.Query(ctx, `
	  SELECT hostname, kind, serial, not_before, not_after, renew_after, fingerprint, installed_at,
	         pending_order_id::text, pending_since, last_error, last_attempt_at
	    FROM public.portal_certificates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*ptCertRow{}
	for rows.Next() {
		var c ptCertRow
		var serial, fp, pid, le *string
		if err := rows.Scan(&c.Hostname, &c.Kind, &serial, &c.NotBefore, &c.NotAfter, &c.RenewAfter, &fp,
			&c.InstalledAt, &pid, &c.PendingSince, &le, &c.LastAttemptAt); err != nil {
			return nil, err
		}
		c.Serial, c.Fingerprint, c.PendingOrderID, c.LastError = nz(serial), nz(fp), nz(pid), nz(le)
		out[c.Hostname] = &c
	}
	return out, rows.Err()
}

func (r ptPGRepo) UpsertCert(ctx context.Context, c *ptCertRow) error {
	_, err := r.db.Exec(ctx, `
	  INSERT INTO public.portal_certificates (hostname, kind, serial, not_before, not_after, renew_after,
	         fingerprint, installed_at, pending_order_id, pending_since, last_error, last_attempt_at, updated_at)
	  VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::uuid, $10, $11, $12, now())
	  ON CONFLICT (hostname) DO UPDATE SET kind=EXCLUDED.kind, serial=EXCLUDED.serial,
	         not_before=EXCLUDED.not_before, not_after=EXCLUDED.not_after, renew_after=EXCLUDED.renew_after,
	         fingerprint=EXCLUDED.fingerprint, installed_at=EXCLUDED.installed_at,
	         pending_order_id=EXCLUDED.pending_order_id, pending_since=EXCLUDED.pending_since,
	         last_error=EXCLUDED.last_error, last_attempt_at=EXCLUDED.last_attempt_at, updated_at=now()`,
		c.Hostname, c.Kind, nullIfEmpty(c.Serial), c.NotBefore, c.NotAfter, c.RenewAfter, nullIfEmpty(c.Fingerprint),
		c.InstalledAt, nullIfEmpty(c.PendingOrderID), c.PendingSince, nullIfEmpty(boundText(c.LastError, 1000)), c.LastAttemptAt)
	return err
}

func (r ptPGRepo) DeleteCert(ctx context.Context, host string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM public.portal_certificates WHERE hostname = $1`, host)
	return err
}

// ---- wiring and the local API --------------------------------------------------------------------------------

// initPortalTLS builds the agent. talksToCentral is false for an appliance with no Central or one Central
// removed: the agent still runs, and serves whatever valid certificate is installed until it expires.
func (s *server) initPortalTLS(c cfg, talksToCentral bool) *portalTLSAgent {
	store := &portaltls.Store{Dir: c.PortalTLSDir, Warn: func(msg string, args ...any) { slog.Warn(msg, args...) }}
	trust := ptTrustSystem
	if c.PortalTLSExtraRoots != "" {
		roots, err := portaltls.LoadExtraRoots(c.PortalTLSExtraRoots)
		if err != nil {
			slog.Error("portal-tls: SCD_PORTAL_TLS_EXTRA_ROOTS could not be loaded; only the system roots are trusted",
				"path", c.PortalTLSExtraRoots, "err", err)
		} else {
			store.ExtraRoots = roots
			trust = ptTrustTestRoots
			slog.Warn("portal certificates may chain to test roots",
				"path", c.PortalTLSExtraRoots, "roots", len(roots),
				"note", "SCD_PORTAL_TLS_EXTRA_ROOTS is for staging and test issuance only; never set it in production")
		}
	}
	if err := store.EnsureDir(); err != nil {
		slog.Warn("portal-tls: the certificate directory could not be prepared; HTTPS stays on the fallback until it can",
			"dir", store.Dir, "err", err)
	}
	var central ptCentral
	if talksToCentral {
		central = ptHTTPCentral{s: s}
	}
	return newPortalTLSAgent(ptPGRepo{db: s.db}, central, store, trust)
}

// portalTLSState: GET /v1/portal/tls-state. Guest-surface (portald polls it) and read by edged. Answered from
// memory: portald asks every 15 seconds and must never wait on the database for it.
func (s *server) portalTLSState(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.portalTLS.State())
}

// portalTLSKick: POST /v1/portal/tls/kick. Administrative (edged, after a Portal HTTPS settings change or
// "Check now").
func (s *server) portalTLSKick(w http.ResponseWriter, _ *http.Request) {
	if s.portalTLS == nil {
		httpErr(w, http.StatusServiceUnavailable, "portal tls agent not running")
		return
	}
	s.portalTLS.Kick()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// portalTLSRenew: POST /v1/portal/tls/renew. Administrative ("Renew now"): a new key and order for every
// hostname without an open order.
func (s *server) portalTLSRenew(w http.ResponseWriter, _ *http.Request) {
	if s.portalTLS == nil {
		httpErr(w, http.StatusServiceUnavailable, "portal tls agent not running")
		return
	}
	s.portalTLS.Renew()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}
