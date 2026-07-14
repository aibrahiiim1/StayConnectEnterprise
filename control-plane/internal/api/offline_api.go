package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
	"github.com/stayconnect/enterprise/control-plane/internal/offline"
)

// OfflineBase generates appliance-specific signed offline activation packages
// (signed with the vendor key the appliance already trusts) and reconciles
// their consumption when the appliance later comes online.
type OfflineBase struct {
	*Base
	priv    ed25519.PrivateKey
	caPEM   string
}

// NewOfflineBase loads the vendor signing key + CA bundle; nil if unavailable.
func NewOfflineBase(base *Base, vendorKeyPath, caBundlePath string) *OfflineBase {
	raw, err := os.ReadFile(vendorKeyPath)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil
	}
	ca, _ := os.ReadFile(caBundlePath)
	return &OfflineBase{Base: base, priv: ed25519.PrivateKey(raw), caPEM: string(ca)}
}

func (b *OfflineBase) Routes() http.Handler {
	r := chi.NewRouter()
	reauth := RequireReauth(b.Redis)
	r.With(auth.RequirePermission("platform.appliances.view")).Get("/", b.list)
	r.With(auth.RequirePermission("platform.certificates.issue"), reauth).Post("/{applianceID}/generate", b.generate)
	return r
}

func (b *OfflineBase) generate(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "applianceID")
	ctx, cancel := DBCtx(r)
	defer cancel()
	// Resolve binding facts from the authoritative DB (never client-supplied).
	var serial, pubKey, tenantID, siteID, certFpr string
	err := b.DB.QueryRow(ctx, `
        SELECT serial, COALESCE(public_key,''), COALESCE(tenant_id::text,''), COALESCE(site_id::text,''),
               COALESCE(current_cert_fingerprint,'')
          FROM appliances WHERE id=$1`, appID).Scan(&serial, &pubKey, &tenantID, &siteID, &certFpr)
	if err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	// current signed license envelope for the site (offline-usable).
	var licEnv string
	b.DB.QueryRow(ctx, `SELECT signed_envelope FROM licenses WHERE site_id=$1 AND status IN ('active','suspended') ORDER BY issued_at DESC LIMIT 1`, siteID).Scan(&licEnv)

	idFpr := ""
	if raw, err := b64raw(pubKey); err == nil && len(raw) == ed25519.PublicKeySize {
		idFpr = offline.KeyID(ed25519.PublicKey(raw))
	}
	nonce := offlineNonce()
	pkgID := newUUIDv4()
	now := time.Now()
	var validHours = 168
	var body struct {
		ValidHours int `json:"valid_hours"`
	}
	_ = DecodeJSON(r, &body)
	if body.ValidHours > 0 && body.ValidHours <= 8760 {
		validHours = body.ValidHours
	}
	pkg := &offline.Package{
		PackageID: pkgID, ApplianceID: appID, Serial: serial,
		IdentityKeyFpr: idFpr, MTLSKeyFpr: certFpr, TenantID: tenantID, SiteID: siteID,
		LicenseEnvelope: rawOrNull(licEnv), Entitlements: json.RawMessage(`{}`),
		CABundlePEM: b.caPEM, IssuedAt: now.Unix(), ExpiresAt: now.Add(time.Duration(validHours) * time.Hour).Unix(),
		Nonce: nonce,
	}
	offline.Sign(b.priv, pkg)

	operatorID := ""
	if s := auth.FromContext(r.Context()); s != nil {
		operatorID = s.OperatorID
	}
	_, err = b.DB.Exec(ctx, `
        INSERT INTO offline_activation_packages (package_id, appliance_id, serial, tenant_id, site_id, nonce, signer_key_id, issued_by, expires_at)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,NULLIF($8,'')::uuid, to_timestamp($9))`,
		pkgID, appID, serial, tenantID, siteID, nonce, pkg.SignerKeyID, operatorID, pkg.ExpiresAt)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "package store failed: "+err.Error())
		return
	}
	audit.Op(r.Context(), b.DB, r, "offline_package.generated", "offline_package", pkgID, map[string]any{
		"appliance_id": appID, "expires_at": pkg.ExpiresAt})
	// Returned ONCE for download; the appliance imports it locally.
	WriteJSON(w, http.StatusCreated, map[string]any{"package_id": pkgID, "package": pkg})
}

func (b *OfflineBase) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	rows, err := b.DB.Query(ctx, `
        SELECT package_id::text, appliance_id::text, serial, issued_at, expires_at, consumed_at, reconciled_at
          FROM offline_activation_packages ORDER BY issued_at DESC LIMIT 200`)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "query failed")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, appID, serial string
		var iss, exp time.Time
		var cons, rec *time.Time
		_ = rows.Scan(&id, &appID, &serial, &iss, &exp, &cons, &rec)
		out = append(out, map[string]any{"package_id": id, "appliance_id": appID, "serial": serial,
			"issued_at": iss, "expires_at": exp, "consumed_at": cons, "reconciled_at": rec})
	}
	WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}

func b64raw(s string) ([]byte, error) { return base64.RawStdEncoding.DecodeString(s) }

func rawOrNull(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage(`null`)
	}
	return json.RawMessage(s)
}

func offlineNonce() string {
	n := 16
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
