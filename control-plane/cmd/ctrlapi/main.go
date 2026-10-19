package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
		case "serve", "":
			goto serve
		default:
			fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
			fmt.Fprintf(os.Stderr, "usage: ctrlapi [serve | seed-admin | gen-vendor-key | gen-assignment-key | gen-registry-key | assignment-key verify-only|revoke | gen-mfa-key | reset-mfa]\n")
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
	// Host hardening is checked before anything listens: a production Central does not start with the operator
	// API on every interface or with an insecure session cookie (config.Validate).
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

	// NO APPLIANCE TRANSPORT. Central serves this product for LICENSING ONLY (CLAUDE.md §0E): what an
	// appliance needs from Central -- identity, its certificate, its licence, the signed assignment -- is
	// all HTTPS on this API. There is no message bus connection.
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

	// DEDICATED assignment-signing key. Deliberately a separate key from the licence and CA keys: an
	// assignment must never be signable by any of those, and the appliance enforces this by trusting only
	// the keys in its local assignment trust registry. Absent → activation, moves and retirement refuse.
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
		slog.Warn("dedicated assignment signing key unavailable — activation disabled",
			"path", assignKeyPath, "hint", "run: ctrlapi gen-assignment-key --out "+assignKeyPath)
	}

	// Registry ROOT-OF-TRUST key: signs the versioned trust registry the appliance verifies.
	regRoot := loadRegistryRoot()
	if regRoot != nil {
		regBase := &api.RegistryBase{Base: &api.Base{DB: pool}, RootKey: regRoot}
		if sr, err := regBase.Rebuild(rootCtx, "ctrlapi boot"); err != nil {
			slog.Warn("signed registry rebuild failed", "err", err)
		} else if sr != nil {
			slog.Info("signed trust registry published", "registry_version", sr.RegistryVersion, "keys", len(sr.Keys))
		}
	}

	// Housekeeping, every minute: a retirement never acknowledged within policy becomes
	// terminal_delivery_failed + a security alert (credentials are NOT revoked); an elapsed replacement
	// window raises an alert; and, hourly, assignment-fetch log rows older than 30 days are deleted and
	// certificates superseded by a rotation more than a day ago are recorded as revoked, and Central's own
	// records are removed or anonymised at the end of their retention periods (migration 0054).
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
				if n, err := api.ReconcileTerminalTimeouts(rootCtx, tb); err != nil {
					slog.Warn("terminal-delivery timeout reconcile failed", "err", err)
				} else if n > 0 {
					slog.Warn("retirement unconfirmed (no ack within policy)", "count", n)
				}
				if n, err := api.ReconcileReplacements(rootCtx, tb); err != nil {
					slog.Warn("replacement window reconcile failed", "err", err)
				} else if n > 0 {
					slog.Warn("replacement window elapsed (operator decision required)", "count", n)
				}
				if time.Since(lastPrune) >= time.Hour {
					lastPrune = time.Now()
					if n, err := api.PruneAssignmentFetchLog(rootCtx, tb); err != nil {
						slog.Warn("assignment fetch log retention failed", "err", err)
					} else if n > 0 {
						slog.Info("assignment fetch log retention", "deleted", n)
					}
					if n, err := api.RevokeSupersededCertificates(rootCtx, tb); err != nil {
						slog.Warn("superseded certificate revocation failed", "err", err)
					} else if n > 0 {
						slog.Info("superseded appliance certificates revoked", "count", n)
					}
					if done, err := api.ApplyRetention(rootCtx, tb); err != nil {
						slog.Warn("data retention failed", "err", err)
					} else if len(done) > 0 {
						slog.Info("data retention applied", "done", done)
					}
				}
			}
		}
	}()

	// Portal hostnames and certificates (docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md). The
	// worker always runs and does nothing until an operator has configured the ACME contact and the DNS token
	// from the console, so a fresh Central needs no extra step to start. The TEST-ONLY overrides let the whole
	// chain run against Pebble + challtestsrv; when any is on, every start says so.
	ptlsOpts, ptlsOverrides := portaltls.OptionsFromEnv()
	var ptls *portaltls.Service
	if err := ptlsOpts.Check(); err != nil {
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
	// issuance uses ONLY the intermediate key. Absent key → PKI routes and the mTLS listener stay disabled.
	var appCA *pki.CA
	rootKeyPath := envOrDefault("CTRLAPI_ROOT_CA_KEY", "/etc/stayconnect/pki/root-ca.key")
	intKeyPath := envOrDefault("CTRLAPI_INTERMEDIATE_CA_KEY", "/etc/stayconnect/pki/intermediate-ca.key")
	sharedReplay := applianceauth.NewReplayCache(2*time.Minute, 8192)
	if ca, err := pki.LoadCAChain(rootKeyPath, intKeyPath, 1); err == nil {
		appCA = ca
		relocateRootOffline(rootKeyPath)
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
		mtlsAddr := envOrDefault("CTRLAPI_MTLS_ADDR", ":9443")
		applianceMTLSAPI := api.ApplianceMTLSRouter(pool, rdb, sharedReplay, licSvc, ca, assignKey, regRoot, ptls)
		go func() {
			if err := apihttp.StartMTLS(rootCtx, pool, ca, mtlsAddr, applianceMTLSAPI); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("mTLS listener error", "err", err)
			}
		}()
	} else {
		slog.Warn("appliance CA unavailable — PKI/mTLS disabled", "path", intKeyPath, "err", err)
	}

	// CONSOLE TWO-STEP SIGN-IN (0052, GDPR plan C10). Without the key no operator can sign in: the console
	// refuses rather than letting a password alone through. central-install.sh creates it; the export bundle
	// carries it, so moving Central keeps everyone's authenticator.
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

func loadRegistryRoot() ed25519.PrivateKey {
	regRootPath := envOrDefault("CTRLAPI_REGISTRY_ROOT_KEY", "/etc/stayconnect/assignment-registry-root.key")
	raw, err := os.ReadFile(regRootPath)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		slog.Warn("assignment registry root key unavailable — signed registry disabled",
			"path", regRootPath, "hint", "run: ctrlapi gen-registry-key --out "+regRootPath)
		return nil
	}
	k := ed25519.PrivateKey(raw)
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

// relocateRootOffline moves the root CA private key out of the runtime PKI directory into a restricted
// offline holding area once the intermediate has been signed. The runtime never opens it again. In
// production it should be physically moved to cold storage.
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

	slog.Info("seeded platform admin", "email", *email, "id", id)
	return nil
}

// defaultMFAKeyPath is where central-install.sh puts the console two-step sign-in key.
const defaultMFAKeyPath = "/etc/stayconnect/operator-mfa.key"

// runGenMFAKey writes a new 32-byte console two-step sign-in key (0052). It refuses to overwrite one: replacing
// the key makes every operator's authenticator unreadable, and every operator would need a reset.
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

// runResetMFA removes one operator's authenticator from Central's own command line (0052): the break-glass for
// the last platform administrator, when nobody is left to reset them in the console. The operator's sessions
// end, and they set up a new authenticator at their next sign-in. Audited as user.mfa_reset.
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
	slog.Info("two-step sign-in reset; a new authenticator is set up at the next sign-in", "email", *email, "had_authenticator", had)
	return nil
}
