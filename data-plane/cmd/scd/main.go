// scd — Session Controller Daemon.
//
// Owns the nftables auth_ipv4 set and the `sessions` table.
// Listens on a Unix socket; portald is the primary client.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/appliancecert"
	"github.com/stayconnect/enterprise/data-plane/internal/identity"
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/notifyloader"
	"github.com/stayconnect/enterprise/data-plane/internal/outbox"
	"github.com/stayconnect/enterprise/data-plane/internal/pms"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsloader"
	"github.com/stayconnect/enterprise/data-plane/internal/session"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/social"
	"github.com/stayconnect/enterprise/data-plane/internal/socialloader"
	"github.com/stayconnect/enterprise/data-plane/internal/voucher"
)

type cfg struct {
	SocketPath  string
	DBURL       string
	TenantID    string
	SiteID      string
	ApplianceID string
	MailLogPath string
	SMSLogPath  string

	// Phase 5.1 — appliance identity. When IdentityDir holds an identity.json
	// it takes precedence over the legacy TenantID/SiteID/ApplianceID env
	// vars. On first boot with no identity file, BootstrapToken + Serial +
	// CtrlAPIBase drive the enrollment flow.
	IdentityDir    string
	CtrlAPIBase    string
	BootstrapToken string
	Serial         string

	// Phase 5.2 — remote transport. When set, scd subscribes to
	// scd.{applianceID}.> and serves ctrlapi RPCs over NATS.
	NATSURL string

	// Phase 13 — optional TCP listener for /metrics. Empty = disabled.
	MetricsAddr string

	// Edge-first refactor — signed license enforcement. LicenseDir holds
	// the installed envelope + anti-rollback state; VendorPub is the vendor
	// verification key. LicenseRequired=false keeps pre-cutover pilots
	// permissive when no license material exists yet.
	LicenseDir      string
	VendorPub       string
	LicenseRequired bool

	// mTLS transport (Phase B). CertDir holds the client cert + CA bundle;
	// MTLSBase is the Central mutual-TLS listener. Empty MTLSBase disables the
	// mTLS cutover (signed-JWT over the HTTPS ingress remains).
	CertDir  string
	MTLSBase string
	// NATSMTLSURL, when set, makes scd connect to Central NATS over mTLS (client
	// cert, no username/password) instead of SCD_NATS_URL. Staged cutover: set
	// this to the :4223 endpoint; SCD_NATS_URL remains as documented rollback.
	NATSMTLSURL string
}

func loadCfg() cfg {
	return cfg{
		SocketPath:  envOr("SCD_SOCKET", "/run/stayconnect/scd.sock"),
		DBURL:       envOr("SCD_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"),
		TenantID:    os.Getenv("SCD_TENANT_ID"),
		SiteID:      os.Getenv("SCD_SITE_ID"),
		ApplianceID: os.Getenv("SCD_APPLIANCE_ID"),
		MailLogPath: envOr("SCD_MAIL_LOG", "/var/log/stayconnect/otp-mail.log"),
		SMSLogPath:  envOr("SCD_SMS_LOG", "/var/log/stayconnect/otp-sms.log"),

		IdentityDir:    envOr("SCD_IDENTITY_DIR", "/etc/stayconnect/identity"),
		CtrlAPIBase:    os.Getenv("SCD_CTRLAPI_BASE"),
		BootstrapToken: os.Getenv("SCD_BOOTSTRAP_TOKEN"),
		Serial:         os.Getenv("SCD_SERIAL"),

		NATSURL: os.Getenv("SCD_NATS_URL"),

		MetricsAddr: os.Getenv("SCD_METRICS_ADDR"),

		LicenseDir:      envOr("SCD_LICENSE_DIR", "/etc/stayconnect/license"),
		VendorPub:       envOr("SCD_VENDOR_PUB", "/etc/stayconnect/vendor-license.pub"),
		LicenseRequired: os.Getenv("SCD_LICENSE_REQUIRED") == "true",

		CertDir:     envOr("SCD_CERT_DIR", "/etc/stayconnect/certs"),
		MTLSBase:    os.Getenv("SCD_MTLS_BASE"),
		NATSMTLSURL: os.Getenv("SCD_NATS_MTLS_URL"),
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type server struct {
	nft       *nftSync // wraps nft.Client with NATS replication; API unchanged
	shp       *shape.Client
	vou       *voucher.Store
	sess      *session.Manager
	mail      mail.Mailer
	sms       sms.Sender
	socialReg *social.Registry

	// PMS registry is live-reloadable (phase 5.3). All readers must go
	// through currentPMSReg(); the reload path atomically swaps it under
	// pmsMu. pmsBuilt holds the current generation's providers so we can
	// Stop() them during the next reload.
	pmsMu    sync.RWMutex
	pmsReg   *pms.Registry
	pmsBuilt []pms.Provider

	db     *pgxpool.Pool
	tenID  string
	siteID string
	applID string

	// Cloud connection config surfaced (read-only, secrets masked) to the Hotel
	// Admin Cloud Connection page via edged.
	ctrlBase string
	natsURL  string
	serial   string

	// certMgr owns the mTLS client-certificate lifecycle (nil if disabled).
	certMgr *appliancecert.Manager
	// natsConn is the live NATS connection (nil if not connected).
	natsConn *nats.Conn
	// identityKeyFpr is the fingerprint of the identity-signing public key
	// (distinct from the mTLS cert fingerprint) — shown in the setup wizard.
	identityKeyFpr string
	// idStore + bootstrap token config for runtime enrollment (setup wizard).
	idStore  *identity.Store
	enrolled bool
	idPriv   ed25519.PrivateKey // identity-signing key (for signed reconcile calls)

	// legacyBridge is the fallback ingress interface for sessions whose IP
	// matches no configured guest network (pre-Phase-19 / legacy network).
	legacyBridge string

	met *metrics.Registry // phase 7

	// Edge-first refactor: signed-license manager, telemetry outbox and the
	// on-demand cloud license fetch (nil when not configured).
	lic      *licstate.Manager
	obx      *outbox.Outbox
	licFetch func(context.Context) error
}

func (s *server) currentPMSReg() *pms.Registry {
	s.pmsMu.RLock()
	defer s.pmsMu.RUnlock()
	return s.pmsReg
}

// pmsReloadSafetyLoop fires reloadPMS every 10 minutes regardless of NATS
// activity. Cheap; the no-op case (no DB changes) just re-Configure()s the
// same providers on a new generation.
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

// reloadPMS re-reads pms_providers from the DB, builds a fresh provider set,
// swaps it in atomically, then stops the previous generation. Callers that
// fail mid-build leave the old registry intact.
func (s *server) reloadPMS(ctx context.Context) error {
	reg, built, err := pmsloader.Load(ctx, s.db, s.tenID, s.siteID)
	if err != nil {
		return fmt.Errorf("reloadPMS load: %w", err)
	}
	pmsloader.StartAll(ctx, built)
	// Re-seed stub reservations if SCD_PMS_STUB_SEED=true. A fresh Stub
	// instance was built by pmsloader.Load above, so seed data from the
	// previous generation is gone.
	maybeSeedPMSStubs(built)

	s.pmsMu.Lock()
	oldBuilt := s.pmsBuilt
	s.pmsReg = reg
	s.pmsBuilt = built
	s.pmsMu.Unlock()

	// Stop the prior generation *after* the swap — any in-flight request
	// that already grabbed the old registry can finish without racing on
	// torn-down resources.
	pmsloader.StopAll(oldBuilt)
	slog.Info("pms reloaded", "count", len(built))
	return nil
}

type authorizeReq struct {
	IP      string `json:"ip"`
	MAC     string `json:"mac"`
	Voucher string `json:"voucher"`
}
type authorizeResp struct {
	SessionID       string `json:"session_id"`
	GuestID         string `json:"guest_id"`
	DurationSeconds int    `json:"duration_seconds"`
	ExpiresAt       string `json:"expires_at,omitempty"`
}

func (s *server) authorize(w http.ResponseWriter, r *http.Request) {
	var req authorizeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	// Voucher is the basic-access method: gated only on the license state
	// permitting new sessions (works through Restricted/Suspended).
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
	red, err := s.vou.Validate(r.Context(), s.tenID, req.Voucher)
	if err != nil {
		switch {
		case errors.Is(err, voucher.ErrNotFound):
			httpErr(w, http.StatusNotFound, "voucher not found")
		case errors.Is(err, voucher.ErrExpired),
			errors.Is(err, voucher.ErrExhausted),
			errors.Is(err, voucher.ErrRevoked):
			httpErr(w, http.StatusForbidden, err.Error())
		default:
			slog.Error("validate", "err", err)
			httpErr(w, http.StatusInternalServerError, "internal")
		}
		return
	}

	// Enforce tenant plan limit: max_concurrent_devices.
	// Configured + Limit >= 0 + Current >= Limit  → block.
	// Limit == -1 means "unlimited".
	if cc, err := s.sess.CheckConcurrency(r.Context()); err != nil {
		slog.Warn("concurrency check", "err", err)
	} else if cc.Configured && cc.Limit >= 0 && cc.Current >= cc.Limit {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":     "limit_exceeded",
			"limit_key": "max_concurrent_devices",
			"limit":     cc.Limit,
			"current":   cc.Current,
		})
		return
	}

	nc := s.resolveNetwork(r.Context(), ip)
	ttl := time.Duration(red.DurationSeconds) * time.Second
	if err := s.nft.Allow(r.Context(), nc.Bridge, ip, ttl); err != nil {
		slog.Error("nft allow", "err", err)
		httpErr(w, http.StatusInternalServerError, "nft allow failed")
		return
	}
	if err := s.shp.AddSession(r.Context(), ip, red.DownKbps, red.UpKbps); err != nil {
		slog.Error("shape add", "err", err)
		_ = s.nft.Deny(context.Background(), nc.Bridge, ip)
		httpErr(w, http.StatusInternalServerError, "shape add failed")
		return
	}

	au, err := s.sess.Start(r.Context(), mac, ip, red.VoucherID, red.DurationSeconds)
	if err != nil {
		slog.Error("session start", "err", err)
		_ = s.nft.Deny(context.Background(), nc.Bridge, ip)
		_ = s.shp.DeleteSession(context.Background(), ip)
		httpErr(w, http.StatusInternalServerError, "session start failed")
		return
	}
	s.recordSessionNetwork(r.Context(), au.SessionID, nc)
	if err := s.vou.Activate(r.Context(), red.VoucherID); err != nil {
		slog.Warn("voucher activate", "err", err)
	}
	s.met.SessionsStarted.WithLabelValues("voucher").Inc()

	resp := authorizeResp{
		SessionID:       au.SessionID,
		GuestID:         au.GuestID,
		DurationSeconds: red.DurationSeconds,
	}
	if au.ExpiresAt != nil {
		resp.ExpiresAt = au.ExpiresAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
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
	nc := s.resolveNetwork(r.Context(), ip)
	if err := s.nft.Deny(r.Context(), nc.Bridge, ip); err != nil {
		slog.Error("nft deny", "err", err)
	}
	if err := s.shp.DeleteSession(r.Context(), ip); err != nil {
		slog.Warn("shape delete", "err", err)
	}
	if req.Reason == "" {
		req.Reason = "admin"
	}
	if err := s.sess.End(r.Context(), ip, req.Reason); err != nil {
		slog.Error("session end", "err", err)
		httpErr(w, http.StatusInternalServerError, "session end failed")
		return
	}
	s.met.SessionsClosed.WithLabelValues(req.Reason).Inc()
	writeJSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func (s *server) status(w http.ResponseWriter, r *http.Request) {
	ip := net.ParseIP(r.URL.Query().Get("ip"))
	if ip == nil {
		httpErr(w, http.StatusBadRequest, "bad ip")
		return
	}
	id, active, err := s.sess.FindActive(r.Context(), ip)
	if err != nil {
		slog.Error("find active", "err", err)
		httpErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ip":         ip.String(),
		"session_id": id,
		"active":     active,
	})
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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	c := loadCfg()

	// Stamp the software version into every signed control-plane request.
	applianceauth.SetVersion(scdVersion)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Phase 5.1 — resolve appliance identity. Priority:
	//   1. identity.json + keypair already on disk → load them
	//   2. no identity, but SCD_BOOTSTRAP_TOKEN + SCD_SERIAL set → enroll
	//   3. no identity, but legacy SCD_TENANT_ID/SITE_ID/APPLIANCE_ID set →
	//      run un-enrolled (no signed calls to ctrlapi) with a warning
	//   4. otherwise fatal
	idStore := &identity.Store{Dir: c.IdentityDir}
	ident, err := idStore.LoadOrEnroll(rootCtx, c.CtrlAPIBase, c.BootstrapToken, c.Serial)
	if err != nil {
		slog.Error("identity: load/enroll failed", "err", err)
		os.Exit(1)
	}
	// Tenant/site are NEVER authoritative from identity.json or env: a generic
	// appliance ships with NO customer identity. The authoritative source is the
	// signed ASSIGNMENT document, persisted locally after Central assigns this
	// appliance. Before assignment the appliance runs in awaiting-assignment mode
	// (tenant/site empty; guest plane pre-license; setup wizard + assignment agent
	// active). The legacy SCD_TENANT_ID/SITE_ID env vars are honored ONLY as a
	// migration fallback when no signed assignment exists yet.
	asgStore := &assignment.Store{Dir: envOr("SCD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment")}
	if ident != nil {
		slog.Info("identity loaded", "appliance_id", ident.ApplianceID, "serial", ident.Serial)
		c.ApplianceID = ident.ApplianceID
		if c.Serial == "" {
			c.Serial = ident.Serial
		}
		// The persisted assignment is RE-VERIFIED on every boot against the local
		// trust registry (signature by an ACTIVE dedicated assignment key + binding
		// to this appliance). An appliance never operates under an assignment it
		// cannot verify — e.g. one signed by a retired key, or by the license /
		// command / update key, which are absent from the registry.
		aTen, aSite, aState, aVer := verifiedAssignment(asgStore, ident)
		if aTen != "" && aSite != "" {
			c.TenantID, c.SiteID = aTen, aSite
			slog.Info("assignment resolved", "tenant_id", aTen, "site_id", aSite, "version", aVer)
		} else if aState == "" && c.TenantID != "" && c.SiteID != "" {
			slog.Warn("no signed assignment; using legacy env tenant/site as migration fallback",
				"tenant_id", c.TenantID, "site_id", c.SiteID)
		} else {
			c.TenantID, c.SiteID = "", ""
			slog.Warn("awaiting assignment: appliance enrolled but not yet assigned to a tenant/site",
				"assignment_state", aState)
		}
	} else {
		// No identity at all → awaiting enrollment. scd still boots to serve the
		// local setup wizard; the guest plane is pre-license until enrolled.
		c.TenantID, c.SiteID, c.ApplianceID = "", "", ""
		slog.Warn("awaiting enrollment: no appliance identity; run the local setup wizard to enroll")
	}

	pool, err := pgxpool.New(rootCtx, c.DBURL)
	if err != nil {
		slog.Error("db open", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Social provider registry. Default: in-process Stub for "google" so
	// dev environments work without OAuth credentials. The loader then
	// overrides any provider that has an enabled social_oauth_providers
	// row for this tenant — that's how the real Google client takes over
	// in production.
	stubAuthBase := envOr("SCD_OAUTH_STUB_BASE", "http://portal.stayconnect.local:8380/api/oauth/stub/authorize")
	socialReg := social.NewRegistry()
	socialReg.Register(&social.Stub{ProviderName: "google", AuthorizeBase: stubAuthBase})

	// awaiting = enrolled-but-unassigned OR un-enrolled: there is no tenant/site,
	// so the tenant-scoped guest-plane subsystems (social/PMS providers, which
	// query by tenant/site UUID) are skipped. scd still boots to serve the setup
	// wizard + run the assignment agent; the guest data plane is pre-license until
	// a signed assignment arrives, after which scd re-execs into the full path.
	awaiting := c.TenantID == "" || c.SiteID == ""
	if !awaiting {
		sctx, scancel := context.WithTimeout(rootCtx, 10*time.Second)
		if _, err := socialloader.Load(sctx, pool, c.TenantID, socialReg); err != nil {
			slog.Warn("socialloader: load failed; using fallback stubs", "err", err)
		}
		scancel()
	}

	// PMS provider registry — config-driven from pms_providers (Phase 4.5.5a).
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
		nft:          newNFTSync(nft.New(), nil, c.ApplianceID, c.SiteID),
		shp:          shape.New(),
		vou:          &voucher.Store{DB: pool},
		sess:         &session.Manager{DB: pool, TenantID: c.TenantID, SiteID: c.SiteID, ApplianceID: c.ApplianceID},
		mail:         mail.NewStub(c.MailLogPath),
		sms:          sms.NewStub(c.SMSLogPath),
		socialReg:    socialReg,
		pmsReg:       pmsReg,
		pmsBuilt:     pmsBuilt,
		db:           pool,
		tenID:        c.TenantID,
		siteID:       c.SiteID,
		applID:       c.ApplianceID,
		ctrlBase:     c.CtrlAPIBase,
		natsURL:      c.NATSURL,
		serial:       c.Serial,
		legacyBridge: envOr("SCD_LEGACY_BRIDGE", "br-lan"),
		met: metrics.New("0.0.3-dev", prometheus.Labels{
			"tenant_id":    c.TenantID,
			"site_id":      c.SiteID,
			"appliance_id": c.ApplianceID,
		}),
	}
	// Hand the metric registry to subsystems that produce telemetry but
	// were constructed before s existed (nft wrapper).
	s.nft.SetMetrics(s.met)

	// Setup-wizard state: identity store (for runtime enrollment) + identity
	// key fingerprint (distinct from the mTLS cert fingerprint).
	s.idStore = idStore
	s.enrolled = ident != nil
	if ident != nil {
		s.idPriv = ident.PrivateKey()
		if raw, err := base64.RawStdEncoding.DecodeString(ident.PublicKeyB64); err == nil && len(raw) == ed25519.PublicKeySize {
			s.identityKeyFpr = applianceauth.KeyID(ed25519.PublicKey(raw))
		}
	}

	// Assignment agent — the ONLY channel by which this appliance adopts or changes
	// its tenant/site. Started EARLY and deliberately independent of the mTLS
	// client certificate: it authenticates with the identity-signing JWT over the
	// HTTPS ingress, so a freshly-enrolled appliance can be assigned before (or
	// without) a certificate being issued. It verifies the vendor signature + its
	// own binding + a monotonic version, persists atomically, repoints local
	// guest-network ownership, and re-execs scd so every subsystem adopts it.
	if ident != nil && c.CtrlAPIBase != "" {
		s.startAssignmentAgent(rootCtx, c.CtrlAPIBase)
	}

	// Edge-first refactor: signed-license manager. Evaluates the on-disk
	// envelope offline; bridges limits into tenant_effective_limits; gates
	// auth methods per entitlement. Without vendor key material it runs in
	// permissive unlicensed mode (pilot pre-cutover) unless
	// SCD_LICENSE_REQUIRED=true.
	s.lic = licstate.New(pool, c.TenantID, c.LicenseDir, c.VendorPub, c.LicenseRequired)
	s.lic.Load(rootCtx)
	if ident != nil && c.CtrlAPIBase != "" {
		priv := ident.PrivateKey()
		applID := ident.ApplianceID
		s.licFetch = func(ctx context.Context) error {
			return s.lic.FetchFromCloud(ctx, c.CtrlAPIBase, applID, priv)
		}
		s.lic.StartLoops(rootCtx, c.CtrlAPIBase, applID, priv, 6*time.Hour)
	} else {
		s.lic.StartLoops(rootCtx, "", "", nil, 0)
	}

	// Phase 8 — resolve real notification providers from the DB. Falls
	// back to the existing Stubs when no row exists for a channel. The
	// chosen impl is then wrapped with metric instrumentation so every
	// Send() emits scd_notification_send_* and updates the DB health
	// columns (last_success_at / last_error_at) for the admin UI.
	{
		nctx, ncancel := context.WithTimeout(rootCtx, 10*time.Second)
		nloaded, err := notifyloader.Load(nctx, pool, c.TenantID, s.mail, s.sms)
		ncancel()
		if err != nil {
			slog.Warn("notifyloader: load failed; using stubs", "err", err)
		}
		s.mail = notifyloader.WrapMailer(nloaded.Mailer, nloaded.MailerKind, s.met, pool, c.TenantID)
		s.sms = notifyloader.WrapSender(nloaded.Sender, nloaded.SenderKind, s.met, pool, c.TenantID)
		slog.Info("notification providers loaded", "email", nloaded.MailerKind, "sms", nloaded.SenderKind)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(10 * time.Second))
	r.Get("/v1/health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	r.Method("GET", "/metrics", s.met.Handler())
	r.Post("/v1/sessions/authorize", s.authorize)
	r.Post("/v1/sessions/authorize-otp", s.authorizeOTP)
	r.Post("/v1/sessions/revoke", s.revoke)
	r.Get("/v1/sessions/status", s.status)
	r.Post("/v1/auth/otp/issue", s.otpIssue)
	r.Post("/v1/auth/social/start", s.socialStart)
	r.Post("/v1/sessions/authorize-social", s.authorizeSocial)
	r.Post("/v1/auth/pms/verify", s.pmsVerify)
	r.Post("/v1/admin/pms/{name}/test", s.pmsAdminTest)
	r.Get("/v1/admin/pms/{name}/cache", s.pmsAdminCache)
	r.Get("/v1/admin/pms/{name}/health", s.pmsAdminHealth)
	r.Get("/v1/tenant/auth-methods", s.tenantAuthMethods)
	// Edge-first refactor: license + local-admin plumbing for edged.
	r.Get("/v1/license/status", s.licenseStatus)
	r.Post("/v1/license/install", s.licenseInstall)
	r.Post("/v1/license/refresh", s.licenseRefresh)
	r.Post("/v1/admin/pms/reload", s.pmsAdminReload)
	r.Post("/v1/admin/walled-garden/reload", s.gardenReload)
	r.Get("/v1/admin/outbox/stats", s.outboxStats)
	// Hotel Admin TLS cert lifecycle: scd runs as root and drives the privileged
	// manager here (edged is sandboxed with NoNewPrivileges and cannot). edged
	// already enforced Hotel-IT permission + step-up before proxying to us.
	r.Post("/v1/hotel-admin-cert/check", s.hotelAdminCertCheck)
	r.Post("/v1/hotel-admin-cert/rotate", s.hotelAdminCertRotate)
	r.Post("/v1/hotel-admin-cert/renew", s.hotelAdminCertRenew)
	r.Get("/v1/cloud/info", s.cloudInfo)
	r.Get("/v1/setup/status", s.setupStatus)
	r.Post("/v1/setup/enroll", s.setupEnroll)
	r.Post("/v1/setup/offline-import", s.setupOfflineImport)

	_ = os.MkdirAll("/run/stayconnect", 0o755)
	_ = os.Remove(c.SocketPath)
	ln, err := net.Listen("unix", c.SocketPath)
	if err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
	_ = os.Chmod(c.SocketPath, 0o660)
	if g, err := user.LookupGroup("stayconnect"); err == nil {
		if gid, err := strconv.Atoi(g.Gid); err == nil {
			if err := os.Chown(c.SocketPath, 0, gid); err != nil {
				slog.Warn("chown socket", "err", err)
			}
		}
	}

	srv := &http.Server{Handler: r, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		slog.Info("scd listening", "socket", c.SocketPath)
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("serve", "err", err)
		}
	}()

	// Phase 13 — optional TCP listener for Prometheus scraping. Unix
	// sockets can't be scraped by a Prometheus running elsewhere, so when
	// SCD_METRICS_ADDR is set we stand up a second server that ONLY
	// serves /metrics. Bind to localhost or a management interface, never
	// the guest LAN.
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
	// Phase 6.4 — session reaper. Closes expired/idle rows that acctd
	// can't see (no traffic = no accounting tick).
	go s.startReaperLoop(rootCtx)
	// Edge-first refactor: walled-garden rules from the (site) DB are now
	// actually enforced — reconciled into the nft walled_garden_ip set.
	go s.gardenReconcileLoop(rootCtx)
	// Phase 5.7.B — periodic safety-net reload. Live config events arrive
	// over NATS (5.3) and apply within ~ms, but a NATS reconnect storm or
	// missed delivery can leave us stale. A 10-minute background sweep
	// guarantees eventual consistency from the DB.
	go s.pmsReloadSafetyLoop(rootCtx)

	// Phase 5.2 — NATS RPC surface. When SCD_NATS_URL is set, subscribe to
	// scd.{applianceID}.> so the control plane can drive admin calls without
	// needing to share a filesystem with scd.
	var natsConn *nats.Conn
	// Decide the NATS transport: mTLS (:4223, client cert, no user/pass) when
	// SCD_NATS_MTLS_URL is set AND a client cert is available; otherwise the
	// legacy user/pass URL (rollback path).
	natsURL := c.NATSURL
	var natsTLS *tls.Config
	if c.NATSMTLSURL != "" && ident != nil && c.MTLSBase != "" {
		cm := appliancecert.New(c.CertDir, c.CtrlAPIBase, c.MTLSBase, ident.ApplianceID, ident.PrivateKey())
		// Bounded: first issuance may await Platform approval and Ensure would
		// otherwise block startup for up to 10 minutes on a freshly-enrolled
		// appliance. Stay on the legacy transport if the cert isn't ready yet; the
		// async certificate manager below obtains it and mTLS comes up on restart.
		ectx, ecancel := context.WithTimeout(rootCtx, 5*time.Second)
		err := cm.Ensure(ectx)
		ecancel()
		if err != nil {
			slog.Warn("nats mTLS: cert not ready, staying on legacy NATS", "err", err)
		} else if tc, err := cm.NATSTLSConfig(); err != nil {
			slog.Warn("nats mTLS: tls config failed, staying on legacy NATS", "err", err)
		} else {
			natsURL, natsTLS = c.NATSMTLSURL, tc
			s.certMgr = cm
			slog.Info("nats transport: mTLS", "url", natsURL)
		}
	}
	if natsURL != "" && c.ApplianceID != "" {
		nc, err := startNATSDispatcher(rootCtx, s, natsURL, c.ApplianceID, natsTLS)
		if err != nil {
			slog.Error("nats dispatcher: failed to start", "err", err)
			// non-fatal — scd keeps serving over the unix socket for portald.
		} else {
			natsConn = nc
			s.natsConn = nc
			// Upgrade the nft wrapper so its Allow/Deny publish to peers,
			// and subscribe to peer ops. Must happen AFTER the dispatcher
			// connects so we share the same *nats.Conn.
			s.nft = newNFTSync(s.nft.client, nc, c.ApplianceID, c.SiteID)
			s.nft.SetMetrics(s.met) // re-attach (the new wrapper starts metric-blind)
			if err := startNFTSyncSubscriber(rootCtx, s, nc, c.SiteID); err != nil {
				slog.Warn("nft sync subscriber: failed", "err", err)
			}
			// Signed command channel (Phase 8): subscribe to this appliance's
			// command subject over the mTLS transport.
			if err := s.startCommandHandler(rootCtx, nc, c.ApplianceID, envOr("SCD_COMMAND_PUB", "/etc/stayconnect/command-signing.pub")); err != nil {
				slog.Warn("command handler: failed", "err", err)
			}
			// Signed software-update agent (Phase 9).
			if err := s.startUpdateAgent(rootCtx, nc, c.ApplianceID, envOr("SCD_UPDATE_PUB", "/etc/stayconnect/update-signing.pub")); err != nil {
				slog.Warn("update agent: failed", "err", err)
			}
			// Boot reconcile: rebuild our local set from DB so we don't
			// start empty (important for a backup promoted to active or
			// a plain crash-restart).
			rctx, cancel := context.WithTimeout(rootCtx, 10*time.Second)
			applied, err := s.reconcileNFTFromDB(rctx, c.SiteID)
			cancel()
			if err != nil {
				slog.Warn("nft reconcile failed", "err", err)
			} else if applied > 0 {
				slog.Info("nft reconciled from DB", "entries", applied)
			}
		}
	}

	// Edge-first refactor: durable telemetry outbox. Enqueue works with or
	// without NATS (rows wait locally through outages); the drainer only
	// publishes when connected. Aggregated summaries only — no guest PII.
	scdStarted := time.Now()
	if c.ApplianceID != "" {
		s.obx = &outbox.Outbox{DB: pool, NC: natsConn, ApplianceID: c.ApplianceID}
		s.obx.Start(rootCtx)
		go s.telemetryLoop(rootCtx, scdStarted)
		s.enqueueLicenseAck(rootCtx)
	}

	// One-shot signed hello against ctrlapi on boot. Only runs for enrolled
	// appliances (ident != nil) and when a control-plane base URL is set.
	// This is the 5.1 smoke-test; 5.2 replaces it with a real RPC loop.
	if ident != nil && c.CtrlAPIBase != "" {
		go func() {
			hctx, cancel := context.WithTimeout(rootCtx, 10*time.Second)
			defer cancel()
			tok, err := applianceauth.SignRequest(ident.PrivateKey(), ident.ApplianceID, "GET", "/v1/appliance/hello", nil)
			if err != nil {
				slog.Warn("hello: sign failed", "err", err)
				return
			}
			req, _ := http.NewRequestWithContext(hctx, "GET", c.CtrlAPIBase+"/v1/appliance/hello", nil)
			req.Header.Set("Authorization", "Bearer "+tok)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				slog.Warn("hello: call failed", "err", err)
				return
			}
			defer resp.Body.Close()
			if resp.StatusCode == 200 {
				slog.Info("hello: ctrlapi signed-auth ok")
			} else {
				slog.Warn("hello: unexpected status", "status", resp.StatusCode)
			}
		}()
	}

	// Phase B — appliance-side mTLS. Ensure a client certificate exists
	// (CSR→issue→fetch→store), verify the mTLS transport, then rotate before
	// expiry. Runs alongside the signed-JWT layer (defence in depth); a failure
	// here never affects local guest operation.
	if ident != nil && c.CtrlAPIBase != "" && c.MTLSBase != "" {
		certMgr := s.certMgr // reuse the one created for NATS mTLS if present
		if certMgr == nil {
			certMgr = appliancecert.New(c.CertDir, c.CtrlAPIBase, c.MTLSBase, ident.ApplianceID, ident.PrivateKey())
			s.certMgr = certMgr
		}
		go func() {
			if err := certMgr.Ensure(rootCtx); err != nil {
				slog.Warn("appliancecert: ensure failed", "err", err)
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
			t := time.NewTicker(6 * time.Hour)
			defer t.Stop()
			for {
				select {
				case <-rootCtx.Done():
					return
				case <-t.C:
					if err := certMgr.MaybeRotate(rootCtx, 14*24*time.Hour); err != nil {
						slog.Warn("appliancecert: rotation failed", "err", err)
					}
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
	if natsConn != nil {
		_ = natsConn.Drain()
	}
}
