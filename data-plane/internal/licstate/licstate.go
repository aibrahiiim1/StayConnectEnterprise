// Package licstate manages the appliance's local view of its vendor-signed license: loading and verifying the
// envelope, fetching renewals, evaluating state, and bridging limits into the site database. Guest authentication
// never calls the cloud; it evaluates against the on-disk envelope and the local clock.
package licstate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	lic "github.com/stayconnect/enterprise/license"
)

const (
	FeatEmailOTP = "email_otp"
	FeatSMSOTP   = "sms_otp"
	// FeatWhatsAppOTP is the WhatsApp one-time code channel. It is its own module (whatsapp_otp), never SMS.
	FeatWhatsAppOTP = "whatsapp_otp"
	FeatSocialLogin = "social_login"
	FeatPMS         = "pms"
	FeatPaidWiFi    = "paid_wifi"
	FeatHA          = "ha"
	FeatWhiteLabel  = "white_label"
)

type Manager struct {
	store       *lic.Store
	db          *pgxpool.Pool
	tenantID    string
	applianceID string

	// local is this appliance's identity/hardware facts used to verify a license was minted for this box; the identity
	// key is the trust anchor, serial/hardware/WAN-MAC are mismatch and clone signals.
	// hwMismatch holds a reason when the identity matches but the WAN MAC differs (NIC swap): a warning, not a reject.
	// wrongDevice holds a reason when the licence is bound to a different appliance. It is recomputed on every
	// evaluation, boot included, so a copied licence is refused after a reboot.
	local lic.LocalIdentity
	// assignmentLookup reports the customer and site of this appliance's granting signed assignment (AD-15(b)).
	assignmentLookup func() (tenantID, siteID string, ok bool)
	hwMismatch       string
	wrongDevice      string

	// Required=false (dev/pilot): no licence gives permissive unlicensed mode. Required=true (production): no licence
	// behaves as Expired.
	required bool

	mu      sync.RWMutex
	current lic.Evaluation
	loaded  bool

	// loadMu serialises Load so two evaluations never interleave their state updates and limit syncs.
	// lastSynced is the licence whose limits were last written successfully, lastLogged the last logged evaluation;
	// an unchanged evaluation neither rewrites the limit rows nor logs again.
	loadMu       sync.Mutex
	lastSynced   string
	lastLogged   string
	syncLimitsFn func(context.Context, *lic.Document) error

	// mTLS transport: once a client cert exists, fetches use the mutual-TLS listener; nil means ctrlBase.
	mtlsClient *http.Client
	mtlsBase   string

	// kick asks for an immediate fetch; onFetch observes each fetch outcome; fastEvery is the poll interval while no
	// licence is installed.
	kick      chan struct{}
	onFetch   func(error)
	fastEvery time.Duration
}

// DefaultFastFetchInterval is how often an appliance with no installed licence asks Central for one.
const DefaultFastFetchInterval = time.Minute

// ErrNoLicenseYet is Central answering that no licence has been issued yet; a successful conversation, not a
// connectivity failure.
var ErrNoLicenseYet = errors.New("no license issued for this appliance yet")

type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("license fetch: HTTP %d: %s", e.Code, e.Body) }

func (m *Manager) OnFetch(fn func(error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onFetch = fn
}

// Kick asks the fetch loop to contact Central now; non-blocking, and pending kicks merge.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// SetMTLSTransport routes cloud licence fetches over the given mTLS client and base URL; safe at runtime.
func (m *Manager) SetMTLSTransport(client *http.Client, base string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mtlsClient = client
	m.mtlsBase = base
}

func (m *Manager) transport() (*http.Client, string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.mtlsClient != nil && m.mtlsBase != "" {
		return m.mtlsClient, m.mtlsBase, true
	}
	return nil, "", false
}

// New creates the manager. One is returned even when the pub key is missing (unlicensed mode) so the appliance
// boots; Install/Fetch fail until the key exists.
func New(db *pgxpool.Pool, tenantID, dir, pubKeyPath string, required bool) *Manager {
	m := &Manager{db: db, tenantID: tenantID, required: required, kick: make(chan struct{}, 1),
		fastEvery: DefaultFastFetchInterval}
	raw, err := os.ReadFile(pubKeyPath)
	if err != nil || len(raw) != 32 {
		if required {
			slog.Error("licstate: vendor public key unavailable and license required",
				"path", pubKeyPath, "err", err)
		} else {
			slog.Warn("licstate: vendor public key unavailable — unlicensed dev mode",
				"path", pubKeyPath)
		}
		return m
	}
	m.store = lic.NewStore(dir, lic.NewVerifier(raw))
	return m
}

func (m *Manager) Load(ctx context.Context) {
	if m.store == nil {
		return
	}
	m.loadMu.Lock()
	defer m.loadMu.Unlock()
	ev, err := m.store.Evaluate(time.Now().UTC())
	m.mu.Lock()
	if err != nil {
		if errors.Is(err, lic.ErrStateDamaged) {
			slog.Error("licstate: licence state damaged — treating the appliance as unlicensed", "err", err)
		} else if !errors.Is(err, lic.ErrNoLicense) {
			slog.Warn("licstate: evaluate failed", "err", err)
		}
		m.loaded = false
		m.wrongDevice, m.hwMismatch = "", ""
		m.lastSynced, m.lastLogged = "", ""
		m.mu.Unlock()
		return
	}
	// The binding is checked on every evaluation, not only at install, so a licence copied from another appliance or
	// an identity replaced under an installed licence fails closed after a reboot.
	res, reason := lic.BindingOK, ""
	if ev.Doc != nil {
		res, reason = ev.Doc.CheckBinding(m.local)
	}
	prevWrong := m.wrongDevice
	m.wrongDevice, m.hwMismatch = "", ""
	switch res {
	case lic.BindingWrongDevice:
		m.wrongDevice = reason
	case lic.BindingWANMismatch:
		m.hwMismatch = reason
	}
	m.current = ev
	m.loaded = true
	wrong := m.wrongDevice
	m.mu.Unlock()

	if wrong != "" {
		if prevWrong == "" {
			slog.Error("licstate: installed licence is bound to a DIFFERENT appliance — refusing it (no new guest sessions)",
				"reason", wrong, "license_id", ev.Doc.LicenseID)
		}
		return // never bridge the limits of a licence this appliance does not hold
	}

	docKey := documentKey(ev.Doc)
	evKey := fmt.Sprintf("%s|%s|%t|%t", docKey, ev.State, ev.CloudStale, ev.ClockRollback)
	if evKey != m.lastLogged {
		slog.Info("licstate: license evaluated",
			"state", ev.State, "license_id", ev.Doc.LicenseID,
			"plan", ev.Doc.CommercialPlanCode, "valid_until", ev.Doc.ValidUntil,
			"cloud_stale", ev.CloudStale, "clock_rollback", ev.ClockRollback,
			"unbound_legacy_licence", ev.Doc.IsUnbound())
		m.lastLogged = evKey
	}
	if docKey == m.lastSynced {
		return
	}
	syncFn := m.syncLimits
	if m.syncLimitsFn != nil {
		syncFn = m.syncLimitsFn
	}
	if err := syncFn(ctx, ev.Doc); err != nil {
		slog.Warn("licstate: limit bridge sync failed", "err", err)
		return
	}
	m.lastSynced = docKey
}

// documentKey identifies a licence document by its signed content; the limits bridged into the database depend on
// nothing else.
func documentKey(d *lic.Document) string {
	raw, err := d.Marshal()
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (m *Manager) Evaluation() (lic.Evaluation, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current, m.loaded
}

// State returns the effective operational state; unlicensed maps to Active (dev) or Expired (required).
func (m *Manager) State() lic.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.loaded && m.wrongDevice != "" {
		// A licence for another appliance is no licence at all, in EVERY build profile.
		return lic.StateUnlicensed
	}
	if !m.loaded {
		if m.required {
			// No valid signed licence on a production appliance: the safe state. Never Active, so no guest auth, nft, shaping,
			// accounting or session is created; config errors fail closed.
			return lic.StateUnlicensed
		}
		return lic.StateActive // permissive unlicensed-dev (development builds only)
	}
	return m.current.State
}

func (m *Manager) AllowsNewSessions() bool { return m.State().AllowsNewSessions() }

// MaxConcurrentOnlineGuests returns the concurrent-guest cap from the local signed licence (-1 unlimited). It never
// consults Central.
func (m *Manager) MaxConcurrentOnlineGuests() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.loaded && m.wrongDevice != "" {
		return 0
	}
	if !m.loaded || m.current.Doc == nil {
		if m.required {
			return 0 // unlicensed production: cap 0 (defence in depth; gate denies first)
		}
		return -1 // permissive dev only
	}
	if v := m.current.Doc.EffectiveMaxConcurrentOnlineGuests(); v > 0 {
		return int64(v)
	}
	return -1
}

// FeatureEnabled aliases ModuleEnabled: since licence v4 the signed modules map is the sole authority (v1-v3 are
// read through license.AuthorizedModules) and a v4 Features block is never consulted.
func (m *Manager) FeatureEnabled(name string) bool {
	switch name {
	case FeatPMS:
		return m.ModuleEnabled(lic.ModuleHospitality)
	case FeatPaidWiFi:
		return m.ModuleEnabled(lic.ModulePaidAccess)
	case FeatEmailOTP, FeatSMSOTP, FeatWhatsAppOTP, FeatSocialLogin, FeatHA, FeatWhiteLabel:
		return m.ModuleEnabled(name)
	default:
		return false
	}
}

// ModuleEnabled reports whether the licence authorises module id and the state allows entitled features (Active or
// Grace). Unlicensed dev mode allows everything.
func (m *Manager) ModuleEnabled(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded {
		return !m.required
	}
	if m.wrongDevice != "" || m.current.Doc == nil {
		return false
	}
	return lic.FeatureEnabled(m.current.State, m.current.Doc.AuthorizedModules()[id])
}

// AuthorizedModules returns the modules the licence authorises regardless of state (for display).
func (m *Manager) AuthorizedModules() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded || m.wrongDevice != "" || m.current.Doc == nil {
		return nil
	}
	return m.current.Doc.AuthorizedModules()
}

func (m *Manager) LicenseRequired() bool { return m.required }

func (m *Manager) Install(ctx context.Context, raw []byte) (*lic.Document, error) {
	if m.store == nil {
		return nil, errors.New("vendor public key not installed")
	}
	// Enforce the binding before persisting so a licence for another appliance is never written to disk.
	pre, err := m.store.Verify(raw)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	local, applianceID, asgLookup := m.local, m.applianceID, m.assignmentLookup
	m.mu.RUnlock()
	// AD-15: install is stricter than evaluation. An unbound v4 licence, a blank local identity field the licence
	// names, or an appliance outside the licence's list is refused here; a licence ALREADY installed keeps being
	// evaluated by CheckBinding in Load, so no running appliance is bricked by this.
	res, reason := pre.CheckInstallBinding(local)
	if res == lic.BindingOK && asgLookup != nil {
		// AD-15(b): when this appliance holds a granting signed assignment, the licence must name the same customer and
		// site. Without an assignment yet (a licence can arrive first) there is nothing to compare.
		if tenant, site, ok := asgLookup(); ok && (pre.TenantID != tenant || pre.SiteID != site) {
			res, reason = lic.BindingWrongDevice, "the licence is for a different customer or site than this appliance's signed assignment"
		}
	}
	if res == lic.BindingWrongDevice {
		slog.Warn("licstate: license REJECTED — bound to a different appliance",
			"reason", reason, "license_id", pre.LicenseID,
			"doc_serial", pre.ApplianceSerial, "local_serial", local.Serial,
			"doc_idfpr", pre.IdentityKeyFingerprint, "local_idfpr", local.IdentityKeyFingerprint)
		return nil, fmt.Errorf("license rejected: %s — this license is bound to a different appliance", reason)
	}

	doc, err := m.store.Install(raw, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	// A WAN MAC mismatch is a soft signal: the licence stays in force with a warning until a rebind. Load re-derives it
	// on every evaluation.
	if res == lic.BindingWANMismatch {
		slog.Warn("licstate: hardware binding mismatch", "reason", reason,
			"license_id", doc.LicenseID, "doc_wan_mac", doc.WANMAC, "local_wan_mac", local.WANMAC)
	}
	if applianceID != "" && len(doc.ApplianceIDs) > 0 && !applianceBound(doc, applianceID) {
		slog.Warn("licstate: appliance not listed in license binding",
			"appliance_id", applianceID, "license_id", doc.LicenseID, "bound_count", len(doc.ApplianceIDs))
	}
	m.Load(ctx)
	return doc, nil
}

// SetAssignmentLookup gives Install the signed assignment's customer and site to compare a new licence with.
func (m *Manager) SetAssignmentLookup(fn func() (tenantID, siteID string, ok bool)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.assignmentLookup = fn
}

func (m *Manager) SetLocalIdentity(local lic.LocalIdentity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.local = local
}

// WrongHardware returns a reason when the licence is bound to a different appliance and is refused.
func (m *Manager) WrongHardware() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded {
		return ""
	}
	return m.wrongDevice
}

// HasUsableLicense reports whether a real signed licence for this appliance is installed; it sets the fast
// first-licence fetch cadence.
func (m *Manager) HasUsableLicense() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loaded && m.wrongDevice == "" && m.current.Doc != nil
}

// HardwareMismatch returns a reason when the licence matches this identity but the WAN MAC differs (rebind needed).
func (m *Manager) HardwareMismatch() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.hwMismatch
}

func applianceBound(d *lic.Document, id string) bool {
	for _, a := range d.ApplianceIDs {
		if a == id {
			return true
		}
	}
	return false
}

// syncLimits rewrites the license-sourced rows of tenant_effective_limits in the site database; licence 0
// (unlimited) maps to the view's -1.
func (m *Manager) syncLimits(ctx context.Context, d *lic.Document) error {
	if m.db == nil {
		return nil
	}
	unlim := func(v int) int64 {
		if v <= 0 {
			return -1
		}
		return int64(v)
	}
	ints := map[string]int64{
		"max_concurrent_devices":    unlim(d.EffectiveMaxConcurrentOnlineGuests()),
		"max_operators":             unlim(d.Limits.MaxLocalOperators),
		"max_guest_access_plans":    unlim(d.Limits.MaxGuestAccessPlans),
		"max_appliances":            unlim(d.Limits.MaxAppliancesForSite),
		"retention_days_accounting": unlim(d.Limits.AccountingRetentionDays),
		"retention_days_audit":      unlim(d.Limits.AuditRetentionDays),
	}
	am := d.AuthorizedModules()
	bools := map[string]bool{
		"feature.pms_integration":   am[lic.ModuleHospitality],
		"feature.paid_wifi":         am[lic.ModulePaidAccess],
		"feature.auth.sms_otp":      am[lic.ModuleSMSOTP],
		"feature.auth.whatsapp_otp": am[lic.ModuleWhatsAppOTP],
		"feature.auth.email_otp":    am[lic.ModuleEmailOTP],
		"feature.auth.social":       am[lic.ModuleSocialLogin],
		"feature.ha_pair":           am[lic.ModuleHA],
		"feature.white_label":       am[lic.ModuleWhiteLabel],
	}
	tx, err := m.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for k, v := range ints {
		if _, err := tx.Exec(ctx, `
            INSERT INTO tenant_effective_limits (tenant_id, key, value_type, int_value, source, updated_at)
            VALUES ($1,$2,'int',$3,'license',now())
            ON CONFLICT (tenant_id, key) DO UPDATE
              SET int_value = EXCLUDED.int_value, value_type='int', source='license', updated_at=now()
        `, m.tenantID, k, v); err != nil {
			return err
		}
	}
	for k, v := range bools {
		if _, err := tx.Exec(ctx, `
            INSERT INTO tenant_effective_limits (tenant_id, key, value_type, bool_value, source, updated_at)
            VALUES ($1,$2,'bool',$3,'license',now())
            ON CONFLICT (tenant_id, key) DO UPDATE
              SET bool_value = EXCLUDED.bool_value, value_type='bool', source='license', updated_at=now()
        `, m.tenantID, k, v); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// FetchFromCloud pulls the signed licence over the authenticated channel and installs it; a 200 counts as cloud
// validation and revoked ids feed the local revocation store.
func (m *Manager) FetchFromCloud(ctx context.Context, ctrlBase, applianceID string, priv ed25519.PrivateKey) error {
	if m.store == nil {
		return errors.New("vendor public key not installed")
	}
	m.mu.Lock()
	m.applianceID = applianceID
	m.mu.Unlock()
	tok, err := applianceauth.SignRequest(priv, applianceID, http.MethodGet, "/v1/appliance/license", nil)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	// Prefer the mTLS transport once a client cert is installed (signed-JWT still applies on top).
	base := ctrlBase
	client := &http.Client{Timeout: 10 * time.Second}
	mc, mb, viaMTLS := m.transport()
	if viaMTLS {
		base, client = mb, mc
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/appliance/license", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent {
		return ErrNoLicenseYet
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &HTTPError{Code: resp.StatusCode, Body: string(body)}
	}
	var out struct {
		LicenseID string          `json:"license_id"`
		Envelope  json.RawMessage `json:"envelope"`
		Revoked   []string        `json:"revoked"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return err
	}
	// AD-16: a revocation list is not signed, and a revocation cannot be undone, so it is honoured only over the
	// mutual-TLS channel (pinned to Central's CA; AD-2). Before the client certificate exists, a TLS-intercepting proxy
	// could otherwise revoke the licence; the list is ignored and the next mTLS fetch carries it.
	if viaMTLS {
		// A revocation that cannot be stored stops the fetch, so it is retried on the next one instead of being lost.
		for _, id := range out.Revoked {
			if err := m.store.AddRevocation(id); err != nil {
				return fmt.Errorf("store revocation: %w", err)
			}
		}
	} else if len(out.Revoked) > 0 {
		slog.Warn("licstate: ignored revocations received outside mutual TLS; they are applied once the client certificate is in use",
			"count", len(out.Revoked))
	}
	// A revocation-only response carries a null envelope: apply revocations and keep the installed doc. Nothing in it
	// is signed, so it counts as cloud validation only when it came over the mutual-TLS channel.
	if len(out.Envelope) == 0 || string(out.Envelope) == "null" {
		if viaMTLS {
			if err := m.store.MarkCloudValidated(time.Now().UTC()); err != nil {
				slog.Warn("licstate: mark cloud validated failed", "err", err)
			}
		}
		m.Load(ctx)
		slog.Info("licstate: revocation-only response applied", "revoked", len(out.Revoked))
		return nil
	}
	if _, err := m.Install(ctx, out.Envelope); err != nil {
		return err
	}
	if err := m.store.MarkCloudValidated(time.Now().UTC()); err != nil {
		slog.Warn("licstate: mark cloud validated failed", "err", err)
	}
	m.Load(ctx)
	slog.Info("licstate: license refreshed from cloud", "license_id", out.LicenseID)
	return nil
}

// StartLoops runs a minute-level re-evaluation (time-based transitions and the hardware binding) and the cloud
// fetch loop when configured; both stop with ctx.
func (m *Manager) StartLoops(ctx context.Context, ctrlBase, applianceID string, priv ed25519.PrivateKey, refreshEvery time.Duration) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				m.Load(ctx)
			}
		}
	}()
	if ctrlBase == "" || applianceID == "" || len(priv) == 0 || m.store == nil {
		return
	}
	go m.runFetchLoop(ctx, func(ctx context.Context) error {
		return m.FetchFromCloud(ctx, ctrlBase, applianceID, priv)
	}, refreshEvery)
}

// nextFetchInterval is the cadence rule: while no usable licence is installed (factory-clean, waiting for
// activation, just activated, or a licence for another appliance) ask every fastEvery; once a licence is in
// place, every slow interval. Kick() overrides either.
func (m *Manager) nextFetchInterval(slow time.Duration) time.Duration {
	m.mu.RLock()
	fast := m.fastEvery
	m.mu.RUnlock()
	if !m.HasUsableLicense() && fast > 0 && fast < slow {
		return fast
	}
	return slow
}

// runFetchLoop fetches immediately, then on the cadence nextFetchInterval decides, and at once on Kick.
func (m *Manager) runFetchLoop(ctx context.Context, fetch func(context.Context) error, slow time.Duration) {
	for {
		fctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := fetch(fctx)
		cancel()
		if err != nil && !errors.Is(err, ErrNoLicenseYet) {
			slog.Warn("licstate: cloud license fetch failed (offline-safe)", "err", err)
		}
		m.mu.RLock()
		obs := m.onFetch
		m.mu.RUnlock()
		if obs != nil {
			obs(err)
		}
		t := time.NewTimer(m.nextFetchInterval(slow))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-m.kick:
			t.Stop()
		}
	}
}
