// Package activation is the OFFLINE FIRST-ACTIVATION protocol: an appliance never activated and with no route to Central is activated by carrying two
// files by hand. Deliberately SEPARATE from package offline (licence transport for an ALREADY assigned appliance), whose envelope must not change.
//
// Request (appliance -> Central): hardware evidence plus the PUBLIC half of a locally generated identity key, self-signed with the private half, which
// never leaves the appliance. Package (Central -> appliance): signed assignment, trust material and signed licence, bound to that request and identity,
// single-use, expiring, signed by the vendor key.
//
// Safety: a wrong appliance is refused by identity-key fingerprint; replay by a single-use nonce recorded in a reboot-persistent ledger before anything
// applies; rollback by the assignment and licence own version checks, which this envelope does not bypass; tampering by one vendor signature over every field.
// Mirrored in control-plane/internal/activation: THE SIGNED BYTES MUST MATCH BYTE FOR BYTE.
package activation

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

// SchemaVersion is part of the signed bytes, so an older appliance cannot be talked into a newer shape and a newer one refuses an older one explicitly.
const SchemaVersion = 1

// Emitted by a factory-clean appliance; contains no secret.
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
	// HoldsCustomerID is the tenant whose data the local site database still holds (empty if factory-clean); Central refuses any other customer.
	// Omitted from the signed bytes when empty, so requests predating the field verify as before.
	HoldsCustomerID string `json:"holds_customer_id,omitempty"`
	// Signature, by the private half of PublicKey over requestSigningBytes, proves the emitter holds the key; without it anyone could submit another
	// appliance hardware facts and receive a package bound to a key they control.
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

// Checked before creating anything: a request that cannot prove possession of its own key is just a file somebody wrote.
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
	// Assignment is the vendor-signed Document, the ONLY authority for tenant and site; the appliance verifies its signature and version separately.
	Assignment      json.RawMessage `json:"assignment"`
	LicenseEnvelope json.RawMessage `json:"license_envelope"`
	Entitlements    json.RawMessage `json:"entitlements"`
	CABundlePEM     string          `json:"ca_bundle_pem"`
	// CentralBase is a stable HTTPS FQDN so an offline appliance learns where to reconcile.
	CentralBase string `json:"central_base"`
	IssuedAt    int64  `json:"issued_at"`
	ExpiresAt   int64  `json:"expires_at"`
	Nonce       string `json:"nonce"`
	SignerKeyID string `json:"signer_key_id"`
	// AssignmentRegistry is the vendor-signed assignment-signing-key registry document (assignment.SignedRegistry) exactly as Central serves it
	// to appliances. OPTIONAL: omitted from the signed bytes when empty, so a package without it signs and verifies exactly as before. The
	// appliance NEVER trusts it because the package is signed: it verifies it against its own pinned registry-root key.
	AssignmentRegistry json.RawMessage `json:"assignment_registry,omitempty"`
	Signature          string          `json:"signature"`
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
	// Last and omitempty: a package without a registry keeps the pre-field signed bytes.
	AssignmentRegistry json.RawMessage `json:"assignment_registry,omitempty"`
}

func packageSigningBytes(p *Package) []byte {
	b, _ := json.Marshal(packageSignView{
		p.SchemaVersion, p.PackageID, p.RequestID, p.RequestNonce, p.ApplianceID, p.Serial,
		p.IdentityKeyFpr, p.TenantID, p.SiteID, p.Assignment, p.LicenseEnvelope, p.Entitlements,
		p.CABundlePEM, p.CentralBase, p.IssuedAt, p.ExpiresAt, p.Nonce, p.SignerKeyID, p.AssignmentRegistry,
	})
	return b
}

func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(sum[:8])
}

func SignPackage(priv ed25519.PrivateKey, p *Package) {
	p.SchemaVersion = SchemaVersion
	p.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	p.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, packageSigningBytes(p)))
}

// VerifyPackage checks the vendor signature only.
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

// Decides whether this appliance may apply the package; returns empty when it may. Binding rests on the identity key fingerprint (no appliance id yet),
// and an empty value on the package side never matches anything.
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
	// FAIL CLOSED on an appliance that already has an assignment: first activation is for a factory-clean box; re-pointing an assigned one is a
	// deliberate lifecycle action, not something a file on a USB stick may do.
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
