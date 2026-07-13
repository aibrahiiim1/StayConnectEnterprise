// Package-level file: appliance enrollment + bootstrap-token CRUD.
//
// This covers three endpoints with very different auth stances:
//
//  1. /v1/appliance-bootstrap-tokens (operator-authenticated CRUD) — admin
//     UI mints / lists / revokes one-time tokens. Plaintext is returned
//     exactly once at create time.
//  2. /v1/appliances/enroll (PUBLIC) — scd's first-boot POST. Verifies the
//     token hash, binds the public key, returns identity.
//  3. /v1/appliance/hello (appliance-JWT-authenticated) — proves the signed
//     auth loop works. Phase 5.2 replaces this stub with real RPC endpoints.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

type EnrollmentBase struct {
	*Base
	ReplayCache *applianceauth.ReplayCache
}

// ---- Bootstrap token CRUD (operator-facing) ----

type BootstrapToken struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	SiteID          string     `json:"site_id"`
	ExpectedSerial  string     `json:"expected_serial,omitempty"`
	TokenHint       string     `json:"token_hint"`
	CreatedBy       string     `json:"created_by,omitempty"`
	ExpiresAt       time.Time  `json:"expires_at"`
	ConsumedAt      *time.Time `json:"consumed_at,omitempty"`
	ConsumedByAppID string     `json:"consumed_by_appliance,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
}

type tokenCreateReq struct {
	SiteID         string `json:"site_id"`
	ExpectedSerial string `json:"expected_serial"`
	TTLHours       int    `json:"ttl_hours"`
}

type tokenCreateResp struct {
	Token string          `json:"token"` // plaintext; returned ONCE at create
	Row   *BootstrapToken `json:"row"`
}

func (b *EnrollmentBase) TokenRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(auth.RequireTenant)
	// Enrollment tokens are a Platform enrollment control. Tenants may view
	// (own scope) but only Platform may mint or revoke them.
	r.With(auth.RequirePermission("platform.appliances.view")).Get("/", b.listTokens)
	r.With(auth.RequirePermission("platform.enrollment_tokens.create")).Post("/", b.createToken)
	r.With(auth.RequirePermission("platform.enrollment_tokens.revoke")).Delete("/{id}", b.revokeToken)
	return r
}

func (b *EnrollmentBase) listTokens(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT id, tenant_id, site_id, COALESCE(expected_serial,''), token_hint,
               COALESCE(created_by::text, ''), expires_at, consumed_at,
               COALESCE(consumed_by_appliance::text, ''), created_at
          FROM appliance_bootstrap_tokens
         WHERE tenant_id = $1
         ORDER BY created_at DESC
    `, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	var out []BootstrapToken
	for rows.Next() {
		var t BootstrapToken
		var consumed sql.NullTime
		if err := rows.Scan(&t.ID, &t.TenantID, &t.SiteID, &t.ExpectedSerial, &t.TokenHint,
			&t.CreatedBy, &t.ExpiresAt, &consumed, &t.ConsumedByAppID, &t.CreatedAt); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "scan failed")
			return
		}
		if consumed.Valid {
			t.ConsumedAt = &consumed.Time
		}
		out = append(out, t)
	}
	WriteList(w, out, ListMeta{})
}

func (b *EnrollmentBase) createToken(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	var req tokenCreateReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	if req.SiteID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site_id required")
		return
	}
	if req.TTLHours <= 0 || req.TTLHours > 7*24 {
		req.TTLHours = 24
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// Belt-and-braces: site must belong to this tenant.
	var ok int
	if err := b.DB.QueryRow(ctx,
		`SELECT count(*) FROM sites WHERE id = $1 AND tenant_id = $2`,
		req.SiteID, tenantID).Scan(&ok); err != nil || ok == 0 {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "site not in tenant")
		return
	}

	plaintext, err := mintTokenPlaintext()
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "token mint failed")
		return
	}
	sum := sha256.Sum256([]byte(plaintext))
	hint := plaintext[len(plaintext)-4:]
	expires := time.Now().Add(time.Duration(req.TTLHours) * time.Hour)

	sess := auth.FromContext(r.Context())
	var createdBy any
	if sess != nil {
		createdBy = sess.OperatorID
	}

	var expectedSerial any
	if s := strings.TrimSpace(req.ExpectedSerial); s != "" {
		expectedSerial = s
	}

	var id string
	err = b.DB.QueryRow(ctx, `
        INSERT INTO appliance_bootstrap_tokens(
            tenant_id, site_id, expected_serial, token_hash, token_hint,
            created_by, expires_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING id
    `, tenantID, req.SiteID, expectedSerial, sum[:], hint, createdBy, expires).Scan(&id)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "insert failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "appliance_bootstrap_token.created",
		"bootstrap_token", id, map[string]any{
			"_tenant_id": tenantID, "site_id": req.SiteID, "serial": req.ExpectedSerial,
		})
	row := BootstrapToken{
		ID: id, TenantID: tenantID, SiteID: req.SiteID,
		ExpectedSerial: req.ExpectedSerial, TokenHint: hint,
		ExpiresAt: expires, CreatedAt: time.Now(),
	}
	WriteJSON(w, http.StatusCreated, tokenCreateResp{Token: plaintext, Row: &row})
}

func (b *EnrollmentBase) revokeToken(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.EffectiveTenantID(r)
	id := chi.URLParam(r, "id")
	ctx, cancel := DBCtx(r)
	defer cancel()
	tag, err := b.DB.Exec(ctx, `
        DELETE FROM appliance_bootstrap_tokens
         WHERE id = $1 AND tenant_id = $2 AND consumed_at IS NULL
    `, id, tenantID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "delete failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "token not found or already consumed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "appliance_bootstrap_token.revoked",
		"bootstrap_token", id, map[string]any{"_tenant_id": tenantID})
	w.WriteHeader(http.StatusNoContent)
}

// OfflineReconcile (appliance-authed) marks an offline activation package as
// consumed/reconciled centrally. Idempotent: repeating it never creates a
// duplicate record and never re-activates. The package must belong to the
// authenticated appliance.
func (b *EnrollmentBase) OfflineReconcile(w http.ResponseWriter, r *http.Request) {
	ident := auth.ApplianceFromContext(r.Context())
	if ident == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	var in struct {
		PackageID string `json:"package_id"`
	}
	if err := DecodeJSON(r, &in); err != nil || in.PackageID == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "package_id required")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Idempotent: set consumed_at once, refresh reconciled_at; scope to the
	// authenticated appliance so one appliance can't reconcile another's.
	tag, err := b.DB.Exec(ctx, `
        UPDATE offline_activation_packages
           SET consumed_at = COALESCE(consumed_at, now()), reconciled_at = now()
         WHERE package_id = $1 AND appliance_id = $2`, in.PackageID, ident.ApplianceID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "reconcile failed")
		return
	}
	if tag.RowsAffected() == 0 {
		// Unknown/foreign package — do not create anything.
		WriteJSON(w, http.StatusOK, map[string]any{"status": "no_match"})
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"status": "reconciled", "package_id": in.PackageID})
}

// auditEnrollFail records a rejected enrollment attempt to the immutable audit
// log so brute-force / misconfiguration is visible. The reason is coarse on
// the wire (opaque "enrollment rejected") but precise in the audit trail.
func auditEnrollFail(ctx context.Context, db *pgxpool.Pool, r *http.Request, serial, reason string) {
	audit.Emit(ctx, db, audit.Entry{
		ActorType:  "appliance",
		Action:     "appliance.enroll_rejected",
		TargetType: "appliance",
		IP:         clientIP(r),
		UserAgent:  r.UserAgent(),
		Payload:    map[string]any{"serial": serial, "reason": reason},
	})
}

// ---- Public enrollment endpoint ----

type enrollReq struct {
	BootstrapToken      string `json:"bootstrap_token"`
	Serial              string `json:"serial"`
	PublicKey           string `json:"public_key"` // base64-raw Ed25519 (32 bytes)
	WANMAC              string `json:"wan_mac"`
	LANMAC              string `json:"lan_mac"`
	HardwareFingerprint string `json:"hardware_fingerprint"`
	Hostname            string `json:"hostname"`
	Model               string `json:"model"`
}
type enrollResp struct {
	ApplianceID string `json:"appliance_id"`
	TenantID    string `json:"tenant_id"`
	SiteID      string `json:"site_id"`
	Serial      string `json:"serial"`
}

// EnrollHandler is PUBLIC — scd calls it before any other endpoint. On any
// rejection we return the same opaque "enrollment rejected" so scanners
// can't tell expired-token from wrong-serial.
func (b *EnrollmentBase) EnrollHandler(w http.ResponseWriter, r *http.Request) {
	var req enrollReq
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bad body")
		return
	}
	req.BootstrapToken = strings.TrimSpace(req.BootstrapToken)
	req.Serial = strings.TrimSpace(req.Serial)
	req.PublicKey = strings.TrimSpace(req.PublicKey)
	if req.BootstrapToken == "" || req.Serial == "" || req.PublicKey == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "bootstrap_token, serial, public_key required")
		return
	}
	if _, err := base64.RawStdEncoding.DecodeString(req.PublicKey); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "public_key must be base64 (no padding)")
		return
	}

	sum := sha256.Sum256([]byte(req.BootstrapToken))

	ctx, cancel := DBCtx(r)
	defer cancel()

	tx, err := b.DB.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "tx failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		tokID, tenantID, siteID, expectedSerial string
		expiresAt                               time.Time
		consumedAt                              sql.NullTime
		consumedBy                              sql.NullString
	)
	err = tx.QueryRow(ctx, `
        SELECT id, tenant_id::text, site_id::text, COALESCE(expected_serial,''),
               expires_at, consumed_at, consumed_by_appliance::text
          FROM appliance_bootstrap_tokens
         WHERE token_hash = $1
         FOR UPDATE
    `, sum[:]).Scan(&tokID, &tenantID, &siteID, &expectedSerial, &expiresAt, &consumedAt, &consumedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		auditEnrollFail(ctx, b.DB, r, req.Serial, "unknown token")
		Fail(w, r, http.StatusForbidden, CodeForbidden, "enrollment rejected")
		return
	}
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	}
	if time.Now().After(expiresAt) {
		auditEnrollFail(ctx, b.DB, r, req.Serial, "expired token")
		Fail(w, r, http.StatusForbidden, CodeForbidden, "enrollment rejected")
		return
	}
	if consumedAt.Valid {
		// Idempotent retry: if THIS token already enrolled an appliance with
		// the same serial AND same public key, return that result rather than
		// rejecting — a network blip that dropped the original 200 must not
		// strand the installer. Any other reuse is a single-use violation.
		if consumedBy.Valid {
			var exSerial, exPK, exSite string
			if e2 := tx.QueryRow(ctx,
				`SELECT serial, COALESCE(public_key,''), COALESCE(site_id::text,'') FROM appliances WHERE id=$1`,
				consumedBy.String).Scan(&exSerial, &exPK, &exSite); e2 == nil &&
				exSerial == req.Serial && exPK == req.PublicKey {
				_ = tx.Commit(ctx)
				WriteJSON(w, http.StatusOK, enrollResp{
					ApplianceID: consumedBy.String, TenantID: tenantID, SiteID: exSite, Serial: req.Serial,
				})
				return
			}
		}
		auditEnrollFail(ctx, b.DB, r, req.Serial, "token already consumed")
		Fail(w, r, http.StatusForbidden, CodeForbidden, "enrollment rejected")
		return
	}
	if expectedSerial != "" && expectedSerial != req.Serial {
		auditEnrollFail(ctx, b.DB, r, req.Serial, "serial mismatch")
		Fail(w, r, http.StatusForbidden, CodeForbidden, "enrollment rejected")
		return
	}

	// Find-or-create the appliance row by serial within this tenant.
	var appID, existingPK string
	err = tx.QueryRow(ctx,
		`SELECT id, COALESCE(public_key,'') FROM appliances WHERE tenant_id = $1 AND serial = $2`,
		tenantID, req.Serial).Scan(&appID, &existingPK)
	if errors.Is(err, pgx.ErrNoRows) {
		// Duplicate-key clone defense: the SAME public key must not appear on a
		// DIFFERENT appliance identity. If it does, this is a copied identity
		// directory / cloned key — alert and reject rather than create a twin.
		var dupID string
		if e := tx.QueryRow(ctx,
			`SELECT id::text FROM appliances WHERE public_key = $1 AND public_key <> '' LIMIT 1`,
			req.PublicKey).Scan(&dupID); e == nil && dupID != "" {
			_, _ = tx.Exec(ctx, `
                INSERT INTO appliance_security_alerts(appliance_id, serial, kind, detail, source_ip)
                VALUES ($1, $2, 'duplicate_public_key', $3::jsonb, $4)`,
				dupID, req.Serial, `{"reason":"public key already bound to another appliance (possible clone)"}`, clientIP(r))
			_ = tx.Commit(ctx)
			auditEnrollFail(ctx, b.DB, r, req.Serial, "duplicate public key")
			Fail(w, r, http.StatusForbidden, CodeForbidden, "enrollment rejected: duplicate identity key")
			return
		}
		// New appliance → pending_approval under the token's tenant/site until a
		// Platform operator approves/assigns it.
		err = tx.QueryRow(ctx, `
            INSERT INTO appliances(tenant_id, site_id, serial, name, status, lifecycle_state, public_key,
                                   wan_mac, lan_mac, hardware_fingerprint, hostname, model,
                                   enrolled_at, first_seen_at, last_seen_at, last_public_ip)
            VALUES ($1, $2, $3, $3, 'pending', 'pending_approval', $4,
                    NULLIF($6,''), NULLIF($7,''), NULLIF($8,''), NULLIF($9,''), NULLIF($10,''),
                    now(), now(), now(), $5)
            RETURNING id
        `, tenantID, siteID, req.Serial, req.PublicKey, clientIP(r),
			req.WANMAC, req.LANMAC, req.HardwareFingerprint, req.Hostname, req.Model).Scan(&appID)
		if err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "create appliance failed")
			return
		}
		recordLifecycle(ctx, tx, appID, "installed_unenrolled", "pending_approval", "appliance", clientIP(r), "token enrollment")
	} else if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup appliance failed")
		return
	} else {
		// CLONE / IDENTITY-MISMATCH protection: an already-enrolled appliance that
		// re-presents with a DIFFERENT public key is a possible clone / copied
		// identity / reverted snapshot. Alert + reject; never silently overwrite.
		if existingPK != "" && existingPK != req.PublicKey {
			_, _ = tx.Exec(ctx, `
                INSERT INTO appliance_security_alerts(appliance_id, serial, kind, detail, source_ip)
                VALUES ($1, $2, 'identity_mismatch', $3::jsonb, $4)`,
				appID, req.Serial, `{"reason":"public_key changed on re-enroll (possible clone)"}`, clientIP(r))
			recordLifecycle(ctx, tx, appID, "", "suspended", "system", clientIP(r), "clone/identity mismatch")
			_ = tx.Commit(ctx) // persist the alert + event
			Fail(w, r, http.StatusForbidden, CodeForbidden, "enrollment rejected: appliance identity mismatch (possible clone)")
			return
		}
		if _, err := tx.Exec(ctx, `
            UPDATE appliances SET
                site_id     = $2,
                public_key  = $3,
                status      = 'enrolled',
                enrolled_at = now(),
                last_public_ip = $4,
                wan_mac     = COALESCE(NULLIF($5,''), wan_mac),
                lan_mac     = COALESCE(NULLIF($6,''), lan_mac),
                hardware_fingerprint = COALESCE(NULLIF($7,''), hardware_fingerprint),
                hostname    = COALESCE(NULLIF($8,''), hostname),
                model       = COALESCE(NULLIF($9,''), model),
                last_seen_at = now(),
                updated_at  = now()
             WHERE id = $1
        `, appID, siteID, req.PublicKey, clientIP(r),
			req.WANMAC, req.LANMAC, req.HardwareFingerprint, req.Hostname, req.Model); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "update appliance failed")
			return
		}
	}

	if _, err := tx.Exec(ctx, `
        UPDATE appliance_bootstrap_tokens
           SET consumed_at = now(), consumed_by_appliance = $2
         WHERE id = $1
    `, tokID, appID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "consume failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "commit failed")
		return
	}

	// Best-effort audit after commit.
	audit.Op(r.Context(), b.DB, r, "appliance.enrolled", "appliance", appID, map[string]any{
		"_tenant_id": tenantID, "serial": req.Serial, "site_id": siteID, "token_id": tokID,
	})

	WriteJSON(w, http.StatusOK, enrollResp{
		ApplianceID: appID, TenantID: tenantID, SiteID: siteID, Serial: req.Serial,
	})
}

// ---- Signed hello endpoint (appliance-JWT-authenticated) ----

func (b *EnrollmentBase) HelloHandler(w http.ResponseWriter, r *http.Request) {
	a := auth.ApplianceFromContext(r.Context())
	if a == nil {
		Fail(w, r, http.StatusUnauthorized, CodeUnauthenticated, "no appliance context")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"appliance_id": a.ApplianceID,
		"tenant_id":    a.TenantID,
		"site_id":      a.SiteID,
		"serial":       a.Serial,
		"server_time":  time.Now().UTC().Format(time.RFC3339),
	})
}

// ---- Token plaintext generation ----

// mintTokenPlaintext returns a 32-character base32 token (160 bits of
// entropy). Ops usually copy-paste it into the appliance's scd.env; it
// doesn't need to be human-friendly so stdlib base32 is fine.
func mintTokenPlaintext() (string, error) {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]), nil
}
