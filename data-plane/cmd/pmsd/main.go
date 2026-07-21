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
	"crypto/ed25519"
	"encoding/hex"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
	"github.com/stayconnect/enterprise/data-plane/internal/stayengine"
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

	// control-plane assignment-verification public key (hex ed25519) + secret keyring + evidence key.
	pub, _ := hex.DecodeString(os.Getenv("PMSD_ASSIGNMENT_PUBKEY_HEX"))
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
		LoadAssignment: pmsd.FileAssignmentLoader(os.Getenv("PMSD_ASSIGNMENT_FILE"), ed25519.PublicKey(pub)),
		OpenRepo: func(ctx context.Context, _ pmsd.Assignment) (pmsd.Repo, error) {
			p, err := getPool(ctx)
			if err != nil {
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
		NewStayApplier: func(ctx context.Context, _ pmsd.Assignment) (pmsd.StayApplier, error) {
			p, err := getPool(ctx)
			if err != nil {
				return nil, err
			}
			return stayengine.NewProcessorWithCheckout(p, checkout.NewConverter(p)), nil
		},
		Dial: pmsd.NewFIASDial(netDialer, pmsd.AdapterKeys{
			IdentityKey: identKey, IdentityKeyVersion: identKeyVer,
			EvidenceKey: evKey, EvidenceKeyVersion: evKeyVer,
		}, time.Now),
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
