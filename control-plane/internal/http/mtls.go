package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/pki"
)

// StartMTLS runs the appliance mutual-TLS listener. A client certificate signed by the appliance CA is
// required; applianceAPI rejects revoked certificates and retired identities.
func StartMTLS(ctx context.Context, db *pgxpool.Pool, ca *pki.CA, addr string, applianceAPI http.Handler) error {
	stls, err := newServerTLS(db, ca)
	if err != nil {
		return err
	}
	slog.Info("mTLS server certificate loaded", "not_after", stls.notAfter())
	go stls.monitor(ctx)
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(ca.CertPEM())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	if applianceAPI != nil {
		mux.Handle("/v1/appliance/", applianceAPI)
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		TLSConfig: &tls.Config{
			GetCertificate: stls.getCertificate,
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
	return srv.ListenAndServeTLS("", "")
}

// serverTLS manages Central's mTLS server certificate: persistent, hot-swapped on rotation after verification.
type serverTLS struct {
	db  *pgxpool.Pool
	ca  *pki.CA
	dir string
	// names is resolved once so issuing and verifying cannot disagree.
	names []string
	mu    sync.RWMutex
	cur   *tls.Certificate
	leaf  *x509.Certificate
}

const serverTLSLifetime = 825 * 24 * time.Hour

// serverTLSThreshold is the rotate-before-expiry window (default 30d, CTRLAPI_SERVER_TLS_ROTATE_DAYS).
func serverTLSThreshold() time.Duration {
	if v := os.Getenv("CTRLAPI_SERVER_TLS_ROTATE_DAYS"); v != "" {
		if d, err := strconv.Atoi(v); err == nil {
			return time.Duration(d) * 24 * time.Hour
		}
	}
	return 30 * 24 * time.Hour
}

// mtlsServerNames is what the mTLS certificate is issued for, and so what an appliance may dial:
// CTRLAPI_APPLIANCE_BASE, plus CTRLAPI_MTLS_SANS (comma-separated, for appliances still dialling an old
// address during a move), plus 127.0.0.1 for local health checks.
func mtlsServerNames() []string {
	seen := map[string]bool{}
	var out []string
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	if base := os.Getenv("CTRLAPI_APPLIANCE_BASE"); base != "" {
		h := strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
		if i := strings.IndexAny(h, ":/"); i >= 0 {
			h = h[:i]
		}
		add(h)
	}
	for _, extra := range strings.Split(os.Getenv("CTRLAPI_MTLS_SANS"), ",") {
		add(extra)
	}
	add("127.0.0.1")
	return out
}

func newServerTLS(db *pgxpool.Pool, ca *pki.CA) (*serverTLS, error) {
	dir := os.Getenv("CTRLAPI_SERVER_TLS_DIR")
	if dir == "" {
		dir = "/etc/stayconnect/pki"
	}
	s := &serverTLS{db: db, ca: ca, dir: dir, names: mtlsServerNames()}
	slog.Info("mTLS server certificate names", "sans", s.names)
	pair, leaf, err := s.load()
	if err != nil {
		if err := s.issueAndPersist(); err != nil {
			return nil, err
		}
		pair, leaf, err = s.load()
		if err != nil {
			return nil, err
		}
	} else if missing := s.namesNotCoveredBy(leaf); len(missing) > 0 {
		// A valid stored certificate can still lack a name appliances dial, which looks like a network fault; re-issue now.
		slog.Info("re-issuing the mTLS server certificate: it does not cover every configured name",
			"missing", missing, "configured", s.names)
		if err := s.issueAndPersist(); err != nil {
			// A cert missing one name is better than no listener.
			slog.Error("could not re-issue the mTLS server certificate; continuing with the stored one",
				"err", err, "missing", missing)
		} else if p2, l2, err2 := s.load(); err2 == nil {
			pair, leaf = p2, l2
		}
	}
	s.cur, s.leaf = pair, leaf
	return s, nil
}

func (s *serverTLS) namesNotCoveredBy(leaf *x509.Certificate) []string {
	var missing []string
	for _, n := range s.names {
		if leaf.VerifyHostname(n) != nil {
			missing = append(missing, n)
		}
	}
	return missing
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

// issueAndPersist writes a fresh server cert atomically and keeps the previous pair as .prev.
func (s *serverTLS) issueAndPersist() error {
	certPEM, keyPEM, err := s.ca.SignServer(s.names, serverTLSLifetime)
	if err != nil {
		return err
	}
	// Verify before persisting or switching.
	if err := s.verify(certPEM); err != nil {
		return err
	}
	_ = os.MkdirAll(s.dir, 0o700)
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
	// Every configured name must be covered; a missing one fails only the appliances that dial it.
	for _, n := range s.names {
		if err := leaf.VerifyHostname(n); err != nil {
			return fmt.Errorf("issued mTLS certificate is not valid for %q: %w", n, err)
		}
	}
	return nil
}

// CheckAndRotate rotates the cert inside the threshold; on any failure the old cert stays active.
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

func (s *serverTLS) monitor(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	s.CheckAndRotate(ctx)
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
