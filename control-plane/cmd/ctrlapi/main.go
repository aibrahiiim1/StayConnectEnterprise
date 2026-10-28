package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/api"
	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/config"
	"github.com/stayconnect/enterprise/control-plane/internal/db"
	apihttp "github.com/stayconnect/enterprise/control-plane/internal/http"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/metrics"
	"github.com/stayconnect/enterprise/control-plane/internal/pki"
	"github.com/stayconnect/enterprise/control-plane/internal/portaltls"
	"github.com/stayconnect/enterprise/license"
)

var version = "0.0.2-dev"

// logLevel is set from CTRLAPI_LOG_LEVEL once the configuration is loaded.
var logLevel = new(slog.LevelVar)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel})))

	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "seed-admin":
			err = runSeedAdmin(os.Args[2:])
		case "gen-vendor-key":
			err = runGenVendorKey(os.Args[2:])
		case "gen-assignment-key":
			err = runGenAssignmentKey(os.Args[2:])
		case "gen-registry-key":
			err = runGenRegistryKey(os.Args[2:])
		case "assignment-key":
			err = runAssignmentKey(os.Args[2:])
		case "gen-mfa-key":
			err = runGenMFAKey(os.Args[2:])
		case "reset-mfa":
			err = runResetMFA(os.Args[2:])
		case "unlock-signin":
			err = runUnlockSignin(os.Args[2:])
		case "check-mfa-key":
			err = runCheckMFAKey(os.Args[2:])
		case "init-ca":
			err = runInitCA(os.Args[2:])
		case "serve", "":
			goto serve
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
			fmt.Fprintf(os.Stderr, "usage: ctrlapi [serve | init-ca | seed-admin | gen-vendor-key | gen-assignment-key | gen-registry-key | assignment-key verify-only|revoke | gen-mfa-key | check-mfa-key | reset-mfa | unlock-signin]\n")
			os.Exit(2)
		}
		if err != nil {
			slog.Error(os.Args[1]+" failed", "err", err)
			os.Exit(1)
		}
		return
	}
serve:
	serve()
}

func serve() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logLevel.Set(cfg.LogLevel)
	// Checked before anything listens: production refuses the operator API on every interface or an insecure session cookie.
	hardenErrs, hardenWarns := cfg.Validate()
	for _, w := range hardenWarns {
		slog.Warn("configuration is not hardened", "problem", w)
	}
	if len(hardenErrs) > 0 {
		for _, e := range hardenErrs {
			slog.Error("refusing to start: configuration is not hardened", "problem", e)
		}
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

	// Appliances reach Central over HTTPS on this API only; there is no message bus.
	met := metrics.New(version)

	// Vendor licence signing key. Without it ctrlapi serves everything except licence issuing/fetching.
	var licSvc *licensing.Service
	vendorKeyPath := envOrDefault("CTRLAPI_VENDOR_KEY", "/etc/stayconnect/vendor-license.key")
	if signer, err := license.LoadSigner(vendorKeyPath); err == nil {
		licSvc = &licensing.Service{DB: pool, Signer: signer}
		slog.Info("vendor license signer loaded", "key_id", signer.KeyID(), "path", vendorKeyPath)
	} else {
		slog.Warn("vendor license signer unavailable — licensing endpoints disabled",
			"path", vendorKeyPath, "err", err)
	}

	// Dedicated assignment-signing key, never the licence or CA key: appliances trust only keys in their assignment
	// trust registry. Absent: activation, moves and retirement refuse.
	assignKeyPath := envOrDefault("CTRLAPI_ASSIGN_KEY", "/etc/stayconnect/assignment-signing.key")
	var assignKey ed25519.PrivateKey
	if k, err := loadEd25519File(assignKeyPath); err == nil {
		assignKey = k
	} else {
		slog.Warn("dedicated assignment signing key unavailable — activation disabled",
			"path", assignKeyPath, "err", err, "hint", "run: ctrlapi gen-assignment-key --out "+assignKeyPath)
	}

	// Registry ROOT-OF-TRUST key: signs the versioned trust registry the appliance verifies.
	regRoot := loadRegistryRoot()

	// No two signing roles may share a key (audit Phase 1 M17): with a shared key the assignment signer and
	// the registry root are both refused, so nothing is signed under a key that has two meanings.
	var vendorRaw ed25519.PrivateKey
	if raw, err := os.ReadFile(vendorKeyPath); err == nil && len(raw) == ed25519.PrivateKeySize {
		vendorRaw = ed25519.PrivateKey(raw)
	}
	if err := distinctKeys(map[string]ed25519.PrivateKey{"vendor licence": vendorRaw, "assignment signing": assignKey,
		"registry root": regRoot}); err != nil {
		slog.Error("refusing to sign assignments or the trust registry", "err", err)
		assignKey, regRoot = nil, nil
	}
	if assignKey != nil {
		assignPub := assignKey.Public().(ed25519.PublicKey)
		slog.Info("dedicated assignment signing key loaded", "key_id", assignment.KeyID(assignPub), "path", assignKeyPath)
		if err := api.RegisterActiveKey(rootCtx, &api.Base{DB: pool}, assignPub, "ctrlapi boot"); err != nil {
			slog.Warn("assignment signing key registration failed", "err", err)
		}
	}
	if regRoot != nil {
		regBase := &api.RegistryBase{Base: &api.Base{DB: pool}, RootKey: regRoot}
		// Every start re-signs, so a key-state change whose re-signing failed is published now (audit Phase 1 M3).
		if sr, err := regBase.Rebuild(rootCtx, "ctrlapi boot"); err != nil {
			slog.Error("signed registry rebuild failed: appliances keep the previous registry", "err", err)
		} else if sr != nil {
			slog.Info("signed trust registry published", "registry_version", sr.RegistryVersion, "keys", len(sr.Keys))
		}
	}

	// Housekeeping: an unacknowledged retirement becomes terminal_delivery_failed plus an alert (credentials are NOT
	// revoked); hourly, old fetch logs are deleted, superseded certificates recorded revoked, and retention applied.
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		tb := &api.Base{DB: pool}
		var lastPrune time.Time
		for {
			select {
			case <-rootCtx.Done():
				return
			case <-t.C:
				housekeepingPass(rootCtx, tb, &lastPrune)
			}
		}
	}()

	// The worker always runs but does nothing until the ACME contact and DNS token are configured. The TEST-ONLY
	// overrides point the chain at Pebble + challtestsrv; every start says when one is on.
	ptlsOpts, ptlsOverrides := portaltls.OptionsFromEnv()
	var ptls *portaltls.Service
	if err := ptlsOpts.CheckEnv(cfg.Production()); err != nil {
		slog.Error("portal TLS disabled: invalid configuration", "err", err)
	} else {
		ptls = portaltls.New(pool, ptlsOpts)
		ptls.Overrides = ptlsOverrides
		if len(ptlsOverrides) > 0 {
			slog.Warn("portal TLS TEST-ONLY overrides are active; this Central is not issuing production portal certificates",
				"overrides", strings.Join(ptlsOverrides, " "))
		}
		go ptls.Run(rootCtx)
	}

	// Appliance certificate authority. Two-tier: an offline Root signs an online Intermediate; runtime
	// issuance uses ONLY the intermediate. serve never creates CA material (`ctrlapi init-ca` does, once): a
	// missing or inconsistent CA file, or a CA that differs from the one the database records, leaves the
	// PKI routes and the mTLS listener off (audit Phase 1 H1).
	var appCA *pki.CA
	sharedReplay := applianceauth.NewReplayCache(2*time.Minute, 8192)
	if ca, err := startPKI(rootCtx, pool, caPathsFromEnv()); err == nil {
		appCA = ca
		slog.Info("appliance CA chain loaded (root offline → online intermediate)", "intermediate_version", ca.Version, "subject", ca.Subject())
		mtlsAddr := envOrDefault("CTRLAPI_MTLS_ADDR", ":9443")
		applianceMTLSAPI := api.ApplianceMTLSRouter(pool, rdb, sharedReplay, licSvc, ca, assignKey, regRoot, ptls)
		go func() {
			defer recoverAndLog("mTLS listener")
			if err := apihttp.StartMTLS(rootCtx, pool, ca, mtlsAddr, applianceMTLSAPI); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("mTLS listener error", "err", err)
			}
		}()
	} else {
		slog.Error("appliance CA unavailable — PKI/mTLS disabled; restore the CA files (never re-create them on a Central that has appliances)", "err", err)
	}

	// Console two-step sign-in. Without the key no operator can sign in: the console refuses rather than accept a password alone.
	mfaKeyPath := envOrDefault("CTRLAPI_OPERATOR_MFA_KEY", defaultMFAKeyPath)
	var mfaSvc *auth.MFA
	if raw, err := auth.LoadMFAKeyFile(mfaKeyPath); err != nil {
		slog.Error("console two-step sign-in key missing -- every console sign-in will be refused",
			"path", mfaKeyPath, "err", err, "hint", "run: ctrlapi gen-mfa-key --out "+mfaKeyPath)
	} else if mfaSvc, err = auth.NewMFA(pool, raw); err != nil {
		slog.Error("console two-step sign-in key unusable -- every console sign-in will be refused", "path", mfaKeyPath, "err", err)
		mfaSvc = nil
	}

	handler := apihttp.NewRouter(apihttp.Deps{
		DB:            pool,
		Redis:         rdb,
		Metrics:       met,
		Licensing:     licSvc,
		AssignKey:     assignKey,
		CA:            appCA,
		ReplayCache:   sharedReplay,
		VendorKeyPath: vendorKeyPath,
		CABundlePath:  envOrDefault("CTRLAPI_CA_BUNDLE", "/etc/stayconnect/pki/ca-bundle.crt"),
		ApplianceBase: os.Getenv("CTRLAPI_APPLIANCE_BASE"),
		Version:       version,
		AllowOrigins:  cfg.AllowOrigins,
		CookieSecure:  cfg.CookieSecure,
		PortalTLS:     ptls,
		MFA:           mfaSvc,
	})

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       apihttp.ServerReadTimeout, // audit Phase 1 M10
		WriteTimeout:      apihttp.ServerWriteTimeout,
		IdleTimeout:       apihttp.ServerIdleTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("ctrlapi listening", "addr", cfg.Addr, "env", cfg.Env, "version", version, "log_level", cfg.LogLevel.String())
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

// housekeepingStepTimeout bounds each housekeeping statement, so one slow pass cannot hold up the others
// (perf audit D9-M1). Retention deletes whole periods and gets longer.
const (
	housekeepingStepTimeout = 60 * time.Second
	retentionStepTimeout    = 10 * time.Minute
)

// housekeepingPass runs one pass. A panic is logged and the loop carries on (audit Phase 1 L19).
func housekeepingPass(ctx context.Context, tb *api.Base, lastPrune *time.Time) {
	defer recoverAndLog("housekeeping")
	step := func(name string, fn func(context.Context)) {
		limit := housekeepingStepTimeout
		if name == "retention" {
			limit = retentionStepTimeout
		}
		sctx, cancel := context.WithTimeout(ctx, limit)
		defer cancel()
		defer recoverAndLog("housekeeping: " + name)
		fn(sctx)
	}
	step("terminal timeouts", func(ctx context.Context) {
		if n, err := api.ReconcileTerminalTimeouts(ctx, tb); err != nil {
			slog.Warn("terminal-delivery timeout reconcile failed", "err", err)
		} else if n > 0 {
			slog.Warn("retirement unconfirmed (no ack within policy)", "count", n)
		}
	})
	step("replacements", func(ctx context.Context) {
		if n, err := api.ReconcileReplacements(ctx, tb); err != nil {
			slog.Warn("replacement window reconcile failed", "err", err)
		} else if n > 0 {
			slog.Warn("replacement window elapsed (operator decision required)", "count", n)
		}
	})
	if time.Since(*lastPrune) < time.Hour {
		return
	}
	*lastPrune = time.Now()
	step("fetch log retention", func(ctx context.Context) {
		if n, err := api.PruneAssignmentFetchLog(ctx, tb); err != nil {
			slog.Warn("assignment fetch log retention failed", "err", err)
		} else if n > 0 {
			slog.Info("assignment fetch log retention", "deleted", n)
		}
	})
	step("superseded certificates", func(ctx context.Context) {
		if n, err := api.RevokeSupersededCertificates(ctx, tb); err != nil {
			slog.Warn("superseded certificate revocation failed", "err", err)
		} else if n > 0 {
			slog.Info("superseded appliance certificates revoked", "count", n)
		}
	})
	step("retention", func(ctx context.Context) {
		if done, err := api.ApplyRetention(ctx, tb); err != nil {
			slog.Warn("data retention failed", "err", err)
		} else if len(done) > 0 {
			slog.Info("data retention applied", "done", done)
		}
	})
}

// recoverAndLog is deferred by long-lived goroutines: a panic is logged with its stack instead of ending the
// process (audit Phase 1 L19).
func recoverAndLog(what string) {
	if p := recover(); p != nil {
		slog.Error(what+" panicked", "panic", p, "stack", string(debug.Stack()))
	}
}

func loadRegistryRoot() ed25519.PrivateKey {
	regRootPath := envOrDefault("CTRLAPI_REGISTRY_ROOT_KEY", "/etc/stayconnect/assignment-registry-root.key")
	k, err := loadEd25519File(regRootPath)
	if err != nil {
		slog.Warn("assignment registry root key unavailable — signed registry disabled",
			"path", regRootPath, "err", err, "hint", "run: ctrlapi gen-registry-key --out "+regRootPath)
		return nil
	}
	slog.Info("assignment registry root key loaded",
		"key_id", assignment.KeyID(k.Public().(ed25519.PublicKey)), "path", regRootPath)
	return k
}

func envOrDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// caPaths are the appliance CA files. The root key is only written by init-ca, straight to the offline directory.
type caPaths struct {
	rootCert, intCert, intKey, rootKeyOffline string
}

func caPathsFromEnv() caPaths {
	rootKeyPath := envOrDefault("CTRLAPI_ROOT_CA_KEY", "/etc/stayconnect/pki/root-ca.key")
	intKeyPath := envOrDefault("CTRLAPI_INTERMEDIATE_CA_KEY", "/etc/stayconnect/pki/intermediate-ca.key")
	offlineDir := envOrDefault("CTRLAPI_ROOT_CA_OFFLINE_DIR", "/etc/stayconnect/pki-offline")
	return caPaths{
		rootCert:       pki.CertSibling(rootKeyPath),
		intCert:        pki.CertSibling(intKeyPath),
		intKey:         intKeyPath,
		rootKeyOffline: filepath.Join(offlineDir, "root-ca.key"),
	}
}

// startPKI loads the CA (never creating it) and records it in appliance_ca_versions without overwriting: a
// stored row that differs from the loaded certificate means the files belong to another Central, and the PKI
// stays off with the row untouched.
func startPKI(ctx context.Context, pool *pgxpool.Pool, p caPaths) (*pki.CA, error) {
	ca, err := pki.LoadCA(p.rootCert, p.intCert, p.intKey, 1)
	if err != nil {
		return nil, err
	}
	if err := recordCA(ctx, pool, ca); err != nil {
		return nil, err
	}
	return ca, nil
}

func recordCA(ctx context.Context, pool *pgxpool.Pool, ca *pki.CA) error {
	rows := []struct {
		version              int
		pem, subject, keyFpr string
	}{
		{0, string(ca.RootPEM()), "StayConnect Root CA", "offline"},
		{ca.Version, string(ca.IntermediatePEM()), ca.Subject(), ca.KeyFingerprint()},
	}
	for _, r := range rows {
		if _, err := pool.Exec(ctx, `
            INSERT INTO appliance_ca_versions (version, cert_pem, subject, key_fingerprint, active)
            VALUES ($1,$2,$3,$4,true) ON CONFLICT (version) DO NOTHING`, r.version, r.pem, r.subject, r.keyFpr); err != nil {
			return fmt.Errorf("record appliance CA version %d: %w", r.version, err)
		}
		var stored string
		if err := pool.QueryRow(ctx, `SELECT cert_pem FROM appliance_ca_versions WHERE version=$1`, r.version).Scan(&stored); err != nil {
			return fmt.Errorf("read appliance CA version %d: %w", r.version, err)
		}
		if !samePEMCert(stored, r.pem) {
			return fmt.Errorf("the database records a different appliance CA (version %d) than the loaded files; refusing to start the PKI", r.version)
		}
	}
	return nil
}

// samePEMCert compares the first certificate of two PEM texts by DER, ignoring whitespace and line breaks.
func samePEMCert(a, b string) bool {
	ba, _ := pem.Decode([]byte(a))
	bb, _ := pem.Decode([]byte(b))
	return ba != nil && bb != nil && bytes.Equal(ba.Bytes, bb.Bytes)
}

// runInitCA creates the appliance CA of a brand-new Central. It refuses when any CA file exists or the
// database already records a CA: a restored or moved Central carries its CA, it never gets a new one.
func runInitCA(args []string) error {
	fs := flag.NewFlagSet("init-ca", flag.ExitOnError)
	noDB := fs.Bool("no-db", false, "do not check or record appliance_ca_versions (the first serve records it)")
	_ = fs.Parse(args)
	p := caPathsFromEnv()
	var pool *pgxpool.Pool
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if !*noDB {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		if pool, err = db.Open(ctx, cfg.DBURL); err != nil {
			return err
		}
		defer pool.Close()
	}
	if _, err := initCA(ctx, pool, p); err != nil {
		return err
	}
	fmt.Printf("appliance CA created: %s, %s, %s; the root key is %s — move it to cold storage and delete it here\n",
		p.rootCert, p.intCert, p.intKey, p.rootKeyOffline)
	return nil
}

func initCA(ctx context.Context, pool *pgxpool.Pool, p caPaths) (*pki.CA, error) {
	if pool != nil {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM appliance_ca_versions`).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			return nil, errors.New("the database already records an appliance CA; restore its files instead of creating a new CA")
		}
	}
	ca, err := pki.InitCA(p.rootKeyOffline, p.rootCert, p.intCert, p.intKey, 1)
	if err != nil {
		return nil, err
	}
	if pool != nil {
		if err := recordCA(ctx, pool, ca); err != nil {
			return nil, err
		}
	}
	return ca, nil
}

// runAssignmentKey changes the state of an assignment-signing key and re-signs the trust registry:
//
//	ctrlapi assignment-key verify-only --key-id <id> --reason "<why>"
//	ctrlapi assignment-key revoke      --key-id <id> --reason "<why>" [--emergency]
//
// verify-only stops a key signing while keeping it trusted (always safe). revoke removes all trust and is
// refused while the key still signs a CURRENT assignment, unless --emergency (confirmed compromise).
func runAssignmentKey(args []string) error {
	if len(args) == 0 || (args[0] != "verify-only" && args[0] != "revoke") {
		return fmt.Errorf("usage: ctrlapi assignment-key verify-only|revoke --key-id <id> --reason <text> [--emergency]")
	}
	fs := flag.NewFlagSet("assignment-key "+args[0], flag.ExitOnError)
	keyID := fs.String("key-id", "", "assignment signing key id")
	reason := fs.String("reason", "", "why (recorded in the audit log)")
	emergency := fs.Bool("emergency", false, "revoke even while current assignments depend on the key (confirmed compromise)")
	_ = fs.Parse(args[1:])
	if *keyID == "" || *reason == "" {
		return fmt.Errorf("--key-id and --reason are required")
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	b := &api.Base{DB: pool}
	regRoot := loadRegistryRoot()
	if args[0] == "verify-only" {
		if err := api.KeyToVerifyOnly(ctx, b, regRoot, *keyID, *reason); err != nil {
			return err
		}
		fmt.Printf("key %s is now verify_only (no longer signs; still verifies issued documents)\n", *keyID)
		return nil
	}
	deps, err := api.KeyRevoke(ctx, b, regRoot, *keyID, *reason, *emergency)
	if err != nil {
		return err
	}
	fmt.Printf("key %s revoked (current assignments it still signed: %d)\n", *keyID, deps)
	return nil
}

// runGenVendorKey creates the vendor license-signing keypair. It refuses to overwrite an existing key:
// rotating means a new key file and re-signed licenses, never silently replacing the trust root.
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

// runSeedAdmin upserts a platform_admin operator; each run updates the password.
func runSeedAdmin(args []string) error {
	fs := flag.NewFlagSet("seed-admin", flag.ExitOnError)
	email := fs.String("email", "", "admin email")
	password := fs.String("password", "", "admin password (min 10 chars) -- DISCOURAGED: visible in ps and shell history; "+
		"omit it and pass the password on stdin or in CTRLAPI_SEED_ADMIN_PASSWORD")
	displayName := fs.String("name", "Platform Admin", "display name")
	_ = fs.Parse(args)
	// The password does not have to be on the command line, where `ps` and the shell history would keep it.
	if *password == "" {
		if v := os.Getenv("CTRLAPI_SEED_ADMIN_PASSWORD"); v != "" {
			*password = v
		} else if line, err := bufio.NewReader(os.Stdin).ReadString('\n'); err == nil || line != "" {
			*password = strings.TrimRight(line, "\r\n")
		}
	} else {
		slog.Warn("seed-admin: --password puts the password in the process list and shell history; prefer stdin")
	}

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

	slog.Info("seeded platform admin", "id", id) // the id, not the email: the journal is outside retention
	return nil
}

// defaultMFAKeyPath is where central-install.sh puts the console two-step sign-in key.
const defaultMFAKeyPath = "/etc/stayconnect/operator-mfa.key"

// runGenMFAKey writes a new console two-step key. It refuses to overwrite one: replacing it makes every authenticator unreadable.
func runGenMFAKey(args []string) error {
	fs := flag.NewFlagSet("gen-mfa-key", flag.ExitOnError)
	out := fs.String("out", defaultMFAKeyPath, "where to write the key (0600)")
	_ = fs.Parse(args)
	if _, err := os.Stat(*out); err == nil {
		return fmt.Errorf("%s already exists; refusing to replace it (every operator would have to set up their authenticator again)", *out)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	f, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	slog.Info("console two-step sign-in key written", "path", *out)
	return nil
}

// runResetMFA removes one operator's authenticator: the break-glass for the last platform administrator.
// Their sessions end; audited as user.mfa_reset.
func runResetMFA(args []string) error {
	fs := flag.NewFlagSet("reset-mfa", flag.ExitOnError)
	email := fs.String("email", "", "the operator's email")
	reason := fs.String("reason", "", "why (recorded in the audit log)")
	_ = fs.Parse(args)
	if strings.TrimSpace(*email) == "" || len(strings.TrimSpace(*reason)) < 4 {
		return fmt.Errorf("--email and --reason (at least 4 characters) are required")
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
	var id string
	if err := pool.QueryRow(ctx, `SELECT id::text FROM operators WHERE lower(email) = lower($1)`, strings.TrimSpace(*email)).Scan(&id); err != nil {
		return fmt.Errorf("no operator %q: %w", *email, err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	had, err := auth.ResetMFA(ctx, tx, id)
	if err != nil {
		return err
	}
	if err := auth.EndOperatorSessions(ctx, tx, id); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	audit.System(ctx, pool, "user.mfa_reset", "operator", id, map[string]any{
		"reason": strings.TrimSpace(*reason), "had_authenticator": had, "via": "command line"})
	slog.Info("two-step sign-in reset; the operator signs in with the password alone until they turn it on again", "id", id, "had_authenticator", had)
	return nil
}

// runUnlockSignin lifts every wrong-password lock on one sign-in name, from every address. The name need not
// have an account (locks on unknown names are kept like real ones). Audited as user.signin_unlocked.
//
// Lock rows are keyed by an HMAC under the two-step key (CTRLAPI_OPERATOR_MFA_KEY); without one, the plain-hash names are cleared.
func runUnlockSignin(args []string) error {
	fs := flag.NewFlagSet("unlock-signin", flag.ExitOnError)
	email := fs.String("email", "", "the sign-in name (email) to unlock")
	reason := fs.String("reason", "", "why (recorded in the audit log)")
	_ = fs.Parse(args)
	if strings.TrimSpace(*email) == "" || len(strings.TrimSpace(*reason)) < 4 {
		return fmt.Errorf("--email and --reason (at least 4 characters) are required")
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
	var mfa *auth.MFA
	keyPath := envOrDefault("CTRLAPI_OPERATOR_MFA_KEY", defaultMFAKeyPath)
	if raw, kerr := auth.LoadMFAKeyFile(keyPath); kerr != nil {
		slog.Warn("two-step sign-in key unreadable: clearing the locks a keyless ctrlapi writes", "path", keyPath, "err", kerr)
	} else if mfa, err = auth.NewMFA(pool, raw); err != nil {
		return err
	}
	id, n, err := unlockSignin(ctx, pool, mfa, *email, *reason)
	if err != nil {
		return err
	}
	slog.Info("sign-in locks cleared", "id", id, "cleared", n)
	return nil
}

// unlockSignin clears every lock on email and audits it by account id, never by email.
func unlockSignin(ctx context.Context, pool *pgxpool.Pool, mfa *auth.MFA, email, reason string) (id string, n int64, err error) {
	n, err = auth.ClearSigninLocks(ctx, pool, mfa.SigninName(email))
	if err != nil {
		return "", 0, err
	}
	_ = pool.QueryRow(ctx, `SELECT id::text FROM operators WHERE lower(email) = lower($1)`, strings.TrimSpace(email)).Scan(&id)
	audit.System(ctx, pool, "user.signin_unlocked", "operator", id, map[string]any{
		"reason": strings.TrimSpace(reason), "locks_cleared": n, "via": "command line"})
	return id, n, nil
}

// runCheckMFAKey checks the console two-step key before a release depends on it (central-deploy.sh runs it with the new binary).
//
//	ctrlapi check-mfa-key [--key <path>] [--no-db]
//
// It fails when the key is missing or malformed, or (without --no-db) when confirmed authenticators exist and the key opens none.
func runCheckMFAKey(args []string) error {
	fs := flag.NewFlagSet("check-mfa-key", flag.ExitOnError)
	keyPath := fs.String("key", envOrDefault("CTRLAPI_OPERATOR_MFA_KEY", defaultMFAKeyPath), "the key file")
	noDB := fs.Bool("no-db", false, "check the file only, not against the enrolled authenticators")
	_ = fs.Parse(args)
	raw, err := auth.LoadMFAKeyFile(*keyPath)
	if err != nil {
		return fmt.Errorf("console two-step sign-in key: %w (create one with: ctrlapi gen-mfa-key --out %s)", err, *keyPath)
	}
	if *noDB {
		m, err := auth.NewMFA(nil, raw)
		if err != nil {
			return err
		}
		fmt.Printf("console two-step sign-in key %s loads (key id %s); not checked against the database\n", *keyPath, m.KeyID())
		return nil
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	m, err := auth.NewMFA(pool, raw)
	if err != nil {
		return err
	}
	opened, unreadable, err := m.CheckKey(ctx)
	if errors.Is(err, auth.ErrMFAWrongKey) {
		return fmt.Errorf("%s (key id %s) opens none of the %d confirmed authenticators: it is not the key they were set up "+
			"with. Restore the right key file; do not replace it", *keyPath, m.KeyID(), unreadable)
	}
	if err != nil {
		return err
	}
	if unreadable > 0 {
		slog.Warn("some authenticators were set up under another key and cannot be used until reset (ctrlapi reset-mfa)",
			"unreadable", unreadable)
	}
	fmt.Printf("console two-step sign-in key %s is good (key id %s): opens %d of %d confirmed authenticators\n",
		*keyPath, m.KeyID(), opened, opened+unreadable)
	return nil
}
