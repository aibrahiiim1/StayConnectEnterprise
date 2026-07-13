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
	FeatEmailOTP    = "email_otp"
	FeatSMSOTP      = "sms_otp"
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
	// migration) — a grace state, not a hard reject.
	local      lic.LocalIdentity
	hwMismatch string

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
	m := &Manager{db: db, tenantID: tenantID, required: required}
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
		m.mu.Unlock()
		return
	}
	m.current = ev
	m.loaded = true
	m.mu.Unlock()

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
	if !m.loaded {
		if m.required {
			return lic.StateExpired
		}
		return lic.StateActive // unlicensed dev mode
	}
	return m.current.State
}

// AllowsNewSessions gates every guest auth path.
func (m *Manager) AllowsNewSessions() bool { return m.State().AllowsNewSessions() }

// AllowsProvisioning gates management writes (plans, batches, providers).
func (m *Manager) AllowsProvisioning() bool { return m.State().AllowsProvisioning() }

// FeatureEnabled evaluates a commercial feature under the current state.
// Unlicensed dev mode allows everything (with the boot warning); a loaded
// license is authoritative.
func (m *Manager) FeatureEnabled(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.loaded {
		return !m.required
	}
	f := m.current.Doc.Features
	var entitled bool
	switch name {
	case FeatEmailOTP:
		entitled = f.EmailOTP
	case FeatSMSOTP:
		entitled = f.SMSOTP
	case FeatSocialLogin:
		entitled = f.SocialLogin
	case FeatPMS:
		entitled = f.PMS
	case FeatPaidWiFi:
		entitled = f.PaidWiFi
	case FeatHA:
		entitled = f.HA
	case FeatWhiteLabel:
		entitled = f.WhiteLabel
	default:
		return false
	}
	return lic.FeatureEnabled(m.current.State, entitled)
}

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
	// WAN MAC mismatch is a soft hardware signal (NIC replaced / migration):
	// the license stays installed and the hotel keeps running in a grace state
	// while a Hardware Binding Mismatch alert is surfaced until an authorized
	// rebind issues a corrected license.
	if res == lic.BindingWANMismatch {
		m.mu.Lock()
		m.hwMismatch = reason
		m.mu.Unlock()
		slog.Warn("licstate: hardware binding mismatch", "reason", reason,
			"license_id", doc.LicenseID, "doc_wan_mac", doc.WANMAC, "local_wan_mac", m.local.WANMAC)
	} else {
		m.mu.Lock()
		m.hwMismatch = ""
		m.mu.Unlock()
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
		"max_concurrent_devices":    unlim(d.Limits.MaxConcurrentGuestSessions),
		"max_operators":             unlim(d.Limits.MaxLocalOperators),
		"max_guest_access_plans":    unlim(d.Limits.MaxGuestAccessPlans),
		"max_appliances":            unlim(d.Limits.MaxAppliancesForSite),
		"retention_days_accounting": unlim(d.Limits.AccountingRetentionDays),
		"retention_days_audit":      unlim(d.Limits.AuditRetentionDays),
	}
	bools := map[string]bool{
		"feature.pms_integration": d.Features.PMS,
		"feature.paid_wifi":       d.Features.PaidWiFi,
		"feature.auth.sms_otp":    d.Features.SMSOTP,
		"feature.auth.email_otp":  d.Features.EmailOTP,
		"feature.auth.social":     d.Features.SocialLogin,
		"feature.ha_pair":         d.Features.HA,
		"feature.white_label":     d.Features.WhiteLabel,
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
	if resp.StatusCode == http.StatusNotFound {
		return errors.New("no license issued for this site yet")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("license fetch: HTTP %d: %s", resp.StatusCode, string(body))
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

// StartLoops runs (a) a minute-level re-evaluation so time-based state
// transitions take effect, and (b) a cloud refresh every refreshEvery when
// fetch parameters are configured. Both stop with ctx.
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
	go func() {
		// Immediate first fetch, then periodic.
		if err := m.FetchFromCloud(ctx, ctrlBase, applianceID, priv); err != nil {
			slog.Warn("licstate: initial cloud license fetch failed (offline-safe)", "err", err)
		}
		t := time.NewTicker(refreshEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := m.FetchFromCloud(ctx, ctrlBase, applianceID, priv); err != nil {
					slog.Warn("licstate: cloud license refresh failed (offline-safe)", "err", err)
				}
			}
		}
	}()
}
