package auth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/clientip"
	"github.com/stayconnect/enterprise/control-plane/internal/ratebudget"
)

// maxSignedBody bounds the request body buffered to hash for signature binding.
const maxSignedBody = 1 << 20

// ApplianceIdent is what RequireAppliance attaches to the request context.
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

// ContextWithAppliance attaches an identity for callers that drive a handler without the middleware (tests).
func ContextWithAppliance(ctx context.Context, a *ApplianceIdent) context.Context {
	return context.WithValue(ctx, applianceCtxKey, a)
}

// RequireAppliance verifies the Ed25519-signed request JWT (see applianceauth), blocks replay via the shared
// ReplayCache, rejects retired identities, and audits failed attempts.
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
			// An issuer that is not a uuid names no appliance: "not enrolled", audited, never a 500 from the
			// uuid cast (audit Phase 1 L12).
			if !uuidRe.MatchString(iss) {
				applianceAuthFail(r.Context(), db, r, "", "not enrolled (issuer is not an appliance id)")
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "appliance not enrolled", r)
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
				// WIRE CONTRACT: deployed appliances detect that their record was deleted by a 401 whose body
				// says "not enrolled" (data-plane cmd/scd orphan recovery). Keep the wording.
				applianceAuthFail(r.Context(), db, r, iss, "not enrolled")
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "appliance not enrolled", r)
				return
			}
			if err != nil {
				jsonErr(w, http.StatusInternalServerError, "internal", "appliance lookup failed", r)
				return
			}
			// Only a retired identity is refused. Licence state is deliberately not consulted: a suspended or revoked
			// licence must not cut the appliance off, or it could not fetch the suspension or revocation list.
			// Retired appliances learn of it through the certificate-only assignment channel.
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
				if errors.Is(err, applianceauth.ErrReplayCacheFull) {
					jsonErr(w, http.StatusServiceUnavailable, "busy", "Central is busy; try again shortly", r)
					return
				}
				applianceAuthFail(r.Context(), db, r, iss, "replay detected")
				jsonErr(w, http.StatusUnauthorized, "unauthenticated", "replay detected", r)
				return
			}
			// last_seen_at is read to the minute; writing it on every call churned the table (perf audit D9-L3).
			_, _ = db.Exec(r.Context(),
				`UPDATE appliances SET last_seen_at = now(),
				        version = COALESCE(NULLIF($2,''), version)
				  WHERE id = $1 AND (last_seen_at IS NULL OR last_seen_at < now() - interval '60 seconds'
				                     OR version IS DISTINCT FROM COALESCE(NULLIF($2,''), version))`,
				iss, claims.Ver)
			ident := &ApplianceIdent{
				ApplianceID: iss, TenantID: tenantID, SiteID: siteID, Serial: serial, Version: claims.Ver,
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), applianceCtxKey, ident)))
		})
	}
}

// applianceAuthFailures bounds failed-authentication audit rows, since these routes are reachable by anyone: 5 a
// minute per client address, 300 overall; suppressed ones are recorded as one appliance.auth_failures_suppressed row.
var applianceAuthFailures = ratebudget.New(5, 300, time.Minute)

// applianceAuthFail records a failed authentication within applianceAuthFailures. It writes directly, not via
// internal/audit, to avoid an import cycle, and never fails the request path.
func applianceAuthFail(ctx context.Context, db *pgxpool.Pool, r *http.Request, applianceID, reason string) {
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ok, suppressed := applianceAuthFailures.Allow(clientip.From(r))
	if suppressed > 0 {
		_, _ = db.Exec(wctx, `INSERT INTO audit_log (ts, actor_type, action, payload)
            VALUES (now(), 'system', 'appliance.auth_failures_suppressed', jsonb_build_object('count', $1::int, 'window_seconds', 60))`,
			suppressed)
	}
	if !ok {
		return
	}
	_, _ = db.Exec(wctx, `
        INSERT INTO audit_log (ts, actor_type, actor_id, action, target_type, target_id, ip, user_agent, payload)
        VALUES (now(), 'appliance', NULLIF($1,''), 'appliance.auth_failed', 'appliance', NULLIF($1,''),
                CASE WHEN $2 = '' THEN NULL ELSE $2::inet END, NULLIF($3,''), $4::jsonb)`,
		applianceID, clientip.From(r), r.UserAgent(), authFailPayload(reason, r.Method, r.URL.Path))
}

// authFailPayload is built with encoding/json, which escapes every control character; the hand-written escaper
// it replaces did not, and the row was dropped (audit Phase 1 L12).
func authFailPayload(reason, method, path string) string {
	b, _ := json.Marshal(map[string]string{"reason": reason, "method": method, "path": path})
	return string(b)
}

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func bearerFrom(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// peekIss reads the `iss` claim without verifying it, only to select the public key to verify against.
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
