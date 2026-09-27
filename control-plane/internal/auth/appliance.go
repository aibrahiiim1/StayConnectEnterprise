package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
)

// maxSignedBody bounds how much request body we buffer to hash for signature
// binding. Appliance requests are tiny; 1 MiB is a generous ceiling.
const maxSignedBody = 1 << 20

// ApplianceIdent is what RequireAppliance attaches to the request context.
// Handlers that need the appliance's tenant scope read it from here.
type ApplianceIdent struct {
	ApplianceID string
	TenantID    string
	SiteID      string
	Serial      string
	Version     string
}

const applianceCtxKey ctxKey = 2

func ApplianceFromContext(ctx context.Context) *ApplianceIdent {
	a, _ := ctx.Value(applianceCtxKey).(*ApplianceIdent)
	return a
}

// RequireAppliance verifies an Ed25519-signed request JWT that BINDS the
// method, path, body hash, audience and signing-key id (see applianceauth).
// It blocks replay via the shared ReplayCache, rejects suspended/revoked/
// decommissioned identities, and audits every failed attempt.
func RequireAppliance(db *pgxpool.Pool, cache *applianceauth.ReplayCache) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerFrom(r)
			if token == "" {
				applianceAuthFail(r.Context(), db, r, "", "missing appliance JWT")
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "missing appliance JWT", r)
				return
			}
			iss, err := peekIss(token)
			if err != nil {
				applianceAuthFail(r.Context(), db, r, "", "unparseable token")
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "bad appliance JWT", r)
				return
			}
			var pubB64, tenantID, siteID, serial, lifecycle string
			err = db.QueryRow(r.Context(), `
                SELECT COALESCE(public_key,''), COALESCE(tenant_id::text,''), COALESCE(site_id::text,''),
                       serial, COALESCE(lifecycle_state,'')
                  FROM appliances
                 WHERE id = $1
            `, iss).Scan(&pubB64, &tenantID, &siteID, &serial, &lifecycle)
			if errors.Is(err, pgx.ErrNoRows) || (err == nil && pubB64 == "") {
				applianceAuthFail(r.Context(), db, r, iss, "not enrolled")
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "appliance not enrolled", r)
				return
			}
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "appliance lookup failed", r)
				return
			}
			// Identity-level revocation. ONLY a retired identity (revoked / decommissioned) is refused.
			//
			// Licence state is deliberately NOT consulted: a suspended or revoked LICENCE must never cut the
			// appliance off from Central, or it could not fetch the signed suspension or the revocation list
			// that tells it so. Retired appliances learn of their retirement through the certificate-only
			// assignment channel, which does not run this middleware.
			if lifecycle == "revoked" || lifecycle == "decommissioned" {
				applianceAuthFail(r.Context(), db, r, iss, "identity "+lifecycle)
				jsonErr(w, http.StatusForbidden, "forbidden", "appliance identity "+lifecycle, r)
				return
			}
			raw, err := base64.RawStdEncoding.DecodeString(pubB64)
			if err != nil || len(raw) != ed25519.PublicKeySize {
				jsonErr(w, http.StatusInternalServerError, "internal", "appliance key corrupt", r)
				return
			}
			// Buffer + restore the body so we can hash it for binding.
			body, err := io.ReadAll(io.LimitReader(r.Body, maxSignedBody))
			if err != nil {
				jsonErr(w, http.StatusBadRequest, "bad_request", "body read failed", r)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(body)))

			pub := ed25519.PublicKey(raw)
			now := time.Now()
			claims, err := applianceauth.VerifyRequest(token, pub, now, applianceauth.RequestParams{
				Audience: applianceauth.Audience,
				Method:   r.Method,
				Path:     r.URL.Path,
				Body:     body,
				KeyID:    applianceauth.KeyID(pub),
			})
			if err != nil {
				applianceAuthFail(r.Context(), db, r, iss, applianceauth.Describe(err))
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", err.Error(), r)
				return
			}
			if err := cache.Use(claims.Jti, now); err != nil {
				applianceAuthFail(r.Context(), db, r, iss, "replay detected")
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "replay detected", r)
				return
			}
			_, _ = db.Exec(r.Context(),
				`UPDATE appliances SET identity_verified_at = now(), last_seen_at = now(),
				        version = COALESCE(NULLIF($2,''), version) WHERE id = $1`,
				iss, claims.Ver)
			ident := &ApplianceIdent{
				ApplianceID: iss, TenantID: tenantID, SiteID: siteID, Serial: serial, Version: claims.Ver,
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), applianceCtxKey, ident)))
		})
	}
}

// applianceAuthFail records a failed appliance authentication to audit_log.
// Written directly (not via internal/audit) to avoid an auth→audit import
// cycle. Never fails the request path.
func applianceAuthFail(ctx context.Context, db *pgxpool.Pool, r *http.Request, applianceID, reason string) {
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = db.Exec(wctx, `
        INSERT INTO audit_log (ts, actor_type, actor_id, action, target_type, target_id, ip, user_agent, payload)
        VALUES (now(), 'appliance', NULLIF($1,''), 'appliance.auth_failed', 'appliance', NULLIF($1,''),
                CASE WHEN $2 = '' THEN NULL ELSE $2::inet END, NULLIF($3,''), $4::jsonb)`,
		applianceID, clientip.From(r), r.UserAgent(),
		`{"reason":`+jsonString(reason)+`,"method":`+jsonString(r.Method)+`,"path":`+jsonString(r.URL.Path)+`}`)
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func bearerFrom(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// peekIss pulls the `iss` claim out without verifying the signature. Used
// only to select which public key to verify against.
func peekIss(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	s := string(raw)
	i := strings.Index(s, `"iss":"`)
	if i < 0 {
		return "", errors.New("no iss")
	}
	s = s[i+len(`"iss":"`):]
	j := strings.IndexByte(s, '"')
	if j < 0 {
		return "", errors.New("no iss")
	}
	return s[:j], nil
}
