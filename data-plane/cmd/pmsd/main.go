// pmsd — dedicated read-only PMS connector daemon (Phase 3, ADR-0001).
//
// Owns each PMS Interface connection under a DB advisory single-owner lock, one independent supervised
// worker per Interface, persisting the interface-level freshness axes to iam_v2.pms_interface_runtime via
// independent compare-and-set updates. Reuses the accepted FIAS protocol layer (internal/pms); emits no
// financial Posting (PS/PA) record. Tenant/Site derive ONLY from the verified signed appliance assignment.
//
// DARK by default: with STAYCONNECT_PHASE3_PMS_CONNECTOR (and its master) OFF, pmsd loads no assignment,
// opens no database connection, reads no secret, creates no worker, and opens no PMS socket, then exits
// cleanly. The shared DB pool is created lazily on the first repository/lock use, so a flags-OFF run never
// contacts PostgreSQL. The systemd unit uses Restart=on-failure so a clean flags-OFF exit does not storm.
package main

import (
	"context"
	"encoding/hex"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
	"github.com/stayconnect/enterprise/data-plane/internal/stayengine"
	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := iamv2.LoadPMSConfigFromEnv(os.Getenv)
	if err != nil {
		log.Error("pmsd: config fail-closed", "code", "CONFIG_INVALID")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := os.Getenv("PMSD_DB_URL")

	// lazily-created SHARED pool: never constructed while dark (Run returns before OpenRepo when flags OFF).
	var (
		poolOnce sync.Once
		pool     *pgxpool.Pool
		poolErr  error
	)
	getPool := func(ctx context.Context) (*pgxpool.Pool, error) {
		poolOnce.Do(func() { pool, poolErr = pgxpool.New(ctx, dsn) })
		return pool, poolErr
	}
	defer func() {
		if pool != nil {
			pool.Close()
		}
	}()

	// The interface-secret keyring + evidence/identity keys. There is deliberately NO assignment-verification
	// key here: pmsd does not carry its own trust root for scope. Tenant/site come from the canonical
	// Central-signed assignment, verified against the registry anchored by the manufacture-time root — see
	// pmsd.CentralAssignmentLoader.
	keyring := pmsd.MapKeyring{}
	if kid := os.Getenv("PMSD_SECRET_KEY_ID"); kid != "" {
		if kb, err := hex.DecodeString(os.Getenv("PMSD_SECRET_KEY_HEX")); err == nil {
			keyring[kid] = kb
		}
	}
	evKey, _ := hex.DecodeString(os.Getenv("PMSD_EVIDENCE_KEY_HEX"))
	identKey, _ := hex.DecodeString(os.Getenv("PMSD_EVENT_IDENTITY_KEY_HEX"))
	evKeyVer := envInt("PMSD_EVIDENCE_KEY_VERSION")
	identKeyVer := envInt("PMSD_EVENT_IDENTITY_KEY_VERSION")
	netDialer := func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}

	deps := pmsd.Deps{
		LoadAssignment: pmsd.CentralAssignmentLoader(assignment.Paths{
			Dir:              envOr("PMSD_ASSIGNMENT_DIR", "/etc/stayconnect/assignment"),
			RegistryPath:     envOr("PMSD_ASSIGNMENT_REGISTRY", "/etc/stayconnect/assignment/registry.json"),
			RegistryRootPath: envOr("PMSD_ASSIGNMENT_REGISTRY_ROOT", "/etc/stayconnect/assignment-registry-root.pub"),
			TrustPath:        envOr("PMSD_ASSIGNMENT_TRUST", "/etc/stayconnect/assignment-trust.json"),
		}, envOr("PMSD_IDENTITY_DIR", "/etc/stayconnect/identity"), log),
		OpenRepo: func(ctx context.Context, _ pmsd.Assignment) (pmsd.Repo, error) {
			p, err := getPool(ctx)
			if err != nil {
				return nil, err
			}
			// The boundary check lives HERE, on the path that opens the repository, rather than in main's
			// preamble: while dark pmsd never reaches this point, so the check runs exactly when Phase-3
			// writes become possible and never a moment before.
			if err := writerguard.Verify(ctx, p, writerguard.Phase3Requirements()); err != nil {
				return nil, err
			}
			return pmsd.NewPgRepoFromPool(p), nil
		},
		NewLocker: func(ctx context.Context) (pmsd.Locker, error) {
			p, err := getPool(ctx)
			if err != nil {
				return nil, err
			}
			return pmsd.NewPgLocker(ctx, p)
		},
		DecryptSecret: func(ctx context.Context, iface pmsd.Interface, rev pmsd.Revision, sg pmsd.SecretGeneration) (pmsd.SecretMaterial, error) {
			p, err := getPool(ctx)
			if err != nil {
				return pmsd.SecretMaterial{}, err
			}
			return pmsd.NewPgSecretDecryptor(p, keyring)(ctx, iface, rev, sg)
		},
		// The Stay-Event application owner: the real Stay Engine with the real Checkout Converter wired in, so
		// a typed GO event's application and its whole conversion are ONE transaction. Constructed only when
		// the ingest flag is on (Run gates the call), and never while dark.
		//
		// THE CHECKOUT CONVERTER IS GATED BY ITS OWN FLAG.
		//
		// It used to be wired unconditionally, so a GO event ran the whole checkout conversion — creating
		// grace entitlements, purchases and session updates — while STAYCONNECT_PHASE3_CHECKOUT_GRACE
		// reported OFF. An operator reading the flags would have concluded that surface was inert while it
		// was in fact executing on every checkout, and the privilege grant needed to support it would have
		// looked unexplainable next to a flag that said the feature was disabled.
		//
		// With the flag OFF the converter is absent, and stayengine refuses a GO with
		// ErrCheckoutConverterRequired rather than inventing a Stay-only checkout. That refusal is the
		// honest outcome: the event stays in the inbox, visible and replayable, instead of being applied
		// under semantics nobody enabled.
		NewStayApplier: func(ctx context.Context, _ pmsd.Assignment) (pmsd.StayApplier, error) {
			p, err := getPool(ctx)
			if err != nil {
				return nil, err
			}
			if !cfg.CheckoutGraceOn() {
				log.Warn("pmsd: checkout grace is OFF — GO events will be refused, not converted",
					"flag", iamv2.EnvPhase3CheckoutGrace)
				return stayengine.NewProcessor(p), nil
			}
			return stayengine.NewProcessorWithCheckout(p, checkout.NewConverter(p)), nil
		},
		Dial: pmsd.NewFIASDial(netDialer, pmsd.AdapterKeys{
			IdentityKey: identKey, IdentityKeyVersion: identKeyVer,
			EvidenceKey: evKey, EvidenceKeyVersion: evKeyVer,
		}, time.Now, log),
		Log: log,
	}

	if err := pmsd.Run(ctx, cfg, deps); err != nil {
		log.Error("pmsd: exiting on error", "code", pmsd.Classify(err).String())
		os.Exit(1)
	}
	log.Info("pmsd: stopped cleanly")
}

// envInt parses an integer env var (0 when unset/invalid; startup validation rejects a zero where required).
func envInt(name string) int {
	n, _ := strconv.Atoi(os.Getenv(name))
	return n
}

// envOr reads an environment override, falling back to the packaged default. The assignment chain's paths are
// deployment facts rather than policy, so they are overridable for tests and offline tooling — but never
// absent, because a daemon that cannot find the canonical assignment must fail closed rather than invent one.
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}
