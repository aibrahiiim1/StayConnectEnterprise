package main

// THE ONE ANSWER TO "IS THIS APPLIANCE ACTIVATED, LICENSED AND TALKING TO CENTRAL".
//
// docs/CENTRAL_CONTROL_PLANE.md section 8. Hotel Admin used to assemble this from four endpoints that each
// computed their own version of it -- setup status, cloud info, licence status and the dashboard health -- and
// they disagreed: one said "unlicensed", another expected "Unlicensed"; one called a factory-clean appliance
// "Active"; the activation word came from the RAW assignment file rather than the verified one; and every poll
// of the setup screen fired a DNS lookup and three TCP dials at Central. computeCentralStatus is now the only
// place these states are derived, and everything that shows them -- /v1/central/status, the dashboard health
// summary and the overview -- reads it.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/appliancecert"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	lic "github.com/stayconnect/enterprise/license"
)

// Section 8 vocabularies.
const (
	activationNotRegistered = "not_registered"
	activationWaiting       = "waiting"
	activationActivating    = "activating"
	activationActivated     = "activated"
	activationRetired       = "retired"

	licNone          = "none"
	licActive        = "active"
	licExpiring      = "expiring"
	licGrace         = "grace"
	licExpired       = "expired"
	licSuspended     = "suspended"
	licRevoked       = "revoked"
	licWrongHardware = "wrong_hardware"

	centralConnected   = "connected"
	centralUnreachable = "unreachable"
	// centralCredentialRefused: Central is answering (a recent successful call) but refuses this appliance's
	// certificate on its mTLS calls -- typically after "Reissue certificate", until the new one is installed.
	// It is not "unreachable", and saying so sent operators looking for a network fault that did not exist.
	centralCredentialRefused = "credential_refused"
	centralNotConfigured     = "not_configured"
)

// expiringWithin is when an active licence starts being reported as "expiring" (section 3: <= 30 days).
const expiringWithin = 30 * 24 * time.Hour

// connectedWithin is how recent the last successful authenticated answer from Central must be for the link to
// count as connected. Every agent that talks to Central does so at least every five minutes while it has work
// (assignment poll 30 s; licence poll 60 s while unlicensed; certificate bootstrap <= 5 min; hello 5 min), so
// three of the slowest intervals without a single answer is a link that is down, not one that is idle.
const connectedWithin = 15 * time.Minute

// ---- Central contact tracking ------------------------------------------------------------------------------

// centralContact remembers the last time Central answered an authenticated call, and the last failure, in words
// an operator can act on. It is fed by every agent that talks to Central.
type centralContact struct {
	mu       sync.Mutex
	lastOK   time.Time
	lastFail time.Time
	lastErr  string
	// lastFailCode is the HTTP status of the last failure (0 when it was not an HTTP answer).
	lastFailCode int
}

// record notes one call's outcome. Central answering "nothing for you yet" (no licence, certificate pending) is
// a successful conversation.
func (c *centralContact) record(err error, now time.Time) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil || errors.Is(err, licstate.ErrNoLicenseYet) || errors.Is(err, appliancecert.ErrCertPending) {
		c.lastOK = now
		return
	}
	c.lastFail = now
	c.lastErr = describeCentralErr(err)
	c.lastFailCode = httpCodeOf(err)
}

// httpCodeOf returns the HTTP status an error carries, or 0.
func httpCodeOf(err error) int {
	var se *identity.StatusError
	var he *licstate.HTTPError
	var ce *httpStatusErr
	switch {
	case errors.As(err, &se):
		return se.Code
	case errors.As(err, &he):
		return he.Code
	case errors.As(err, &ce):
		return ce.code
	}
	return 0
}

func (c *centralContact) failCode() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastFailCode
}

func (c *centralContact) snapshot() (ok, fail time.Time, lastErr string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastOK, c.lastFail, c.lastErr
}

// httpStatusErr is Central answering an appliance call with an unexpected status.
type httpStatusErr struct{ code int }

func (e *httpStatusErr) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// describeCentralErr turns a transport or protocol error into one short sentence for the Hotel Admin screen.
// It never includes URLs, keys or response bodies.
func describeCentralErr(err error) string {
	code := 0
	var se *identity.StatusError
	var he *licstate.HTTPError
	var ce *httpStatusErr
	switch {
	case errors.As(err, &se):
		code = se.Code
	case errors.As(err, &he):
		code = he.Code
	case errors.As(err, &ce):
		code = ce.code
	}
	if code != 0 {
		switch {
		case code == http.StatusUnauthorized || code == http.StatusForbidden:
			return fmt.Sprintf("OneGate Central did not accept this appliance's credentials (HTTP %d).", code)
		case code >= 500:
			return fmt.Sprintf("OneGate Central reported a problem on its side (HTTP %d).", code)
		default:
			return fmt.Sprintf("OneGate Central refused the request (HTTP %d).", code)
		}
	}
	var cve *tls.CertificateVerificationError
	var ua x509.UnknownAuthorityError
	var hn x509.HostnameError
	var ci x509.CertificateInvalidError
	if errors.As(err, &cve) || errors.As(err, &ua) || errors.As(err, &hn) || errors.As(err, &ci) ||
		strings.Contains(err.Error(), "x509:") {
		return "This appliance does not trust OneGate Central's certificate."
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return "The OneGate Central address could not be resolved (DNS)."
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "OneGate Central did not answer in time."
	}
	if strings.Contains(err.Error(), "connection refused") {
		return "OneGate Central refused the connection."
	}
	return "Could not reach OneGate Central."
}

// ---- the computation ----------------------------------------------------------------------------------------

// licSnapshot is the licence as the manager evaluates it locally.
type licSnapshot struct {
	Installed     bool // a signed licence document is installed (whatever its state or binding)
	WrongHardware string
	WANMismatch   string
	State         lic.State
	ValidUntil    time.Time
	GraceEnds     time.Time
	MaxGuests     int64 // > 0 capped; <= 0 unlimited
	Version       int64
	LicenseID     string
}

type centralInputs struct {
	Now          time.Time
	ApplianceID  string
	Serial       string
	Asg          assignment.Resolution
	CustomerName string
	SiteName     string
	SiteType     string

	CertRequired bool // a certificate lifecycle is configured on this appliance
	CertReady    bool
	CertFpr      string
	CertNotAfter time.Time

	Lic           licSnapshot
	CurrentGuests *int64

	CentralConfigured bool
	LastOK, LastFail  time.Time
	LastErr           string
	LastFailCode      int

	IdentityFpr, WANMAC, LANMAC, Endpoint string
	Version, PermissiveBlocked            string

	// Removed: Central deleted this appliance after it held a customer (removed_from_central.go).
	Removed bool
}

type centralLicense struct {
	State         string     `json:"state"`
	ValidUntil    *time.Time `json:"valid_until"`
	GraceEndsAt   *time.Time `json:"grace_ends_at"`
	DaysLeft      *int       `json:"days_left"`
	MaxGuests     *int64     `json:"max_concurrent_online_guests"` // null: no licence, or unlimited
	CurrentGuests *int64     `json:"current_online_guests"`
	// HardwareNotice is set when the WAN network adapter differs from the one the licence names. The licence
	// stays in force (there is no time limit); a rebind issues a corrected one.
	HardwareNotice string `json:"hardware_notice,omitempty"`
}

type centralLink struct {
	State         string     `json:"state"`
	LastContactAt *time.Time `json:"last_contact_at"`
	LastError     *string    `json:"last_error"`
}

type centralDetails struct {
	IdentityKeyFingerprint string     `json:"identity_key_fingerprint"`
	CertFingerprint        string     `json:"cert_fingerprint"`
	CertNotAfter           *time.Time `json:"cert_not_after"`
	AssignmentVersion      *int64     `json:"assignment_version"`
	LicenseVersion         *int64     `json:"license_version"`
	WANMAC                 string     `json:"wan_mac"`
	LANMAC                 string     `json:"lan_mac"`
	CentralEndpoint        string     `json:"central_endpoint"`
	// Additive, for support: the verified assignment outcome, so "waiting" caused by an assignment the appliance
	// refused is distinguishable from "waiting" because nothing was ever sent.
	AssignmentStatus  string `json:"assignment_status"`
	LicenseID         string `json:"license_id,omitempty"`
	SoftwareVersion   string `json:"software_version,omitempty"`
	BuildProfile      string `json:"build_profile,omitempty"`
	PermissiveBlocked string `json:"permissive_blocked,omitempty"`
	// Reason qualifies a "retired" activation. "removed_from_central": Central deleted this appliance after it
	// had held a customer; it keeps its data, serves no new guests and will not register again until it is
	// factory-reset and activated anew.
	Reason string `json:"reason,omitempty"`
}

// CentralStatus is the section 8 document.
type CentralStatus struct {
	Activation   string  `json:"activation"`
	Serial       string  `json:"serial"`
	ApplianceID  *string `json:"appliance_id"`
	CustomerName *string `json:"customer_name"`
	SiteName     *string `json:"site_name"`
	// SiteType is descriptive metadata from the signed assignment. It authorises nothing.
	SiteType *string        `json:"site_type"`
	License  centralLicense `json:"license"`
	Central  centralLink    `json:"central"`
	Details  centralDetails `json:"details"`
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func daysUntil(t, now time.Time) *int {
	d := int(math.Ceil(t.Sub(now).Hours() / 24))
	if d < 0 {
		d = 0
	}
	return &d
}

func centralLinkState(in centralInputs) centralLink {
	out := centralLink{LastContactAt: timePtr(in.LastOK)}
	switch {
	case !in.CentralConfigured:
		out.State = centralNotConfigured
		out.LastContactAt = nil
		return out
	case !in.LastOK.IsZero() && !in.LastOK.Before(in.LastFail) && in.Now.Sub(in.LastOK) <= connectedWithin:
		out.State = centralConnected
		return out
	}
	// Central answered recently, and the failing calls are refusals of the certificate: reachable, not trusted.
	if (in.LastFailCode == http.StatusUnauthorized || in.LastFailCode == http.StatusForbidden) &&
		!in.LastOK.IsZero() && in.Now.Sub(in.LastOK) <= connectedWithin && in.LastFail.After(in.LastOK) {
		out.State = centralCredentialRefused
		e := "OneGate Central answers, but no longer accepts this appliance's certificate (HTTP " +
			strconv.Itoa(in.LastFailCode) + "). This follows a certificate reissue in Central; the appliance requests a new certificate by itself."
		out.LastError = &e
		return out
	}
	out.State = centralUnreachable
	switch {
	case !in.LastFail.IsZero() && in.LastFail.After(in.LastOK) && in.LastErr != "":
		out.LastError = strPtr(in.LastErr)
	case !in.LastOK.IsZero():
		e := "OneGate Central has not answered for more than 15 minutes."
		out.LastError = &e
	}
	return out
}

func licenseView(in centralInputs) centralLicense {
	l := in.Lic
	out := centralLicense{CurrentGuests: in.CurrentGuests, HardwareNotice: l.WANMismatch}
	if !l.Installed {
		out.State = licNone
		out.HardwareNotice = ""
		return out
	}
	out.ValidUntil = timePtr(l.ValidUntil)
	out.GraceEndsAt = timePtr(l.GraceEnds)
	if l.MaxGuests > 0 {
		m := l.MaxGuests
		out.MaxGuests = &m
	}
	if l.WrongHardware != "" {
		out.State = licWrongHardware
		out.MaxGuests = nil
		return out
	}
	switch l.State {
	case lic.StateRevoked:
		out.State = licRevoked
	case lic.StateSuspended:
		out.State = licSuspended
	case lic.StateExpired:
		out.State = licExpired
		zero := 0
		out.DaysLeft = &zero
	case lic.StateGracePeriod, lic.StateRestricted:
		out.State = licGrace
		out.DaysLeft = daysUntil(l.GraceEnds, in.Now)
	case lic.StateActive:
		out.State = licActive
		if !l.ValidUntil.IsZero() {
			out.DaysLeft = daysUntil(l.ValidUntil, in.Now)
			if l.ValidUntil.Sub(in.Now) <= expiringWithin {
				out.State = licExpiring
			}
		}
	default: // unlicensed or unknown: nothing usable is installed
		out.State = licNone
		out.ValidUntil, out.GraceEndsAt, out.MaxGuests = nil, nil, nil
	}
	return out
}

// computeCentralStatus derives every section 8 state from its inputs. Pure: no I/O, no clock.
func computeCentralStatus(in centralInputs) CentralStatus {
	link := centralLinkState(in)
	licv := licenseView(in)
	out := CentralStatus{
		Serial:      in.Serial,
		ApplianceID: strPtr(in.ApplianceID),
		License:     licv,
		Central:     link,
		Details: centralDetails{
			IdentityKeyFingerprint: in.IdentityFpr, CertFingerprint: in.CertFpr, CertNotAfter: timePtr(in.CertNotAfter),
			WANMAC: in.WANMAC, LANMAC: in.LANMAC, CentralEndpoint: in.Endpoint,
			AssignmentStatus: in.Asg.Outcome.String(), LicenseID: in.Lic.LicenseID,
			SoftwareVersion: in.Version, BuildProfile: buildprofile.Name, PermissiveBlocked: in.PermissiveBlocked,
		},
	}
	if in.Asg.Version > 0 {
		v := in.Asg.Version
		out.Details.AssignmentVersion = &v
	}
	if in.Lic.Installed && in.Lic.Version > 0 {
		v := in.Lic.Version
		out.Details.LicenseVersion = &v
	}

	switch {
	case in.ApplianceID == "":
		out.Activation = activationNotRegistered
	case in.Asg.Outcome == assignment.OutcomeNotGranting &&
		(in.Asg.State == assignment.StateRevoked || in.Asg.State == assignment.StateDecommissioned):
		out.Activation = activationRetired
	case in.Asg.Outcome == assignment.OutcomeGranted:
		out.CustomerName, out.SiteName = strPtr(in.CustomerName), strPtr(in.SiteName)
		out.SiteType = strPtr(in.SiteType)
		switch {
		case !in.Lic.Installed:
			// Assigned, licence not collected yet.
			out.Activation = activationActivating
		case in.CertRequired && !in.CertReady && link.State == centralConnected:
			// Licence in, certificate still being collected while Central is answering. An offline-activated
			// appliance that cannot reach Central is NOT held here: it is fully operational without one.
			out.Activation = activationActivating
		default:
			out.Activation = activationActivated
		}
	default:
		// Registered, and no verified granting assignment: absent, refused, or returned to inventory.
		out.Activation = activationWaiting
	}
	if in.Removed {
		// Whatever the assignment on disk says, Central no longer knows this appliance and it will not
		// register again: to the operator it is retired, with the reason that tells them what to do.
		out.Activation = activationRetired
		out.Details.Reason = removedFromCentralCode
	}
	return out
}

// ---- gathering the inputs -----------------------------------------------------------------------------------

func (s *server) licSnapshot() licSnapshot {
	if s.lic == nil {
		return licSnapshot{}
	}
	ev, loaded := s.lic.Evaluation()
	out := licSnapshot{Installed: loaded && ev.Doc != nil, State: s.lic.State(),
		WrongHardware: s.lic.WrongHardware(), WANMismatch: s.lic.HardwareMismatch()}
	if out.Installed {
		out.State = ev.State
		out.ValidUntil, out.GraceEnds = ev.Doc.ValidUntil, ev.GraceUntil
		out.MaxGuests = s.lic.MaxConcurrentOnlineGuests()
		out.Version, out.LicenseID = ev.Doc.LicenseVersion, ev.Doc.LicenseID
	}
	return out
}

// resolveOwnAssignment is the verified assignment, exactly as boot resolves it. No logger: this runs on every
// status poll, and boot already logged why a document was refused.
func (s *server) resolveOwnAssignment(now time.Time) (assignment.Resolution, *assignment.Document) {
	if s.applID == "" {
		return assignment.Resolution{Outcome: assignment.OutcomeAbsent}, nil
	}
	r := assignment.Resolve(assignmentPaths(), assignment.ApplianceBinding{
		ApplianceID: s.applID, Serial: s.serial, PublicKeyB64: s.identityPubB64,
	}, now, nil)
	var doc *assignment.Document
	if r.Assigned() {
		doc = s.assignmentStore().Doc()
	}
	return r, doc
}

func (s *server) centralInputs(ctx context.Context) centralInputs {
	now := time.Now().UTC()
	in := centralInputs{
		Now: now, ApplianceID: s.applID, Serial: firstNonEmpty(s.hw.Serial, s.serial),
		Lic: s.licSnapshot(), CentralConfigured: s.ctrlBase != "",
		IdentityFpr: s.identityKeyFpr, WANMAC: s.hw.WANMAC, LANMAC: s.hw.LANMAC, Endpoint: s.ctrlBase,
		Version: scdVersion, PermissiveBlocked: s.permissiveBlocked,
		Removed: s.isRemovedFromCentral(),
	}
	var doc *assignment.Document
	in.Asg, doc = s.resolveOwnAssignment(now)
	if doc != nil {
		in.CustomerName, in.SiteName, in.SiteType = doc.TenantName, doc.SiteName, doc.SiteType
	}
	if s.certMgr != nil {
		in.CertRequired = true
		st := s.certMgr.Status()
		in.CertReady, _ = st["mtls_ready"].(bool)
		in.CertFpr, _ = st["cert_fingerprint"].(string)
		in.CertNotAfter, _ = st["not_after"].(time.Time)
	}
	in.LastOK, in.LastFail, in.LastErr = s.central.snapshot()
	in.LastFailCode = s.central.failCode()
	if s.db != nil {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		if n, err := s.activeSessionCount(cctx); err == nil {
			v := int64(n)
			in.CurrentGuests = &v
		}
		cancel()
	}
	return in
}

// ---- handlers -----------------------------------------------------------------------------------------------

// centralStatus: GET /v1/central/status. Local facts only -- it never contacts Central, so polling it costs
// nothing outside the appliance.
func (s *server) centralStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, computeCentralStatus(s.centralInputs(r.Context())))
}

// centralRefresh: POST /v1/central/refresh -- Check now. Registers if the appliance is not registered yet;
// otherwise asks Central now (hello), and wakes the certificate, assignment and licence agents so they do not
// wait out their backoff. Network diagnostics run HERE, on request, and nowhere else.
func (s *server) centralRefresh(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	var diag map[string]any
	wg.Add(1)
	go func() { defer wg.Done(); diag = s.networkChecks(ctx) }()

	// A removed appliance does not talk to Central at all: Check now only re-reads the local state.
	if s.ctrlBase != "" && !s.isRemovedFromCentral() {
		if s.applID == "" {
			if s.reg != nil {
				_, _ = s.reg.attempt(ctx) // on success the registrar re-execs scd shortly after we answer
			}
		} else {
			if s.certMgr != nil {
				s.certMgr.Kick()
			}
			if s.certReconcile != nil {
				select {
				case s.certReconcile <- struct{}{}:
				default:
				}
			}
			if s.lic != nil {
				s.lic.Kick()
			}
			select {
			case s.asgKick <- struct{}{}:
			default:
			}
			_ = s.hello(ctx)
		}
	}
	wg.Wait()
	st := computeCentralStatus(s.centralInputs(r.Context()))
	b, _ := json.Marshal(st)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	out["diagnostics"] = diag
	writeJSON(w, http.StatusOK, out)
}

// networkChecks probes Central reachability without leaking secrets: DNS resolution and the HTTPS / mutual-TLS
// ports. It is run only by Check now -- it used to run on every five-second poll of the setup screen.
func (s *server) networkChecks(ctx context.Context) map[string]any {
	out := map[string]any{"clock": time.Now().UTC()}
	host, port := "", "443"
	if u, err := url.Parse(s.ctrlBase); err == nil {
		host = u.Hostname()
		if u.Port() != "" {
			port = u.Port()
		}
	}
	if host == "" {
		return out
	}
	var res net.Resolver
	_, err := res.LookupHost(ctx, host)
	out["dns_ok"] = err == nil
	dial := func(addr string) bool {
		d := net.Dialer{Timeout: 3 * time.Second}
		c, err := d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return false
		}
		c.Close()
		return true
	}
	out["central_https"] = dial(net.JoinHostPort(host, port))
	if s.certMgr != nil {
		if _, base, _ := s.certMgr.Transport(); base != "" {
			if u, err := url.Parse(base); err == nil && u.Host != "" {
				out["central_mtls"] = dial(u.Host)
			}
		}
	}
	return out
}

// hello is the signed liveness call (GET /v1/appliance/hello). It records Central contact and returns the
// status so the orphan detector can act on a definitive "unknown appliance".
func (s *server) hello(ctx context.Context) error {
	_, _, err := s.helloRaw(ctx)
	return err
}

func (s *server) helloRaw(ctx context.Context) (int, []byte, error) {
	if s.idPriv == nil || s.applID == "" || s.ctrlBase == "" {
		return 0, nil, errors.New("not registered")
	}
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, http.MethodGet, "/v1/appliance/hello", nil)
	if err != nil {
		return 0, nil, err
	}
	req, _ := http.NewRequestWithContext(hctx, http.MethodGet, s.ctrlBase+"/v1/appliance/hello", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.central.record(err, time.Now())
		return 0, nil, err
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		herr := &httpStatusErr{code: resp.StatusCode}
		s.central.record(herr, time.Now())
		return resp.StatusCode, body, herr
	}
	s.central.record(nil, time.Now())
	return resp.StatusCode, body, nil
}

// centralOfflinePackage: POST /v1/central/offline-package. One upload for both files an operator may carry in:
// a FIRST-ACTIVATION package (answers this appliance's activation request: signed assignment + trust + licence)
// or an offline LICENCE package for an appliance that is already activated. The appliance tells them apart by
// shape and verifies each on its own terms.
func (s *server) centralOfflinePackage(w http.ResponseWriter, r *http.Request) {
	if s.refuseWhileRemoved(w) {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil || len(bytes.TrimSpace(raw)) == 0 {
		httpErr(w, http.StatusBadRequest, "empty upload")
		return
	}
	var probe struct {
		RequestID  string          `json:"request_id"`
		Assignment json.RawMessage `json:"assignment"`
	}
	if json.Unmarshal(raw, &probe) != nil {
		httpErr(w, http.StatusBadRequest, "that file is not an activation or licence package")
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if probe.RequestID != "" && len(probe.Assignment) > 0 && string(probe.Assignment) != "null" {
		s.setupActivationPackage(w, r)
		return
	}
	s.setupOfflineImport(w, r)
}
