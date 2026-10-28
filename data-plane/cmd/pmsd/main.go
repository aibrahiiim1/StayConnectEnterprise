// pmsd is the dedicated read-only PMS connector daemon, one supervised worker per Interface under a DB advisory single-owner lock.
// DARK by default: with the Phase-3 connector flags off it loads no assignment, opens no database or PMS socket, and exits cleanly (Restart=on-failure so it does not storm).
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/stayconnect/enterprise/data-plane/internal/postinghandoff"
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

	var (
		poolOnce sync.Once
		pool     *pgxpool.Pool
		poolErr  error
	)
	getPool := func(ctx context.Context) (*pgxpool.Pool, error) {
		poolOnce.Do(func() { pool, poolErr = pgxpool.New(ctx, dsn) })
		return pool, poolErr
	}

	// Room charge relay: pmsd carries a posting command verbatim once the database confirms exactly those bytes; the relay socket exists only when transmission is deployed on this process.
	var relay *pmsd.FinancialRelay
	if pmsd.RelayTransmitDeployed(os.Getenv) {
		relay = pmsd.NewFinancialRelay(true, func(ctx context.Context, iface string, pn int64, sha string) (string, error) {
			p, err := getPool(ctx)
			if err != nil {
				return "", err
			}
			var verdict string
			err = p.QueryRow(ctx, `SELECT iam_v2.p4_posting_command_authorised($1::uuid, $2, $3)`,
				iface, strconv.FormatInt(pn, 10), sha).Scan(&verdict)
			return verdict, err
		}, log)
		go func() {
			if err := relay.ServeSocket(ctx, strings.TrimSpace(os.Getenv(postinghandoff.EnvSocket))); err != nil {
				log.Error("pmsd: posting relay socket stopped", "err", err)
			}
		}()
		log.Warn("pmsd: room-charge transmission is DEPLOYED on this process; the posting relay is listening")
	}
	defer func() {
		if pool != nil {
			pool.Close()
		}
	}()

	// No assignment-verification key here: tenant/site come from the Central-signed assignment (pmsd.CentralAssignmentLoader), not a pmsd-local trust root.
	// A key that is set must decode to at least 32 bytes; with the connector on, a bad one stops start-up and names the
	// variable (never the value) instead of surfacing later as an unnamed CONFIG_INVALID at dial time (audit P7 L8).
	// An absent key keeps its existing meaning: the dial refuses without it. Dark (connector off) never stops here.
	keyErr := func(name string, err error) {
		if err != nil && cfg.ConnectorOn() {
			log.Error("pmsd: key setting invalid", "variable", name, "code", "CONFIG_INVALID", "reason", err.Error())
			os.Exit(1)
		}
	}
	keyring := pmsd.MapKeyring{}
	if kid := os.Getenv("PMSD_SECRET_KEY_ID"); kid != "" {
		kb, err := decodeKeyEnv(os.Getenv, "PMSD_SECRET_KEY_HEX")
		keyErr("PMSD_SECRET_KEY_HEX", err)
		if err == nil && kb != nil {
			keyring[kid] = kb
		}
	}
	evKey, err := decodeKeyEnv(os.Getenv, "PMSD_EVIDENCE_KEY_HEX")
	keyErr("PMSD_EVIDENCE_KEY_HEX", err)
	identKey, err := decodeKeyEnv(os.Getenv, "PMSD_EVENT_IDENTITY_KEY_HEX")
	keyErr("PMSD_EVENT_IDENTITY_KEY_HEX", err)
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
			// The boundary check lives on the path that opens the repository so it runs exactly when Phase-3 writes become possible.
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
		// The Stay-Event owner wires the real Checkout Converter, constructed only when the ingest flag is on.
		// The converter is gated by its own flag: with it OFF stayengine refuses a GO with ErrCheckoutConverterRequired, leaving the event in the inbox rather than applying it under semantics nobody enabled.
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
		Dial: pmsd.NewRegistryDial(
			pmsd.NewFIASDialWithRelay(netDialer, pmsd.AdapterKeys{
				IdentityKey: identKey, IdentityKeyVersion: identKeyVer,
				EvidenceKey: evKey, EvidenceKeyVersion: evKeyVer,
			}, time.Now, log, relay),
			pmsd.NewRESTDial(pmsd.RESTDialDeps{
				Keys: pmsd.AdapterKeys{
					IdentityKey: identKey, IdentityKeyVersion: identKeyVer,
					EvidenceKey: evKey, EvidenceKeyVersion: evKeyVer,
				},
				Now: time.Now, Log: log,
			}),
		),
		Log: log,
	}

	if err := pmsd.Run(ctx, cfg, deps); err != nil {
		log.Error("pmsd: exiting on error", "code", pmsd.Classify(err).String())
		os.Exit(1)
	}
	log.Info("pmsd: stopped cleanly")
}

// minKeyBytes is the documented key size (openssl rand -hex 32).
const minKeyBytes = 32

// decodeKeyEnv returns nil, nil for an unset variable; a set one must be hex of at least minKeyBytes. The error
// never carries the value.
func decodeKeyEnv(getenv func(string) string, name string) ([]byte, error) {
	v := strings.TrimSpace(getenv(name))
	if v == "" {
		return nil, nil
	}
	b, err := hex.DecodeString(v)
	if err != nil {
		return nil, errors.New("not hexadecimal")
	}
	if len(b) < minKeyBytes {
		return nil, fmt.Errorf("shorter than %d bytes", minKeyBytes)
	}
	return b, nil
}

func envInt(name string) int {
	n, _ := strconv.Atoi(os.Getenv(name))
	return n
}

// envOr: the assignment chain's paths are overridable for tests but never absent, since a daemon that cannot find the canonical assignment must fail closed.
func envOr(name, def string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return def
}
