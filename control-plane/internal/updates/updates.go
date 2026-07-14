// Package updates defines the signed software-update manifest shared by Central
// (signer) and the appliance (verifier). Signed with the DEDICATED
// update-signing key (separate from CA/license/command keys). Packages are
// built OFF the appliance; the manifest carries a SHA-256 the appliance
// independently verifies. Mirror on the data-plane; signed bytes MUST match.
package updates

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Manifest describes one signed update. The package archive is delivered
// alongside (base64) and verified against SHA256.
type Manifest struct {
	UpdateID         string `json:"update_id"`
	Component        string `json:"component"`
	Version          string `json:"version"`
	MinSourceVersion string `json:"min_source_version"`
	Model            string `json:"model"`
	Channel          string `json:"channel"`
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
	ApplianceID      string `json:"appliance_id"`
	SignerKeyID      string `json:"signer_key_id"`
	Signature        string `json:"signature"`
}

type signView struct {
	UpdateID         string `json:"update_id"`
	Component        string `json:"component"`
	Version          string `json:"version"`
	MinSourceVersion string `json:"min_source_version"`
	Model            string `json:"model"`
	Channel          string `json:"channel"`
	SHA256           string `json:"sha256"`
	Size             int64  `json:"size"`
	ApplianceID      string `json:"appliance_id"`
	SignerKeyID      string `json:"signer_key_id"`
}

func signingBytes(m *Manifest) []byte {
	b, _ := json.Marshal(signView{m.UpdateID, m.Component, m.Version, m.MinSourceVersion, m.Model, m.Channel, m.SHA256, m.Size, m.ApplianceID, m.SignerKeyID})
	return b
}

func KeyID(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	return fmt.Sprintf("%x", s[:8])
}

func Sign(priv ed25519.PrivateKey, m *Manifest) {
	m.SignerKeyID = KeyID(priv.Public().(ed25519.PublicKey))
	sig := ed25519.Sign(priv, signingBytes(m))
	m.Signature = base64.StdEncoding.EncodeToString(sig)
}

func VerifySig(pub ed25519.PublicKey, m *Manifest) bool {
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return false
	}
	return ed25519.Verify(pub, signingBytes(m), sig)
}

// SHA256Hex is the canonical package checksum.
func SHA256Hex(pkg []byte) string {
	s := sha256.Sum256(pkg)
	return fmt.Sprintf("%x", s[:])
}

// Accept validates a manifest + package for a source version. Returns "" if
// acceptable, else a rejection reason. Duplicate/rollback are the agent's job.
func Accept(pub ed25519.PublicKey, m *Manifest, pkg []byte, sourceVersion, model string) string {
	if !VerifySig(pub, m) {
		return "manifest signature invalid"
	}
	if SHA256Hex(pkg) != m.SHA256 {
		return "checksum mismatch"
	}
	if m.Model != "" && model != "" && m.Model != model {
		return "incompatible model"
	}
	if m.MinSourceVersion != "" && sourceVersion != "" && sourceVersion < m.MinSourceVersion {
		return "source version below minimum"
	}
	return ""
}
