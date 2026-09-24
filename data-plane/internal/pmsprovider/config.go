package pmsprovider

import (
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Validation error codes. Bounded machine codes the API returns as the "error" field of a 400.
const (
	CodeUnknownField    = "unknown_provider_field"
	CodeFieldRequired   = "provider_field_required"
	CodeFieldInvalid    = "provider_field_invalid"
	CodeUnknownKind     = "unknown_connector_kind"
	CodeCredentialShape = "credential_invalid"
)

// ValidationError is a configuration refusal with a bounded code, the offending key and a plain message.
type ValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

func verr(code, field, format string, a ...any) *ValidationError {
	return &ValidationError{Code: code, Field: field, Message: fmt.Sprintf(format, a...)}
}

// Coerce validates ONE supplied value against its field definition and returns the typed value (int64 for
// int, string for url/host_port/string/enum, bool for bool). JSON numbers may arrive as json.Number (edged
// decodes with UseNumber), float64 or an integer type; a fractional number is refused rather than rounded.
func Coerce(f Field, raw any) (any, *ValidationError) {
	switch f.Type {
	case FieldInt:
		n, ok := asInt(raw)
		if !ok {
			return nil, verr(CodeFieldInvalid, f.Key, "%s must be a whole number", f.Label)
		}
		if f.Min != nil && n < *f.Min || f.Max != nil && n > *f.Max {
			return nil, verr(CodeFieldInvalid, f.Key, "%s must be between %d and %d", f.Label, deref(f.Min), deref(f.Max))
		}
		return n, nil
	case FieldBool:
		b, ok := raw.(bool)
		if !ok {
			return nil, verr(CodeFieldInvalid, f.Key, "%s must be true or false", f.Label)
		}
		return b, nil
	}
	s, ok := raw.(string)
	if !ok {
		return nil, verr(CodeFieldInvalid, f.Key, "%s must be text", f.Label)
	}
	s = strings.TrimSpace(s)
	if len(s) > 512 || strings.ContainsAny(s, "\x00\r\n") {
		return nil, verr(CodeFieldInvalid, f.Key, "%s is too long or contains control characters", f.Label)
	}
	switch f.Type {
	case FieldURL:
		if err := validHTTPSURL(s); err != "" {
			return nil, verr(CodeFieldInvalid, f.Key, "%s %s", f.Label, err)
		}
		s = strings.TrimRight(s, "/")
	case FieldHostPort:
		host, port, err := net.SplitHostPort(s)
		p, perr := strconv.Atoi(port)
		if err != nil || host == "" || perr != nil || p < 1 || p > 65535 {
			return nil, verr(CodeFieldInvalid, f.Key, "%s must be host:port with a port in 1..65535", f.Label)
		}
	case FieldEnum:
		found := false
		for _, o := range f.Options {
			if o.Value == s {
				found = true
			}
		}
		if !found {
			return nil, verr(CodeFieldInvalid, f.Key, "%s is not one of the allowed values", f.Label)
		}
	}
	if s != "" && f.Pattern != "" && !regexp.MustCompile(f.Pattern).MatchString(s) {
		return nil, verr(CodeFieldInvalid, f.Key, "%s is not in the expected format", f.Label)
	}
	return s, nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func asInt(raw any) (int64, bool) {
	switch v := raw.(type) {
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	case float64:
		if v != math.Trunc(v) || math.IsInf(v, 0) || math.IsNaN(v) || math.Abs(v) > 1<<53 {
			return 0, false
		}
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case int32:
		return int64(v), true
	}
	return 0, false
}

// validHTTPSURL refuses anything but an absolute https URL with a host and no credentials, query or fragment.
// Provider addresses are dialled with the property's credentials attached, so plain http is never acceptable.
func validHTTPSURL(s string) string {
	u, err := url.Parse(s)
	switch {
	case err != nil || s == "":
		return "must be a URL"
	case u.Scheme != "https":
		return "must start with https://"
	case u.Host == "" || u.Hostname() == "":
		return "must include a host name"
	case u.User != nil:
		return "must not contain a user name or password"
	case u.RawQuery != "" || u.Fragment != "":
		return "must not contain a query or fragment"
	}
	return ""
}

// RESTConfig is the normalised, validated configuration of a REST provider revision.
type RESTConfig struct {
	Kind string
	// Provider holds the provider-specific values (URLs, ids, poll settings), keyed by field key. It is stored
	// verbatim as config.provider.
	Provider       map[string]any
	SourceTimezone string
	MaxAuthCache   int64
	PollInterval   time.Duration
	RequestTimeout time.Duration
	FullResync     time.Duration
}

// commonKeys are fields every REST schema carries that are NOT stored under config.provider but mapped onto
// the revision's own columns / keys.
var commonKeys = map[string]bool{"source_timezone": true, "max_auth_cache_age_seconds": true}

// NormalizeREST validates a REST provider's provider_config against its schema. Unknown keys are refused;
// defaults fill omitted optional values; required values must be present. topTimezone/topMaxAuth are the
// revision body's top-level values, used when provider_config does not carry them.
func NormalizeREST(p Provider, in map[string]any, topTimezone string, topMaxAuth int64) (RESTConfig, *ValidationError) {
	if !p.IsREST() {
		return RESTConfig{}, verr(CodeUnknownKind, "", "%s is not a polled REST connector", p.Kind)
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := p.Field(k); !ok {
			return RESTConfig{}, verr(CodeUnknownField, k, "%q is not a setting of the %s connector", k, p.Label)
		}
	}
	out := RESTConfig{Kind: p.Kind, Provider: map[string]any{}, SourceTimezone: strings.TrimSpace(topTimezone), MaxAuthCache: topMaxAuth}
	for _, f := range p.Fields {
		raw, present := in[f.Key]
		if present && raw != nil {
			if s, isStr := raw.(string); isStr && strings.TrimSpace(s) == "" && f.Type != FieldString {
				present = false
			}
		}
		if !present || raw == nil {
			switch {
			case commonKeys[f.Key]:
				continue // falls back to the top-level value
			case f.Default != nil:
				raw = f.Default
			case f.Required:
				return RESTConfig{}, verr(CodeFieldRequired, f.Key, "%s is required", f.Label)
			default:
				continue
			}
		}
		v, e := Coerce(f, raw)
		if e != nil {
			return RESTConfig{}, e
		}
		if s, ok := v.(string); ok && s == "" {
			if f.Required {
				return RESTConfig{}, verr(CodeFieldRequired, f.Key, "%s is required", f.Label)
			}
			continue
		}
		switch f.Key {
		case "source_timezone":
			out.SourceTimezone = v.(string)
		case "max_auth_cache_age_seconds":
			out.MaxAuthCache = v.(int64)
		default:
			out.Provider[f.Key] = v
		}
	}
	if out.SourceTimezone == "" {
		return RESTConfig{}, verr(CodeFieldRequired, "source_timezone", "Property time zone is required")
	}
	if _, err := time.LoadLocation(out.SourceTimezone); err != nil {
		return RESTConfig{}, verr(CodeFieldInvalid, "source_timezone", "%q is not a known IANA time zone", out.SourceTimezone)
	}
	if out.MaxAuthCache < 0 || out.MaxAuthCache > 604800 {
		return RESTConfig{}, verr(CodeFieldInvalid, "max_auth_cache_age_seconds", "Offline sign-in window must be between 0 and 604800 seconds")
	}
	out.PollInterval = time.Duration(out.Provider["poll_interval_seconds"].(int64)) * time.Second
	out.RequestTimeout = time.Duration(out.Provider["request_timeout_seconds"].(int64)) * time.Second
	out.FullResync = time.Duration(out.Provider["full_resync_minutes"].(int64)) * time.Minute
	return out, nil
}

// Endpoint is the base address stored as config.endpoint (what pmsd's revision reads as the endpoint).
func (c RESTConfig) Endpoint() string {
	switch c.Kind {
	case KindMews:
		return str(c.Provider, "platform_url")
	case KindApaleo:
		return str(c.Provider, "api_url")
	case KindOperaCloud:
		return str(c.Provider, "gateway_url")
	}
	return ""
}

// SourceIdentity is the provider's identity of the PHYSICAL property, fed to SourceFingerprint. It never
// includes a credential. For Mews without an enterprise id the property is identified by its access token,
// which is secret, so two such interfaces on one platform fingerprint alike: a duplicate-source warning that
// errs towards asking the operator rather than missing a real duplicate.
func (c RESTConfig) SourceIdentity() string {
	switch c.Kind {
	case KindMews:
		return str(c.Provider, "platform_url") + "|" + strings.ToLower(str(c.Provider, "enterprise_id")) + "|" +
			strings.ToLower(str(c.Provider, "service_id"))
	case KindApaleo:
		return str(c.Provider, "api_url") + "|" + str(c.Provider, "property_id")
	case KindOperaCloud:
		return str(c.Provider, "gateway_url") + "|" + str(c.Provider, "hotel_id")
	}
	return ""
}

// Timings are the seven revision duration keys pmsd.Revision requires, derived from the poll settings so a
// REST revision satisfies exactly the same Validate() contract as a FIAS one:
//
//	dial/read/write timeout = request timeout
//	heartbeat interval      = poll interval (each successful poll is the keep-alive)
//	heartbeat timeout       = max(3 x poll, poll + 2 x request timeout): three missed polls, never less than
//	                          one poll plus a slow request and its retry
//	feed freshness          = heartbeat timeout
//	complete sync           = full refresh interval
func (c RESTConfig) Timings() map[string]int64 {
	poll := c.PollInterval.Milliseconds()
	req := c.RequestTimeout.Milliseconds()
	hbTimeout := 3 * poll
	if alt := poll + 2*req; alt > hbTimeout {
		hbTimeout = alt
	}
	return map[string]int64{
		"dial_timeout_ms":       req,
		"read_timeout_ms":       req,
		"write_timeout_ms":      req,
		"heartbeat_interval_ms": poll,
		"heartbeat_timeout_ms":  hbTimeout,
		"feed_freshness_ms":     hbTimeout,
		"complete_sync_ms":      c.FullResync.Milliseconds(),
	}
}

// StoredConfig is the revision config jsonb for a REST provider: the same keys pgRepo.LoadInterface projects
// for every connector, plus config.provider.
func (c RESTConfig) StoredConfig() map[string]any {
	cfg := map[string]any{
		"endpoint":         c.Endpoint(),
		"resync_supported": true,
		"auth":             map[string]any{"credential_mode": CredentialAuthKey, "read_only": true},
		"provider":         c.Provider,
	}
	for k, v := range c.Timings() {
		cfg[k] = v
	}
	if c.MaxAuthCache > 0 {
		cfg["max_auth_cache_age_seconds"] = c.MaxAuthCache
	}
	return cfg
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// NormalizeSecret validates a credential for a REST provider and returns the canonical JSON plaintext that is
// sealed into the secret generation. raw is the request's "secret" value: a JSON object of the credential
// fields, or a JSON string containing such an object. A provider with exactly one credential field also
// accepts a plain string for it. Unknown keys are refused; values are never echoed in an error.
func NormalizeSecret(p Provider, raw json.RawMessage) ([]byte, *ValidationError) {
	if p.Credential.Mode != CredentialAuthKey {
		return nil, verr(CodeCredentialShape, "secret", "the %s connector does not use a stored credential", p.Label)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, verr(CodeCredentialShape, "secret", "the credential must be an object of %s", credentialKeyList(p))
		}
		if json.Unmarshal([]byte(s), &obj) != nil || obj == nil {
			if len(p.Credential.Fields) != 1 {
				return nil, verr(CodeCredentialShape, "secret", "the credential must be an object of %s", credentialKeyList(p))
			}
			obj = map[string]any{p.Credential.Fields[0].Key: s}
		}
	}
	known := map[string]CredentialField{}
	for _, f := range p.Credential.Fields {
		known[f.Key] = f
	}
	out := map[string]string{}
	for k, v := range obj {
		f, ok := known[k]
		if !ok {
			return nil, verr(CodeUnknownField, k, "%q is not a credential of the %s connector (expected %s)", k, p.Label, credentialKeyList(p))
		}
		s, isStr := v.(string)
		s = strings.TrimSpace(s)
		if !isStr || len(s) > 4096 || strings.ContainsAny(s, "\x00\r\n") {
			return nil, verr(CodeCredentialShape, k, "%s must be a single line of text", f.Label)
		}
		if s != "" && f.Pattern != "" && !regexp.MustCompile(f.Pattern).MatchString(s) {
			return nil, verr(CodeCredentialShape, k, "%s is not in the expected format", f.Label)
		}
		if s != "" {
			out[k] = s
		}
	}
	for _, f := range p.Credential.Fields {
		if f.Required && out[f.Key] == "" {
			return nil, verr(CodeFieldRequired, f.Key, "%s is required", f.Label)
		}
	}
	b, _ := json.Marshal(out) // map keys marshal sorted: deterministic plaintext
	return b, nil
}

func credentialKeyList(p Provider) string {
	ks := make([]string, 0, len(p.Credential.Fields))
	for _, f := range p.Credential.Fields {
		ks = append(ks, f.Key)
	}
	return strings.Join(ks, ", ")
}

// ParseStoredProvider reads a stored config.provider object back into the typed map NormalizeREST produced
// (numbers become int64). Used by pmsd, which reads the jsonb as bytes.
func ParseStoredProvider(p Provider, raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("provider configuration missing")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, fmt.Errorf("provider configuration unreadable")
	}
	out := map[string]any{}
	for k, v := range m {
		f, ok := p.Field(k)
		if !ok || commonKeys[k] {
			return nil, fmt.Errorf("provider configuration carries an unknown key")
		}
		cv, e := Coerce(f, v)
		if e != nil {
			return nil, fmt.Errorf("provider configuration value invalid: %s", f.Key)
		}
		out[k] = cv
	}
	for _, f := range p.Fields {
		if f.Required && !commonKeys[f.Key] {
			if _, ok := out[f.Key]; !ok {
				return nil, fmt.Errorf("provider configuration missing required key: %s", f.Key)
			}
		}
	}
	return out, nil
}
