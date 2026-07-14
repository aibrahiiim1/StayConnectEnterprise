// Package commands mirrors control-plane/internal/commands — the signed,
// allow-listed command envelope. The signed byte layout MUST match the
// control-plane exactly.
package commands

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

var Allowed = map[string]bool{
	"request_heartbeat":             true,
	"refresh_license":               true,
	"retry_telemetry":               true,
	"collect_sanitized_diagnostics": true,
	"rotate_certificate":            true,
	"restart_stayconnect_service":   true,
	"schedule_update":               true,
	"controlled_reboot":             true,
}

var RestartAllowList = map[string]bool{
	"stayconnect-scd": true, "stayconnect-edged": true, "stayconnect-netd": true,
	"stayconnect-portald": true, "stayconnect-acctd": true, "stayconnect-hotel-admin": true,
}

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

func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return fmt.Sprintf("%x", s[:8])
}

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
