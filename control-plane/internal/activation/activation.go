// Package activation is the offline first-activation protocol: two hand-carried files, separate from package offline.
//
// Mirrored from data-plane/internal/activation. THE SIGNED BYTES MUST MATCH BYTE FOR BYTE.
package activation

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// SchemaVersion is the wire version of both documents; it is part of the signed bytes.
const SchemaVersion = 1

// Request is what a factory-clean appliance emits. It contains no secret.
type Request struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Serial        string `json:"serial"`
	PublicKey     string `json:"public_key"` // base64 raw std, ed25519
	WANMAC        string `json:"wan_mac"`
	LANMAC        string `json:"lan_mac"`
	HardwareFpr   string `json:"hardware_fingerprint"`
	Hostname      string `json:"hostname"`
	Model         string `json:"model"`
	CreatedAt     int64  `json:"created_at"`
	Nonce         string `json:"nonce"`
	// HoldsCustomerID is the customer whose data the appliance still holds ("" when factory-clean); Central
	// activates it for no other. Omitted from the signed bytes when empty, so older requests still verify.
	HoldsCustomerID string `json:"holds_customer_id,omitempty"`
	// Signature, made with the private half of PublicKey, proves the emitter holds the key it asks to be bound to.
	Signature string `json:"signature"`
}

type requestSignView struct {
	SchemaVersion int    `json:"schema_version"`
	RequestID     string `json:"request_id"`
	Serial        string `json:"serial"`
	PublicKey     string `json:"public_key"`
	WANMAC        string `json:"wan_mac"`
	LANMAC        string `json:"lan_mac"`
	HardwareFpr   string `json:"hardware_fingerprint"`
	Hostname      string `json:"hostname"`
	Model         string `json:"model"`
	CreatedAt     int64  `json:"created_at"`
	Nonce         string `json:"nonce"`
	// omitempty keeps the signed bytes of a request without it identical to the pre-field format.
	HoldsCustomerID string `json:"holds_customer_id,omitempty"`
}

func requestSigningBytes(r *Request) []byte {
	b, _ := json.Marshal(requestSignView{
		r.SchemaVersion, r.RequestID, r.Serial, r.PublicKey, r.WANMAC, r.LANMAC,
		r.HardwareFpr, r.Hostname, r.Model, r.CreatedAt, r.Nonce, r.HoldsCustomerID,
	})
	return b
}

func SignRequest(priv ed25519.PrivateKey, r *Request) {
	r.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, requestSigningBytes(r)))
}

// VerifyRequest checks the self-signature against the request's own public key; Central calls it before creating anything.
func VerifyRequest(r *Request) bool {
	if r == nil || r.SchemaVersion != SchemaVersion || r.PublicKey == "" || r.Signature == "" {
		return false
	}
	pub, err := base64.RawStdEncoding.DecodeString(r.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	sig, err := base64.RawStdEncoding.DecodeString(r.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(ed25519.PublicKey(pub), requestSigningBytes(r), sig)
}

// Package is what Central returns for one request. Every field is covered by Signature.
type Package struct {
	SchemaVersion  int    `json:"schema_version"`
	PackageID      string `json:"package_id"`
	RequestID      string `json:"request_id"`
	RequestNonce   string `json:"request_nonce"`
	ApplianceID    string `json:"appliance_id"`
	Serial         string `json:"serial"`
	IdentityKeyFpr string `json:"identity_key_fingerprint"`
	TenantID       string `json:"tenant_id"`
	SiteID         string `json:"site_id"`
	// Assignment is the vendor-signed assignment, the only authority for tenant and site; the appliance verifies it separately.
	Assignment json.RawMessage `json:"assignment"`
	// LicenseEnvelope is the vendor-signed licence, verified on its own terms.
	LicenseEnvelope json.RawMessage `json:"license_envelope"`
	Entitlements    json.RawMessage `json:"entitlements"`
	CABundlePEM     string          `json:"ca_bundle_pem"`
	// CentralBase tells an offline appliance where to reconcile.
	CentralBase string `json:"central_base"`
	IssuedAt    int64  `json:"issued_at"`
	ExpiresAt   int64  `json:"expires_at"`
	Nonce       string `json:"nonce"`
	SignerKeyID string `json:"signer_key_id"`
	Signature   string `json:"signature"`
}

type packageSignView struct {
	SchemaVersion   int             `json:"schema_version"`
	PackageID       string          `json:"package_id"`
	RequestID       string          `json:"request_id"`
	RequestNonce    string          `json:"request_nonce"`
	ApplianceID     string          `json:"appliance_id"`
	Serial          string          `json:"serial"`
	IdentityKeyFpr  string          `json:"identity_key_fingerprint"`
	TenantID        string          `json:"tenant_id"`
	SiteID          string          `json:"site_id"`
	Assignment      json.RawMessage `json:"assignment"`
	LicenseEnvelope json.RawMessage `json:"license_envelope"`
	Entitlements    json.RawMessage `json:"entitlements"`
	CABundlePEM     string          `json:"ca_bundle_pem"`
	CentralBase     string          `json:"central_base"`
	IssuedAt        int64           `json:"issued_at"`
	ExpiresAt       int64           `json:"expires_at"`
	Nonce           string          `json:"nonce"`
	SignerKeyID     string          `json:"signer_key_id"`
}

func packageSigningBytes(p *Package) []byte {
	b, _ := json.Marshal(packageSignView{
		p.SchemaVersion, p.PackageID, p.RequestID, p.RequestNonce, p.ApplianceID, p.Serial,
		p.IdentityKeyFpr, p.TenantID, p.SiteID, p.Assignment, p.LicenseEnvelope, p.Entitlements,
		p.CABundlePEM, p.CentralBase, p.IssuedAt, p.ExpiresAt, p.Nonce, p.SignerKeyID,
	})
	return b
}

// KeyID is the short fingerprint used for both signer and identity keys.
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

func SignPackage(priv ed25519.PrivateKey, p *Package) {
	p.SchemaVersion = SchemaVersion
	p.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	p.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, packageSigningBytes(p)))
}

func VerifyPackage(pub ed25519.PublicKey, p *Package) bool {
	if p == nil || p.Signature == "" {
		return false
	}
	sig, err := base64.RawStdEncoding.DecodeString(p.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, packageSigningBytes(p), sig)
}

// AcceptForFirstActivation returns "" when this appliance may apply the package, else the reason.
// The appliance has no id yet, so binding rests on the identity key fingerprint; an empty value on the
// package side never means "matches anything".
func AcceptForFirstActivation(pub ed25519.PublicKey, p *Package, serial, identityFpr, requestID,
	requestNonce string, alreadyAssigned bool, now time.Time) string {
	if p == nil {
		return "no package"
	}
	if p.SchemaVersion != SchemaVersion {
		return "unsupported package version"
	}
	if !VerifyPackage(pub, p) {
		return "signature invalid (modified or wrong signer)"
	}
	if now.Unix() > p.ExpiresAt {
		return "package expired"
	}
	// Fail closed: re-pointing an assigned appliance is a lifecycle action with its own authority, not something a carried file may do.
	if alreadyAssigned {
		return "appliance is already assigned — use licence renewal, not first activation"
	}
	if p.IdentityKeyFpr == "" || identityFpr == "" || p.IdentityKeyFpr != identityFpr {
		return "package is not bound to this appliance's identity key"
	}
	if p.RequestID == "" || requestID == "" || p.RequestID != requestID {
		return "package does not answer this appliance's activation request"
	}
	if p.RequestNonce == "" || requestNonce == "" || p.RequestNonce != requestNonce {
		return "activation request nonce mismatch (stale or replayed package)"
	}
	if p.Serial != "" && serial != "" && p.Serial != serial {
		return "serial mismatch"
	}
	if p.ApplianceID == "" || p.TenantID == "" || p.SiteID == "" {
		return "package is missing appliance/tenant/site identity"
	}
	if len(p.Assignment) == 0 || string(p.Assignment) == "null" {
		return "package carries no signed assignment"
	}
	return ""
}
