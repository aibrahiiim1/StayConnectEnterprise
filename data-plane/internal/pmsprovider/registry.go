// Package pmsprovider is the single catalogue of PMS connectors; pmsd's and edged's allowlists, the revision
// validator and the source fingerprint all derive from Providers(), so a kind not here can be neither authored
// nor dialled. A capability is true only when the adapter does it. Protel FIAS is the protected baseline: its
// fields and fingerprint match the pre-registry behaviour exactly.
package pmsprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

type Transport string

const (
	TransportSocket   Transport = "SOCKET"    // a long-lived TCP link (FIAS)
	TransportRESTPoll Transport = "REST_POLL" // HTTPS request/response, polled
)

type Verification string

const (
	// VerifiedLive: proven against a real provider installation.
	VerifiedLive Verification = "LIVE_PROVIDER_VERIFIED"
	// VerifiedContract: only documentation-based contract tests; never run against the real service.
	VerifiedContract Verification = "AUTOMATED_CONTRACT_VERIFIED"
)

// Credential modes match iam_v2.pms_interface_runtime.credential_mode.
const (
	CredentialNone    = "NONE"
	CredentialAuthKey = "AUTH_KEY"
)

type FieldType string

const (
	FieldURL      FieldType = "url"
	FieldHostPort FieldType = "host_port"
	FieldString   FieldType = "string"
	FieldInt      FieldType = "int"
	FieldEnum     FieldType = "enum"
	FieldBool     FieldType = "bool"
)

type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

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
	// Pattern is an anchored regexp checked server-side only.
	Pattern string `json:"-"`
}

// CredentialField is sealed into the secret generation, never stored in a revision.
type CredentialField struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Secret   bool   `json:"secret"`
	Required bool   `json:"required"`
	Help     string `json:"help,omitempty"`
	Pattern  string `json:"-"`
}

type Credential struct {
	Mode   string            `json:"mode"`
	Fields []CredentialField `json:"fields"`
}

type Capabilities struct {
	FullResync     bool `json:"full_resync"`
	LiveEvents     bool `json:"live_events"`
	TestConnection bool `json:"test_connection"`
	Arrivals       bool `json:"arrivals"`
	Departures     bool `json:"departures"`
}

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

func (p Provider) Field(key string) (Field, bool) {
	for _, f := range p.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

func (p Provider) IsREST() bool { return p.Transport == TransportRESTPoll }

const (
	KindProtelFIAS = "protel-fias"
	KindMews       = "mews"
	KindApaleo     = "apaleo"
	KindOperaCloud = "opera-cloud"
)

// Providers returns the catalogue in stable display order, live-verified connector first.
func Providers() []Provider {
	return []Provider{protelFIAS(), mews(), apaleo(), operaCloud()}
}

func Get(kind string) (Provider, bool) {
	for _, p := range Providers() {
		if p.Kind == kind {
			return p, true
		}
	}
	return Provider{}, false
}

func Kinds() []string {
	ps := Providers()
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Kind)
	}
	sort.Strings(out)
	return out
}

func KindSet() map[string]struct{} {
	m := map[string]struct{}{}
	for _, k := range Kinds() {
		m[k] = struct{}{}
	}
	return m
}

// SourceFingerprint identifies the physical source (kind plus normalised identity, 32 hex chars). For
// protel-fias the identity is host:port and the output must stay byte-identical to the original edged
// function (pinned by regression tests).
func SourceFingerprint(connectorKind, identity string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(connectorKind)) + "|" +
		strings.ToLower(strings.TrimSpace(identity))))
	return hex.EncodeToString(sum[:])[:32]
}

func i64(v int64) *int64 { return &v }
