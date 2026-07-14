// Command nats-authz is StayConnect's dedicated NATS Auth Callout service.
//
// NATS authenticates the client's TLS certificate (mTLS, verified against the
// appliance intermediate CA) and then delegates AUTHORIZATION to this service.
// For each connection NATS sends a signed authorization request to
// $SYS.REQ.USER.AUTH; this service:
//
//   - decodes the request (optionally XKey-decrypting it),
//   - extracts the client certificate NATS already verified,
//   - reads the appliance_id ONLY from the certificate's URI SAN
//     (stayconnect://appliance/<id>) — never from username/subject/metadata,
//   - validates the cert fingerprint + validity + revocation + appliance
//     lifecycle against the authoritative Central database,
//   - resolves tenant/site from the database (never client-supplied),
//   - returns connection-specific, per-appliance publish/subscribe permissions
//     (deny-by-default),
//   - audits every accept/reject, and
//   - FAILS CLOSED if the database or this service is unavailable.
//
// It runs as a dedicated, non-root service with its own least-privilege NATS
// user and its own signing (account) + encryption (xkey) keys — separate from
// the appliance CA, license, command and update keys.
package main

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	njwt "github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "genkeys" {
		genKeys()
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := loadCfg()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pool, err := pgxpool.New(ctx, cfg.dbURL)
	if err != nil {
		slog.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	issuerKP, err := nkeys.FromSeed([]byte(cfg.issuerSeed))
	if err != nil {
		slog.Error("issuer seed invalid", "err", err)
		os.Exit(1)
	}
	var curveKP nkeys.KeyPair
	if cfg.xkeySeed != "" {
		curveKP, err = nkeys.FromCurveSeed([]byte(cfg.xkeySeed))
		if err != nil {
			slog.Error("xkey seed invalid", "err", err)
			os.Exit(1)
		}
	}

	opts := []nats.Option{
		nats.Name("nats-authz"),
		nats.MaxReconnects(-1),
		nats.UserInfo(cfg.authUser, cfg.authPass),
	}
	if cfg.clientCert != "" {
		opts = append(opts, nats.ClientCert(cfg.clientCert, cfg.clientKey))
	}
	if cfg.serverCA != "" {
		opts = append(opts, nats.RootCAs(cfg.serverCA))
	}
	nc, err := nats.Connect(cfg.natsURL, opts...)
	if err != nil {
		slog.Error("nats connect failed", "err", err)
		os.Exit(1)
	}
	defer nc.Drain()

	svc := &authz{pool: pool, issuer: issuerKP, curve: curveKP, targetAccount: cfg.targetAccount, userTTL: cfg.userTTL}
	if _, err := nc.Subscribe("$SYS.REQ.USER.AUTH", svc.handle); err != nil {
		slog.Error("subscribe failed", "err", err)
		os.Exit(1)
	}
	slog.Info("nats-authz ready", "nats", cfg.natsURL, "xkey", cfg.xkeySeed != "")
	select {}
}

type config struct {
	natsURL, issuerSeed, xkeySeed, dbURL, targetAccount string
	authUser, authPass, clientCert, clientKey, serverCA string
	userTTL                                             time.Duration
}

func loadCfg() config {
	ttl := 300 * time.Second // default max active-revocation latency = 5 min
	if v := os.Getenv("AUTHZ_USER_TTL_SECONDS"); v != "" {
		if n, err := time.ParseDuration(v + "s"); err == nil {
			ttl = n
		}
	}
	return config{
		natsURL:       env("AUTHZ_NATS_URL", "nats://127.0.0.1:4223"),
		issuerSeed:    os.Getenv("AUTHZ_ISSUER_SEED"),
		xkeySeed:      os.Getenv("AUTHZ_XKEY_SEED"),
		dbURL:         os.Getenv("AUTHZ_DB_URL"),
		targetAccount: env("AUTHZ_TARGET_ACCOUNT", "APPLIANCES"),
		authUser:      env("AUTHZ_AUTH_USER", "authz"),
		authPass:      os.Getenv("AUTHZ_AUTH_PASS"),
		clientCert:    os.Getenv("AUTHZ_CLIENT_CERT"),
		clientKey:     os.Getenv("AUTHZ_CLIENT_KEY"),
		serverCA:      os.Getenv("AUTHZ_SERVER_CA"),
		userTTL:       ttl,
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type authz struct {
	pool          *pgxpool.Pool
	issuer        nkeys.KeyPair
	curve         nkeys.KeyPair
	targetAccount string
	userTTL       time.Duration
}

func (a *authz) handle(msg *nats.Msg) {
	data := msg.Data
	// XKey-decrypt the request if encryption is configured.
	if a.curve != nil {
		xkHeader := msg.Header.Get("Nats-Server-Xkey")
		if xkHeader != "" {
			dec, err := a.curve.Open(data, xkHeader)
			if err != nil {
				slog.Warn("authz: xkey open failed", "err", err)
				return
			}
			data = dec
		}
	}
	rc, err := njwt.DecodeAuthorizationRequestClaims(string(data))
	if err != nil {
		slog.Warn("authz: decode request failed", "err", err)
		return
	}
	userNkey := rc.UserNkey
	serverID := rc.Server.ID

	// A verified central-SERVICE certificate (URI stayconnect://service/<name>)
	// is a Central consumer (fleet/heartbeat), not an appliance. Grant it broad
	// read of appliance telemetry/heartbeat + command publish. It is not subject
	// to appliance DB lookups.
	if svc := serviceNameFromReq(rc); svc != "" {
		uc := njwt.NewUserClaims(userNkey)
		uc.Name = "central-service:" + svc
		uc.Audience = a.targetAccount
		if a.userTTL > 0 {
			uc.Expires = time.Now().Add(a.userTTL).Unix()
		}
		uc.Permissions.Sub.Allow.Add("hb.*", "telemetry.>", "appliances.>", "_INBOX.>")
		uc.Permissions.Pub.Allow.Add("appliances.*.commands", "appliances.*.license", "appliances.*.updates", "appliances.*.config", "_INBOX.>")
		uToken, err := uc.Encode(a.issuer)
		if err != nil {
			a.respond(msg, userNkey, serverID, "", "internal")
			return
		}
		a.audit(context.Background(), "", "nats.auth_accepted", "central service "+svc)
		a.respond(msg, userNkey, serverID, uToken, "")
		return
	}

	applianceID, tenantID, siteID, denyReason := a.authorize(rc)
	if denyReason != "" {
		a.audit(context.Background(), "", "nats.auth_rejected", denyReason)
		a.respond(msg, userNkey, serverID, "", denyReason)
		return
	}

	// Build the connection-specific, per-appliance permissions (deny-by-default).
	uc := njwt.NewUserClaims(userNkey)
	uc.Name = applianceID
	uc.Audience = a.targetAccount
	// Short-lived authorization: NATS disconnects the client when this expires,
	// forcing a reconnect that re-runs this callout. A revoked/suspended cert is
	// then rejected — this is how ACTIVE revocation of an already-connected
	// appliance is enforced. Max revocation latency ≈ userTTL.
	if a.userTTL > 0 {
		uc.Expires = time.Now().Add(a.userTTL).Unix()
	}
	id := applianceID
	base := "appliances." + id + "."
	uc.Permissions.Pub.Allow.Add(
		// New per-appliance scheme:
		base+"heartbeat", base+"telemetry", base+"commands.results",
		base+"license.ack", base+"updates.status", base+"diagnostics.results",
		// Current production data subjects (appliance-id scoped → isolated):
		"hb."+id, "telemetry."+id,
		"_INBOX.>", // request/reply
	)
	uc.Permissions.Sub.Allow.Add(
		base+"commands", base+"license", base+"updates", base+"config",
		// Central→appliance control (id-scoped RPC), plus site/tenant-scoped
		// config-reload + nft peer sync (shared within the site/tenant only —
		// never cross-tenant).
		"scd."+id+".>",
		"_INBOX.>",
	)
	if tenantID != "" {
		uc.Permissions.Sub.Allow.Add("config." + tenantID + ".>")
	}
	if siteID != "" {
		uc.Permissions.Sub.Allow.Add("nft." + siteID)
		uc.Permissions.Pub.Allow.Add("nft." + siteID)
	}
	uToken, err := uc.Encode(a.issuer)
	if err != nil {
		slog.Error("authz: encode user claims failed", "err", err)
		a.respond(msg, userNkey, serverID, "", "internal")
		return
	}
	a.audit(context.Background(), applianceID, "nats.auth_accepted", "per-appliance permissions issued")
	a.respond(msg, userNkey, serverID, uToken, "")
}

// authorize returns the validated appliance_id + tenant/site or a deny reason.
// It trusts ONLY the verified client certificate for identity.
func (a *authz) authorize(rc *njwt.AuthorizationRequestClaims) (appID, tenantID, siteID, deny string) {
	cert, err := clientCert(rc)
	if err != nil {
		return "", "", "", "no verified client certificate"
	}
	applianceID := applianceIDFromCert(cert)
	if applianceID == "" {
		return "", "", "", "certificate missing appliance URI SAN"
	}
	fpr := sha256Hex(cert.Raw)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Revocation check (fail closed on DB error).
	var revoked int
	if err := a.pool.QueryRow(ctx, `SELECT count(*) FROM appliance_certificate_revocations WHERE fingerprint_sha256=$1`, fpr).Scan(&revoked); err != nil {
		return "", "", "", "authz db unavailable (fail closed)"
	}
	if revoked > 0 {
		return "", "", "", "certificate revoked"
	}
	// The cert fingerprint must match an ACTIVE cert bound to this appliance,
	// within validity, and the appliance lifecycle must be allowed. Tenant/Site
	// resolved from the DB (never client-supplied).
	var dbApplianceID, lifecycle, dbTenant, dbSite string
	var notAfter time.Time
	err = a.pool.QueryRow(ctx, `
        SELECT c.appliance_id::text, COALESCE(a.lifecycle_state,''), c.not_after,
               COALESCE(a.tenant_id::text,''), COALESCE(a.site_id::text,'')
          FROM appliance_certificates c JOIN appliances a ON a.id=c.appliance_id
         WHERE c.fingerprint_sha256=$1 AND c.status='active'`, fpr).Scan(&dbApplianceID, &lifecycle, &notAfter, &dbTenant, &dbSite)
	if errors.Is(err, pgxNoRows) || dbApplianceID == "" {
		return "", "", "", "certificate not found / not active in database"
	}
	if err != nil {
		return "", "", "", "authz db error (fail closed)"
	}
	if dbApplianceID != applianceID {
		return "", "", "", "certificate/database appliance-id mismatch"
	}
	if time.Now().After(notAfter) {
		return "", "", "", "certificate expired"
	}
	switch lifecycle {
	case "suspended", "revoked", "decommissioned":
		return "", "", "", "appliance " + lifecycle
	}
	return applianceID, dbTenant, dbSite, ""
}

func (a *authz) respond(msg *nats.Msg, userNkey, serverID, userJWT, errMsg string) {
	arc := njwt.NewAuthorizationResponseClaims(userNkey)
	arc.Audience = serverID
	if userJWT != "" {
		arc.Jwt = userJWT
	} else {
		arc.Error = errMsg
	}
	token, err := arc.Encode(a.issuer)
	if err != nil {
		slog.Error("authz: encode response failed", "err", err)
		return
	}
	out := []byte(token)
	// Encrypt the response back to the server's xkey if in use.
	if a.curve != nil {
		xkHeader := msg.Header.Get("Nats-Server-Xkey")
		if xkHeader != "" {
			if enc, err := a.curve.Seal(out, xkHeader); err == nil {
				out = enc
			}
		}
	}
	_ = msg.Respond(out)
}

func (a *authz) audit(ctx context.Context, applianceID, action, detail string) {
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = a.pool.Exec(c, `
        INSERT INTO audit_log (ts, actor_type, actor_id, action, target_type, target_id, payload)
        VALUES (now(), 'appliance', NULLIF($1,''), $2, 'nats', NULLIF($1,''), jsonb_build_object('detail',$3::text))`,
		applianceID, action, detail)
}

// ---- cert helpers ----

var pgxNoRows = errors.New("no rows in result set")

func clientCert(rc *njwt.AuthorizationRequestClaims) (*x509.Certificate, error) {
	tlsInfo := rc.TLS
	if tlsInfo == nil {
		return nil, errors.New("no tls info")
	}
	// Prefer the verified chain leaf; fall back to presented certs.
	var pemStr string
	if len(tlsInfo.VerifiedChains) > 0 && len(tlsInfo.VerifiedChains[0]) > 0 {
		pemStr = tlsInfo.VerifiedChains[0][0]
	} else if len(tlsInfo.Certs) > 0 {
		pemStr = tlsInfo.Certs[0]
	} else {
		return nil, errors.New("no client cert in request")
	}
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		// Some servers send raw base64 DER without PEM armor.
		return nil, errors.New("client cert PEM decode failed")
	}
	return x509.ParseCertificate(block.Bytes)
}

func applianceIDFromCert(cert *x509.Certificate) string {
	for _, u := range cert.URIs {
		if u.Scheme == "stayconnect" && u.Host == "appliance" {
			return strings.TrimPrefix(u.Path, "/")
		}
	}
	return ""
}

// serviceNameFromReq returns the central-service name if the verified client
// cert carries a stayconnect://service/<name> URI SAN.
func serviceNameFromReq(rc *njwt.AuthorizationRequestClaims) string {
	cert, err := clientCert(rc)
	if err != nil {
		return ""
	}
	for _, u := range cert.URIs {
		if u.Scheme == "stayconnect" && u.Host == "service" {
			return strings.TrimPrefix(u.Path, "/")
		}
	}
	return ""
}

func sha256Hex(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// genKeys emits a fresh issuer account keypair + xkey for one-time setup.
func genKeys() {
	acc, _ := nkeys.CreateAccount()
	accSeed, _ := acc.Seed()
	accPub, _ := acc.PublicKey()
	xk, _ := nkeys.CreateCurveKeys()
	xkSeed, _ := xk.Seed()
	xkPub, _ := xk.PublicKey()
	fmt.Printf("ISSUER_ACCOUNT_PUBLIC=%s\n", accPub)
	fmt.Printf("ISSUER_ACCOUNT_SEED=%s\n", string(accSeed))
	fmt.Printf("XKEY_PUBLIC=%s\n", xkPub)
	fmt.Printf("XKEY_SEED=%s\n", string(xkSeed))
	_ = url.URL{}
}
