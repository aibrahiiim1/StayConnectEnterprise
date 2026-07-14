// Package offline implements Appliance-specific, vendor-signed offline
// activation packages. A package binds a signed license to ONE appliance
// (id/serial/key fingerprints/tenant/site), is single-use (nonce), expiring,
// and signed with the vendor key the appliance already trusts. Mirror the
// envelope + Verify on the data-plane side; the signed bytes MUST match.
package offline

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// Package is the signed offline activation envelope.
type Package struct {
	PackageID       string          `json:"package_id"`
	ApplianceID     string          `json:"appliance_id"`
	Serial          string          `json:"serial"`
	IdentityKeyFpr  string          `json:"identity_key_fingerprint"`
	MTLSKeyFpr      string          `json:"mtls_key_fingerprint"`
	TenantID        string          `json:"tenant_id"`
	SiteID          string          `json:"site_id"`
	LicenseEnvelope json.RawMessage `json:"license_envelope"` // vendor-signed license doc
	Entitlements    json.RawMessage `json:"entitlements"`
	CABundlePEM     string          `json:"ca_bundle_pem"`
	IssuedAt        int64           `json:"issued_at"`
	ExpiresAt       int64           `json:"expires_at"`
	Nonce           string          `json:"nonce"`
	SignerKeyID     string          `json:"signer_key_id"`
	Signature       string          `json:"signature"`
}

type signView struct {
	PackageID       string          `json:"package_id"`
	ApplianceID     string          `json:"appliance_id"`
	Serial          string          `json:"serial"`
	IdentityKeyFpr  string          `json:"identity_key_fingerprint"`
	MTLSKeyFpr      string          `json:"mtls_key_fingerprint"`
	TenantID        string          `json:"tenant_id"`
	SiteID          string          `json:"site_id"`
	LicenseEnvelope json.RawMessage `json:"license_envelope"`
	Entitlements    json.RawMessage `json:"entitlements"`
	CABundlePEM     string          `json:"ca_bundle_pem"`
	IssuedAt        int64           `json:"issued_at"`
	ExpiresAt       int64           `json:"expires_at"`
	Nonce           string          `json:"nonce"`
	SignerKeyID     string          `json:"signer_key_id"`
}

func signingBytes(p *Package) []byte {
	b, _ := json.Marshal(signView{
		p.PackageID, p.ApplianceID, p.Serial, p.IdentityKeyFpr, p.MTLSKeyFpr, p.TenantID, p.SiteID,
		p.LicenseEnvelope, p.Entitlements, p.CABundlePEM, p.IssuedAt, p.ExpiresAt, p.Nonce, p.SignerKeyID,
	})
	return b
}

// KeyID is the short fingerprint of the signing public key.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return fmt.Sprintf("%x", s[:8])
}

// Sign fills SignerKeyID + Signature with the vendor key.
func Sign(priv ed25519.PrivateKey, p *Package) {
	p.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	sig := ed25519.Sign(priv, signingBytes(p))
	p.Signature = base64.StdEncoding.EncodeToString(sig)
}

// Verify checks the signature only (binding/expiry/nonce are the importer's job).
func Verify(pub ed25519.PublicKey, p *Package) bool {
	sig, err := base64.StdEncoding.DecodeString(p.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, signingBytes(p), sig)
}

// AcceptFor validates the package binding, expiry and signature for a specific
// appliance identity. Returns "" if acceptable, else a rejection reason. The
// caller separately enforces single-use via a local nonce/package_id ledger.
func AcceptFor(pub ed25519.PublicKey, p *Package, applianceID, serial, identityFpr, mtlsFpr string, now time.Time) string {
	if !Verify(pub, p) {
		return "signature invalid (modified or wrong signer)"
	}
	if now.Unix() > p.ExpiresAt {
		return "package expired"
	}
	if p.ApplianceID != applianceID {
		return "package bound to a different appliance"
	}
	if p.Serial != "" && serial != "" && p.Serial != serial {
		return "serial mismatch"
	}
	if p.IdentityKeyFpr != "" && identityFpr != "" && p.IdentityKeyFpr != identityFpr {
		return "identity key mismatch"
	}
	if p.MTLSKeyFpr != "" && mtlsFpr != "" && p.MTLSKeyFpr != mtlsFpr {
		return "mtls key mismatch"
	}
	return ""
}
