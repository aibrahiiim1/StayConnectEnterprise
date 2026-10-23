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

// ApplianceMTLSRouter builds the appliance API on the mutual-TLS listener. Requests carry both a verified client
// certificate and a signed request JWT, which must agree on the appliance id. ptls may be nil (tests).
func ApplianceMTLSRouter(db *pgxpool.Pool, rdb *redis.Client, replay *applianceauth.ReplayCache, lic *licensing.Service, ca *pki.CA, assignKey, regRoot ed25519.PrivateKey, ptls *portaltls.Service) http.Handler {
	r := chi.NewRouter()

	// The assignment fetch authenticates solely from the verified client certificate (strictMTLSSelf); it
	// deliberately skips RequireAppliance, so no JWT is required or consulted.
	if assignKey != nil {
		assignBase := &AssignmentBase{Base: &Base{DB: db, Redis: rdb}, SignKey: assignKey}
		r.With(RateLimit(rdb, "assignment-fetch", 30, time.Minute)).
			Get("/v1/appliance/assignment", assignBase.StrictApplianceAssignmentHandler)
	}

	// Everything else needs the certificate and a JWT whose issuer equals the certificate's appliance id.
	r.Group(func(r chi.Router) {
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
			// The ack payload is itself signed by the appliance identity key.
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
			// The limits leave room for retries only; the daily order cap is enforced per appliance inside portaltls.
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

// mtlsCertBinding requires the client certificate to match the JWT appliance identity and not be revoked.
// It runs after RequireAppliance.
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
			// Must be the current certificate or one superseded within SupersededOverlap; a CA-valid but old,
			// revoked or unrecorded certificate is refused.
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
