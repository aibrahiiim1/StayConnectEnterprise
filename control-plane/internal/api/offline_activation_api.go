package api

// OFFLINE FIRST ACTIVATION — the Central half.
//
//	POST /cloud/v1/offline-activation/requests            import an appliance's activation request
//	POST /cloud/v1/offline-activation/{applianceID}/package   mint the signed activation package
//
// This is separate from /cloud/v1/offline-packages, which ships a licence to an appliance that is already
// assigned. That endpoint and its envelope are untouched.
//
// WHAT AN IMPORTED REQUEST MAY AND MAY NOT DO
//
// It may create a PENDING appliance and nothing more. It carries no authority over customer or site: the
// operator chooses those in the UI, and the binding is made real by the SIGNED ASSIGNMENT, which is minted
// by the same assignment authority the online path uses. A file on a USB stick can announce a box exists; it
// cannot decide whose hotel it belongs to.
//
// A request is accepted only if it is self-signed by the key it carries, which is what distinguishes an
// appliance's own evidence from a file somebody typed.

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/activation"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/auth"
)

// OfflineActivationBase mints first-activation packages.
type OfflineActivationBase struct {
	*Base
	assign *AssignmentBase
	priv   ed25519.PrivateKey
	caPEM  string
	// centralBase is the APPLIANCE-FACING endpoint carried into the package so an offline appliance learns
	// where to reconcile without anyone typing an address into it. It is a stable HTTPS FQDN, never an IP:
	// an appliance that hard-codes today's address cannot be moved to cloud hosting later.
	centralBase string
}

// NewOfflineActivationBase returns nil when the vendor signing key is unavailable, which disables the
// endpoints rather than serving unsigned packages.
func NewOfflineActivationBase(base *Base, assign *AssignmentBase, vendorKeyPath, caBundlePath,
	centralBase string) *OfflineActivationBase {
	raw, err := os.ReadFile(vendorKeyPath)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil
	}
	ca, _ := os.ReadFile(caBundlePath)
	return &OfflineActivationBase{Base: base, assign: assign, priv: ed25519.PrivateKey(raw),
		caPEM: string(ca), centralBase: centralBase}
}

func (b *OfflineActivationBase) Routes() http.Handler {
	r := chi.NewRouter()
	reauth := RequireReauth(b.Redis)
	r.With(auth.RequirePermission("platform.appliances.manage")).Post("/requests", b.importRequest)
	r.With(auth.RequirePermission("platform.certificates.issue"), reauth).
		Post("/{applianceID}/package", b.generatePackage)
	return r
}

// importRequest verifies an activation request and creates (or finds) the PENDING appliance it describes.
func (b *OfflineActivationBase) importRequest(w http.ResponseWriter, r *http.Request) {
	var req activation.Request
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "that file is not an activation request")
		return
	}
	if req.SchemaVersion != activation.SchemaVersion {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest,
			"this activation request uses an unsupported version")
		return
	}
	// PROOF OF POSSESSION. Without this an operator could be handed another appliance's hardware facts with
	// an attacker's public key, and Central would bind the package to a key the attacker holds.
	if !activation.VerifyRequest(&req) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest,
			"the activation request is not signed by the key it carries")
		return
	}
	if req.Serial == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the activation request carries no serial")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// An appliance already known by this serial is reused rather than duplicated — the same rule the online
	// token-less registration follows.
	var appID, status, existingPub string
	err := b.DB.QueryRow(ctx,
		`SELECT id::text, status, COALESCE(public_key,'') FROM appliances WHERE serial=$1`, req.Serial).
		Scan(&appID, &status, &existingPub)
	switch {
	case err == nil && existingPub != "" && existingPub != req.PublicKey:
		// A different key claiming an existing serial is a clone or a rebuild, and either way it is not
		// something an offline import may resolve on its own.
		Fail(w, r, http.StatusConflict, CodeConflict,
			"an appliance with this serial is already registered with a different identity key")
		return
	case err == nil:
		if existingPub == "" {
			_, _ = b.DB.Exec(ctx, `UPDATE appliances SET public_key=$2, wan_mac=NULLIF($3,''),
                lan_mac=NULLIF($4,''), hardware_fingerprint=NULLIF($5,''), hostname=NULLIF($6,''),
                model=NULLIF($7,''), updated_at=now() WHERE id=$1`,
				appID, req.PublicKey, req.WANMAC, req.LANMAC, req.HardwareFpr, req.Hostname, req.Model)
		}
	default:
		// Brand new → PENDING, unassigned. Exactly what the online self-registration produces.
		if err := b.DB.QueryRow(ctx, `
            INSERT INTO appliances(serial, name, status, lifecycle_state, public_key,
                                   wan_mac, lan_mac, hardware_fingerprint, hostname, model,
                                   enrolled_at, first_seen_at, last_seen_at)
            VALUES ($1, $1, 'pending', 'pending_approval', $2,
                    NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''),
                    now(), now(), now())
            RETURNING id::text`,
			req.Serial, req.PublicKey, req.WANMAC, req.LANMAC, req.HardwareFpr, req.Hostname, req.Model).
			Scan(&appID); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "could not register the appliance")
			return
		}
	}
	// Remember the request so the package can be bound to it, and so a replayed request cannot quietly
	// change the nonce a package was issued against. The table is declared by migration 0044, not created
	// here: a schema that appears on first use is invisible to anyone reading the migrations and absent on a
	// database restored from before the first import.
	if _, err := b.DB.Exec(ctx, `
        INSERT INTO offline_activation_requests (request_id, appliance_id, serial, public_key, nonce, imported_by)
        VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid) ON CONFLICT (request_id) DO NOTHING`,
		req.RequestID, appID, req.Serial, req.PublicKey, req.Nonce, operatorIDOf(r)); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "could not record the request")
		return
	}
	audit.Op(r.Context(), b.DB, r, "offline_activation.request_imported", "appliance", appID,
		map[string]any{"serial": req.Serial, "request_id": req.RequestID})
	WriteJSON(w, http.StatusOK, map[string]any{
		"appliance_id": appID, "serial": req.Serial, "request_id": req.RequestID,
		"status": "pending_activation",
		"note":   "the appliance is registered as pending; choose customer, site and licence terms, then generate the package",
	})
}

// generatePackage mints the one file that completes a first activation.
func (b *OfflineActivationBase) generatePackage(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "applianceID")
	var body struct {
		RequestID  string `json:"request_id"`
		ValidHours int    `json:"valid_hours"`
	}
	_ = DecodeJSON(r, &body)
	ctx, cancel := DBCtx(r)
	defer cancel()

	// The request being answered. Binding the package to it is what stops a package issued for one physical
	// box being applied to another that happens to share a serial.
	var reqID, reqNonce, pubKey, serial string
	q := `SELECT request_id, nonce, public_key, serial FROM offline_activation_requests
           WHERE appliance_id=$1 AND consumed_at IS NULL`
	args := []any{appID}
	if body.RequestID != "" {
		q += ` AND request_id=$2`
		args = append(args, body.RequestID)
	}
	q += ` ORDER BY imported_at DESC LIMIT 1`
	if err := b.DB.QueryRow(ctx, q, args...).Scan(&reqID, &reqNonce, &pubKey, &serial); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound,
			"no outstanding activation request for this appliance — import the request file first")
		return
	}
	// Customer and site come from the appliance record the operator has just assigned, never from the file.
	var tenantID, siteID string
	if err := b.DB.QueryRow(ctx,
		`SELECT COALESCE(tenant_id::text,''), COALESCE(site_id::text,'') FROM appliances WHERE id=$1`,
		appID).Scan(&tenantID, &siteID); err != nil {
		Fail(w, r, http.StatusNotFound, CodeNotFound, "appliance not found")
		return
	}
	if tenantID == "" || siteID == "" {
		Fail(w, r, http.StatusConflict, CodeConflict,
			"choose a customer and site for this appliance before generating its activation package")
		return
	}
	// THE SIGNED ASSIGNMENT, from the same authority the online path uses. This is what actually binds the
	// appliance to a hotel; the envelope only carries it.
	if b.assign == nil {
		Fail(w, r, http.StatusServiceUnavailable, CodeInternal, "assignment signing is unavailable")
		return
	}
	doc, err := b.assign.Issue(ctx, appID, "assigned")
	if err != nil || doc == nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal,
			"could not issue the signed assignment: "+errText(err))
		return
	}
	asgRaw, _ := json.Marshal(doc)

	var licEnv string
	_ = b.DB.QueryRow(ctx,
		`SELECT signed_envelope FROM licenses WHERE site_id=$1 AND status IN ('active','suspended')
          ORDER BY issued_at DESC LIMIT 1`, siteID).Scan(&licEnv)
	if licEnv == "" {
		Fail(w, r, http.StatusConflict, CodeConflict,
			"this site has no signed licence yet — issue one before generating the activation package")
		return
	}
	idFpr := ""
	if raw, err := base64.RawStdEncoding.DecodeString(pubKey); err == nil && len(raw) == ed25519.PublicKeySize {
		idFpr = activation.KeyID(ed25519.PublicKey(raw))
	}
	validHours := 168
	if body.ValidHours > 0 && body.ValidHours <= 8760 {
		validHours = body.ValidHours
	}
	now := time.Now()
	pkg := &activation.Package{
		SchemaVersion:   activation.SchemaVersion,
		PackageID:       newUUIDv4(),
		RequestID:       reqID,
		RequestNonce:    reqNonce,
		ApplianceID:     appID,
		Serial:          serial,
		IdentityKeyFpr:  idFpr,
		TenantID:        tenantID,
		SiteID:          siteID,
		Assignment:      asgRaw,
		LicenseEnvelope: json.RawMessage(licEnv),
		Entitlements:    json.RawMessage(`{}`),
		CABundlePEM:     b.caPEM,
		CentralBase:     b.centralBase,
		IssuedAt:        now.Unix(),
		ExpiresAt:       now.Add(time.Duration(validHours) * time.Hour).Unix(),
		Nonce:           offlineNonce(),
	}
	activation.SignPackage(b.priv, pkg)

	operatorID := ""
	if s := auth.FromContext(r.Context()); s != nil {
		operatorID = s.OperatorID
	}
	if _, err := b.DB.Exec(ctx, `
        INSERT INTO offline_activation_packages (package_id, appliance_id, serial, tenant_id, site_id,
                                                 nonce, signer_key_id, issued_by, expires_at)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,NULLIF($8,'')::uuid,to_timestamp($9))`,
		pkg.PackageID, appID, serial, tenantID, siteID, pkg.Nonce, pkg.SignerKeyID, operatorID,
		pkg.ExpiresAt); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "package store failed: "+err.Error())
		return
	}
	_, _ = b.DB.Exec(ctx,
		`UPDATE offline_activation_requests SET consumed_at=now() WHERE request_id=$1`, reqID)
	audit.Op(r.Context(), b.DB, r, "offline_activation.package_generated", "appliance", appID,
		map[string]any{"package_id": pkg.PackageID, "request_id": reqID, "expires_at": pkg.ExpiresAt})

	WriteJSON(w, http.StatusCreated, map[string]any{"package_id": pkg.PackageID, "package": pkg})
}

func errText(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

// operatorIDOf returns the acting operator, or "" when the session carries none.
func operatorIDOf(r *http.Request) string {
	if s := auth.FromContext(r.Context()); s != nil {
		return s.OperatorID
	}
	return ""
}
