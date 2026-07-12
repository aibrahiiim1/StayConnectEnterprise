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
)

// ApplianceMTLSRouter builds the appliance API served over the mutual-TLS
// listener. Every request carries BOTH a verified client certificate
// (transport identity, checked by the TLS layer + this router) AND a signed
// request JWT (application identity, RequireAppliance) — defence in depth. The
// two identities MUST agree: the cert's bound appliance_id must equal the JWT
// issuer, and the client cert must not be revoked.
func ApplianceMTLSRouter(db *pgxpool.Pool, rdb *redis.Client, replay *applianceauth.ReplayCache, lic *licensing.Service, ca *pki.CA, assignKey, regRoot ed25519.PrivateKey) http.Handler {
	r := chi.NewRouter()

	// ---- CERTIFICATE-ONLY channel ----
	// GET /v1/appliance/assignment authenticates SOLELY from the verified client
	// certificate: URI-SAN appliance_id → exact certificate serial → exact
	// certificate fingerprint → Central appliance record (strictMTLSSelf). It does
	// NOT run RequireAppliance, so no appliance JWT, bearer, bootstrap or enrollment
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
		r.Use(auth.RequireAppliance(db, replay))
		r.Use(mtlsCertBinding(db))

		enrollBase := &EnrollmentBase{Base: &Base{DB: db}, ReplayCache: replay}
		r.Get("/v1/appliance/hello", enrollBase.HelloHandler)
		r.Post("/v1/appliance/offline-reconcile", enrollBase.OfflineReconcile)
		if lic != nil {
			licBase := &LicensesBase{Base: &Base{DB: db}, Svc: lic}
			r.Get("/v1/appliance/license", licBase.ApplianceLicenseHandler)
		}
		if assignKey != nil {
			assignBase := &AssignmentBase{Base: &Base{DB: db, Redis: rdb}, SignKey: assignKey}
			// Signed terminal-adoption acknowledgment (Phase-1 completion). The ack
			// payload is itself signed by the appliance identity key; the JWT here is
			// belt-and-braces on the POST.
			r.With(RateLimit(rdb, "assignment-ack", 10, time.Minute)).
				Post("/v1/appliance/assignment/ack", assignBase.AckHandler)
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
			r.Get("/v1/appliance/ca", certBase.CAHandler)
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
			var revoked int
			db.QueryRow(r.Context(), `SELECT count(*) FROM appliance_certificate_revocations WHERE fingerprint_sha256=$1`, fpr).Scan(&revoked)
			if revoked > 0 {
				Fail(w, r, http.StatusForbidden, CodeForbidden, "client certificate revoked")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
