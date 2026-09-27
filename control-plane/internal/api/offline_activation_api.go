package api

// OFFLINE FILES — activation and licence for an appliance with no route to Central.
//
//	POST /cloud/v1/offline-activation/requests                  import an appliance's signed activation request
//	POST /cloud/v1/appliances/{id}/offline-activation-package   the activation package (after Activate)
//	POST /cloud/v1/appliances/{id}/offline-license              a signed licence file for an activated appliance
//
// Offline activation is the SAME Activate step as online, with a file in each direction: the imported request
// makes the appliance WAITING; the operator activates it (customer, site, licence); the package carries the
// signed assignment and licence that activation produced. An imported request carries no authority over
// customer or site — a file on a USB stick can announce that a box exists, it cannot decide whose hotel it
// belongs to.
//
// Both envelopes (package activation and package offline) are unchanged; their signed bytes must match the
// appliance byte for byte.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/control-plane/internal/activation"
	"github.com/stayconnect/enterprise/control-plane/internal/assignment"
	"github.com/stayconnect/enterprise/control-plane/internal/audit"
	"github.com/stayconnect/enterprise/control-plane/internal/licensing"
	"github.com/stayconnect/enterprise/control-plane/internal/offline"
)

// OfflineBase mints the offline files. It is nil (and the routes answer 503) when the vendor key is missing.
type OfflineBase struct {
	*Base
	priv  ed25519.PrivateKey
	caPEM string
	// centralBase is the APPLIANCE-FACING endpoint carried into an activation package so an offline
	// appliance learns where to reconcile. A stable HTTPS FQDN, never an IP. Empty disables activation
	// packages (not offline licences), rather than minting packages naming an endpoint nobody operates.
	centralBase string
}

// NewOfflineBase loads the vendor signing key and the CA bundle handed to appliances. It returns nil when the
// vendor key is unavailable. The CA bundle is read from caBundlePath; when that file is absent the appliance
// CA chain Central issues certificates from is used instead.
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
	// PROOF OF POSSESSION: without it an operator could be handed another appliance's hardware facts with an
	// attacker's public key, and Central would bind the package to a key the attacker holds.
	if !activation.VerifyRequest(&req) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the activation request is not signed by the key it carries")
		return
	}
	if req.Serial == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "the activation request carries no serial")
		return
	}
	// holds_customer_id is covered by the request's self-signature when present (package activation), and
	// is applied exactly as an online registration's: stored, and enforced by activate.
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
	var appID, lifecycle, existingPub string
	err := b.DB.QueryRow(ctx,
		`SELECT id::text, lifecycle_state, COALESCE(public_key,'') FROM appliances WHERE serial=$1`, req.Serial).
		Scan(&appID, &lifecycle, &existingPub)
	switch {
	case err == nil && existingPub != "" && existingPub != req.PublicKey:
		// A different key claiming an existing serial is a clone or a rebuild; an offline import may not
		// resolve that on its own.
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
	// Whose customer data the appliance holds, as of this (signed) request. NULL when it reports none.
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
	audit.Op(r.Context(), b.DB, r, "offline_activation.request_imported", "appliance", appID,
		map[string]any{"serial": req.Serial, "request_id": req.RequestID, "holds_customer_id": held})
	b.writeAppliance(w, r, http.StatusOK, appID)
}

// activationPackage mints the one file that completes an offline first activation. The appliance must
// already be activated; the package carries the signed assignment and the licence that activation issued,
// bound to the imported request.
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

	// THE SIGNED ASSIGNMENT the activation produced. It is what binds the appliance to a hotel; the envelope
	// only carries it.
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
	// The appliance's OWN current licence (F10: never another appliance's licence at the same site).
	licEnv, ok := b.currentEnvelope(w, r, appID)
	if !ok {
		return
	}
	idFpr := ""
	if raw, err := base64.RawStdEncoding.DecodeString(pubKey); err == nil && len(raw) == ed25519.PublicKeySize {
		idFpr = activation.KeyID(ed25519.PublicKey(raw))
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
		TenantID:        asg.TenantID,
		SiteID:          asg.SiteID,
		Assignment:      asgRaw,
		LicenseEnvelope: json.RawMessage(licEnv),
		Entitlements:    json.RawMessage(`{}`),
		CABundlePEM:     b.caPEM,
		CentralBase:     b.centralBase,
		IssuedAt:        now.Unix(),
		ExpiresAt:       now.Add(time.Duration(validHours(body.ValidHours)) * time.Hour).Unix(),
		Nonce:           offlineNonce(),
	}
	activation.SignPackage(b.priv, pkg)

	operatorID, _ := actorOf(r)
	if _, err := b.DB.Exec(ctx, `
        INSERT INTO offline_activation_packages (package_id, appliance_id, serial, tenant_id, site_id,
                                                 nonce, signer_key_id, issued_by, expires_at)
        VALUES ($1,$2,$3,NULLIF($4,'')::uuid,NULLIF($5,'')::uuid,$6,$7,NULLIF($8,'')::uuid,to_timestamp($9))`,
		pkg.PackageID, appID, serial, pkg.TenantID, pkg.SiteID, pkg.Nonce, pkg.SignerKeyID, operatorID,
		pkg.ExpiresAt); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "package store failed")
		return
	}
	_, _ = b.DB.Exec(ctx, `UPDATE offline_activation_requests SET consumed_at=now() WHERE request_id=$1`, reqID)
	audit.Op(r.Context(), b.DB, r, "offline_activation.package_generated", "appliance", appID, map[string]any{
		"_tenant_id": pkg.TenantID, "package_id": pkg.PackageID, "request_id": reqID, "expires_at": pkg.ExpiresAt})
	writeFile(w, "activation-package-"+serial+".json", map[string]any{"package_id": pkg.PackageID, "package": pkg})
}

// offlineLicense signs a licence file for an activated appliance: its own current licence (F10).
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
	// The downloadable document is under "license" (the activation package's is under "package").
	writeFile(w, "license-"+serial+".json", map[string]any{"package_id": pkg.PackageID, "license": pkg})
}

// currentEnvelope is the appliance's own current signed licence, or writes the refusal. Fail closed: only a
// verified "no licence" is reported as such; licensing being unavailable or the lookup failing is a 503.
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

// writeFile answers with a JSON document the browser saves as a file.
func writeFile(w http.ResponseWriter, name string, v any) {
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	WriteJSON(w, http.StatusCreated, v)
}

// unavailable answers 503 for a route whose signing material is not configured.
func unavailable(code, msg string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		Fail(w, r, http.StatusServiceUnavailable, code, msg)
	}
}
