// Package commands defines the signed, allow-listed command envelope shared by
// Central (signer) and the appliance (verifier). Commands are Ed25519-signed
// with the DEDICATED command-signing key (separate from CA/license/update keys)
// and are strictly allow-listed — no arbitrary shell, path, unit or URL.
//
// Mirror this file on the data-plane side (data-plane/internal/commands); the
// signed byte layout MUST match exactly.
package commands

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Allowed is the exhaustive command allow-list. Anything else is rejected.
var Allowed = map[string]bool{
	"request_heartbeat":           true,
	"refresh_license":             true,
	"retry_telemetry":             true,
	"collect_sanitized_diagnostics": true,
	"rotate_certificate":          true,
	"restart_stayconnect_service": true,
	"schedule_update":             true,
	"controlled_reboot":           true,
}

// RestartAllowList bounds which systemd units restart_stayconnect_service may
// target. Never an arbitrary unit name.
var RestartAllowList = map[string]bool{
	"stayconnect-scd": true, "stayconnect-edged": true, "stayconnect-netd": true,
	"stayconnect-portald": true, "stayconnect-acctd": true, "stayconnect-hotel-admin": true,
}

// Envelope is the on-wire signed command. Signature is excluded from the
// signed bytes (see signingView).
type Envelope struct {
	CommandID      string          `json:"command_id"`
	ApplianceID    string          `json:"appliance_id"`
	CommandType    string          `json:"command_type"`
	Params         json.RawMessage `json:"params"`
	IssuedAt       int64           `json:"issued_at"`
	ExpiresAt      int64           `json:"expires_at"`
	IdempotencyKey string          `json:"idempotency_key"`
	SignerKeyID    string          `json:"signer_key_id"`
	Signature      string          `json:"signature"`
}

// signingView is the canonical, signature-free projection that gets signed.
type signingView struct {
	CommandID      string          `json:"command_id"`
	ApplianceID    string          `json:"appliance_id"`
	CommandType    string          `json:"command_type"`
	Params         json.RawMessage `json:"params"`
	IssuedAt       int64           `json:"issued_at"`
	ExpiresAt      int64           `json:"expires_at"`
	IdempotencyKey string          `json:"idempotency_key"`
	SignerKeyID    string          `json:"signer_key_id"`
}

func signingBytes(e *Envelope) []byte {
	b, _ := json.Marshal(signingView{
		e.CommandID, e.ApplianceID, e.CommandType, e.Params, e.IssuedAt, e.ExpiresAt, e.IdempotencyKey, e.SignerKeyID,
	})
	return b
}

// KeyID is the short fingerprint of the command-signing public key.
func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return fmt.Sprintf("%x", s[:8])
}

// Sign fills SignerKeyID + Signature over the canonical bytes.
func Sign(priv ed25519.PrivateKey, e *Envelope) {
	e.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	sig := ed25519.Sign(priv, signingBytes(e))
	e.Signature = base64.StdEncoding.EncodeToString(sig)
}

// Verify checks the signature against pub and that the command type is allowed.
func Verify(pub ed25519.PublicKey, e *Envelope) bool {
	if !Allowed[e.CommandType] {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, signingBytes(e), sig)
}
