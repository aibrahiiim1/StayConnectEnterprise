// scd is the Session Controller Daemon: it owns the nftables auth_ipv4 set and the `sessions` table; portald is its primary client over a Unix socket.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/appliancecert"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/buildprofile"
	"github.com/stayconnect/enterprise/data-plane/internal/deployment"
	"github.com/stayconnect/enterprise/data-plane/internal/hwid"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/modules"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/otp"
	"github.com/stayconnect/enterprise/data-plane/internal/otpkey"
	"github.com/stayconnect/enterprise/data-plane/internal/pms"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsloader"
	"github.com/stayconnect/enterprise/data-plane/internal/reqlog"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/startupbackoff"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
	lic "github.com/stayconnect/enterprise/license"
)

type cfg struct {
	SocketPath  string
	DBURL       string
	TenantID    string
	SiteID      string
	ApplianceID string
	MailLogPath string
	SMSLogPath  string
	// WhatsAppLogPath is the development stub's file for WhatsApp codes (used only when no provider is set).
	WhatsAppLogPath string

	// Appliance identity (identity.json and the private key live in IdentityDir); a factory-clean appliance registers token-less with Central at CtrlAPIBase.
	IdentityDir string
	CtrlAPIBase string
	Serial      string
	// AutoRegister: a factory-clean appliance self-registers token-less so it appears as waiting for activation.
	// SCD_AUTO_REGISTER=false leaves offline activation as the only way in (a site with no route to Central).
	AutoRegister bool

	// Optional TCP listener for /metrics; empty = disabled.
	MetricsAddr string

	// Signed license enforcement: LicenseDir holds the installed envelope and anti-rollback state, VendorPub the verification key.
	// LicenseRequired=false keeps pilots permissive when no license material exists.
	LicenseDir      string
	VendorPub       string
	LicenseRequired bool

	// mTLS: CertDir holds the client cert + CA bundle, MTLSBase is Central's mutual-TLS listener; empty MTLSBase keeps signed-JWT over the HTTPS ingress.
	CertDir  string
	MTLSBase string

	// Dark-auth flags, all default OFF. SecretsDir holds appliance-local key files. DurableThrottle loads throttle.key
	// (startup fails if absent) and enforces the DB throttle on every auth handler. OTPHMAC loads the OTP key ring and
	// issues keyed-HMAC OTPs (legacy rows still verify). IAM-v2 flags are read by iamv2.LoadConfigFromEnv.
	SecretsDir      string
	DurableThrottle bool
	OTPHMAC         bool

	// PortalTLSExtraRoots is an opt-in PEM bundle of extra trust anchors for staging issuance (Let's Encrypt staging, Pebble); empty, the production value, trusts the system roots only.
	PortalTLSDir        string
	PortalTLSExtraRoots string
}

func loadCfg() cfg {
	return cfg{
		SocketPath:      envOr("SCD_SOCKET", "/run/stayconnect/scd.sock"),
		DBURL:           envOr("SCD_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"),
		TenantID:        os.Getenv("SCD_TENANT_ID"),
		SiteID:          os.Getenv("SCD_SITE_ID"),
		ApplianceID:     os.Getenv("SCD_APPLIANCE_ID"),
		MailLogPath:     envOr("SCD_MAIL_LOG", "/var/log/stayconnect/otp-mail.log"),
		SMSLogPath:      envOr("SCD_SMS_LOG", "/var/log/stayconnect/otp-sms.log"),
		WhatsAppLogPath: envOr("SCD_WHATSAPP_LOG", "/var/log/stayconnect/otp-whatsapp.log"),

		IdentityDir:  envOr("SCD_IDENTITY_DIR", "/etc/stayconnect/identity"),
		CtrlAPIBase:  os.Getenv("SCD_CTRLAPI_BASE"),
		Serial:       os.Getenv("SCD_SERIAL"),
		AutoRegister: os.Getenv("SCD_AUTO_REGISTER") != "false",

		MetricsAddr: os.Getenv("SCD_METRICS_ADDR"),

		LicenseDir:      envOr("SCD_LICENSE_DIR", "/etc/stayconnect/license"),
		VendorPub:       envOr("SCD_VENDOR_PUB", "/etc/stayconnect/vendor-license.pub"),
		LicenseRequired: os.Getenv("SCD_LICENSE_REQUIRED") == "true",

		CertDir:  envOr("SCD_CERT_DIR", "/etc/stayconnect/certs"),
		MTLSBase: os.Getenv("SCD_MTLS_BASE"),

		SecretsDir:      envOr("SCD_SECRETS_DIR", "/etc/stayconnect/secrets"),
		DurableThrottle: os.Getenv("SCD_DURABLE_THROTTLE") == "true",
		OTPHMAC:         os.Getenv("SCD_OTP_HMAC") == "true",

		PortalTLSDir:        envOr("SCD_PORTAL_TLS_DIR", "/etc/stayconnect/portal-tls"),
		PortalTLSExtraRoots: os.Getenv("SCD_PORTAL_TLS_EXTRA_ROOTS"),
	}
}

const scdVersion = "0.0.3-dev"

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func restartSCDUnit() {
	_ = exec.Command("systemctl", "restart", "stayconnect-scd").Run()
}

func removeDirContents(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		_ = os.RemoveAll(dir + "/" + e.Name())
	}
	return nil
}

type server struct {
	// methodSwitches reads Hotel Admin's sign-in method switches; nil means tenantcfg.Load. Tests without a database supply their own.
	methodSwitches func(ctx context.Context) (*tenantcfg.AuthMethods, error)
	nft            *localNFT
	shp            *shape.Client
	flows          flowEnder
	mail           mail.Mailer
	sms            sms.Sender
	whatsapp       whatsapp.Sender
	// otpIssuer issues a challenge; nil means otp.Issue against s.db. A test with no database supplies its own.
	otpIssuer func(ctx context.Context, p otp.IssueParams) (*otp.Issued, error)
	// Social sign-in: the live provider registry generation (swapped whole on reload), socialBase (dev stub only, never in production), socialKey (seals provider secrets), socialMu (serialises reloads).
	socialSet  atomic.Pointer[socialSet]
	socialBase []social.Provider
	socialKey  sealbox.Key
	socialMu   sync.Mutex
	// socialPortal reports HTTPS state and canonical hostname for social sign-in; nil or noPortalHTTPS{} means the HTTP fallback (no real provider offered).
	socialPortal portalHosts
	loginRL      *loginLimiter // layered throttling for guest username/password logins (optional fast in-memory layer)

	// Dark-auth machinery, inert unless enabled: authThrottle (durable DB throttle; nil = in-memory only), otpRing
	// (keyed-HMAC OTPs; nil = legacy), iamv2Auth (all method flags OFF, never touches its nil repository).
	authThrottle *throttle.Store
	otpRing      *otpkey.Ring
	iamv2Auth    *iamv2.Authenticator
	// iamv2Cfg is the config iamv2Auth was built from; entry points consult it so their switch and the Authenticator's gate cannot disagree.
	iamv2Cfg    iamv2.Config
	applianceID string

	// signInAttemptKeyring seals the credential half of a sign-in attempt record; nil when the key file is absent, deliberately not fatal (see loadSignInAttemptKeyring).
	signInAttemptKeyring signinattempt.Keyring

	// commerce is always constructed but holds a nil repository while the master flag is OFF; commerceCfg gates whether the commerce routes are mounted.
	commerce    *iamv2.CommerceEngine
	commerceCfg iamv2.CommerceConfig

	// The PMS auth slice: nil while dark, routes not mounted (an absent surface cannot be probed).
	p3auth     *phase3Auth
	p5poststay *phase5PostStay
	p6devices  *phase6Devices
	// p6cfg lets unconditionally mounted surfaces (the status endpoint) tell whether aggregate mode may be reported; not a second gate.
	p6cfg iamv2.Phase6Config

	// ceiling is the deployment ceiling and modules the module resolver (modules.go); product logic asks these, never the phase flags.
	ceiling deployment.Ceiling
	// providerReadiness overrides the sender/social-application lookup (tests); nil reads the database.
	providerReadiness providerReadinessFunc
	modules           *modules.Resolver
	card              *cardState
	openAccess        *iamv2.OpenAccess
	openLimiter       *recoveryLimiter
	// resumeKey / resumeLimiter: the remembered-device credential; nil = not remembered. Email account link and token keys derive from resumeKey.
	resumeKey     []byte
	resumeLimiter *recoveryLimiter
	selfService   selfServiceState
	// notify: the live sender trio, swappable on reload (notify_admin.go); notifyKey seals sender secrets.
	notify         notifySet
	notifyFallback struct {
		mail     mail.Mailer
		sms      sms.Sender
		whatsapp whatsapp.Sender
	}
	notifyKey sealbox.Key
	// otpEscrowKey seals the staff-readable copy of a live code; same local key file, its own domain.
	otpEscrowKey sealbox.Key
	ledgerRT     ledgerRuntime
	// operatorMFA seals operators' authenticator secrets and MACs their recovery codes.
	operatorMFA operatorMFAKeys

	// The PMS registry is live-reloadable: readers use currentPMSReg(); reload swaps it under pmsMu, and pmsBuilt holds the generation's providers to Stop() on the next reload.
	pmsMu    sync.RWMutex
	pmsReg   *pms.Registry
	pmsBuilt []pms.Provider

	db     *pgxpool.Pool
	tenID  string
	siteID string
	applID string

	// tenantBlocked is set when a cross-tenant purge could not complete: the appliance authorizes NO guests (fail-closed) until it does. tenantBlockReason is the operator-facing explanation.
	tenantBlocked     atomic.Bool
	tenantBlockReason string

	ctrlBase string
	serial   string

	certMgr *appliancecert.Manager
	// noMTLSLogged counts consecutive assignment polls skipped for lack of an mTLS certificate; only the assignment agent's goroutine touches it, so it needs no lock.
	noMTLSLogged int
	// identityKeyFpr is the identity-signing key fingerprint, distinct from the mTLS cert fingerprint.
	identityKeyFpr string
	identityPubB64 string
	idStore        *identity.Store
	idPriv         ed25519.PrivateKey // identity-signing key (for signed reconcile calls)

	central *centralContact
	reg     *registrar
	asgKick chan struct{}
	// certReconcile asks the certificate agent to check that Central still honours the installed certificate; sent on a 401/403 from an mTLS call.
	certReconcile chan struct{}

	// Messages to Central retried until confirmed (central_retry.go): the terminal-assignment ack and the offline-package reconciliation. centralTransport overrides the mTLS transport in tests.
	centralTransport func() (*http.Client, string, bool)
	ackRunner        terminalAckRunner
	reconcileKick    chan struct{}

	// legacyBridge is the fallback ingress interface for sessions whose IP matches no configured guest network.
	legacyBridge string

	// hw is the stable hardware identity (DMI product UUID + permanent MACs), detected once; it survives reboot, de-enrollment and factory reset.
	hw hwid.Info

	met *metrics.Registry // phase 7

	lic *licstate.Manager

	// permissiveBlocked is non-empty when a production appliance rejected an attempt to enable permissive/dev licensing; Hotel Admin shows a critical alert.
	permissiveBlocked string

	// removed is set when Central deleted this appliance after it held a customer: no new guests, registration, Central agent, or licence/activation import; only a factory-clean install undoes it. Persisted on disk.
	removed   atomic.Pointer[removedRecord]
	restartFn func()

	// portalTLS is the portal HTTPS certificate agent and the one answer to "is the portal on HTTPS"; a nil agent reports the HTTP fallback.
	portalTLS *portalTLSAgent
}

func (s *server) currentPMSReg() *pms.Registry {
	s.pmsMu.RLock()
	defer s.pmsMu.RUnlock()
	return s.pmsReg
}

// pmsReloadSafetyLoop reloads every 10 minutes as a safety net; Hotel Admin changes reload immediately through /v1/admin/pms/reload.
func (s *server) pmsReloadSafetyLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := s.reloadPMS(rctx); err != nil {
				slog.Warn("pms safety-net reload failed", "err", err)
			}
			cancel()
		}
	}
}

// reloadPMS swaps in a freshly built provider set, then stops the previous generation; a failure mid-build leaves the old registry intact.
func (s *server) reloadPMS(ctx context.Context) error {
	reg, built, err := pmsloader.Load(ctx, s.db, s.tenID, s.siteID)
	if err != nil {
		return fmt.Errorf("reloadPMS load: %w", err)
	}
	pmsloader.StartAll(ctx, built)
	// Re-seed stub reservations if SCD_PMS_STUB_SEED=true: the fresh Stub lost the previous generation's seed.
	maybeSeedPMSStubs(built)

	s.pmsMu.Lock()
	oldBuilt := s.pmsBuilt
	s.pmsReg = reg
	s.pmsBuilt = built
	s.pmsMu.Unlock()

	// Stop the prior generation after the swap so in-flight requests holding the old registry finish without racing on torn-down resources.
	pmsloader.StopAll(oldBuilt)
	slog.Info("pms reloaded", "count", len(built))
	return nil
}

type authorizeReq struct {
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
	Voucher string `json:"voucher"`
}

func (s *server) authorize(w http.ResponseWriter, r *http.Request) {
	var req authorizeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	// Voucher is the basic-access method: gated only on the license permitting new sessions (works through Restricted/Suspended).
	if !s.licenseGate(w, "") {
		return
	}
	ip := net.ParseIP(req.IP)
	if ip == nil || ip.To4() == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	mac, err := net.ParseMAC(req.MAC)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "bad mac")
		return
	}
	if s.refuseDisabledMethod(w, r, guestMethodVoucher) {
		return
	}
	// Durable throttle (no-op unless enabled), charged before validation so a throttled attempt creates no session/nft/shaping/accounting.
	if ok, retry := s.throttleGuard(r.Context(), "voucher", ip, mac, ""); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"error": "TOO_MANY_ATTEMPTS"})
		return
	}
	// IAM-v2 is the guest authority and always handles the request, success or refusal; there is no fallback.
	s.authorizeViaIAMv2(w, r, iamv2.MethodVoucher,
		iamv2.Request{Secret: req.Voucher, Credential: iamv2.CredentialVoucher, Device: iamv2.DeviceContext{MAC: mac.String()}}, ip)
}

type revokeReq struct {
	IP     string `json:"ip"`
	Reason string `json:"reason"`
}

func (s *server) revoke(w http.ResponseWriter, r *http.Request) {
	var req revokeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	ip := net.ParseIP(req.IP)
	if ip == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	// Clamp to a known reason so an unexpected one ends the session cleanly instead of 500. Metrics keep lower-case labels; the session row records the upper-case code (sessionEndCode).
	switch req.Reason {
	// "logout" is the guest's own sign-out, recorded as such so the console does not show it as staff disconnecting them.
	case "quota_bytes", "quota_time", "admin", "idle", "dhcp_expired", "policy", "logout":
	default:
		req.Reason = "admin"
	}
	// The row ends first, then the kernel: deleting netd's shaping class while the row still said active left a session claiming enforcement that no longer existed.
	_, endErr := s.endActiveSessionByIP(r.Context(), ip, req.Reason)
	// The kernel step runs either way: a database failure is not a reason to leave an address admitted that an operator took off the network.
	s.denyAtGate(r.Context(), ip)
	if endErr != nil {
		slog.Error("session end", "err", endErr)
		httpErr(w, http.StatusInternalServerError, "session end failed")
		return
	}
	s.met.SessionsClosed.WithLabelValues(req.Reason).Inc()
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

// denyAtGate is the kernel half of an operator disconnect (the caller ends the session row): the address leaves the guest
// authorization set and its shaping class goes. Both steps are best effort; netd also converges from iam_v2.sessions.
// Order: legacy set, then netd (authorization revoked, open flows ended), then, only for an address netd does not manage,
// the legacy tc class. Deleting a class while the address is still authorized leaves an open flow unshaped and uncounted.
func (s *server) denyAtGate(ctx context.Context, ip net.IP) {
	nc := s.resolveNetwork(ctx, ip)
	if s.nft != nil && s.nft.client != nil {
		if err := s.nft.Deny(ctx, nc.Bridge, ip); err != nil {
			slog.Error("nft deny", "err", err)
		}
	}
	managed := false
	if s.flows != nil {
		res, err := s.flows.EndFlows(ctx, ip)
		managed = res.Phase3Managed
		if err != nil {
			// netd also converges from iam_v2.sessions on its next pass; what is lost here is the immediacy.
			slog.Error("netd could not end the address's flows", "ip", ip.String(), "err", err)
		}
	}
	if managed {
		return // the class is netd's; its reconciliation removes it once nothing can reach it
	}
	if s.shp != nil {
		if err := s.shp.DeleteSession(ctx, nc.Bridge, ip); err != nil {
			slog.Warn("shape delete", "err", err)
		}
	}
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	ip := net.ParseIP(r.URL.Query().Get("ip"))
	if ip == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	id, active, err := s.findActiveSessionByIP(r.Context(), ip)
	if err != nil {
		slog.Error("find active", "err", err)
		httpErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	out := map[string]any{
		"ip":         ip.String(),
		"session_id": id,
		"active":     active,
	}
	// When the access ends by the calendar (the entitlement's window), for the "You're online" page and captive API. Absent for access with no end.
	if active && id != "" && s.db != nil {
		var ends *time.Time
		if err := s.db.QueryRow(r.Context(), `SELECT e.window_ends_at FROM iam_v2.sessions x
		     JOIN iam_v2.entitlements e ON e.id = x.entitlement_id
		     WHERE x.id = $1::uuid AND x.tenant_id = $2 AND x.site_id = $3`, id, s.tenID, s.siteID).Scan(&ends); err == nil && ends != nil {
			out["access_ends_at"] = ends.UTC().Format(time.RFC3339)
		}
	}
	// A guest with an online-time budget needs both clocks. Aggregate mode stays behind its dark flag; a VALIDITY_WINDOW
	// plan's time allowance is enforced everywhere, so its remaining time is answered regardless, matching what accrual spends.
	if id != "" {
		if agg := s.aggregateStatusFor(r.Context(), id); agg != nil &&
			(agg.TimeMode != "AGGREGATE_ONLINE_TIME" || s.p6cfg.AggregateTimeOn()) {
			out["time_mode"] = agg.TimeMode
			out["remaining_online_seconds"] = agg.RemainingOnlineSeconds
			out["online_time_allowance"] = agg.OnlineTimeAllowance
			if agg.HardExpiry != "" {
				out["hard_expiry"] = agg.HardExpiry
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func main() {
	// `scd --print-build-profile` prints the compiled licensing profile and exits, so CI/deploy can refuse to ship a non-production binary.
	if len(os.Args) > 1 && os.Args[1] == "--print-build-profile" {
		fmt.Println(buildprofile.Name)
		return
	}
	// `scd backup-crypt ...` encrypts the nightly backup to the hotel's keys and decrypts one for an engineer restoring it (backup_crypt.go).
	if len(os.Args) > 1 && os.Args[1] == "backup-now" {
		os.Exit(backupNowMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "backup-restore" {
		os.Exit(backupRestoreMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "marker-advance" {
		os.Exit(markerAdvanceMain(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "backup-crypt" {
		os.Exit(backupCryptMain(os.Args[2:]))
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// Adaptive crash-loop backoff: a rapidly restarting scd sleeps with bounded jittered exponential backoff so a persistent fault never becomes a restart storm; a transient failure returns immediately.
	startupbackoff.Guard("scd")

	c := loadCfg()

	applianceauth.SetVersion(scdVersion)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Appliance identity: a registered identity is loaded from disk; a factory-clean appliance registers token-less with Central, and if unreachable the registration loop retries in the background (registration.go).
	central := &centralContact{}
	// Removed from Central: such an appliance never registers again, under any key, until factory-reset; read before anything could register.
	removedRec := loadRemoved(removedMarkerPath())
	if removedRec != nil {
		slog.Warn("this appliance was removed from OneGate Central after holding a customer: it admits no new guests, "+
			"does not register and does not contact Central until it is factory-reset", "since", removedRec.At)
	}
	// The site database is opened before registration, which reports whose customer data it still holds (held_customer.go). pgxpool.New does not connect.
	pool, err := pgxpool.New(rootCtx, c.DBURL)
	if err != nil {
		slog.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	heldAssignmentDir := envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")
	idStore := &identity.Store{Dir: c.IdentityDir, HeldCustomer: func(ctx context.Context) (string, error) {
		return heldCustomerID(ctx, pool, heldAssignmentDir)
	}}
	ident, err := idStore.LoadBound()
	identityDamaged := errors.Is(err, identity.ErrIdentityDamaged)
	if identityDamaged {
		// Never re-register under a new key: that would turn this activated appliance into an unknown one. scd keeps serving
		// the Admin Console so an operator can see and repair it.
		slog.Error("identity: identity.json exists but its key is missing or does not match; registration stopped until it is repaired",
			"err", err)
		ident, err = nil, nil
	}
	if err != nil {
		slog.Error("identity: load failed", "err", err)
		os.Exit(1)
	}
	if ident == nil && !identityDamaged && mayRegister(c.AutoRegister, c.CtrlAPIBase, removedRec) {
		rctx, rcancel := context.WithTimeout(rootCtx, 10*time.Second)
		ident, err = idStore.Register(rctx, c.CtrlAPIBase, nil)
		rcancel()
		central.record(err, time.Now())
		if err != nil {
			slog.Warn("self-registration did not complete at boot; retrying in the background", "err", err)
			ident = nil
		}
	}
	// Tenant/site are never authoritative from identity.json or env: the signed ASSIGNMENT document, re-verified every boot, is.
	// The legacy SCD_TENANT_ID/SITE_ID pair is honoured only with no assignment at all and only in a development build (assignment.EnvFallbackAllowed).
	envTenant, envSite := c.TenantID, c.SiteID
	c.TenantID, c.SiteID = "", ""
	asgStore := &assignment.Store{Dir: envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
	if ident != nil {
		slog.Info("identity loaded", "appliance_id", ident.ApplianceID, "serial", ident.Serial)
		c.ApplianceID = ident.ApplianceID
		// The identity serial must win over a stale SCD_SERIAL env, or assignment adoption fails with "serial mismatch" and the box never converges.
		if ident.Serial != "" {
			c.Serial = ident.Serial
		}
		// Re-verified every boot against the local trust registry (trusted signature + binding to this appliance); an appliance never operates under an assignment it cannot verify.
		res := verifiedAssignment(asgStore, ident)
		switch {
		case res.Assigned():
			c.TenantID, c.SiteID = res.TenantID, res.SiteID
			slog.Info("assignment resolved", "tenant_id", res.TenantID, "site_id", res.SiteID, "version", res.Version)
		case assignment.EnvFallbackAllowed(res) && envTenant != "" && envSite != "":
			c.TenantID, c.SiteID = envTenant, envSite
			slog.Warn("no signed assignment; development build using legacy env tenant/site",
				"tenant_id", envTenant, "site_id", envSite)
		default:
			slog.Warn("awaiting assignment: appliance registered but holds no verified granting assignment",
				"assignment_outcome", res.Outcome.String(), "assignment_state", res.State)
		}
	} else {
		// No registered identity: wait for registration or offline activation; scd still serves Hotel Admin, and the guest plane is pre-license.
		c.ApplianceID = ""
		slog.Warn("awaiting registration: this appliance has no registered identity yet")
	}

	// A development build keeps the in-process Stub for "google" (SCD_OAUTH_STUB_BASE). Production registers none: the stub
	// mints whatever identity its code names, i.e. a sign-in as anyone. Real providers are built from sealed secrets after the key loads.
	var socialBase []social.Provider
	if !buildprofile.Production {
		stubAuthBase := envOr("SCD_OAUTH_STUB_BASE", "http://portal.stayconnect.local:8380/api/oauth/stub/authorize")
		socialBase = append(socialBase, &social.Stub{ProviderName: "google", AuthorizeBase: stubAuthBase})
	}

	// awaiting = enrolled-but-unassigned or un-enrolled: no tenant/site, so tenant-scoped subsystems are skipped; scd serves
	// the setup wizard and runs the assignment agent, then re-execs once a signed assignment arrives.
	awaiting := c.TenantID == "" || c.SiteID == ""

	var pmsReg *pms.Registry
	var pmsBuilt []pms.Provider
	if awaiting {
		pmsReg = pms.NewRegistry()
		slog.Warn("awaiting assignment/enrollment: skipping tenant-scoped guest-plane subsystems (PMS/social)")
	} else {
		pmsReg, pmsBuilt, err = pmsloader.Load(rootCtx, pool, c.TenantID, c.SiteID)
		if err != nil {
			slog.Error("pmsloader: load failed", "err", err)
			os.Exit(1)
		}
		pmsloader.StartAll(rootCtx, pmsBuilt)
		maybeSeedPMSStubs(pmsBuilt)
	}

	s := &server{
		nft:          newLocalNFT(nft.New()),
		shp:          shape.New(),
		flows:        newNetdFlows(envOr("SCD_NETD_SOCKET", "/run/stayconnect/netd.sock")),
		mail:         mail.NewStub(c.MailLogPath),
		sms:          sms.NewStub(c.SMSLogPath),
		whatsapp:     whatsapp.NewStub(c.WhatsAppLogPath),
		socialBase:   socialBase,
		loginRL:      newLoginLimiter(),
		pmsReg:       pmsReg,
		pmsBuilt:     pmsBuilt,
		db:           pool,
		tenID:        c.TenantID,
		siteID:       c.SiteID,
		applianceID:  c.ApplianceID,
		applID:       c.ApplianceID,
		ctrlBase:     c.CtrlAPIBase,
		serial:       c.Serial,
		legacyBridge: envOr("SCD_LEGACY_BRIDGE", "br-lan"),
		hw:           hwid.Detect(),
		met: metrics.New("0.0.3-dev", prometheus.Labels{
			"tenant_id":    c.TenantID,
			"site_id":      c.SiteID,
			"appliance_id": c.ApplianceID,
		}),
	}
	s.nft.SetMetrics(s.met)
	// The portal's HTTPS state for social sign-in: the HTTP fallback until the portal certificate agent starts below.
	s.socialPortal = noPortalHTTPS{}

	// Dark-auth machinery: all OFF unless enabled; an enabled feature with missing or invalid key material fails startup (fail closed).
	if err := s.initAuthSecurity(rootCtx, pool, c, slog.Default()); err != nil {
		slog.Error("auth-security init failed", "err", err)
		os.Exit(1)
	}

	// Ensure the local tenant/site mirror rows exist on every boot (idempotent): guest tables FK to them, and adoption
	// seeds them only on a version change, so a plain restart must reconcile too.
	if s.db != nil && s.tenID != "" && s.siteID != "" {
		s.seedTenantSiteMirror(rootCtx, s.tenID, s.siteID, "", "")

		// If the site DB holds data owned by a previous tenant, purge it securely before any guest is authorized; on
		// failure the appliance is held fail-closed so a partial cleanup never exposes one customer's data to another.
		if err := s.reconcileTenantOwnership(rootCtx); err != nil {
			s.tenantBlockReason = "cross-tenant data cleanup failed; guest access is disabled until it succeeds"
			s.tenantBlocked.Store(true)
			slog.Error("tenant-transition: CLEANUP FAILED — holding appliance FAIL-CLOSED (no guest authorization) until it succeeds",
				"err", err, "current_tenant", s.tenID)
		}
	}

	if removedRec != nil {
		s.removed.Store(removedRec)
	}
	talksToCentral := c.CtrlAPIBase != "" && removedRec == nil

	s.idStore = idStore
	s.central = central
	s.asgKick = make(chan struct{}, 1)
	s.certReconcile = make(chan struct{}, 1)
	s.reconcileKick = make(chan struct{}, 1)
	if ident != nil {
		s.idPriv = ident.PrivateKey()
		s.identityPubB64 = ident.PublicKeyB64
		if raw, err := base64.RawStdEncoding.DecodeString(ident.PublicKeyB64); err == nil && len(raw) == ed25519.PublicKeySize {
			s.identityKeyFpr = applianceauth.KeyID(ed25519.PublicKey(raw))
		}
	} else if !identityDamaged && mayRegister(c.AutoRegister, c.CtrlAPIBase, removedRec) {
		// Keep trying while unregistered; on success scd re-executes so every Central agent starts with the new identity (registration.go).
		s.reg = newRegistrar(
			func(ctx context.Context) (*identity.Identity, error) {
				return idStore.Register(ctx, c.CtrlAPIBase, nil)
			},
			func(err error) { central.record(err, time.Now()) },
			func(id *identity.Identity) {
				slog.Info("registered with Central; re-executing scd to start the Central agents",
					"appliance_id", id.ApplianceID)
				go func() { time.Sleep(500 * time.Millisecond); s.reexec() }()
			})
		go s.reg.run(rootCtx)
	}

	// Assignment agent: the only channel by which the appliance adopts or changes its tenant/site. Deliberately independent
	// of the mTLS certificate (it authenticates with the identity-signing JWT), so a fresh appliance can be assigned before a
	// certificate exists. Verifies signature, binding and monotonic version, persists atomically, then re-execs scd.
	if ident != nil && talksToCentral {
		s.startAssignmentAgent(rootCtx, c.CtrlAPIBase)
		go s.offlineReconcileLoop(rootCtx)
	}

	// Signed license manager. A production build always requires a real signed license: no environment variable or config
	// file enables permissive mode, which exists only in a `-tags devlicense` build. An /etc/stayconnect/production marker
	// forces production even on a dev binary.
	licRequired, devAttempt := resolveLicenseRequired(c.LicenseRequired)
	s.lic = licstate.New(pool, c.TenantID, c.LicenseDir, c.VendorPub, licRequired)
	s.permissiveBlocked = devAttempt
	if devAttempt != "" {
		// Rejected: enforcement stays ON; raise a local critical alert and audit record.
		s.reportPermissiveAttempt(rootCtx, devAttempt)
	}
	// Bind license verification to this box: identity key (primary anchor) plus serial / hardware fingerprint / WAN MAC (mismatch and clone signals).
	s.lic.SetLocalIdentity(lic.LocalIdentity{
		ApplianceID:            s.applID,
		IdentityKeyFingerprint: s.identityKeyFpr,
		Serial:                 s.hw.Serial,
		HardwareFingerprint:    s.hw.Fingerprint,
		WANMAC:                 s.hw.WANMAC,
	})
	s.lic.Load(rootCtx)
	if err := s.initModules(); err != nil {
		slog.Error("deployment ceiling: incoherent configuration", "err", err)
		os.Exit(2)
	}
	s.initCard(rootCtx, c.SecretsDir)
	s.initOpenAccess(c.SecretsDir)
	s.initClientResume(c.SecretsDir)
	s.initNotifyKey(c.SecretsDir)
	s.ledgerInit(c.SecretsDir)
	s.initOperatorMFAKey(c.SecretsDir)
	s.portalTLS = s.initPortalTLS(c, ident != nil && talksToCentral)
	s.socialPortal = s.portalTLS
	// Social sign-in: load the sealing key, seal any plaintext provider secret, build providers from sealed secrets and keep
	// them current; an appliance awaiting assignment serves only the base providers.
	s.initSocialKey(c.SecretsDir)
	if err := s.reloadSocial(rootCtx, true); err != nil {
		slog.Warn("social sign-in: initial provider load failed", "err", err)
	}
	if !awaiting {
		go s.socialReloadLoop(rootCtx)
	}
	s.registerModuleProbes()
	// Crash recovery for offline first activation runs after the licence state loads, because deciding whether an interrupted
	// activation completed means asking whether the licence landed: it finishes the activation or rolls back to unassigned.
	s.recoverInterruptedActivation()
	// The concurrent-guest cap comes from the local signed licence and is enforced atomically in the activation transaction;
	// Central availability plays no part in guest authorization.
	if ident != nil && talksToCentral {
		// Every fetch outcome feeds the Central contact record. Asks every minute while unlicensed, every six hours once licensed; Check now and a new assignment kick it.
		s.lic.OnFetch(func(err error) { central.record(err, time.Now()) })
		s.lic.StartLoops(rootCtx, c.CtrlAPIBase, ident.ApplianceID, ident.PrivateKey(), 6*time.Hour)
	} else {
		s.lic.StartLoops(rootCtx, "", "", nil, 0)
	}

	// Resolve notification providers from the DB (Stubs when no row exists), wrapped with metric instrumentation. Resolved again
	// on every reload (notify_admin.go) so Admin Console changes are live; sealed secrets are preferred, plaintext rows still work.
	s.notifyFallback.mail, s.notifyFallback.sms, s.notifyFallback.whatsapp = s.mail, s.sms, s.whatsapp
	s.loadNotify(rootCtx)
	// Email accounts: loop cadences and the outbox kick, before any route can register an account.
	s.initSelfService(os.Getenv)

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// No path, query or client address in the request log (reqlog; GDPR audit 2026-10-19).
	r.Use(reqlog.Middleware)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(10 * time.Second))
	// Splits guest-authentication routes (portald) from administrative routes (edged: backups, licence install, enrolment,
	// voucher codes) by the caller's identity; see admin_surface.go. After Recoverer and Timeout and before every route, so a
	// refusal is still logged, recovered and bounded.
	r.Use(s.peerGate)
	r.Use(guestCapacity(maxGuestInFlight))
	r.Use(bodyLimit)
	r.Get("/v1/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	r.Method("GET", "/metrics", s.met.Handler())
	// The voucher surface lives here because the DEK does: scd is root and unix-socket only. Reveal and export recover a code in
	// the clear; edged has already authenticated the operator, checked permission, re-verified the password and taken a reason.
	// These handlers open the ciphertext and write the append-only record in the same transaction.
	r.Post("/v1/vouchers/issue", s.issueVouchersIAMv2)
	r.Get("/v1/vouchers", s.listVouchers)
	r.Get("/v1/vouchers/summary", s.voucherSummary)
	r.Get("/v1/vouchers/batches", s.listVoucherBatches)
	r.Post("/v1/vouchers/export", s.exportVoucherCodes)
	r.Post("/v1/vouchers/{id}/reveal", s.revealVoucherCode)
	r.Post("/v1/vouchers/{id}/revoke", s.revokeVoucher)
	r.Post("/v1/voucher-batches/{id}/revoke", s.revokeVoucherBatch)
	// Key rotation: the deliberate audited path for superseding a per-generation blind index key.
	r.Get("/v1/voucher-key-generations", s.listVoucherKeyGenerations)
	r.Post("/v1/voucher-key-generations/{id}/supersede", s.rotateVoucherKeyGeneration)
	// Entitlement -> session: turns the iam_v2 entitlement into something the enforcement plane can act on.
	r.Post("/v1/sessions/activate", s.activateIAMv2Session)
	r.Post("/v1/devices/list", s.clientDevicesList)
	r.Post("/v1/devices/release", s.clientDevicesRelease)
	r.Post("/v1/devices/replace", s.clientDevicesReplace)
	r.Post("/v1/sessions/authorize", s.authorize)
	// Open package selection: the anonymous access subject (open_access.go). A guest route, like authorize.
	r.Post("/v1/sessions/authorize-open", s.authorizeOpen)
	r.Post("/v1/sessions/authorize-otp", s.authorizeOTP)
	r.Post("/v1/sessions/authorize-credentials", s.authorizeGuestAccount)
	r.Post("/v1/sessions/revoke", s.revoke)
	r.Get("/v1/sessions/status", s.status)
	r.Post("/v1/auth/otp/issue", s.otpIssue)
	// Email accounts: guest routes portald calls, plus the operator's resend that edged proxies. Registered here, not in a helper, so admin_surface_test.go classifies every one.
	r.Post("/v1/auth/account/register", s.accountRegister)
	r.Post("/v1/auth/account/status", s.accountStatus)
	r.Post("/v1/auth/account/resend", s.accountResend)
	r.Post("/v1/auth/account/grace", s.accountGrace)
	r.Post("/v1/auth/account/verify", s.accountVerify)
	r.Post("/v1/auth/account/forgot", s.accountForgot)
	r.Post("/v1/auth/account/reset", s.accountReset)
	r.Post("/v1/admin/accounts/{id}/resend-verification", s.adminAccountResendVerification)
	// The Admin Console operator's authenticator: edged only, under /v1/admin/.
	s.operatorMFARoutes(r)
	r.Post("/v1/auth/social/start", s.socialStart)
	r.Post("/v1/sessions/authorize-social", s.authorizeSocial)
	r.Post("/v1/sessions/resume-peek", s.resumePeek)
	r.Post("/v1/sessions/authorize-resume", s.authorizeResume)
	r.Post("/v1/sessions/resume-revoke", s.resumeRevoke)
	// /v1/auth/pms/verify is removed; the PMS guest path is /v1/phase3/auth/pms/resolve then /grant, which derives identity from the connection rather than the body.
	r.Post("/v1/admin/pms/{name}/test", s.pmsAdminTest)
	r.Get("/v1/admin/pms/{name}/cache", s.pmsAdminCache)
	r.Get("/v1/admin/pms/{name}/health", s.pmsAdminHealth)
	r.Get("/v1/tenant/auth-methods", s.tenantAuthMethods)
	r.Get("/v1/tenant/branding", s.tenantBranding)
	r.Get("/v1/ledger/status", s.ledgerStatusGet)
	r.Post("/v1/ledger/prepare", s.ledgerPrepare)
	r.Post("/v1/ledger/commit", s.ledgerCommit)
	r.Post("/v1/ledger/abort", s.ledgerAbort)
	r.Put("/v1/ledger/destination", s.ledgerDestinationPut)
	r.Post("/v1/ledger/destination/probe", s.ledgerDestinationProbe)
	r.Post("/v1/ledger/recover", s.ledgerRecover)
	r.Post("/v1/backup/run", s.backupRun)
	r.Get("/v1/backup/status", s.backupStatus)
	r.Post("/v1/backup/earlier/remove", s.earlierBackupsRemove)
	r.Post("/v1/backup/verify", s.backupVerify)
	r.Post("/v1/backup/key-test", s.backupKeyTest)
	// The destructive one: edged performs step-up, the permission check and the audit record before reaching here. See restore.go.
	r.Post("/v1/backup/restore", s.backupRestore)
	r.Get("/v1/backup/last-restore", s.lastRestore)
	r.Get("/v1/maintenance", s.maintenanceStatus)
	r.Get("/v1/backup/settings", s.backupSettingsGet)
	r.Post("/v1/backup/settings", s.backupSettingsSet)
	// Appliance activation, licence and the link to Central: edged proxies these; central.go holds the one computation of every state they report.
	r.Get("/v1/central/status", s.centralStatus)
	r.Get("/v1/modules", s.modulesStatus)
	s.paymentAdminRoutes(r)
	r.Post("/v1/central/refresh", s.centralRefresh)
	// Offline activation: the request this appliance emits and the package (first activation or licence package) that comes back.
	r.Get("/v1/central/offline-request", s.setupActivationRequest)
	r.Post("/v1/central/offline-package", s.centralOfflinePackage)
	r.Post("/v1/license/install", s.licenseInstall)
	r.Post("/v1/admin/pms/reload", s.pmsAdminReload)
	r.Post("/v1/admin/walled-garden/reload", s.gardenReload)
	s.notifyAdminRoutes(r)
	// Social sign-in administration for edged: the write-only secret, its yes/no state, reload, and the offered/why-not status.
	r.Put("/v1/social/providers/{id}/secret", s.socialSecretStore)
	r.Get("/v1/social/providers/{id}/secret-state", s.socialSecretState)
	r.Post("/v1/social/reload", s.socialReload)
	r.Get("/v1/social/status", s.socialStatus)
	s.tempAccessAdminRoutes(r)
	// Hotel Admin TLS cert lifecycle: scd runs as root and drives the privileged manager (edged is sandboxed with NoNewPrivileges); edged already enforced permission and step-up.
	r.Post("/v1/hotel-admin-cert/check", s.hotelAdminCertCheck)
	r.Post("/v1/hotel-admin-cert/rotate", s.hotelAdminCertRotate)
	r.Post("/v1/hotel-admin-cert/renew", s.hotelAdminCertRenew)
	// The portal HTTPS state: read by portald every 15 seconds and by edged; kick and renew are edged's, after the operator's permission and step-up.
	r.Get("/v1/portal/tls-state", s.portalTLSState)
	r.Post("/v1/portal/tls/kick", s.portalTLSKick)
	r.Post("/v1/portal/tls/renew", s.portalTLSRenew)

	// Commerce routes are mounted only when the portal surface is ON; while dark they are absent (404) and the engine holds a nil repository.
	if s.commerceCfg.PortalOn() {
		r.Get("/v1/commerce/packages", s.commercePackages)
		r.Post("/v1/commerce/quote", s.commerceQuote)
		r.Post("/v1/commerce/confirm", s.commerceConfirm)
		r.Post("/v1/commerce/redeem", s.commerceRedeemVoucher)
		r.Post("/v1/commerce/purchase-status", s.purchaseStatus)
		slog.Info("phase2 portal commerce routes mounted")
	}

	// The PMS auth slice is constructed and mounted only with the master and PMS-auth flags on. The sealing key loads before
	// the arm is built (its recorder uses it); its absence is logged, not fatal, so a missing audit key never takes a property's guest internet offline.
	s.signInAttemptKeyring = loadSignInAttemptKeyring(c.SecretsDir)
	if pmsCfg3, err := iamv2.LoadPMSConfigFromEnv(os.Getenv); err != nil {
		slog.Error("scd: phase3 config fail-closed", "err", err)
		os.Exit(1)
	} else if s.p3auth = newPhase3Auth(pmsCfg3, s); s.p3auth != nil {
		// The controlled-writer boundary must be real before a guest-facing path can create an Entitlement.
		if err := writerguard.Verify(rootCtx, s.db, writerguard.Phase3Requirements()); err != nil {
			slog.Error("scd: refusing to serve the Phase-3 auth surface", "err", err)
			os.Exit(1)
		}
		r.Post("/v1/phase3/auth/pms/resolve", s.p3auth.resolveHandler)
		// The sealed half of a recorded attempt, opened for edged on an authorised operator's behalf; scd holds the key.
		r.Post("/v1/phase3/signin-attempts/credentials", s.p3auth.signInAttemptCredentialsHandler)
		r.Post("/v1/phase3/auth/pms/grant", s.p3auth.grantHandler)
		// Read-only: why the device's most recent access ended (DATA, TIME or nothing), so the portal can say so.
		r.Post("/v1/phase3/access/status", s.p3auth.accessStatusHandler)
		slog.Info("phase3 pms auth routes mounted", "flags", pmsCfg3.SafeFlagSummary())
	}

	// The guest post-stay surface depends on the Phase-3 arm for device identity: a Phase-5 flag set while Phase-3 auth is off is a deployment mistake and must be loud.
	p5cfg, err := iamv2.LoadPhase5ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase-5 flags are incoherent", "err", err)
		os.Exit(1)
	}
	if p5cfg.GuestOn() && s.p3auth == nil {
		slog.Error("phase5 post-stay guest surface is enabled but the Phase-3 auth arm is off",
			"phase5", p5cfg.SafeFlagSummary())
		os.Exit(1)
	}
	if s.p5poststay = newPhase5PostStay(p5cfg, s, s.p3auth); s.p5poststay != nil {
		r.Post("/v1/phase5/poststay/issue", s.p5poststay.issueHandler)
		r.Post("/v1/phase5/auth/post-stay-pin", s.p5poststay.verifyHandler)
		r.Post("/v1/phase5/poststay/convert", s.p5poststay.convertHandler)
		slog.Info("phase5 post-stay guest routes mounted", "flags", p5cfg.SafeFlagSummary())
	} else {
		slog.Info("phase5 post-stay guest surface is DARK (routes absent)", "flags", p5cfg.SafeFlagSummary())
	}

	// Guest device self-service: the deployment gate decides whether routes exist; the per-appliance setting is read from the DB on every request, so turning it off needs no restart or Central call.
	p6cfg, err := iamv2.LoadPhase6ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase6 configuration", "err", err)
		os.Exit(1)
	}
	s.p6cfg = p6cfg
	if p6cfg.DeviceGuestOn() && s.p3auth == nil {
		slog.Error("phase6 guest device surface is enabled but the Phase-3 auth arm is off",
			"phase6", p6cfg.SafeFlagSummary())
		os.Exit(1)
	}
	if s.p6devices = newPhase6Devices(p6cfg, s, s.p3auth, c.ApplianceID); s.p6devices != nil {
		r.Post("/v1/phase6/devices/list", s.p6devices.listHandler)
		r.Post("/v1/phase6/devices/release", s.p6devices.releaseHandler)
		slog.Info("phase6 guest device routes mounted", "flags", p6cfg.SafeFlagSummary())
	} else {
		slog.Info("phase6 guest device surface is DARK (routes absent)", "flags", p6cfg.SafeFlagSummary())
	}

	_ = os.MkdirAll("/run/stayconnect", 0o755)
	_ = os.Remove(c.SocketPath)
	ln, err := net.Listen("unix", c.SocketPath)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	_ = os.Chmod(c.SocketPath, 0o660)
	if g, err := lookupSocketGroup(os.Getenv("SCD_SOCKET_GROUP"), user.LookupGroup); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			if err := os.Chown(c.SocketPath, 0, gid); err != nil {
				slog.Warn("chown socket", "err", err)
			}
			slog.Info("scd socket group", "group", g.Name)
		}
	} else {
		slog.Warn("scd socket: no socket group resolves; the socket is root-only", "err", err)
	}

	// ConnContext reads peer credentials once at accept time; admin routes require edged's uid and exact binary, and portald has its own account (admin_surface.go, peer_identity_linux.go).
	srv := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second, ConnContext: withPeerCred}
	go func() {
		slog.Info("scd listening", "socket", c.SocketPath)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
		}
	}()

	// Optional TCP listener serving only /metrics for a remote Prometheus (SCD_METRICS_ADDR); bind to localhost or a management interface, never the guest LAN.
	var metricsSrv *http.Server
	if c.MetricsAddr != "" {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", s.met.Handler())
		metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("ok"))
		})
		metricsSrv = &http.Server{
			Addr: c.MetricsAddr, Handler: metricsMux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			slog.Info("scd metrics listening", "addr", c.MetricsAddr)
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("metrics serve", "err", err)
			}
		}()
	}
	go s.pmsHealthFlushLoop(rootCtx)
	// The portal HTTPS certificate agent runs on every appliance: one that cannot reach Central still decides HTTPS from the certificate it has until it expires.
	go s.portalTLS.run(rootCtx)
	// Walled-garden rules from the site DB are reconciled into the nft walled_garden_ip set.
	go s.gardenReconcileLoop(rootCtx)
	// Email accounts: the emailed-link outbox and the verification sweep (suspension, reminders).
	if !awaiting {
		go s.selfServiceSendLoop(rootCtx)
		go s.selfServiceSweepLoop(rootCtx)
	}
	go s.cardReconcileLoop(rootCtx)
	go s.postingLoop(rootCtx, s.initPostingWorker(rootCtx))
	// Safety-net reload every 10 minutes in case a Hotel Admin reload was missed.
	go s.pmsReloadSafetyLoop(rootCtx)

	// Bounded-retention cleanup of durable throttle buckets (only when enabled).
	if s.authThrottle != nil {
		go s.authThrottle.RunCleanup(rootCtx, 5*time.Minute, time.Minute)
	}

	// Thirty-day sweep of guest sign-in attempt records: a bounded DELETE on a ticker (no DB scheduler here), only where the Phase-3 arm exists.
	if s.p3auth != nil && s.p3auth.attempts != nil {
		go s.p3auth.attempts.RunPurge(rootCtx, time.Hour)
	}

	// Hourly anonymisation of guest data that outlived the site's retention settings.
	go s.dataRetentionLoop(rootCtx)
	go s.ledgerLoop(rootCtx)
	go s.backupLoop(rootCtx)
	// System logs follow the site's log period.
	go s.logRetentionLoop(rootCtx)

	// Signed hello against ctrlapi on boot and periodically: orphan recovery. If Central deleted this appliance, a sustained
	// definitive reply (orphanTracker) self-resets it to re-register as Pending and stop serving on its stale cached license.
	// Periodic because a deletion while running must be noticed; offline-safe because outages yield connection errors, 5xx or
	// timeouts, never a definitive 401.
	if ident != nil && talksToCentral {
		orphan := func(code int, b []byte) bool {
			l := strings.ToLower(string(b))
			return code == 401 && (strings.Contains(l, "not enrolled") || strings.Contains(l, "unknown appliance") || strings.Contains(l, "not found"))
		}
		// Acted on only when sustained: several consecutive "not found" replies over a minimum window, reset by any other reply.
		tracker := &orphanTracker{}
		helloProbe := func() {
			code, body, err := s.helloRaw(rootCtx)
			notFound := err != nil && orphan(code, body)
			confirmed := tracker.observe(time.Now(), code != 0, notFound)
			if err == nil {
				return
			}
			if code == 0 {
				slog.Warn("hello: call failed", "err", err)
				return
			}
			slog.Warn("hello: unexpected status", "status", code, "body", string(body))
			if c.AutoRegister && confirmed {
				// Whether it may register again depends on whether it ever held a customer.
				s.handleConfirmedOrphan(rootCtx, orphanPaths{
					IdentityDir: c.IdentityDir, AssignmentDir: envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment"),
					LicenseDir: c.LicenseDir, CertDir: c.CertDir, Marker: removedMarkerPath(),
				})
			} else if notFound {
				slog.Warn("hello: Central does not know this appliance; waiting for the reply to be sustained before acting",
					"replies_needed", orphanConfirmReplies, "window", orphanConfirmWindow.String())
			}
		}
		go func() {
			helloProbe() // boot
			interval := 5 * time.Minute
			if v := envOr("SCD_ORPHAN_CHECK_INTERVAL", ""); v != "" {
				if d, e := time.ParseDuration(v); e == nil && d >= 30*time.Second {
					interval = d
				}
			}
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-t.C:
					helloProbe()
				}
			}
		}()
	}

	// Appliance-side mTLS: ensure a client certificate exists (CSR, issue, fetch, store), verify the transport, rotate before expiry. A failure here never affects local guest operation.
	if ident != nil && talksToCentral && c.MTLSBase != "" {
		certMgr := appliancecert.New(c.CertDir, c.CtrlAPIBase, c.MTLSBase, ident.ApplianceID, ident.PrivateKey())
		// AD-2: the appliance Root CA pinned at provisioning (default /etc/stayconnect/appliance-root-ca.crt).
		certMgr.PinPath = envOr("SCD_APPLIANCE_ROOT_CA", "")
		s.certMgr = certMgr
		// Every bootstrap attempt tells us whether Central is answering, including while it waits for an operator to activate.
		certMgr.OnAttempt(func(err error) { central.record(err, time.Now()) })
		go func() {
			// Keep trying: first issuance waits on a human pressing Activate, which has no deadline; giving up would end the
			// certificate lifecycle and take the mTLS-only assignment channel, the licence and convergence with it.
			if err := certMgr.EnsureUntilInstalled(rootCtx); err != nil {
				slog.Warn("appliancecert: certificate bootstrap stopped", "err", err)
				return
			}
			if out, err := certMgr.MTLSHello(rootCtx); err != nil {
				slog.Warn("appliancecert: mTLS hello failed", "err", err)
			} else {
				slog.Info("appliancecert: mTLS transport verified", "server_appliance_id", out["appliance_id"], "transport", out["transport"])
				// Cut license fetch/refresh over to mTLS now that a cert is live.
				if cl, base, ok := certMgr.Transport(); ok {
					s.lic.SetMTLSTransport(cl, base)
					slog.Info("license fetch cut over to mTLS transport", "base", base)
				}
			}
			// A new certificate must reach every user of it: the licence fetcher holds one client, so it is re-handed after every change or it keeps presenting the old certificate.
			rehand := func() {
				if cl, base, ok := certMgr.Transport(); ok {
					s.lic.SetMTLSTransport(cl, base)
				}
				select {
				case s.asgKick <- struct{}{}:
				default:
				}
			}
			var lastReconcile time.Time
			reconcile := func(why string) {
				if time.Since(lastReconcile) < time.Minute {
					return
				}
				lastReconcile = time.Now()
				out, err := certMgr.Reconcile(rootCtx)
				if err != nil {
					central.record(err, time.Now())
					slog.Warn("appliancecert: reconcile failed", "why", why, "err", err)
					return
				}
				switch out {
				case appliancecert.ReconcileInstalled, appliancecert.ReconcileReissued:
					slog.Info("appliancecert: certificate replaced", "why", why, "outcome", string(out))
					rehand()
				case appliancecert.ReconcileWaiting:
					slog.Info("appliancecert: a certificate request is waiting at Central", "why", why)
				}
			}
			reconcile("startup")
			t := time.NewTicker(6 * time.Hour)
			defer t.Stop()
			rt := time.NewTicker(5 * time.Minute)
			defer rt.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-t.C:
					if err := certMgr.MaybeRotate(rootCtx, 14*24*time.Hour); err != nil {
						slog.Warn("appliancecert: rotation failed", "err", err)
					} else {
						rehand()
					}
				case <-rt.C:
					reconcile("periodic")
				case <-s.certReconcile:
					reconcile("mtls_refused")
				}
			}
		}()
	}

	<-rootCtx.Done()
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
	if metricsSrv != nil {
		_ = metricsSrv.Shutdown(shutCtx)
	}
}
