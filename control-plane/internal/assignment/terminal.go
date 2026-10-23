package assignment

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// DocFingerprint identifies a signed document (content and signature) so an ack names exactly which one the
// appliance adopted.
func DocFingerprint(d *Document) string {
	h := sha256.Sum256(append(signingBytes(d), []byte(d.Signature)...))
	return fmt.Sprintf("%x", h[:])
}

// Ack is the appliance's acknowledgment that it adopted a terminal assignment and cleared its authority. It is
// signed with the identity key Central holds from registration, which gates Phase 2 (certificate revocation):
// credentials are pulled only once the appliance has provably given up authority.
type Ack struct {
	ApplianceID   string `json:"appliance_id"`
	Version       int64  `json:"assignment_version"`
	TerminalState string `json:"terminal_state"`
	Fingerprint   string `json:"assignment_fingerprint"`
	AdoptedAt     int64  `json:"adopted_at"`
	Signature     string `json:"signature"`
}

type ackSignView struct {
	ApplianceID   string `json:"appliance_id"`
	Version       int64  `json:"assignment_version"`
	TerminalState string `json:"terminal_state"`
	Fingerprint   string `json:"assignment_fingerprint"`
	AdoptedAt     int64  `json:"adopted_at"`
}

func ackSigningBytes(a *Ack) []byte {
	b, _ := json.Marshal(ackSignView{a.ApplianceID, a.Version, a.TerminalState, a.Fingerprint, a.AdoptedAt})
	return b
}

func SignAck(priv ed25519.PrivateKey, a *Ack) {
	a.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, ackSigningBytes(a)))
}

func VerifyAck(pub ed25519.PublicKey, a *Ack) bool {
	sig, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, ackSigningBytes(a), sig)
}
