package pmsd

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
)

// SignedAssignment is the canonical signed appliance-assignment document. Tenant/Site/Appliance identity is
// authoritative ONLY when the Ed25519 signature over the canonical body verifies against the configured
// control-plane public key.
type SignedAssignment struct {
	ApplianceID    string `json:"appliance_id"`
	TenantID       string `json:"tenant_id"`
	SiteID         string `json:"site_id"`
	DocumentDigest string `json:"document_digest"`
	Version        int    `json:"version"`
	Signature      string `json:"signature"` // base64(ed25519 signature over canonicalBody)
}

var (
	ErrAssignmentUnsigned = errors.New("pmsd: assignment signature missing/invalid")
	ErrAssignmentFields   = errors.New("pmsd: assignment identity fields invalid")
)

// canonicalBody returns the deterministic bytes that are signed: the JSON object of all fields EXCEPT the
// signature, with keys sorted. Any change to identity/version/digest invalidates the signature.
func (a SignedAssignment) canonicalBody() []byte {
	m := map[string]any{
		"appliance_id": a.ApplianceID, "tenant_id": a.TenantID, "site_id": a.SiteID,
		"document_digest": a.DocumentDigest, "version": a.Version,
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		vb, _ := json.Marshal(m[k])
		b.Write(kb)
		b.WriteByte(':')
		b.Write(vb)
	}
	b.WriteByte('}')
	return []byte(b.String())
}

// VerifyAssignment validates identity fields and the Ed25519 signature. It returns the verified Assignment
// or an error; a malformed/forged document never yields a scope.
func VerifyAssignment(doc SignedAssignment, pub ed25519.PublicKey) (Assignment, error) {
	if _, err := parseUUID16(doc.TenantID); err != nil {
		return Assignment{}, ErrAssignmentFields
	}
	if _, err := parseUUID16(doc.SiteID); err != nil {
		return Assignment{}, ErrAssignmentFields
	}
	if strings.TrimSpace(doc.ApplianceID) == "" || doc.Version <= 0 {
		return Assignment{}, ErrAssignmentFields
	}
	sig, err := base64.StdEncoding.DecodeString(doc.Signature)
	if err != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, doc.canonicalBody(), sig) {
		return Assignment{}, ErrAssignmentUnsigned
	}
	return Assignment{ApplianceID: doc.ApplianceID, TenantID: doc.TenantID, SiteID: doc.SiteID}, nil
}

// FileAssignmentLoader builds a Deps.LoadAssignment that reads a signed assignment JSON from filePath and
// verifies it against pub. An absent file path (factory-clean appliance) returns assigned=false with no
// error — the daemon then does zero PMS work. This performs NO appliance/network contact.
func FileAssignmentLoader(filePath string, pub ed25519.PublicKey) func(context.Context) (Assignment, bool, error) {
	return func(ctx context.Context) (Assignment, bool, error) {
		if strings.TrimSpace(filePath) == "" {
			return Assignment{}, false, nil // unassigned / factory-clean
		}
		raw, err := os.ReadFile(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				return Assignment{}, false, nil
			}
			return Assignment{}, false, err
		}
		var doc SignedAssignment
		if err := json.Unmarshal(raw, &doc); err != nil {
			return Assignment{}, false, ErrAssignmentFields
		}
		a, err := VerifyAssignment(doc, pub)
		if err != nil {
			return Assignment{}, false, err // present but unverifiable → fail closed (do not treat as unassigned)
		}
		return a, true, nil
	}
}
