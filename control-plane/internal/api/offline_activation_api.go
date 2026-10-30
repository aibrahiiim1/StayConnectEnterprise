package api

// Offline files for an appliance with no route to Central. Offline activation is the same Activate step as
// online: the imported request makes the appliance WAITING, the operator activates it, and the package carries
// the resulting assignment and licence. An imported request has no authority over customer or site.
//
// The envelopes' signed bytes must match the appliance byte for byte.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/activation"
	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/offline"
)

// OfflineBase mints the offline files; nil (routes answer 503) when the vendor key is missing.
type OfflineBase struct {
	*Base
	priv  ed25519.PrivateKey
	caPEM string
	// centralBase is the appliance-facing endpoint carried into a package (a stable HTTPS FQDN, never an IP).
	// Empty disables activation packages, not offline licences.
	centralBase string
}

// NewOfflineBase returns nil when the vendor key is unavailable. The CA bundle comes from caBundlePath, falling
// back to the appliance CA chain.
func NewOfflineBase(base *Base, vendorKeyPath, caBundlePath, centralBase string) *OfflineBase {
	raw, err := os.ReadFile(vendorKeyPath)
	if err != nil || len(raw) != ed25519.PrivateKeySize {
		return nil
	}
	ca, _ := os.ReadFile(caBundlePath)
	if len(ca) == 0 && base.CA != nil {
		ca = base.CA.CertPEM()
	}
	return &OfflineBase{Base: base, priv: ed25519.PrivateKey(raw), caPEM: string(ca), centralBase: centralBase}
}

func offlineNonce() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func validHours(v int) int {
	if v > 0 && v <= 8760 {
		return v
	}
	return 168
}

// importRequest verifies an activation request and creates (or finds) the WAITING appliance it describes.
func (b *OfflineBase) importRequest(w http.ResponseWriter, r *http.Request) {
	var req activation.Request
	if err := DecodeJSON(r, &req); err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "that file is not an activation request")
		return
	}
	if req.SchemaVersion != activation.SchemaVersion {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "this activation request uses an unsupported version")
		return
	}
	// Proof of possession: otherwise another appliance's hardware facts could arrive with an attacker's key.
	if !activation.VerifyRequest(&req) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the activation request is not signed by the key it carries")
		return
	}
	if req.Serial == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the activation request carries no serial")
		return
	}
	facts, err := applianceFacts{Serial: req.Serial, Hostname: req.Hostname, Model: req.Model,
		WANMAC: req.WANMAC, LANMAC: req.LANMAC, Fingerprint: req.HardwareFpr}.normalize()
	if err != nil {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the activation request is not valid: "+err.Error())
		return
	}
	req.Serial, req.Hostname, req.Model = facts.Serial, facts.Hostname, facts.Model
	req.WANMAC, req.LANMAC, req.HardwareFpr = facts.WANMAC, facts.LANMAC, facts.Fingerprint
	// holds_customer_id is self-signed when present and applied as in an online registration.
	held, ok := normalizeHeldCustomer(req.HoldsCustomerID)
	if !ok {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the activation request carries an invalid holds_customer_id")
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

	// A retired identity cannot be brought back by a file either (see RegisterHandler).
	if retired, err := identityRetired(ctx, b.DB, req.PublicKey); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "lookup failed")
		return
	} else if retired {
		Fail(w, r, http.StatusConflict, "identity_retired",
			"This appliance identity was retired. Factory-reset the appliance and create a new activation request.")
		return
	}
	// AD-1: the operator reads the code off the appliance's own screen and sends it with the file (X-Activation-Code
	// header or activation_code query parameter). The file alone proves only possession of the key it carries.
	code := r.Header.Get("X-Activation-Code")
	if code == "" {
		code = r.URL.Query().Get("activation_code")
	}
	if ref := activationCodeRefusal(req.PublicKey, code); ref != nil {
		audit.Op(r.Context(), b.DB, r, "offline_activation.activation_code_refused", "appliance", "",
			map[string]any{"serial": req.Serial, "supplied": strings.TrimSpace(code) != ""})
		Fail(w, r, ref.Status, ref.Code, ref.Message)
		return
	}
	var appID, lifecycle, existingPub string
	err = b.DB.QueryRow(ctx,
		// Only a live row is this hardware; a retired row with the same serial is history and a new appliance is created.
		`SELECT id::text, lifecycle_state, COALESCE(public_key,'') FROM appliances
          WHERE serial=$1 AND lifecycle_state NOT IN ('revoked','decommissioned')`, req.Serial).
		Scan(&appID, &lifecycle, &existingPub)
	switch {
	case err == nil && existingPub != "" && existingPub != req.PublicKey:
		// A different key claiming an existing serial is a clone or rebuild; an offline import may not resolve that.
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
		if ref := constantAnchorRefusal(req.Serial, req.HardwareFpr); ref != nil { // AD-19
			Fail(w, r, ref.Status, ref.Code, ref.Message)
			return
		}
		if ref := b.registrationCapRefusal(ctx, r, req.Serial, clientIP(r)); ref != nil { // AD-20
			Fail(w, r, ref.Status, ref.Code, ref.Message)
			return
		}
		if err := b.DB.QueryRow(ctx, `
            INSERT INTO appliances(serial, lifecycle_state, public_key,
                                   wan_mac, lan_mac, hardware_fingerprint, hostname, model,
                                   registered_at, first_seen_at)
            VALUES ($1, 'pending_approval', $2,
                    NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), NULLIF($6,''), NULLIF($7,''),
                    now(), now())
            RETURNING id::text`,
			req.Serial, req.PublicKey, req.WANMAC, req.LANMAC, req.HardwareFpr, req.Hostname, req.Model).
			Scan(&appID); err != nil {
			Fail(w, r, http.StatusInternalServerError, CodeInternal, "could not register the appliance")
			return
		}
	}
	if _, err := b.DB.Exec(ctx, `UPDATE appliances SET held_customer_id=NULLIF($2,'')::uuid, updated_at=now() WHERE id=$1`,
		appID, held); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "could not register the appliance")
		return
	}
	operatorID, _ := actorOf(r)
	if _, err := b.DB.Exec(ctx, `
        INSERT INTO offline_activation_requests (request_id, appliance_id, serial, public_key, nonce, imported_by)
        VALUES ($1,$2,$3,$4,$5,NULLIF($6,'')::uuid) ON CONFLICT (request_id) DO NOTHING`,
		req.RequestID, appID, req.Serial, req.PublicKey, req.Nonce, operatorID); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "could not record the request")
		return
	}
	// A request id already recorded for another appliance is a conflict, never a silent success (audit Phase 1 M16).
	var owner string
	if err := b.DB.QueryRow(ctx, `SELECT appliance_id::text FROM offline_activation_requests WHERE request_id=$1`, req.RequestID).
		Scan(&owner); err != nil {
		failInternal(w, r, "could not record the request", err)
		return
	}
	if owner != appID {
		Fail(w, r, http.StatusConflict, CodeConflict, "this activation request id is already recorded for another appliance")
		return
	}
	audit.Op(r.Context(), b.DB, r, "offline_activation.request_imported", "appliance", appID,
		map[string]any{"serial": req.Serial, "request_id": req.RequestID, "holds_customer_id": held})
	b.writeAppliance(w, r, http.StatusOK, appID)
}

// activationPackage mints the file that completes an offline first activation of an already-activated appliance.
func (b *OfflineBase) activationPackage(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	var body struct {
		RequestID  string `json:"request_id"`
		ValidHours int    `json:"valid_hours"`
	}
	_ = DecodeJSON(r, &body)
	if b.centralBase == "" {
		Fail(w, r, http.StatusServiceUnavailable, "offline_activation_unavailable",
			"offline activation is disabled: CTRLAPI_APPLIANCE_BASE is not set")
		return
	}
	row, ok := b.loadOne(w, r, Scope{All: true}, appID)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" || row.Activation == ActivationRetiring {
		invalidState(w, r, "activate the appliance first; the package carries what activation issued", row)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()

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
	var curPub string
	_ = b.DB.QueryRow(ctx, `SELECT COALESCE(public_key,'') FROM appliances WHERE id=$1`, appID).Scan(&curPub)
	if curPub != pubKey {
		Fail(w, r, http.StatusConflict, CodeConflict, "the activation request was made by a different identity than the appliance now holds")
		return
	}

	var asgRaw []byte
	if err := b.DB.QueryRow(ctx, `SELECT signed_doc FROM appliance_signed_assignments WHERE appliance_id=$1`, appID).Scan(&asgRaw); err != nil {
		Fail(w, r, http.StatusConflict, CodeConflict, "the appliance has no signed assignment; activate it first")
		return
	}
	var asg assignment.Document
	if json.Unmarshal(asgRaw, &asg) != nil || !assignment.Grants(asg.State) ||
		asg.TenantID != deref(row.CustomerID) || asg.SiteID != deref(row.SiteID) {
		Fail(w, r, http.StatusConflict, CodeConflict, "the current signed assignment does not match the appliance's activation")
		return
	}
	asgRaw, _ = json.Marshal(&asg) // the document exactly as Issue produced it (jsonb reorders keys)
	// The appliance's own current licence, never another's at the same site.
	licEnv, ok := b.currentEnvelope(w, r, appID)
	if !ok {
		return
	}
	idFpr := ""
	if raw, err := base64.RawStdEncoding.DecodeString(pubKey); err == nil && len(raw) == ed25519.PublicKeySize {
		idFpr = activation.KeyID(ed25519.PublicKey(raw))
	}
	// The CURRENT signed registry, as served to online appliances: a never-online appliance can verify its assignment only if it arrives here.
	// No registry signed yet (registry signing disabled) leaves the field out; any other failure refuses rather than issue a package the
	// appliance may be unable to verify.
	var regDoc json.RawMessage
	switch env, err := currentRegistryEnvelope(ctx, b.DB); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		Fail(w, r, http.StatusServiceUnavailable, "registry_unavailable", "Central could not read the assignment trust registry. Try again later.")
		return
	default:
		if regDoc, err = registryForPackage(env); err != nil {
			Fail(w, r, http.StatusServiceUnavailable, "registry_unavailable", "Central's assignment trust registry is unreadable. Try again later.")
			return
		}
	}
	now := time.Now()
	pkg := &activation.Package{
		SchemaVersion:      activation.SchemaVersion,
		PackageID:          newUUIDv4(),
		RequestID:          reqID,
		RequestNonce:       reqNonce,
		ApplianceID:        appID,
		Serial:             serial,
		IdentityKeyFpr:     idFpr,
		TenantID:           asg.TenantID,
		SiteID:             asg.SiteID,
		Assignment:         asgRaw,
		LicenseEnvelope:    json.RawMessage(licEnv),
		Entitlements:       json.RawMessage(`{}`),
		CABundlePEM:        b.caPEM,
		CentralBase:        b.centralBase,
		AssignmentRegistry: regDoc,
		IssuedAt:           now.Unix(),
		ExpiresAt:          now.Add(time.Duration(validHours(body.ValidHours)) * time.Hour).Unix(),
		Nonce:              offlineNonce(),
	}
	activation.SignPackage(b.priv, pkg)

	operatorID, _ := actorOf(r)
	// The request is consumed by a conditional update in the transaction that records the package: of two
	// concurrent calls only one finds it unconsumed, so one request yields one package (audit Phase 1 M16).
	tx, err := b.DB.Begin(ctx)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "begin failed")
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE offline_activation_requests SET consumed_at=now() WHERE request_id=$1 AND consumed_at IS NULL`, reqID)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "package store failed")
		return
	}
	if tag.RowsAffected() == 0 {
		Fail(w, r, http.StatusConflict, CodeConflict, "this activation request has already been used for a package; import a new request")
		return
	}
	if _, err := tx.Exec(ctx, `
        INSERT INTO offline_activation_packages (package_id, appliance_id, serial, tenant_id, site_id,
                                                 nonce, signer_key_id, issued_by, expires_at)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,NULLIF($8,'')::uuid,to_timestamp($9))`,
		pkg.PackageID, appID, serial, pkg.TenantID, pkg.SiteID, pkg.Nonce, pkg.SignerKeyID, operatorID,
		pkg.ExpiresAt); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "package store failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "package store failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "offline_activation.package_generated", "appliance", appID, map[string]any{
		"_tenant_id": pkg.TenantID, "package_id": pkg.PackageID, "request_id": reqID, "expires_at": pkg.ExpiresAt})
	writeFile(w, "activation-package-"+serial+".json", map[string]any{"package_id": pkg.PackageID, "package": pkg})
}

// registryForPackage turns the stored registry envelope into the document a package carries: parsed into the signed struct and re-marshalled, so the
// bytes are the stable form of exactly the fields the root signature covers (the stored jsonb reorders keys). It is not re-signed or altered.
func registryForPackage(env []byte) (json.RawMessage, error) {
	var sr assignment.SignedRegistry
	if err := json.Unmarshal(env, &sr); err != nil {
		return nil, err
	}
	if sr.RegistryVersion <= 0 || sr.Signature == "" {
		return nil, errors.New("not a signed registry")
	}
	return json.Marshal(&sr)
}

// offlineLicense signs a licence file for an activated appliance: its own current licence.
func (b *OfflineBase) offlineLicense(w http.ResponseWriter, r *http.Request) {
	appID := chi.URLParam(r, "id")
	var body struct {
		ValidHours int `json:"valid_hours"`
	}
	_ = DecodeJSON(r, &body)
	row, ok := b.loadOne(w, r, Scope{All: true}, appID)
	if !ok {
		return
	}
	if row.lifecycle != "assigned" || row.Activation == ActivationRetiring {
		invalidState(w, r, "only an activated appliance can receive a licence file", row)
		return
	}
	ctx, cancel := DBCtx(r)
	defer cancel()
	licEnv, ok := b.currentEnvelope(w, r, appID)
	if !ok {
		return
	}
	var serial, pubKey, certFpr string
	_ = b.DB.QueryRow(ctx, `SELECT serial, COALESCE(public_key,''), COALESCE(current_cert_fingerprint,'')
                              FROM appliances WHERE id=$1`, appID).Scan(&serial, &pubKey, &certFpr)
	idFpr := ""
	if raw, err := base64.RawStdEncoding.DecodeString(pubKey); err == nil && len(raw) == ed25519.PublicKeySize {
		idFpr = offline.KeyID(ed25519.PublicKey(raw))
	}
	now := time.Now()
	pkg := &offline.Package{
		PackageID: newUUIDv4(), ApplianceID: appID, Serial: serial,
		IdentityKeyFpr: idFpr, MTLSKeyFpr: certFpr, TenantID: deref(row.CustomerID), SiteID: deref(row.SiteID),
		LicenseEnvelope: json.RawMessage(licEnv), Entitlements: json.RawMessage(`{}`),
		CABundlePEM: b.caPEM, IssuedAt: now.Unix(),
		ExpiresAt: now.Add(time.Duration(validHours(body.ValidHours)) * time.Hour).Unix(),
		Nonce:     offlineNonce(),
	}
	offline.Sign(b.priv, pkg)
	operatorID, _ := actorOf(r)
	if _, err := b.DB.Exec(ctx, `
        INSERT INTO offline_activation_packages (package_id, appliance_id, serial, tenant_id, site_id, nonce, signer_key_id, issued_by, expires_at)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,NULLIF($8,'')::uuid, to_timestamp($9))`,
		pkg.PackageID, appID, serial, pkg.TenantID, pkg.SiteID, pkg.Nonce, pkg.SignerKeyID, operatorID, pkg.ExpiresAt); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "package store failed")
		return
	}
	audit.Op(r.Context(), b.DB, r, "offline_license.generated", "appliance", appID, map[string]any{
		"_tenant_id": pkg.TenantID, "package_id": pkg.PackageID, "expires_at": pkg.ExpiresAt})
	writeFile(w, "license-"+serial+".json", map[string]any{"package_id": pkg.PackageID, "license": pkg})
}

// currentEnvelope is the appliance's current signed licence, or writes the refusal. Fails closed: only a verified
// "no licence" is reported as such; unavailable licensing or a failed lookup is a 503.
func (b *OfflineBase) currentEnvelope(w http.ResponseWriter, r *http.Request, appID string) (string, bool) {
	if b.Lic == nil {
		Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable", "Licensing is unavailable on Central right now. Try again later.")
		return "", false
	}
	env, _, err := b.Lic.CurrentEnvelopeForAppliance(r.Context(), appID)
	switch {
	case errors.Is(err, licensing.ErrNoLicense):
		Fail(w, r, http.StatusConflict, "no_license", "The appliance has no current licence; set one first.")
		return "", false
	case err != nil:
		Fail(w, r, http.StatusServiceUnavailable, "licensing_unavailable", "Central could not read the appliance's licence. Try again later.")
		return "", false
	}
	return env, true
}

// writeFile sends v as a download. The name carries a serial, so it is encoded by mime, never pasted into the
// header (audit Phase 1 M11).
func writeFile(w http.ResponseWriter, name string, v any) {
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	WriteJSON(w, http.StatusCreated, v)
}

func unavailable(code, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		Fail(w, r, http.StatusServiceUnavailable, code, msg)
	}
}
