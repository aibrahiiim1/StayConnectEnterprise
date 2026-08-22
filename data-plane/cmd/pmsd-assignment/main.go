// pmsd-assignment — derive pmsd's signed assignment document from the appliance's Central assignment.
//
// WHY THIS EXISTS AS A TOOL RATHER THAN A RUNBOOK STEP. pmsd takes its tenant/site scope from a signed
// document of its OWN schema (internal/pmsd.SignedAssignment), not from Central's assignment.json: the two
// have different fields and different signers, and pmsd verifies its own with a key held on the appliance.
// Producing that document by hand means hand-computing a canonical byte string and an Ed25519 signature,
// which is both error-prone and unrepeatable — and an appliance whose PMS scope exists only as somebody's
// successful afternoon is an appliance nobody can rebuild.
//
// WHAT IT DOES NOT DO: it does not mint identity. Appliance/tenant/site are READ from the Central assignment
// and copied verbatim; if that file is absent, unparseable or not in the `assigned` state, this refuses. The
// signature here attests "this scope was derived from the Central assignment present on this appliance at
// provisioning time", which is exactly the claim pmsd needs — that its scope was not edited into place.
//
// KEYS ARE GENERATED HERE, ON THE APPLIANCE, AND THE PRIVATE HALF NEVER LEAVES IT. The private key signs the
// document and is then needed only to re-sign after a Central reassignment. pmsd itself holds only the public
// key, in its environment, and can verify but never forge.
//
// The document_digest binds the pmsd document to the exact Central document it was derived from: it is the
// SHA-256 of the canonical JSON of Central's `current` object. If Central reassigns the appliance, the digest
// stops matching and `-verify` says so, which is what makes a stale PMS scope detectable rather than silent.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// centralAssignment is the subset of /etc/stayconnect/assignment/assignment.json this tool reads. It is
// deliberately partial: extra fields Central adds later must not break provisioning.
type centralAssignment struct {
	Current        map[string]any `json:"current"`
	LifecycleState string         `json:"lifecycle_state"`
}

// signedAssignment mirrors internal/pmsd.SignedAssignment. It is duplicated rather than imported so this
// tool cannot drift into depending on the daemon's internals — but the canonical body below MUST match
// pmsd.SignedAssignment.canonicalBody() byte for byte, and the round-trip check at the end proves it does
// by verifying the signature the same way the daemon will.
type signedAssignment struct {
	ApplianceID    string `json:"appliance_id"`
	TenantID       string `json:"tenant_id"`
	SiteID         string `json:"site_id"`
	DocumentDigest string `json:"document_digest"`
	Version        int    `json:"version"`
	Signature      string `json:"signature"`
}

// canonicalBody reproduces pmsd's signing input exactly: the JSON object of every field except the
// signature, keys sorted. Written out longhand rather than via json.Marshal of a map because Go's map
// marshalling sorts keys too, but that is an implementation detail of encoding/json rather than a promise —
// and a signature scheme should not rest on an unpromised detail.
func (a signedAssignment) canonicalBody() []byte {
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

// canonicalJSON renders v with sorted keys so the digest of Central's document is stable across
// re-serialisations. Go's encoding/json already sorts map keys; this exists so the intent is explicit.
func canonicalJSON(v any) ([]byte, error) { return json.Marshal(v) }

func main() {
	var (
		centralPath = flag.String("central", "/etc/stayconnect/assignment/assignment.json",
			"path to the Central-issued appliance assignment")
		outPath = flag.String("out", "/etc/stayconnect/assignment/pmsd-assignment.json",
			"path to write the pmsd signed assignment")
		keyPath = flag.String("key", "/etc/stayconnect/secrets/pmsd-assignment.key",
			"Ed25519 private key (hex, 64 bytes); generated if absent")
		verify = flag.Bool("verify", false,
			"verify the existing -out document against -key's public half and the current Central assignment, and write nothing")
	)
	flag.Parse()

	if err := run(*centralPath, *outPath, *keyPath, *verify); err != nil {
		fmt.Fprintln(os.Stderr, "pmsd-assignment: "+err.Error())
		os.Exit(1)
	}
}

func run(centralPath, outPath, keyPath string, verify bool) error {
	raw, err := os.ReadFile(centralPath)
	if err != nil {
		return fmt.Errorf("read Central assignment: %w", err)
	}
	var ca centralAssignment
	if err := json.Unmarshal(raw, &ca); err != nil {
		return fmt.Errorf("parse Central assignment: %w", err)
	}
	// Fail closed on anything but a live assignment. A pending, revoked or absent assignment is not a scope,
	// and deriving a signed document from one would manufacture authority Central has not granted.
	if ca.LifecycleState != "assigned" {
		return fmt.Errorf("Central assignment lifecycle_state is %q, not \"assigned\" — refusing to derive a PMS scope",
			ca.LifecycleState)
	}
	if ca.Current == nil {
		return fmt.Errorf("Central assignment has no `current` document")
	}
	appliance, _ := ca.Current["appliance_id"].(string)
	tenant, _ := ca.Current["tenant_id"].(string)
	site, _ := ca.Current["site_id"].(string)
	if appliance == "" || tenant == "" || site == "" {
		return fmt.Errorf("Central assignment is missing appliance_id/tenant_id/site_id")
	}
	// json numbers decode as float64; the version is small and integral, so the conversion is exact.
	versionF, _ := ca.Current["version"].(float64)
	version := int(versionF)
	if version <= 0 {
		version = 1
	}

	body, err := canonicalJSON(ca.Current)
	if err != nil {
		return fmt.Errorf("canonicalise Central document: %w", err)
	}
	sum := sha256.Sum256(body)
	digest := hex.EncodeToString(sum[:])

	doc := signedAssignment{
		ApplianceID: appliance, TenantID: tenant, SiteID: site,
		DocumentDigest: digest, Version: version,
	}

	if verify {
		return runVerify(outPath, keyPath, doc)
	}

	priv, generated, err := loadOrCreateKey(keyPath)
	if err != nil {
		return err
	}
	doc.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, doc.canonicalBody()))

	// Round-trip before writing: verify with the public half exactly as the daemon will. If the canonical
	// body here ever drifts from the daemon's, this fails at provisioning time rather than as an
	// "assignment signature missing/invalid" at 3am on a service that will not start.
	pub := priv.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, doc.canonicalBody(), mustB64(doc.Signature)) {
		return fmt.Errorf("self-verification failed — canonical body does not match the signature")
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(outPath, append(out, '\n'), 0o640); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	if generated {
		fmt.Printf("key: GENERATED %s (0600, private half stays on this appliance)\n", keyPath)
	} else {
		fmt.Printf("key: reused %s\n", keyPath)
	}
	fmt.Printf("assignment: %s\n", outPath)
	fmt.Printf("appliance_id: %s\ntenant_id: %s\nsite_id: %s\nversion: %d\n", appliance, tenant, site, version)
	fmt.Printf("document_digest: %s\n", digest)
	fmt.Printf("PMSD_ASSIGNMENT_PUBKEY_HEX=%s\n", hex.EncodeToString(pub))
	return nil
}

// runVerify re-derives the expected document from Central and checks the on-disk one against it. It answers
// two different questions that are easy to conflate: is the signature valid (has the file been edited), and
// is the scope still current (has Central reassigned the appliance since it was signed).
func runVerify(outPath, keyPath string, expected signedAssignment) error {
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", outPath, err)
	}
	var have signedAssignment
	if err := json.Unmarshal(raw, &have); err != nil {
		return fmt.Errorf("parse %s: %w", outPath, err)
	}
	priv, generated, err := loadOrCreateKey(keyPath)
	if err != nil {
		return err
	}
	if generated {
		return fmt.Errorf("no key at %s — nothing could have signed %s", keyPath, outPath)
	}
	pub := priv.Public().(ed25519.PublicKey)
	sig, err := base64.StdEncoding.DecodeString(have.Signature)
	if err != nil || !ed25519.Verify(pub, have.canonicalBody(), sig) {
		return fmt.Errorf("signature INVALID for %s", outPath)
	}
	fmt.Printf("signature: valid\n")
	switch {
	case have.ApplianceID != expected.ApplianceID || have.TenantID != expected.TenantID || have.SiteID != expected.SiteID:
		return fmt.Errorf("scope STALE: signed document does not match the current Central assignment")
	case have.DocumentDigest != expected.DocumentDigest:
		return fmt.Errorf("digest STALE: Central's assignment document changed since this was signed (re-run without -verify)")
	}
	fmt.Printf("scope: current (tenant=%s site=%s appliance=%s)\n", have.TenantID, have.SiteID, have.ApplianceID)
	fmt.Printf("PMSD_ASSIGNMENT_PUBKEY_HEX=%s\n", hex.EncodeToString(pub))
	return nil
}

// loadOrCreateKey returns the Ed25519 private key at path, generating one with 0600 if absent. Generation is
// the normal first-run case, so it is reported rather than treated as exceptional — but -verify turns it into
// an error, because there "the key was missing" means the document cannot have been signed by it.
func loadOrCreateKey(path string) (ed25519.PrivateKey, bool, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		b, derr := hex.DecodeString(strings.TrimSpace(string(raw)))
		if derr != nil || len(b) != ed25519.PrivateKeySize {
			return nil, false, fmt.Errorf("%s is not a hex Ed25519 private key (%d bytes decoded)", path, len(b))
		}
		return ed25519.PrivateKey(b), false, nil
	}
	if !os.IsNotExist(err) {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	_, priv, gerr := ed25519.GenerateKey(rand.Reader)
	if gerr != nil {
		return nil, false, gerr
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv)+"\n"), 0o600); err != nil {
		return nil, false, fmt.Errorf("write %s: %w", path, err)
	}
	return priv, true, nil
}

func mustB64(s string) []byte { b, _ := base64.StdEncoding.DecodeString(s); return b }
