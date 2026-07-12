// Package assignment implements the signed appliance-assignment document — the
// authoritative, vendor-signed instruction that binds a specific appliance to a
// Tenant + Site. It is the delivery channel that was previously missing: the
// Platform "assign"/"reassign"/"unassign" actions sign one of these, and the
// appliance fetches it over mTLS, verifies the signature + its own binding, and
// persists it as its LOCAL source of truth for tenant/site. A clean appliance
// has no assignment at all (tenant/site null → awaiting-assignment).
//
// Mirror this file on the data-plane side; the signed byte layout MUST match
// exactly (same signView field order + JSON tags), or verification fails.
package assignment

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// States a signed assignment can carry. Ownership only ever CHANGES through one
// of these, delivered in a newer, validly-signed document. An expired, missing or
// unreachable Central response must never implicitly unassign an appliance.
const (
	StateAssigned       = "assigned"       // bound to tenant+site, operational
	StateReassigned     = "reassigned"     // moved to a different tenant+site (still owning)
	StateUnassigned     = "unassigned"     // returned to inventory; tenant/site cleared
	StateRevoked        = "revoked"        // identity revoked; appliance must stop
	StateDecommissioned = "decommissioned" // retired for good; tenant/site cleared
)

// Grants reports whether a document CONFERS ownership (a tenant/site the
// appliance should operate as). Only these states carry tenant/site.
func Grants(state string) bool {
	return state == StateAssigned || state == StateReassigned
}

// Clears reports whether a document explicitly REMOVES ownership. Only an
// explicit, signed, newer document may do this.
func Clears(state string) bool {
	return state == StateUnassigned || state == StateRevoked || state == StateDecommissioned
}

func knownState(s string) bool { return Grants(s) || Clears(s) }

// IsExpired reports whether the document is past its expires_at.
//
// Expiry is a STALENESS signal, never a de-authorisation. The signed assignment
// is durable CONFIGURATION, not a short-lived auth token: a hotel must keep
// serving guests through a Central outage. Passing expires_at therefore does not
// clear tenant/site or return the appliance to awaiting-assignment — it only
// marks the assignment stale so operators can see refresh has not succeeded.
func IsExpired(d *Document, now time.Time) bool {
	return d != nil && d.ExpiresAt != 0 && now.Unix() > d.ExpiresAt
}

// Document is the vendor-signed assignment envelope. Version is monotonic per
// appliance: the edge accepts only a version STRICTLY GREATER than the one it
// has already applied, which defeats replay of a superseded assignment.
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
}

// signView is the signature-free canonical projection. Field ORDER and JSON
// tags here are the wire contract — do not reorder.
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
}

func signingBytes(d *Document) []byte {
	b, _ := json.Marshal(signView{
		d.AssignmentID, d.ApplianceID, d.IdentityKeyFpr, d.Serial, d.TenantID, d.SiteID,
		d.TenantName, d.SiteName, d.Version, d.State, d.IssuedAt, d.ExpiresAt, d.SignerKeyID,
	})
	return b
}

// KeyID is the short fingerprint of the signing public key.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return fmt.Sprintf("%x", s[:8])
}

// Sign fills SignerKeyID + Signature with the vendor key.
func Sign(priv ed25519.PrivateKey, d *Document) {
	d.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	sig := ed25519.Sign(priv, signingBytes(d))
	d.Signature = base64.StdEncoding.EncodeToString(sig)
}

// Verify checks the signature only.
func Verify(pub ed25519.PublicKey, d *Document) bool {
	sig, err := base64.StdEncoding.DecodeString(d.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, signingBytes(d), sig)
}

// AcceptFor validates an assignment against THIS appliance's identity and its
// currently-applied version. Returns "" if acceptable, else a rejection reason.
// haveVersion is the version already persisted (0 if none).
//
// NOTE: expiry is deliberately NOT a rejection. The assignment is durable
// configuration; a stale document keeps the hotel running (see IsExpired). Only a
// bad signature, wrong binding, unknown state or a non-newer version rejects.
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
