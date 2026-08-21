// Package activation is the OFFLINE FIRST-ACTIVATION protocol: an appliance that has never been activated
// and has no route to Central is activated by carrying two files by hand.
//
// It is deliberately SEPARATE from package offline, which transports a signed licence to an appliance that
// is ALREADY assigned. That path keeps its semantics and its signed bytes untouched; nothing here changes
// it. Mixing the two would have meant changing an envelope already in service, invalidating every
// unconsumed package in the field.
//
// THE TWO FILES
//
//	Request  appliance -> operator -> Central. Carries the appliance's own hardware evidence and the PUBLIC
//	         half of an identity keypair it generated locally, SELF-SIGNED with the private half. The private
//	         key never leaves the appliance, and the self-signature is what proves the requester holds it.
//
//	Package  Central -> operator -> appliance. Carries everything first activation needs -- the signed
//	         assignment, the trust material and the signed licence -- bound to that exact request and that
//	         exact identity, single-use and expiring, signed with the vendor key the appliance trusts.
//
// WHAT MAKES IT SAFE
//
//	wrong appliance  the package names the identity key fingerprint from the request; an appliance that does
//	                 not hold that private key refuses it.
//	replay           single-use nonce, recorded in a reboot-persistent local ledger before anything applies.
//	rollback         the assignment and the licence each carry their own version and their own anti-rollback
//	                 check, which this envelope does not bypass -- it only transports them.
//	tampering        one vendor signature covers every field, including the two inner signed documents.
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

// SchemaVersion is the wire version of both documents below. It is part of the signed bytes, so an older
// appliance cannot be talked into accepting a newer shape it does not understand, and a newer appliance can
// refuse an older one explicitly instead of misreading it.
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
	// Signature is made by the private half of PublicKey over requestSigningBytes. It proves the emitter
	// holds the key it is asking to be bound to -- without it, anyone could submit someone else's hardware
	// facts and receive a package bound to a key they control.
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
}

func requestSigningBytes(r *Request) []byte {
	b, _ := json.Marshal(requestSignView{
		r.SchemaVersion, r.RequestID, r.Serial, r.PublicKey, r.WANMAC, r.LANMAC,
		r.HardwareFpr, r.Hostname, r.Model, r.CreatedAt, r.Nonce,
	})
	return b
}

// SignRequest self-signs a request with the appliance's identity private key.
func SignRequest(priv ed25519.PrivateKey, r *Request) {
	r.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, requestSigningBytes(r)))
}

// VerifyRequest checks the self-signature against the public key the request carries. Central calls this
// before creating anything: a request that cannot prove possession of its own key is not evidence of an
// appliance, it is just a file somebody wrote.
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
	// Assignment is the vendor-signed assignment Document -- the ONLY authority for tenant and site. It is
	// transported here, not replaced: the appliance verifies its own signature and version separately.
	Assignment json.RawMessage `json:"assignment"`
	// LicenseEnvelope is the vendor-signed licence, likewise verified on its own terms.
	LicenseEnvelope json.RawMessage `json:"license_envelope"`
	Entitlements    json.RawMessage `json:"entitlements"`
	// CABundlePEM is the trust material the appliance installs so it can later reach Central over mTLS.
	CABundlePEM string `json:"ca_bundle_pem"`
	// CentralBase is the appliance-facing Central endpoint (a stable HTTPS FQDN). Carried so an offline
	// appliance learns where to reconcile without anyone typing an address into it.
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

// SignPackage signs with the vendor key.
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

// AcceptForFirstActivation decides whether this appliance may apply this package, and returns "" when it may.
//
// It is written for the FIRST activation case and says so: the appliance has no appliance id yet, so binding
// rests on the identity key fingerprint -- the one thing a factory-clean appliance can prove. Every field
// that could be used to point a package at the wrong box is checked, and an empty value on the package side
// is never treated as "matches anything".
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
	// FAIL CLOSED on an appliance that already has an assignment. First activation is for a factory-clean
	// box; re-pointing an assigned appliance is a deliberate lifecycle action with its own authority, not
	// something a file left on a USB stick may do.
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
