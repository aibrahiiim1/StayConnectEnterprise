package pmsprovider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRegistry_Invariants(t *testing.T) {
	ps := Providers()
	if ps[0].Kind != KindProtelFIAS {
		t.Fatal("protel-fias must be listed first (the live-verified connector)")
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if seen[p.Kind] {
			t.Fatalf("duplicate kind %s", p.Kind)
		}
		seen[p.Kind] = true
		if !strings.HasPrefix(p.DocsURL, "https://") || p.Label == "" || len(p.SetupSteps) == 0 {
			t.Fatalf("%s: incomplete metadata", p.Kind)
		}
		fk := map[string]bool{}
		for _, f := range p.Fields {
			if fk[f.Key] {
				t.Fatalf("%s: duplicate field %s", p.Kind, f.Key)
			}
			fk[f.Key] = true
			if f.Default != nil {
				if _, e := Coerce(f, f.Default); e != nil {
					t.Fatalf("%s.%s: default %v fails its own validation: %v", p.Kind, f.Key, f.Default, e)
				}
			}
		}
		if !fk["source_timezone"] {
			t.Fatalf("%s: every connector needs the property time zone", p.Kind)
		}
		switch p.Transport {
		case TransportSocket:
			if p.Verification != VerifiedLive || p.Credential.Mode != CredentialNone || p.Capabilities.TestConnection {
				t.Fatalf("%s: socket entry changed", p.Kind)
			}
		case TransportRESTPoll:
			if p.Verification != VerifiedContract {
				t.Fatalf("%s: no REST connector has been live-verified", p.Kind)
			}
			if p.Credential.Mode != CredentialAuthKey || len(p.Credential.Fields) == 0 {
				t.Fatalf("%s: REST connectors authenticate", p.Kind)
			}
			for _, k := range []string{"poll_interval_seconds", "request_timeout_seconds", "full_resync_minutes"} {
				if !fk[k] {
					t.Fatalf("%s: missing %s", p.Kind, k)
				}
			}
			for _, cf := range p.Credential.Fields {
				if fk[cf.Key] {
					t.Fatalf("%s: credential %s must never be a revision field", p.Kind, cf.Key)
				}
			}
		default:
			t.Fatalf("%s: unknown transport", p.Kind)
		}
	}
	for _, k := range []string{"opera-fias", "fidelio-fias", "stub"} {
		if _, ok := Get(k); ok {
			t.Fatalf("%s must not be registered", k)
		}
	}
}

func TestNormalizeREST_DefaultsAndTimings(t *testing.T) {
	p, _ := Get(KindMews)
	rc, e := NormalizeREST(p, map[string]any{}, "Europe/Prague", 0)
	if e != nil {
		t.Fatal(e)
	}
	tm := rc.Timings()
	if tm["heartbeat_interval_ms"] != 60000 || tm["heartbeat_timeout_ms"] != 180000 || tm["complete_sync_ms"] != 3600000 || tm["dial_timeout_ms"] != 30000 {
		t.Fatalf("timings %v", tm)
	}
	rc, _ = NormalizeREST(p, map[string]any{"poll_interval_seconds": json.Number("15"), "request_timeout_seconds": json.Number("120")}, "UTC", 0)
	if tm := rc.Timings(); tm["heartbeat_timeout_ms"] != 15000+240000 {
		t.Fatalf("a slow request must widen the keep-alive timeout: %v", tm)
	}
	if _, e := NormalizeREST(p, map[string]any{"platform_url": "https://user:pw@api.mews.com"}, "UTC", 0); e == nil {
		t.Fatal("credentials in a URL must be refused")
	}
	if _, e := NormalizeREST(p, map[string]any{"platform_url": "https://api.mews.com?x=1"}, "UTC", 0); e == nil {
		t.Fatal("a query string must be refused")
	}
}

func TestNormalizeSecret_Shapes(t *testing.T) {
	m, _ := Get(KindMews)
	b, e := NormalizeSecret(m, json.RawMessage(`{"access_token":" AT ","client_token":"CT"}`))
	if e != nil || string(b) != `{"access_token":"AT","client_token":"CT"}` {
		t.Fatalf("%s %v", b, e)
	}
	// a JSON string containing the object is accepted too
	if _, e := NormalizeSecret(m, json.RawMessage(`"{\"access_token\":\"a\",\"client_token\":\"c\"}"`)); e != nil {
		t.Fatal(e)
	}
	cases := map[string]string{
		"missing field": `{"access_token":"a"}`,
		"unknown field": `{"access_token":"a","client_token":"c","password":"p"}`,
		"not text":      `{"access_token":1,"client_token":"c"}`,
		"newline":       `{"access_token":"a\nb","client_token":"c"}`,
		"bare string":   `"just-a-token"`,
		"array":         `["a"]`,
	}
	for name, raw := range cases {
		_, e := NormalizeSecret(m, json.RawMessage(raw))
		if e == nil {
			t.Errorf("%s accepted", name)
			continue
		}
		if strings.Contains(e.Message, "just-a-token") || strings.Contains(e.Message, "a\nb") {
			t.Errorf("%s: a credential value leaked into the error: %s", name, e.Message)
		}
	}
	o, _ := Get(KindOperaCloud)
	if _, e := NormalizeSecret(o, json.RawMessage(`{"client_id":"i","client_secret":"s","app_key":"not-a-uuid"}`)); e == nil {
		t.Fatal("OHIP app key must be a UUID")
	}
	f, _ := Get(KindProtelFIAS)
	if _, e := NormalizeSecret(f, json.RawMessage(`{}`)); e == nil {
		t.Fatal("protel-fias has no credential fields")
	}
}

func TestParseStoredProvider_RoundTrip(t *testing.T) {
	p, _ := Get(KindApaleo)
	rc, e := NormalizeREST(p, map[string]any{"property_id": "MUC"}, "Europe/Berlin", 0)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(rc.StoredConfig()["provider"])
	back, err := ParseStoredProvider(p, raw)
	if err != nil || back["property_id"] != "MUC" || back["poll_interval_seconds"] != int64(60) {
		t.Fatalf("%v %v", back, err)
	}
	if _, err := ParseStoredProvider(p, []byte(`{"property_id":"MUC","extra":1}`)); err == nil {
		t.Fatal("an unknown stored key must fail closed")
	}
	if _, err := ParseStoredProvider(p, nil); err == nil {
		t.Fatal("missing provider config must fail closed")
	}
}

func TestSourceIdentity_NeverContainsCredentials(t *testing.T) {
	p, _ := Get(KindOperaCloud)
	rc, e := NormalizeREST(p, map[string]any{"gateway_url": "https://gw.example.com", "hotel_id": "H1", "scope": "s"}, "UTC", 0)
	if e != nil {
		t.Fatal(e)
	}
	if rc.SourceIdentity() != "https://gw.example.com|H1" || len(SourceFingerprint(p.Kind, rc.SourceIdentity())) != 32 {
		t.Fatalf("identity %q", rc.SourceIdentity())
	}
}
