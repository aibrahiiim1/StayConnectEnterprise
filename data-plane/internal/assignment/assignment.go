// Package assignment implements the vendor-signed appliance-assignment document that binds an appliance to a Tenant + Site. The appliance fetches it
// over mTLS, verifies the signature and its own binding, and persists it as its LOCAL source of truth; a clean appliance has none (awaiting-assignment).
// Mirrors the control-plane side: the signed byte layout (signView field order + JSON tags) MUST match exactly.
package assignment

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// Ownership only ever CHANGES through one of these states in a newer, validly-signed document; an expired, missing or unreachable Central response
// must never implicitly unassign an appliance.
const (
	StateAssigned       = "assigned"       // bound to tenant+site, operational
	StateReassigned     = "reassigned"     // moved to a different tenant+site (still owning)
	StateUnassigned     = "unassigned"     // returned to inventory; tenant/site cleared
	StateRevoked        = "revoked"        // identity revoked; appliance must stop
	StateDecommissioned = "decommissioned" // retired for good; tenant/site cleared
)

// Grants reports whether a document CONFERS ownership; only these states carry tenant/site.
func Grants(state string) bool {
	return state == StateAssigned || state == StateReassigned
}

// Clears: only an explicit, signed, newer document may remove ownership.
func Clears(state string) bool {
	return state == StateUnassigned || state == StateRevoked || state == StateDecommissioned
}

func knownState(s string) bool { return Grants(s) || Clears(s) }

// Expiry is a STALENESS signal, never a de-authorisation: the assignment is durable configuration and a hotel must keep serving through a Central
// outage, so passing expires_at only marks it stale.
func IsExpired(d *Document, now time.Time) bool {
	return d != nil && d.ExpiresAt != 0 && now.Unix() > d.ExpiresAt
}

// Version is monotonic per appliance: the edge accepts only a version STRICTLY GREATER than the applied one, defeating replay of a superseded assignment.
type Document struct {
	AssignmentID   string `json:"assignment_id"`
	ApplianceID    string `json:"appliance_id"`
	IdentityKeyFpr string `json:"identity_key_fingerprint"`
	Serial         string `json:"serial"`
	TenantID       string `json:"tenant_id"`
	SiteID         string `json:"site_id"`
	TenantName     string `json:"tenant_name"`
	SiteName       string `json:"site_name"`
	Version        int64  `json:"version"`
	State          string `json:"state"`
	IssuedAt       int64  `json:"issued_at"`
	ExpiresAt      int64  `json:"expires_at"` // 0 = no expiry (revision-governed)
	SignerKeyID    string `json:"signer_key_id"`
	Signature      string `json:"signature"`
	// SiteType is descriptive metadata owned by Central (HOTEL, CAFE, ...); it authorises nothing. LAST signed field and omitempty, so a document without
	// it signs byte-identically to the older layout; unknown values are carried as-is.
	SiteType string `json:"site_type,omitempty"`
}

// The signature-free canonical projection: field ORDER and JSON tags are the wire contract, do not reorder.
type signView struct {
	AssignmentID   string `json:"assignment_id"`
	ApplianceID    string `json:"appliance_id"`
	IdentityKeyFpr string `json:"identity_key_fingerprint"`
	Serial         string `json:"serial"`
	TenantID       string `json:"tenant_id"`
	SiteID         string `json:"site_id"`
	TenantName     string `json:"tenant_name"`
	SiteName       string `json:"site_name"`
	Version        int64  `json:"version"`
	State          string `json:"state"`
	IssuedAt       int64  `json:"issued_at"`
	ExpiresAt      int64  `json:"expires_at"`
	SignerKeyID    string `json:"signer_key_id"`
	SiteType       string `json:"site_type,omitempty"`
}

func signingBytes(d *Document) []byte {
	b, _ := json.Marshal(signView{
		d.AssignmentID, d.ApplianceID, d.IdentityKeyFpr, d.Serial, d.TenantID, d.SiteID,
		d.TenantName, d.SiteName, d.Version, d.State, d.IssuedAt, d.ExpiresAt, d.SignerKeyID,
		d.SiteType,
	})
	return b
}

func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return fmt.Sprintf("%x", s[:8])
}

func Sign(priv ed25519.PrivateKey, d *Document) {
	d.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	sig := ed25519.Sign(priv, signingBytes(d))
	d.Signature = base64.StdEncoding.EncodeToString(sig)
}

func Verify(pub ed25519.PublicKey, d *Document) bool {
	sig, err := base64.StdEncoding.DecodeString(d.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, signingBytes(d), sig)
}

// Validates an assignment against THIS appliance identity and applied version (haveVersion, 0 if none); returns empty if acceptable, else a rejection
// reason. Expiry is deliberately NOT a rejection (see IsExpired); only a bad signature, wrong binding, unknown state or non-newer version rejects.
func AcceptFor(pub ed25519.PublicKey, d *Document, applianceID, serial, identityFpr string, haveVersion int64, now time.Time) string {
	if !Verify(pub, d) {
		return "signature invalid (modified or unknown signer)"
	}
	if d.ApplianceID != applianceID {
		return "assignment bound to a different appliance"
	}
	if d.Serial != "" && serial != "" && d.Serial != serial {
		return "serial mismatch"
	}
	if d.IdentityKeyFpr != "" && identityFpr != "" && d.IdentityKeyFpr != identityFpr {
		return "identity key mismatch"
	}
	if d.Version <= haveVersion {
		return "assignment version is not newer than the applied one (replay/superseded)"
	}
	if !knownState(d.State) {
		return "unknown assignment state"
	}
	if Grants(d.State) && (d.TenantID == "" || d.SiteID == "") {
		return "assigned document missing tenant/site"
	}
	return ""
}
