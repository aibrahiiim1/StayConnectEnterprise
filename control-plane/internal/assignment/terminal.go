package assignment

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// DocFingerprint is a stable identifier for a specific signed assignment document
// (content + signature). It lets a terminal-delivery acknowledgment name EXACTLY
// which document the appliance adopted, so Central can confirm the box stood down
// on the right one.
func DocFingerprint(d *Document) string {
	h := sha256.Sum256(append(signingBytes(d), []byte(d.Signature)...))
	return fmt.Sprintf("%x", h[:])
}

// Ack is the appliance's signed acknowledgment that it has adopted a TERMINAL
// assignment (revoked / unassigned / decommissioned) and cleared its authority.
// It is signed with the appliance IDENTITY key (the same key that signs its API
// requests) — Central holds that public key from enrollment, so it can prove the
// ack really came from the appliance being retired. This is what gates Phase 2
// (certificate + NATS shutdown): credentials are only pulled once the appliance
// has provably given up authority.
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

// SignAck signs an ack with the appliance identity private key.
func SignAck(priv ed25519.PrivateKey, a *Ack) {
	a.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, ackSigningBytes(a)))
}

// VerifyAck checks an ack against the appliance identity public key.
func VerifyAck(pub ed25519.PublicKey, a *Ack) bool {
	sig, err := base64.StdEncoding.DecodeString(a.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, ackSigningBytes(a), sig)
}
