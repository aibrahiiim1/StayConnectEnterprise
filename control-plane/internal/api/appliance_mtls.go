package api

import (
	"crypto/ed25519"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	redis "github.com/redis/go-redis/v9"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	pki "github.com/stayconnect/enterprise/control-plane/internal/pki"
	"github.com/stayconnect/enterprise/control-plane/internal/portaltls"
)

// ApplianceMTLSRouter builds the appliance API served over the mutual-TLS
// listener. Every request carries BOTH a verified client certificate
// (transport identity, checked by the TLS layer + this router) AND a signed
// request JWT (application identity, RequireAppliance) — defence in depth. The
// two identities MUST agree: the cert's bound appliance_id must equal the JWT
// issuer, and the client cert must not be revoked.
//
// ptls may be nil (tests): the portal-TLS routes are then absent.
func ApplianceMTLSRouter(db *pgxpool.Pool, rdb *redis.Client, replay *applianceauth.ReplayCache, lic *licensing.Service, ca *pki.CA, assignKey, regRoot ed25519.PrivateKey, ptls *portaltls.Service) http.Handler {
	r := chi.NewRouter()

	// ---- CERTIFICATE-ONLY channel ----
	// GET /v1/appliance/assignment authenticates SOLELY from the verified client
	// certificate: URI-SAN appliance_id → exact certificate serial → exact
	// certificate fingerprint → Central appliance record (strictMTLSSelf). It does
	// NOT run RequireAppliance, so no appliance JWT or bearer
	// token is required or consulted — the mTLS listener has already verified the
	// certificate chain against the CA. GET-only, read-only, rate-limited, audited.
	if assignKey != nil {
		assignBase := &AssignmentBase{Base: &Base{DB: db, Redis: rdb}, SignKey: assignKey}
		r.With(RateLimit(rdb, "assignment-fetch", 30, time.Minute)).
			Get("/v1/appliance/assignment", assignBase.StrictApplianceAssignmentHandler)
	}

	// ---- JWT + cert-binding channel (defence in depth) for everything else ----
	// These endpoints carry BOTH a verified client certificate AND a signed request
	// JWT whose issuer must equal the cert's bound appliance_id.
	r.Group(func(r chi.Router) {
		// Per client address, before authentication (see the :443 router).
		r.Use(RateLimit(rdb, "appliance-mtls", 240, time.Minute))
		r.Use(auth.RequireAppliance(db, replay))
		r.Use(mtlsCertBinding(db))

		identityBase := &IdentityBase{Base: &Base{DB: db}, ReplayCache: replay}
		r.Get("/v1/appliance/hello", identityBase.HelloHandler)
		r.Post("/v1/appliance/offline-reconcile", identityBase.OfflineReconcile)
		if lic != nil {
			licBase := &LicensesBase{Base: &Base{DB: db}, Svc: lic}
			r.Get("/v1/appliance/license", licBase.ApplianceLicenseHandler)
		}
		if assignKey != nil {
			// Signed terminal-adoption acknowledgment (Phase-1 completion). The ack
			// payload is itself signed by the appliance identity key; the JWT here is
			// belt-and-braces on the POST.
			ackBase := &Base{DB: db, Redis: rdb, AssignKey: assignKey}
			r.With(RateLimit(rdb, "assignment-ack", 10, time.Minute)).
				Post("/v1/appliance/assignment/ack", ackBase.AckHandler)
		}
		if regRoot != nil {
			regBase := &RegistryBase{Base: &Base{DB: db}, RootKey: regRoot}
			r.With(RateLimit(rdb, "assignment-registry", 30, time.Minute)).
				Get("/v1/appliance/assignment-registry", regBase.Serve)
		}
		if ca != nil {
			certBase := &CertBase{Base: &Base{DB: db}, CA: ca, ClientValid: 90 * 24 * time.Hour}
			r.Post("/v1/appliance/csr", certBase.SubmitCSR)
			r.Get("/v1/appliance/certificate", certBase.FetchCertificate)
		}
		if ptls != nil {
			// Portal hostname and certificate lifecycle (contract §7). The appliance syncs every few minutes and
			// orders a handful of certificates a year; the limits leave room for retries and nothing more. The
			// daily order cap is enforced per appliance inside portaltls.
			pt := &PortalTLSBase{Svc: ptls}
			r.With(RateLimit(rdb, "portal-tls-sync", 30, time.Minute)).
				Post("/v1/appliance/portal-tls/sync", pt.Sync)
			r.With(RateLimit(rdb, "portal-tls-order", 10, time.Minute)).
				Post("/v1/appliance/portal-tls/orders", pt.Order)
			r.With(RateLimit(rdb, "portal-tls-order-get", 60, time.Minute)).
				Get("/v1/appliance/portal-tls/orders/{id}", pt.GetOrder)
		}
	})
	return r
}

// mtlsCertBinding enforces that the verified client certificate matches the
// signed-JWT appliance identity, and that the certificate is not revoked.
// Runs AFTER RequireAppliance (so the JWT ident is in context).
func mtlsCertBinding(db *pgxpool.Pool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ident := auth.ApplianceFromContext(r.Context())
			if ident == nil {
				Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
				return
			}
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "client certificate required")
				return
			}
			cert := r.TLS.PeerCertificates[0]
			certAppID := pki.ApplianceIDFromCert(cert)
			if certAppID != ident.ApplianceID {
				Fail(w, r, http.StatusForbidden, CodeForbidden, "client certificate does not match appliance identity")
				return
			}
			fpr := pki.FingerprintHex(cert)
			// The certificate must be one Central issued to THIS appliance and still in service: the current one,
			// or one superseded by a rotation within SupersededOverlap. A CA-valid certificate that was superseded
			// long ago, revoked, or never recorded is refused -- before, only an explicit revocation row was.
			var usable bool
			if err := db.QueryRow(r.Context(), `
                SELECT EXISTS (SELECT 1 FROM appliance_certificates c
                                WHERE c.appliance_id::text=$1 AND c.fingerprint_sha256=$2
                                  AND (c.status='active' OR (c.status='superseded' AND c.superseded_at > now() - make_interval(secs => $3))))
                   AND NOT EXISTS (SELECT 1 FROM appliance_certificate_revocations WHERE fingerprint_sha256=$2)`,
				ident.ApplianceID, fpr, SupersededOverlap.Seconds()).Scan(&usable); err != nil {
				Fail(w, r, http.StatusServiceUnavailable, "unavailable", "certificate check failed; retry")
				return
			}
			if !usable {
				Fail(w, r, http.StatusForbidden, CodeForbidden, "client certificate revoked or no longer in service")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
