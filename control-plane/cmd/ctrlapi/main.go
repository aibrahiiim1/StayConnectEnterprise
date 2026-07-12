package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/config"
	"github.com/stayconnect/enterprise/control-plane/internal/configpush"
	"github.com/stayconnect/enterprise/control-plane/internal/db"
	"github.com/stayconnect/enterprise/control-plane/internal/fleet"
	"github.com/stayconnect/enterprise/control-plane/internal/heartbeat"
	apihttp "github.com/stayconnect/enterprise/control-plane/internal/http"
	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/metrics"
	"github.com/stayconnect/enterprise/control-plane/internal/oidc"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
	"github.com/stayconnect/enterprise/control-plane/internal/transport"
	"github.com/stayconnect/enterprise/license"
)

var version = "0.0.2-dev"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "seed-admin":
			if err := runSeedAdmin(os.Args[2:]); err != nil {
				slog.Error("seed-admin failed", "err", err)
				os.Exit(1)
			}
			return
		case "gen-vendor-key":
			if err := runGenVendorKey(os.Args[2:]); err != nil {
				slog.Error("gen-vendor-key failed", "err", err)
				os.Exit(1)
			}
			return
		case "gen-assignment-key":
			if err := runGenAssignmentKey(os.Args[2:]); err != nil {
				slog.Error("gen-assignment-key failed", "err", err)
				os.Exit(1)
			}
			return
		case "gen-registry-key":
			if err := runGenRegistryKey(os.Args[2:]); err != nil {
				slog.Error("gen-registry-key failed", "err", err)
				os.Exit(1)
			}
			return
		case "serve", "":
			// fallthrough to default
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
			fmt.Fprintf(os.Stderr, "usage: ctrlapi [serve | seed-admin --email <e> --password <p> | gen-vendor-key --out <key> --pub-out <pub>]\n")
			os.Exit(2)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Open(rootCtx, cfg.DBURL)
	if err != nil {
		slog.Error("db open failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	slog.Info("db connected")

	// DEPRECATED-route compatibility pool: after a site's cutover the legacy
	// /v1 guest-domain routes must operate on the SITE database. Unset =
	// single-DB mode (pre-cutover behavior). See docs/API_DEPRECATIONS.md.
	var guestPool *pgxpool.Pool
	if u := os.Getenv("CTRLAPI_GUEST_COMPAT_DB_URL"); u != "" {
		guestPool, err = db.Open(rootCtx, u)
		if err != nil {
			slog.Error("guest-compat db open failed", "err", err)
			os.Exit(1)
		}
		defer guestPool.Close()
		slog.Info("guest-domain compatibility pool connected (deprecated /v1 routes → site DB)")
	}

	rOpt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		slog.Error("redis url parse failed", "err", err)
		os.Exit(1)
	}
	rdb := redis.NewClient(rOpt)
	if err := rdb.Ping(rootCtx).Err(); err != nil {
		slog.Error("redis ping failed", "err", err)
		os.Exit(1)
	}
	defer rdb.Close()
	slog.Info("redis connected")

	// Transport selection:
	//   CTRLAPI_NATS_URL set → NATSTransport (phase 5.2 production path)
	//   otherwise            → LocalUnixTransport (dev/co-located fallback)
	var (
		tr       transport.ApplianceTransport
		natsConn *nats.Conn
	)
	// Prefer the mTLS NATS endpoint (:4223) when configured — ctrlapi connects
	// as a central SERVICE presenting a cert with URI SAN stayconnect://service/…
	// (broad consumer/control perms via Auth Callout). Falls back to the legacy
	// user/pass URL otherwise (rollback path).
	natsMTLSURL := os.Getenv("CTRLAPI_NATS_MTLS_URL")
	if natsMTLSURL != "" {
		opts := []nats.Option{nats.Name("ctrlapi"), nats.MaxReconnects(-1), nats.ReconnectWait(2 * time.Second)}
		if cc := os.Getenv("CTRLAPI_NATS_CLIENT_CERT"); cc != "" {
			opts = append(opts, nats.ClientCert(cc, os.Getenv("CTRLAPI_NATS_CLIENT_KEY")))
		}
		if ca := os.Getenv("CTRLAPI_NATS_CA"); ca != "" {
			opts = append(opts, nats.RootCAs(ca))
		}
		nc, err := nats.Connect(natsMTLSURL, opts...)
		if err != nil {
			// Non-fatal: fall back to the legacy NATS URL (or unix) so a NATS
			// mTLS problem never takes down the Cloud API. Cutover-safe.
			slog.Error("nats mTLS connect failed — falling back to legacy transport", "err", err)
			natsMTLSURL = ""
		} else {
			natsConn = nc
			tr = transport.NewNATS(nc, 10*time.Second)
			slog.Info("transport=nats-mtls", "url", natsMTLSURL)
		}
	}
	if natsMTLSURL == "" {
		if u := os.Getenv("CTRLAPI_NATS_URL"); u != "" {
			nc, err := nats.Connect(u,
				nats.Name("ctrlapi"),
				nats.MaxReconnects(-1),
				nats.ReconnectWait(2*time.Second),
			)
			if err != nil {
				slog.Error("nats connect failed", "err", err)
				os.Exit(1)
			}
			natsConn = nc
			tr = transport.NewNATS(nc, 10*time.Second)
			slog.Info("transport=nats", "url", u)
		} else {
			scdSock := os.Getenv("CTRLAPI_SCD_SOCKET")
			if scdSock == "" {
				scdSock = "/run/stayconnect/scd.sock"
			}
			tr = transport.NewLocalUnix(scdSock)
			slog.Info("transport=unix", "socket", scdSock)
		}
	}
	defer func() {
		if natsConn != nil {
			_ = natsConn.Drain()
		}
	}()

	// Phase 5.4 — heartbeat consumer + staleness sweeper. Only runs when
	// NATS is the transport; dev/unix-socket deployments have a single
	// appliance and don't need liveness tracking.
	met := metrics.New(version)
	if natsConn != nil {
		if err := heartbeat.StartConsumer(rootCtx, natsConn, pool, met); err != nil {
			slog.Warn("heartbeat consumer failed to start", "err", err)
		}
	}

	// Phase 5.7.C — bootstrap token expiry sweeper. Hourly DELETE of
	// unconsumed tokens past expires_at; consumed rows stick around for
	// audit. Runs unconditionally (DB-only, no NATS dep).
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		// Fire one immediately so a long-running ctrlapi cleans up at boot.
		runTokenSweep(rootCtx, pool, met)
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-t.C:
				runTokenSweep(rootCtx, pool, met)
			}
		}
	}()

	// OIDC registry for operator SSO. Phase 4.4 ships only the Stub.
	oidcReg := oidc.NewRegistry()
	oidcReg.Register(&oidc.Stub{
		ProviderName:  "stub",
		AuthorizeBase: "/api/oauth/stub/authorize-sso",
	})

	// Vendor license signing key (edge-first refactor). Optional: without it
	// ctrlapi serves everything except license issuing/fetching, so existing
	// deployments keep working until the key is provisioned.
	var licSvc *licensing.Service
	vendorKeyPath := os.Getenv("CTRLAPI_VENDOR_KEY")
	if vendorKeyPath == "" {
		vendorKeyPath = "/etc/stayconnect/vendor-license.key"
	}
	if signer, err := license.LoadSigner(vendorKeyPath); err == nil {
		licSvc = &licensing.Service{DB: pool, Signer: signer}
		slog.Info("vendor license signer loaded", "key_id", signer.KeyID(), "path", vendorKeyPath)
		// Reconcile licensed/grace/license_expired from signed-license validity.
		go func() {
			t := time.NewTicker(60 * time.Second)
			defer t.Stop()
			for {
				if n, err := licSvc.ReconcileStates(rootCtx); err != nil {
					slog.Warn("license state reconcile failed", "err", err)
				} else if n > 0 {
					slog.Info("license state reconcile", "changed", n)
				}
				select {
				case <-rootCtx.Done():
					return
				case <-t.C:
				}
			}
		}()
	} else {
		slog.Warn("vendor license signer unavailable — licensing endpoints disabled",
			"path", vendorKeyPath, "err", err)
	}

	// DEDICATED assignment-signing key. Deliberately a separate key from the
	// license / command / update / CA / auth-callout keys: an assignment must never
	// be signable by any of those, and the appliance enforces this by trusting only
	// the keys in its local assignment trust registry. Absent/invalid → signed
	// assignments disabled (assign still updates Central's DB, but no signed doc).
	assignKeyPath := envOrDefault("CTRLAPI_ASSIGN_KEY", "/etc/stayconnect/assignment-signing.key")
	var assignKey ed25519.PrivateKey
	if raw, err := os.ReadFile(assignKeyPath); err == nil && len(raw) == ed25519.PrivateKeySize {
		assignKey = ed25519.PrivateKey(raw)
		assignPub := assignKey.Public().(ed25519.PublicKey)
		if vraw, verr := os.ReadFile(vendorKeyPath); verr == nil && len(vraw) == ed25519.PrivateKeySize &&
			string(vraw) == string(raw) {
			slog.Error("assignment signing key MUST NOT be the vendor license key — refusing to sign assignments",
				"path", assignKeyPath)
			assignKey = nil
		} else {
			slog.Info("dedicated assignment signing key loaded",
				"key_id", assignment.KeyID(assignPub), "path", assignKeyPath)
			if err := api.RegisterActiveKey(rootCtx, &api.Base{DB: pool}, assignPub, "ctrlapi boot"); err != nil {
				slog.Warn("assignment signing key registration failed", "err", err)
			}
		}
	} else {
		slog.Warn("dedicated assignment signing key unavailable — signed assignments disabled",
			"path", assignKeyPath, "hint", "run: ctrlapi gen-assignment-key --out "+assignKeyPath)
	}

	// Registry ROOT-OF-TRUST key: signs the versioned trust registry the appliance
	// verifies. Distinct from the assignment keys it authorises; its PUBLIC half is
	// a manufacture-time trust anchor on the appliance.
	regRootPath := envOrDefault("CTRLAPI_REGISTRY_ROOT_KEY", "/etc/stayconnect/assignment-registry-root.key")
	var regRoot ed25519.PrivateKey
	if raw, err := os.ReadFile(regRootPath); err == nil && len(raw) == ed25519.PrivateKeySize {
		regRoot = ed25519.PrivateKey(raw)
		slog.Info("assignment registry root key loaded",
			"key_id", assignment.KeyID(regRoot.Public().(ed25519.PublicKey)), "path", regRootPath)
		// Publish (or refresh) the signed registry from the current key table.
		regBase := &api.RegistryBase{Base: &api.Base{DB: pool}, RootKey: regRoot}
		if sr, err := regBase.Rebuild(rootCtx, "ctrlapi boot"); err != nil {
			slog.Warn("signed registry rebuild failed", "err", err)
		} else if sr != nil {
			slog.Info("signed trust registry published", "registry_version", sr.RegistryVersion, "keys", len(sr.Keys))
		}
	} else {
		slog.Warn("assignment registry root key unavailable — signed registry disabled",
			"path", regRootPath, "hint", "run: ctrlapi gen-registry-key --out "+regRootPath)
	}

	// Terminal-delivery timeout reconciler: a Phase-1 delivery that is never
	// acknowledged within policy becomes terminal_delivery_failed + a security
	// alert — credentials are NOT revoked and the box is NOT reported decommissioned.
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		tb := &api.Base{DB: pool}
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-t.C:
				if n, err := api.ReconcileTerminalTimeouts(rootCtx, tb); err != nil {
					slog.Warn("terminal-delivery timeout reconcile failed", "err", err)
				} else if n > 0 {
					slog.Warn("terminal delivery failed (no ack within policy)", "count", n)
				}
			}
		}
	}()

	// Appliance certificate authority (Phase 3 PKI/mTLS). The CA private key
	// lives only in a file on Central; the CA cert (public) is versioned in DB
	// and distributed to appliances as a trust anchor. Absent key → PKI routes
	// and the mTLS listener stay disabled (existing deployments keep working).
	var appCA *pki.CA
	// Two-tier CA: offline Root signs an online Intermediate; runtime issuance
	// uses ONLY the intermediate key. Root key path is used once (to sign the
	// intermediate) then moved offline.
	rootKeyPath := envOrDefault("CTRLAPI_ROOT_CA_KEY", "/etc/stayconnect/pki/root-ca.key")
	intKeyPath := envOrDefault("CTRLAPI_INTERMEDIATE_CA_KEY", "/etc/stayconnect/pki/intermediate-ca.key")
	sharedReplay := applianceauth.NewReplayCache(2*time.Minute, 8192)
	if ca, err := pki.LoadCAChain(rootKeyPath, intKeyPath, 1); err == nil {
		appCA = ca
		// Move the ROOT private key offline: the runtime never needs it again
		// once the intermediate is signed. We relocate it to a restricted
		// offline directory (operationally it should be moved to cold storage).
		relocateRootOffline(rootKeyPath)
		// Persist/refresh CA version rows (root + intermediate) for trust-bundle
		// versioning.
		_, _ = pool.Exec(rootCtx, `
            INSERT INTO appliance_ca_versions (version, cert_pem, subject, key_fingerprint, active)
            VALUES (0, $1, 'StayConnect Root CA', 'offline', true)
            ON CONFLICT (version) DO UPDATE SET cert_pem=EXCLUDED.cert_pem`, string(ca.RootPEM()))
		_, _ = pool.Exec(rootCtx, `
            INSERT INTO appliance_ca_versions (version, cert_pem, subject, key_fingerprint, active)
            VALUES ($1,$2,$3,$4,true)
            ON CONFLICT (version) DO UPDATE SET cert_pem=EXCLUDED.cert_pem, subject=EXCLUDED.subject, key_fingerprint=EXCLUDED.key_fingerprint`,
			ca.Version, string(ca.IntermediatePEM()), ca.Subject(), ca.KeyFingerprint())
		slog.Info("appliance CA chain loaded (root offline → online intermediate)", "intermediate_version", ca.Version, "subject", ca.Subject())
		// Dedicated per-purpose signing keys (never reuse one key for all).
		ensureSigningKey(envOrDefault("CTRLAPI_COMMAND_KEY", "/etc/stayconnect/command-signing.key"), "command")
		ensureSigningKey(envOrDefault("CTRLAPI_UPDATE_KEY", "/etc/stayconnect/update-signing.key"), "update")
		// Dedicated mutual-TLS appliance listener (client-cert required).
		mtlsAddr := os.Getenv("CTRLAPI_MTLS_ADDR")
		if mtlsAddr == "" {
			mtlsAddr = ":9443"
		}
		applianceMTLSAPI := api.ApplianceMTLSRouter(pool, rdb, sharedReplay, licSvc, ca, assignKey, regRoot)
		go func() {
			if err := apihttp.StartMTLS(rootCtx, pool, ca, mtlsAddr, applianceMTLSAPI); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("mTLS listener error", "err", err)
			}
		}()
	} else {
		slog.Warn("appliance CA unavailable — PKI/mTLS disabled", "path", intKeyPath, "err", err)
	}

	// Fleet telemetry ingest (aggregated, non-PII summaries from appliances).
	if natsConn != nil {
		fc := &fleet.Consumer{DB: pool}
		if err := fc.Start(rootCtx, natsConn); err != nil {
			slog.Warn("fleet telemetry consumer failed to start", "err", err)
		} else {
			slog.Info("fleet telemetry consumer started")
		}
	}

	handler := apihttp.NewRouter(apihttp.Deps{
		DB:           pool,
		GuestDB:      guestPool,
		Redis:        rdb,
		Transport:    tr,
		ConfigPush:   configpush.NewWithMetrics(natsConn, met),
		Metrics:      met,
		OIDC:         oidcReg,
		Licensing:    licSvc,
		AssignKey:          assignKey,
		AssignRegistryRoot: regRoot,
		CA:           appCA,
		ReplayCache:  sharedReplay,
		NATSConn:     natsConn,
		CommandKey:   envOrDefault("CTRLAPI_COMMAND_KEY", "/etc/stayconnect/command-signing.key"),
		Version:      version,
		AllowOrigins: cfg.AllowOrigins,
		CookieSecure: cfg.CookieSecure,
	})
	if natsConn != nil {
		if err := api.StartResultsConsumer(rootCtx, pool, natsConn); err != nil {
			slog.Warn("command results consumer failed to start", "err", err)
		}
		if err := api.StartUpdateStatusConsumer(rootCtx, pool, natsConn); err != nil {
			slog.Warn("update status consumer failed to start", "err", err)
		}
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("ctrlapi listening", "addr", cfg.Addr, "env", cfg.Env, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-rootCtx.Done():
		slog.Info("shutdown signal received")
	case err := <-errCh:
		slog.Error("server error", "err", err)
	}

	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("server shutdown error", "err", err)
	}
	slog.Info("bye")
}

// runTokenSweep deletes unconsumed bootstrap tokens past their expiry. Logs
// the count when non-zero so operators can see the sweeper at work; also
// bumps the ctrlapi_bootstrap_tokens_swept_total counter when met != nil.
func runTokenSweep(ctx context.Context, pool *pgxpool.Pool, met *metrics.Registry) {
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tag, err := pool.Exec(sctx, `
        DELETE FROM appliance_bootstrap_tokens
         WHERE consumed_at IS NULL AND expires_at < now()
    `)
	if err != nil {
		slog.Warn("token sweep failed", "err", err)
		return
	}
	if n := tag.RowsAffected(); n > 0 {
		slog.Info("token sweep", "deleted", n)
		if met != nil {
			met.TokensSwept.Add(float64(n))
		}
	}
}

func envOrDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// relocateRootOffline moves the root CA private key out of the runtime PKI
// directory into a restricted offline holding area once the intermediate has
// been signed. The runtime never opens it again. In production this should be
// physically moved to cold storage; here we at least remove it from the
// intermediate signing directory and tighten permissions.
func relocateRootOffline(rootKeyPath string) {
	offlineDir := envOrDefault("CTRLAPI_ROOT_CA_OFFLINE_DIR", "/etc/stayconnect/pki-offline")
	if _, err := os.Stat(rootKeyPath); err != nil {
		return // already moved / absent
	}
	if err := os.MkdirAll(offlineDir, 0o700); err != nil {
		slog.Warn("root CA offline relocate: mkdir failed", "err", err)
		return
	}
	dst := offlineDir + "/root-ca.key"
	if err := os.Rename(rootKeyPath, dst); err != nil {
		slog.Warn("root CA offline relocate failed — move it offline manually", "from", rootKeyPath, "err", err)
		return
	}
	_ = os.Chmod(dst, 0o600)
	slog.Warn("root CA private key relocated offline; move to cold storage", "offline_path", dst)
}

// ensureSigningKey generates a dedicated Ed25519 signing key at path if absent
// (0600, atomic-ish). Used to keep command/update signing keys separate from
// the license key and the CA.
func ensureSigningKey(path, purpose string) {
	if _, err := os.Stat(path); err == nil {
		return
	}
	if _, err := license.GenerateVendorKey(path); err != nil {
		slog.Warn("signing key generate failed", "purpose", purpose, "path", path, "err", err)
		return
	}
	slog.Info("dedicated signing key created", "purpose", purpose, "path", path)
}

// runGenVendorKey creates the vendor license-signing keypair. The private
// key stays on the cloud host (0600); the public key file is what gets
// installed on appliances (/etc/stayconnect/vendor-license.pub). Refuses to
// overwrite an existing key — rotating means issuing a new key file and
// re-signing licenses, never silently replacing the trust root.
func runGenVendorKey(args []string) error {
	fs := flag.NewFlagSet("gen-vendor-key", flag.ExitOnError)
	out := fs.String("out", "/etc/stayconnect/vendor-license.key", "private key output path (cloud only)")
	pubOut := fs.String("pub-out", "/etc/stayconnect/vendor-license.pub", "public key output path (distribute to appliances)")
	_ = fs.Parse(args)

	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("refusing to overwrite existing vendor key %s", *out)
	}
	pub, err := license.GenerateVendorKey(*out)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*pubOut, pub, 0o644); err != nil {
		return err
	}
	slog.Info("vendor license keypair generated",
		"key_id", license.KeyIDFor(pub), "private", *out, "public", *pubOut)
	return nil
}

// runSeedAdmin upserts a platform_admin operator with an argon2id-hashed
// password. Safe to run repeatedly; updates password on each invocation.
func runSeedAdmin(args []string) error {
	fs := flag.NewFlagSet("seed-admin", flag.ExitOnError)
	email := fs.String("email", "", "admin email")
	password := fs.String("password", "", "admin password (min 10 chars)")
	displayName := fs.String("name", "Platform Admin", "display name")
	_ = fs.Parse(args)

	if *email == "" || len(*password) < 10 {
		return fmt.Errorf("--email and --password (min 10 chars) required")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx, `
        INSERT INTO operators (email, display_name, password_hash, status)
        VALUES ($1, $2, $3, 'active')
        ON CONFLICT (email) DO UPDATE
          SET password_hash = EXCLUDED.password_hash,
              display_name  = EXCLUDED.display_name,
              status        = 'active',
              updated_at    = now()
        RETURNING id
    `, *email, *displayName, hash).Scan(&id)
	if err != nil {
		return fmt.Errorf("upsert operator: %w", err)
	}

	_, err = tx.Exec(ctx, `
        INSERT INTO operator_roles (operator_id, tenant_id, role)
        SELECT $1, NULL, 'platform_admin'
         WHERE NOT EXISTS (
             SELECT 1 FROM operator_roles
              WHERE operator_id = $1 AND tenant_id IS NULL AND role = 'platform_admin'
         )
    `, id)
	if err != nil {
		return fmt.Errorf("insert role: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	slog.Info("seeded platform admin", "email", *email, "id", id)
	return nil
}
