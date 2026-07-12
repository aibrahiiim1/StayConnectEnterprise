package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/pki"
)

// StartMTLS runs a dedicated mutual-TLS listener for appliance transport. It
// REQUIRES a client certificate signed by our appliance CA (normal TLS
// verification is fully enabled — no InsecureSkipVerify), extracts the bound
// appliance_id from the cert's URI SAN, and rejects revoked certificates and
// suspended/revoked appliances. This is the transport that carries appliance
// API/RPC once cut over; it runs alongside the signed-JWT layer (defence in
// depth), never replacing certificate verification with an exception.
func StartMTLS(ctx context.Context, db *pgxpool.Pool, ca *pki.CA, addr string, applianceAPI http.Handler) error {
	// The server TLS identity is PERSISTENT and MANAGED: a stable cert signed by
	// the intermediate CA, hot-rotated before expiry via verify-before-switch
	// (no file-deletion-and-hope). See serverTLS.
	stls, err := newServerTLS(db, ca)
	if err != nil {
		return err
	}
	slog.Info("mTLS server certificate loaded", "not_after", stls.notAfter())
	go stls.monitor(ctx) // expiry monitoring + controlled rotation
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(ca.CertPEM())

	mux := http.NewServeMux()
	mux.HandleFunc("/mtls/hello", func(w http.ResponseWriter, r *http.Request) {
		mtlsHandler(w, r, db)
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	// The real appliance API served over mTLS (hello, license, csr, certificate).
	// Guarded by RequireAppliance + client-cert binding inside applianceAPI.
	if applianceAPI != nil {
		mux.Handle("/v1/appliance/", applianceAPI)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		TLSConfig: &tls.Config{
			GetCertificate: stls.getCertificate, // hot-swappable on rotation
			ClientAuth:     tls.RequireAndVerifyClientCert,
			ClientCAs:      caPool,
			MinVersion:     tls.VersionTLS12,
		},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("mTLS appliance listener starting", "addr", addr, "ca_version", ca.Version)
	// Certs supplied via TLSConfig; empty strings are correct here.
	return srv.ListenAndServeTLS("", "")
}

// serverTLS manages Central's mTLS server certificate lifecycle: persistent
// storage, hot-swap on rotation (via GetCertificate), expiry monitoring, and
// controlled rotation with verify-before-switch + rollback + audit + history.
type serverTLS struct {
	db   *pgxpool.Pool
	ca   *pki.CA
	dir  string
	mu   sync.RWMutex
	cur  *tls.Certificate
	leaf *x509.Certificate
}

const serverTLSLifetime = 825 * 24 * time.Hour

// serverTLSThreshold is the rotate-before-expiry window (default 30d). It can be
// raised via CTRLAPI_SERVER_TLS_ROTATE_DAYS for controlled rotation drills.
func serverTLSThreshold() time.Duration {
	if v := os.Getenv("CTRLAPI_SERVER_TLS_ROTATE_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil {
			return time.Duration(d) * 24 * time.Hour
		}
	}
	return 30 * 24 * time.Hour
}

func newServerTLS(db *pgxpool.Pool, ca *pki.CA) (*serverTLS, error) {
	dir := os.Getenv("CTRLAPI_SERVER_TLS_DIR")
	if dir == "" {
		dir = "/etc/stayconnect/pki"
	}
	s := &serverTLS{db: db, ca: ca, dir: dir}
	pair, leaf, err := s.load()
	if err != nil {
		// No valid cert on disk → issue the first one.
		if err := s.issueAndPersist(); err != nil {
			return nil, err
		}
		pair, leaf, err = s.load()
		if err != nil {
			return nil, err
		}
	}
	s.cur, s.leaf = pair, leaf
	return s, nil
}

func (s *serverTLS) certPath() string { return s.dir + "/server-mtls.crt" }
func (s *serverTLS) keyPath() string  { return s.dir + "/server-mtls.key" }

func (s *serverTLS) load() (*tls.Certificate, *x509.Certificate, error) {
	cp, err := os.ReadFile(s.certPath())
	if err != nil {
		return nil, nil, err
	}
	kp, err := os.ReadFile(s.keyPath())
	if err != nil {
		return nil, nil, err
	}
	pair, err := tls.X509KeyPair(cp, kp)
	if err != nil {
		return nil, nil, err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, err
	}
	if time.Now().After(leaf.NotAfter) {
		return nil, nil, os.ErrDeadlineExceeded
	}
	return &pair, leaf, nil
}

func (s *serverTLS) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur, nil
}

func (s *serverTLS) notAfter() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.leaf.NotAfter
}

// issueAndPersist issues a fresh server cert from the intermediate CA and
// writes it atomically (cert 0644, key 0600). Keeps the previous cert as
// history (.prev) for overlap/rollback.
func (s *serverTLS) issueAndPersist() error {
	certPEM, keyPEM, err := s.ca.SignServer([]string{"150.0.0.252", "127.0.0.1"}, serverTLSLifetime)
	if err != nil {
		return err
	}
	// Verify SANs + chain BEFORE persisting/switching.
	if err := s.verify(certPEM); err != nil {
		return err
	}
	_ = os.MkdirAll(s.dir, 0o700)
	// History: preserve the current cert as .prev for rollback/overlap.
	if old, err := os.ReadFile(s.certPath()); err == nil {
		_ = os.WriteFile(s.certPath()+".prev", old, 0o644)
		if oldk, err := os.ReadFile(s.keyPath()); err == nil {
			_ = os.WriteFile(s.keyPath()+".prev", oldk, 0o600)
		}
	}
	if err := writeFileAtomic(s.certPath(), certPEM, 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(s.keyPath(), keyPEM, 0o600); err != nil {
		return err
	}
	return nil
}

// verify checks the freshly-issued cert parses, carries the required SANs, and
// chains to the CA — verify-before-switch.
func (s *serverTLS) verify(certPEM []byte) error {
	leaf, err := parseCertPEM(certPEM)
	if err != nil {
		return err
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(s.ca.CertPEM())
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return err
	}
	if err := leaf.VerifyHostname("150.0.0.252"); err != nil {
		return err
	}
	return nil
}

// CheckAndRotate rotates the server cert when it is within the threshold of
// expiry. Verify-before-switch; on any failure the old cert stays active
// (rollback). Audited with cert history.
func (s *serverTLS) CheckAndRotate(ctx context.Context) {
	na := s.notAfter()
	remaining := time.Until(na)
	if remaining > serverTLSThreshold() {
		return
	}
	slog.Warn("server TLS cert nearing expiry — rotating", "not_after", na, "remaining_hours", int(remaining.Hours()))
	if err := s.issueAndPersist(); err != nil {
		slog.Error("server TLS rotation failed — keeping current cert (rollback)", "err", err)
		s.auditServerTLS(ctx, "server_tls.rotation_failed", err.Error())
		return
	}
	pair, leaf, err := s.load()
	if err != nil {
		slog.Error("server TLS rotation reload failed — keeping current cert", "err", err)
		return
	}
	s.mu.Lock()
	s.cur, s.leaf = pair, leaf
	s.mu.Unlock()
	slog.Info("server TLS cert rotated", "new_not_after", leaf.NotAfter)
	s.auditServerTLS(ctx, "server_tls.rotated", "new_not_after="+leaf.NotAfter.Format(time.RFC3339))
}

func (s *serverTLS) auditServerTLS(ctx context.Context, action, detail string) {
	_, _ = s.db.Exec(ctx, `
        INSERT INTO audit_log (ts, actor_type, action, target_type, target_id, payload)
        VALUES (now(), 'system', $1, 'server_tls', 'mtls-listener', jsonb_build_object('detail', $2::text))`,
		action, detail)
}

// monitor runs a daily expiry check + controlled rotation.
func (s *serverTLS) monitor(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	s.CheckAndRotate(ctx) // check once at startup
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.CheckAndRotate(ctx)
		}
	}
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseCertPEM(p []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(p)
	if block == nil {
		return nil, errors.New("cert PEM decode failed")
	}
	return x509.ParseCertificate(block.Bytes)
}

func mtlsHandler(w http.ResponseWriter, r *http.Request, db *pgxpool.Pool) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "no client certificate"})
		return
	}
	cert := r.TLS.PeerCertificates[0]
	appID := pki.ApplianceIDFromCert(cert)
	fpr := pki.FingerprintHex(cert)
	if appID == "" {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "certificate missing appliance binding"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	// Revocation check (DB-driven; the fingerprint is the revocation key).
	var revoked int
	db.QueryRow(ctx, `SELECT count(*) FROM appliance_certificate_revocations WHERE fingerprint_sha256=$1`, fpr).Scan(&revoked)
	if revoked > 0 {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "certificate revoked", "appliance_id": appID})
		return
	}
	// Bind check: the cert fingerprint must match the appliance's current cert,
	// and the appliance must not be suspended/revoked/decommissioned.
	var lifecycle, curFpr string
	err := db.QueryRow(ctx, `SELECT COALESCE(lifecycle_state,''), COALESCE(current_cert_fingerprint,'') FROM appliances WHERE id=$1`, appID).Scan(&lifecycle, &curFpr)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "unknown appliance", "appliance_id": appID})
		return
	}
	switch lifecycle {
	case "suspended", "revoked", "decommissioned":
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "appliance " + lifecycle, "appliance_id": appID})
		return
	}
	if curFpr != "" && curFpr != fpr {
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "certificate superseded", "appliance_id": appID})
		return
	}
	_, _ = db.Exec(ctx, `UPDATE appliances SET identity_verified_at=now(), last_seen_at=now() WHERE id=$1`, appID)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "appliance_id": appID, "cert_fingerprint": fpr, "transport": "mtls",
	})
}
