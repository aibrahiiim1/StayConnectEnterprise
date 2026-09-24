// Package pmsprovider is the single catalogue of PMS connectors this build can run.
//
// WHY A REGISTRY
// --------------
// The set of supported connector kinds used to be written down four times: pmsd's supportedConnectorKinds,
// edged's pmsAllowedKinds, the revision validator that forced FIAS-only values, and the source fingerprint.
// Four lists that are meant to agree are four lists that eventually do not. Every one of them now derives
// from Providers() below, so adding a connector is one entry here plus its adapter -- and a kind that is not
// here can be neither authored in edged nor dialled by pmsd.
//
// WHAT AN ENTRY DECLARES
// ----------------------
// Metadata an operator reads (label, vendor, integration method, verification status, setup steps), the
// configuration schema the authoring API validates against, the credential fields sealed into the interface's
// secret generation, and the capabilities the connector genuinely implements. Nothing here is aspirational:
// a capability is true only when the adapter does it.
//
// PROTEL FIAS IS THE PROTECTED BASELINE. Its entry describes the fields the authoring path accepted before
// this registry existed, key for key, and its fingerprint function is the exact function edged used. The
// registry adds no behaviour to it.
package pmsprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Transport is how the connector reaches the PMS.
type Transport string

const (
	TransportSocket   Transport = "SOCKET"    // a long-lived TCP link (FIAS)
	TransportRESTPoll Transport = "REST_POLL" // HTTPS request/response, polled
)

// Verification is how far this build's compatibility with the provider has been proven.
type Verification string

const (
	// VerifiedLive: proven against a real provider installation.
	VerifiedLive Verification = "LIVE_PROVIDER_VERIFIED"
	// VerifiedContract: proven only against deterministic contract tests built from the provider's published
	// API documentation. It has never exchanged a request with the real service.
	VerifiedContract Verification = "AUTOMATED_CONTRACT_VERIFIED"
)

// Credential modes, matching iam_v2.pms_interface_runtime.credential_mode.
const (
	CredentialNone    = "NONE"
	CredentialAuthKey = "AUTH_KEY"
)

// FieldType is the closed set of configuration value types the UI renders.
type FieldType string

const (
	FieldURL      FieldType = "url"
	FieldHostPort FieldType = "host_port"
	FieldString   FieldType = "string"
	FieldInt      FieldType = "int"
	FieldEnum     FieldType = "enum"
	FieldBool     FieldType = "bool"
)

// Option is one choice of an enum field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field is one configuration value an operator supplies for a revision.
type Field struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Default     any       `json:"default,omitempty"`
	Help        string    `json:"help,omitempty"`
	Placeholder string    `json:"placeholder,omitempty"`
	Options     []Option  `json:"options,omitempty"`
	Min         *int64    `json:"min,omitempty"`
	Max         *int64    `json:"max,omitempty"`
	Unit        string    `json:"unit,omitempty"`
	// Pattern is an optional anchored regular expression a string value must match (server-side only).
	Pattern string `json:"-"`
}

// CredentialField is one value sealed into the interface's secret generation. Never stored in a revision.
type CredentialField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
	Help     string `json:"help,omitempty"`
	Pattern  string `json:"-"`
}

// Credential describes what the connector authenticates with.
type Credential struct {
	Mode   string            `json:"mode"`
	Fields []CredentialField `json:"fields"`
}

// Capabilities are what the connector genuinely implements.
type Capabilities struct {
	FullResync     bool `json:"full_resync"`
	LiveEvents     bool `json:"live_events"`
	TestConnection bool `json:"test_connection"`
	Arrivals       bool `json:"arrivals"`
	Departures     bool `json:"departures"`
}

// Provider is one catalogue entry.
type Provider struct {
	Kind             string       `json:"kind"`
	Label            string       `json:"label"`
	Vendor           string       `json:"vendor"`
	Integration      string       `json:"integration"`
	Transport        Transport    `json:"transport"`
	Verification     Verification `json:"verification"`
	VerificationNote string       `json:"verification_note"`
	DocsURL          string       `json:"docs_url"`
	Credential       Credential   `json:"credential"`
	Fields           []Field      `json:"fields"`
	Capabilities     Capabilities `json:"capabilities"`
	SetupSteps       []string     `json:"setup_steps"`
}

// Field looks up a field by key.
func (p Provider) Field(key string) (Field, bool) {
	for _, f := range p.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// IsREST reports whether the connector is a polled HTTPS integration.
func (p Provider) IsREST() bool { return p.Transport == TransportRESTPoll }

// KindProtelFIAS is the live-accepted baseline connector.
const (
	KindProtelFIAS = "protel-fias"
	KindMews       = "mews"
	KindApaleo     = "apaleo"
	KindOperaCloud = "opera-cloud"
)

// Providers returns the catalogue in a stable display order (the live-verified connector first).
func Providers() []Provider {
	return []Provider{protelFIAS(), mews(), apaleo(), operaCloud()}
}

// Get returns the provider for a connector kind.
func Get(kind string) (Provider, bool) {
	for _, p := range Providers() {
		if p.Kind == kind {
			return p, true
		}
	}
	return Provider{}, false
}

// Kinds returns every registered connector kind, sorted.
func Kinds() []string {
	ps := Providers()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Kind)
	}
	sort.Strings(out)
	return out
}

// KindSet returns the registered kinds as a set, for the allowlists that derive from this registry.
func KindSet() map[string]struct{} {
	m := map[string]struct{}{}
	for _, k := range Kinds() {
		m[k] = struct{}{}
	}
	return m
}

// SourceFingerprint is the stable identity of the PHYSICAL source a revision points at: connector kind plus the
// normalised source identity, hashed and truncated to 32 hex characters.
//
// For protel-fias the identity is the host:port endpoint and this is, byte for byte, the function edged used
// before the registry existed (pmsSourceFingerprint) -- the regression tests pin its output for known inputs.
// For a REST provider the identity is the provider's own property identity (see RESTSourceIdentity).
func SourceFingerprint(connectorKind, identity string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(connectorKind)) + "|" +
		strings.ToLower(strings.TrimSpace(identity))))
	return hex.EncodeToString(sum[:])[:32]
}

func i64(v int64) *int64 { return &v }
