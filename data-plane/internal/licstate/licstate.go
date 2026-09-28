// Package licstate manages the appliance's local view of its vendor-signed
// license: loading/verifying the envelope from disk, periodically fetching
// renewals from the cloud, evaluating the operational state, and bridging
// the license limits into the site database so existing data-plane queries
// (tenant_effective_limits) keep working unchanged.
//
// Guest authentication NEVER calls the cloud: everything here evaluates
// against the on-disk envelope and the local clock (with rollback
// protection). Cloud reachability only affects renewal freshness.
package licstate

import (
	"context"
	"crypto/ed25519"
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

// Feature names accepted by FeatureEnabled. They mirror the portal auth
// methods plus the coarser commercial features.
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
	applianceID string // set once known; used to verify per-appliance binding

	// local is this appliance's hardware/identity fingerprint used to verify a
	// signed license was minted for THIS box. The identity key fingerprint is
	// the primary trust anchor; serial/hw-fingerprint/WAN-MAC are mismatch and
	// clone-detection signals. hwMismatch holds a non-empty reason when the
	// current license matches the identity but the WAN MAC differs (NIC swap /
	// migration) — a warning, not a reject: the licence stays in force, with no
	// time limit, until a rebind issues a corrected one.
	//
	// wrongDevice holds a non-empty reason when the licence on disk is bound to a
	// DIFFERENT appliance (identity key, appliance id, serial or hardware
	// fingerprint). It is recomputed on EVERY evaluation -- boot included -- so a
	// licence copied onto another box, or a box whose identity was replaced, is
	// refused after a reboot exactly as it is refused at install.
	local       lic.LocalIdentity
	hwMismatch  string
	wrongDevice string

	// Required=false (dev/pilot pre-cutover): no license file → permissive
	// unlicensed mode with a warning. Required=true (production): no
	// license → Expired behavior until one is installed.
	required bool

	mu      sync.RWMutex
	current lic.Evaluation
	loaded  bool

	// mTLS transport (Phase B/#4): once the appliance holds a client cert,
	// license fetch/refresh routes over the mutual-TLS listener instead of the
	// plain HTTPS ingress. Set via SetMTLSTransport; nil = use ctrlBase.
	mtlsClient *http.Client
	mtlsBase   string

	// Cloud fetch scheduling (see runFetchLoop). kick asks for an immediate fetch; onFetch observes every
	// fetch outcome (scd uses it to track when Central last answered). fastEvery is the poll interval while
	// no licence is installed.
	kick      chan struct{}
	onFetch   func(error)
	fastEvery time.Duration
}

// DefaultFastFetchInterval is how often an appliance with NO installed licence asks Central for one. A freshly
// activated appliance used to wait for the next six-hour tick; now it collects its licence within a minute.
const DefaultFastFetchInterval = time.Minute

// ErrNoLicenseYet is Central answering that no licence has been issued for this appliance yet. It is a
// successful conversation with Central, not a connectivity failure.
var ErrNoLicenseYet = errors.New("no license issued for this appliance yet")

// HTTPError is Central answering a licence fetch with an unexpected status.
type HTTPError struct {
	Code int
	Body string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("license fetch: HTTP %d: %s", e.Code, e.Body) }

// OnFetch registers an observer called after every cloud fetch attempt with its outcome.
func (m *Manager) OnFetch(fn func(error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onFetch = fn
}

// Kick asks the fetch loop to contact Central now (Check now, a newly adopted assignment). Non-blocking; a
// kick while one is already pending is merged into it.
func (m *Manager) Kick() {
	select {
	case m.kick <- struct{}{}:
	default:
	}
}

// SetMTLSTransport routes subsequent cloud license fetches over the given mTLS
// client + base URL (the Central mutual-TLS listener). Safe to call at runtime.
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

// New creates the manager. dir is the license directory
// (default /etc/stayconnect/license), pubKeyPath the vendor public key file.
// Returns a manager even when the pub key is missing (unlicensed mode) so
// the appliance still boots; Install/Fetch will fail until the key exists.
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

// Load evaluates the on-disk license and refreshes the DB limit bridge.
func (m *Manager) Load(ctx context.Context) {
	if m.store == nil {
		return
	}
	ev, err := m.store.Evaluate(time.Now().UTC())
	m.mu.Lock()
	if err != nil {
		if !errors.Is(err, lic.ErrNoLicense) {
			slog.Warn("licstate: evaluate failed", "err", err)
		}
		m.loaded = false
		m.wrongDevice, m.hwMismatch = "", ""
		m.mu.Unlock()
		return
	}
	// THE BINDING IS CHECKED ON EVERY EVALUATION, not only at install. It used to live in Install alone, and the
	// verdict was held in memory: a reboot cleared it, and a licence file copied from another appliance -- or a
	// box whose identity had been replaced under an installed licence -- evaluated as valid. Fail closed exactly
	// as install does.
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

	slog.Info("licstate: license evaluated",
		"state", ev.State, "license_id", ev.Doc.LicenseID,
		"plan", ev.Doc.CommercialPlanCode, "valid_until", ev.Doc.ValidUntil,
		"cloud_stale", ev.CloudStale, "clock_rollback", ev.ClockRollback)

	if err := m.syncLimits(ctx, ev.Doc); err != nil {
		slog.Warn("licstate: limit bridge sync failed", "err", err)
	}
}

// Evaluation returns the cached evaluation plus a loaded flag.
func (m *Manager) Evaluation() (lic.Evaluation, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current, m.loaded
}

// State returns the effective operational state. Unlicensed mode maps to
// Active (dev) or Expired (required=true).
func (m *Manager) State() lic.State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.loaded && m.wrongDevice != "" {
		// A licence for another appliance is no licence at all, in EVERY build profile.
		return lic.StateUnlicensed
	}
	if !m.loaded {
		if m.required {
			// No valid signed license on a production appliance: the SAFE state.
			// Never Active — AllowsNewSessions() is false, so no guest auth,
			// nft, shaping, accounting or session is ever created. This is the
			// only path a config error (missing/invalid vendor key) can take —
			// it fails closed, never to permissive.
			return lic.StateUnlicensed
		}
		return lic.StateActive // permissive unlicensed-dev (development builds only)
	}
	return m.current.State
}

// AllowsNewSessions gates every guest auth path.
func (m *Manager) AllowsNewSessions() bool { return m.State().AllowsNewSessions() }

// MaxConcurrentOnlineGuests returns the appliance-wide concurrent
// online-guest cap from the LOCAL signed license: -1 = unlimited (or
// unlicensed dev mode). Enforcement never consults Central.
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

// FeatureEnabled evaluates a legacy feature name under the current state. It
// is a thin alias over ModuleEnabled: since licence v4 the signed `modules`
// map is the SOLE authority, and a v1-v3 licence is read through the
// conservative legacy mapping in license.AuthorizedModules. The Features
// block of a v4 licence is never consulted.
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

// ModuleEnabled reports whether the installed licence authorises module id
// AND the licence state allows entitled features (Active or Grace).
// Unlicensed dev mode allows everything (with the boot warning).
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

// AuthorizedModules returns the modules the installed licence authorises,
// independent of licence state (for display). Nil when no usable licence.
func (m *Manager) AuthorizedModules() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded || m.wrongDevice != "" || m.current.Doc == nil {
		return nil
	}
	return m.current.Doc.AuthorizedModules()
}

// LicenseRequired reports whether this build requires a licence (production).
func (m *Manager) LicenseRequired() bool { return m.required }

// Install verifies and persists a new envelope (Hotel Admin upload or cloud
// push), then reloads.
func (m *Manager) Install(ctx context.Context, raw []byte) (*lic.Document, error) {
	if m.store == nil {
		return nil, errors.New("vendor public key not installed")
	}
	// Enforce the hardware/identity binding BEFORE persisting, so a license
	// minted for a different appliance (or a clone) is never written to disk.
	pre, err := m.store.Verify(raw)
	if err != nil {
		return nil, err // bad signature / unknown signer / malformed / invalid
	}
	res, reason := pre.CheckBinding(m.local)
	if res == lic.BindingWrongDevice {
		slog.Warn("licstate: license REJECTED — bound to a different appliance",
			"reason", reason, "license_id", pre.LicenseID,
			"doc_serial", pre.ApplianceSerial, "local_serial", m.local.Serial,
			"doc_idfpr", pre.IdentityKeyFingerprint, "local_idfpr", m.local.IdentityKeyFingerprint)
		return nil, fmt.Errorf("license rejected: %s — this license is bound to a different appliance", reason)
	}

	doc, err := m.store.Install(raw, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	// WAN MAC mismatch is a soft hardware signal (NIC replaced / migration): the licence stays installed and in
	// force -- there is no time limit -- and a warning is shown until an authorised rebind issues a corrected
	// licence. Load (below) records it, and re-derives it on every evaluation.
	if res == lic.BindingWANMismatch {
		slog.Warn("licstate: hardware binding mismatch", "reason", reason,
			"license_id", doc.LicenseID, "doc_wan_mac", doc.WANMAC, "local_wan_mac", m.local.WANMAC)
	}
	// Legacy site-wide binding warning (v1 / unbound docs).
	if m.applianceID != "" && len(doc.ApplianceIDs) > 0 && !applianceBound(doc, m.applianceID) {
		slog.Warn("licstate: appliance not listed in license binding",
			"appliance_id", m.applianceID, "license_id", doc.LicenseID, "bound_count", len(doc.ApplianceIDs))
	}
	m.Load(ctx)
	return doc, nil
}

// SetLocalIdentity records this appliance's hardware/identity facts for license
// binding verification. Call once identity + hardware are known.
func (m *Manager) SetLocalIdentity(local lic.LocalIdentity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.local = local
}

// WrongHardware returns a non-empty reason when the installed licence is bound to a different appliance and is
// therefore refused (no new guest sessions).
func (m *Manager) WrongHardware() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded {
		return ""
	}
	return m.wrongDevice
}

// HasUsableLicense reports whether a real signed licence for THIS appliance is installed (whatever its time
// state). It is what decides the fast "collect my first licence" fetch cadence.
func (m *Manager) HasUsableLicense() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.loaded && m.wrongDevice == "" && m.current.Doc != nil
}

// HardwareMismatch returns a non-empty reason when the installed license is
// bound to this identity but the WAN MAC no longer matches (rebind needed).
func (m *Manager) HardwareMismatch() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.hwMismatch
}

// applianceBound reports whether id is in the license's ApplianceIDs binding.
func applianceBound(d *lic.Document, id string) bool {
	for _, a := range d.ApplianceIDs {
		if a == id {
			return true
		}
	}
	return false
}

// syncLimits rewrites the license-sourced rows of tenant_effective_limits in
// the SITE database so session.CheckConcurrency and provisioning caps read
// the signed values. License semantics 0 = unlimited map to the view's -1.
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
	// Mirrored from the authorised modules (never from a v4 Features block).
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

// FetchFromCloud pulls the current signed license over the appliance's
// authenticated channel and installs it. A 200 also counts as a cloud
// validation; revoked ids from the response feed the local revocation store.
func (m *Manager) FetchFromCloud(ctx context.Context, ctrlBase, applianceID string, priv ed25519.PrivateKey) error {
	if m.store == nil {
		return errors.New("vendor public key not installed")
	}
	m.applianceID = applianceID // remember for per-appliance binding verification
	tok, err := applianceauth.SignRequest(priv, applianceID, http.MethodGet, "/v1/appliance/license", nil)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	// Prefer the mTLS transport once a client cert is installed; otherwise the
	// plain HTTPS ingress (signed-JWT still applied on top in both cases).
	base := ctrlBase
	client := &http.Client{Timeout: 10 * time.Second}
	if mc, mb, ok := m.transport(); ok {
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
	for _, id := range out.Revoked {
		_ = m.store.AddRevocation(id)
	}
	// A revocation-only response (no current license) carries a null
	// envelope: apply revocations, keep the installed doc, re-evaluate.
	if len(out.Envelope) == 0 || string(out.Envelope) == "null" {
		if err := m.store.MarkCloudValidated(time.Now().UTC()); err != nil {
			slog.Warn("licstate: mark cloud validated failed", "err", err)
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

// StartLoops runs (a) a minute-level re-evaluation so time-based state transitions -- and the hardware binding
// -- are re-checked, and (b) the cloud fetch loop when fetch parameters are configured. Both stop with ctx.
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
