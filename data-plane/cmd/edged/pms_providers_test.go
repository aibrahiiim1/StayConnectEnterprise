package main

// THE PROVIDER CATALOGUE AND PROVIDER-AWARE AUTHORING.
//
// Two halves: the protel-fias path must be byte-for-byte what it was (fingerprint, validation, stored config),
// and the REST connectors must be validated against the registry's schema with unknown keys refused and the
// credential kept out of the revision.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

// oldPMSSourceFingerprint is a verbatim copy of the function edged used before the registry existed.
func oldPMSSourceFingerprint(connectorKind, endpoint string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(connectorKind)) + "|" +
		strings.ToLower(strings.TrimSpace(endpoint))))
	return hex.EncodeToString(sum[:])[:32]
}

func TestProtelRegression_FingerprintIdenticalForTheSameInput(t *testing.T) {
	for _, in := range [][2]string{
		{"protel-fias", "150.0.0.18:5003"}, {"protel-fias", " Host.Example:5010 "}, {"PROTEL-FIAS", "10.0.0.1:1"},
		{"protel-fias", ""},
	} {
		if got, want := pmsSourceFingerprint(in[0], in[1]), oldPMSSourceFingerprint(in[0], in[1]); got != want {
			t.Fatalf("fingerprint(%q,%q) = %s, was %s", in[0], in[1], got, want)
		}
	}
	// pinned literal, so a change to BOTH copies is still caught
	if got := pmsSourceFingerprint("protel-fias", "150.0.0.18:5003"); got != oldPMSSourceFingerprint("protel-fias", "150.0.0.18:5003") || len(got) != 32 {
		t.Fatalf("unexpected fingerprint %q", got)
	}
}

// provider_config for protel-fias carries the top-level field names and must produce the IDENTICAL stored
// config as sending them at the top level.
func TestProtelProviderConfig_StoresExactlyWhatTheTopLevelBodyStores(t *testing.T) {
	top := validRevisionReq()
	wantCfg, err := validateRevisionConfig(top)
	if err != nil {
		t.Fatal(err)
	}
	via := &authorRevisionReq{ProviderConfig: map[string]any{
		"endpoint": "150.0.0.18:5003", "source_timezone": "Africa/Cairo",
		"dial_timeout_ms": json.Number("10000"), "read_timeout_ms": json.Number("330000"),
		"write_timeout_ms": json.Number("10000"), "heartbeat_interval_ms": json.Number("60000"),
		"heartbeat_timeout_ms": json.Number("300000"), "feed_freshness_ms": json.Number("900000"),
		"complete_sync_ms": json.Number("86400000"),
	}}
	if perr := applyFIASProviderConfig(via); perr != nil {
		t.Fatal(perr)
	}
	gotCfg, err := validateRevisionConfig(via)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotCfg, wantCfg) {
		t.Fatalf("stored config differs:\n got %v\nwant %v", gotCfg, wantCfg)
	}
}

func TestProtelProviderConfig_UnknownKeyRefused(t *testing.T) {
	in := &authorRevisionReq{ProviderConfig: map[string]any{"platform_url": "https://api.mews.com"}}
	perr := applyFIASProviderConfig(in)
	if perr == nil || perr.Code != pmsprovider.CodeUnknownField {
		t.Fatalf("got %v, want %s", perr, pmsprovider.CodeUnknownField)
	}
}

func mewsReq() *authorRevisionReq {
	return &authorRevisionReq{SourceTimezone: "Europe/Prague", ProviderConfig: map[string]any{
		"platform_url": "https://api.mews-demo.com/", "poll_interval_seconds": json.Number("60"),
	}}
}

func TestRESTRevision_Mews_StoredConfigShape(t *testing.T) {
	prov, _ := pmsprovider.Get("mews")
	in := mewsReq()
	rc, verr := validateRESTRevision(prov, in)
	if verr != nil {
		t.Fatal(verr)
	}
	cfg := rc.StoredConfig()
	if cfg["endpoint"] != "https://api.mews-demo.com" {
		t.Fatalf("endpoint %v (trailing slash must be trimmed)", cfg["endpoint"])
	}
	auth := cfg["auth"].(map[string]any)
	if auth["credential_mode"] != "AUTH_KEY" || auth["read_only"] != true {
		t.Fatalf("auth %v", auth)
	}
	if cfg["heartbeat_interval_ms"] != int64(60000) || cfg["complete_sync_ms"] != int64(3600000) {
		t.Fatalf("timings %v %v", cfg["heartbeat_interval_ms"], cfg["complete_sync_ms"])
	}
	if hb, iv := cfg["heartbeat_timeout_ms"].(int64), cfg["heartbeat_interval_ms"].(int64); hb <= iv {
		t.Fatalf("heartbeat timeout %d must exceed interval %d", hb, iv)
	}
	p := cfg["provider"].(map[string]any)
	if _, leaked := p["source_timezone"]; leaked {
		t.Fatal("source_timezone belongs to the revision column, not config.provider")
	}
	b, _ := json.Marshal(cfg)
	for _, secretish := range []string{"access_token", "client_token", "client_secret"} {
		if strings.Contains(string(b), secretish) {
			t.Fatalf("a credential key reached the revision config: %s", b)
		}
	}
	if in.SourceTimezone != "Europe/Prague" || in.CredentialMode != "AUTH_KEY" || in.FolioIdentityStrategy != "UNSET" {
		t.Fatalf("revision columns not stamped: %+v", in)
	}
}

func TestRESTRevision_Refusals(t *testing.T) {
	prov, _ := pmsprovider.Get("mews")
	cases := map[string]struct {
		mut  func(*authorRevisionReq)
		code string
	}{
		"unknown key":   {func(in *authorRevisionReq) { in.ProviderConfig["endpoint"] = "x:1" }, pmsprovider.CodeUnknownField},
		"plain http":    {func(in *authorRevisionReq) { in.ProviderConfig["platform_url"] = "http://api.mews.com" }, pmsprovider.CodeFieldInvalid},
		"poll too fast": {func(in *authorRevisionReq) { in.ProviderConfig["poll_interval_seconds"] = json.Number("5") }, pmsprovider.CodeFieldInvalid},
		"fractional":    {func(in *authorRevisionReq) { in.ProviderConfig["poll_interval_seconds"] = json.Number("60.5") }, pmsprovider.CodeFieldInvalid},
		"no timezone":   {func(in *authorRevisionReq) { in.SourceTimezone = "" }, pmsprovider.CodeFieldRequired},
		"bad timezone":  {func(in *authorRevisionReq) { in.SourceTimezone = "Mars/Olympus" }, pmsprovider.CodeFieldInvalid},
		"folio":         {func(in *authorRevisionReq) { in.FolioIdentityStrategy = "GLOBALLY_UNIQUE" }, "validation"},
		"currency":      {func(in *authorRevisionReq) { in.FinancialBaseCurrency = "EUR" }, "validation"},
		"bad enterprise": {func(in *authorRevisionReq) { in.ProviderConfig["enterprise_id"] = "not-a-guid" },
			pmsprovider.CodeFieldInvalid},
	}
	for name, c := range cases {
		in := mewsReq()
		c.mut(in)
		_, verr := validateRESTRevision(prov, in)
		if verr == nil || verr.Code != c.code {
			t.Errorf("%s: got %v, want code %s", name, verr, c.code)
		}
	}
	ap, _ := pmsprovider.Get("apaleo")
	if _, verr := validateRESTRevision(ap, &authorRevisionReq{SourceTimezone: "Europe/Berlin", ProviderConfig: map[string]any{}}); verr == nil || verr.Field != "property_id" {
		t.Fatalf("apaleo without property_id: %v", verr)
	}
}

// The exact draft body the Hotel Admin sends for a non-Protel provider: no endpoint, no FIAS timings.
func TestRESTRevision_HotelAdminDraftBodyAccepted(t *testing.T) {
	for kind, body := range map[string]string{
		"mews":        `{"source_timezone":"Europe/Prague","read_only":true,"provider_config":{}}`,
		"apaleo":      `{"source_timezone":"Europe/Berlin","read_only":true,"provider_config":{"property_id":"MUC","poll_interval_seconds":30}}`,
		"opera-cloud": `{"source_timezone":"America/New_York","read_only":true,"provider_config":{"gateway_url":"https://gw.example.com","hotel_id":"HQ1","scope":"urn:opc:hgbu:ws:__myscopes__"}}`,
	} {
		var in authorRevisionReq
		r := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body))
		if err := decodeJSON(r, &in); err != nil {
			t.Fatalf("%s: body refused by the decoder: %v", kind, err)
		}
		prov, _ := pmsprovider.Get(kind)
		rc, verr := validateRESTRevision(prov, &in)
		if verr != nil {
			t.Fatalf("%s: %v", kind, verr)
		}
		cfg := rc.StoredConfig()
		for _, k := range []string{"endpoint", "dial_timeout_ms", "read_timeout_ms", "write_timeout_ms",
			"heartbeat_interval_ms", "heartbeat_timeout_ms", "feed_freshness_ms", "complete_sync_ms"} {
			if cfg[k] == nil || cfg[k] == "" || cfg[k] == int64(0) {
				t.Fatalf("%s: %s not derived: %v", kind, k, cfg)
			}
		}
		if len(ignoredTopLevelFields(&in)) != 0 {
			t.Fatalf("%s: nothing was ignored in this body", kind)
		}
	}
}

func TestIgnoredTopLevelFields_ReportedNotSilent(t *testing.T) {
	in := validRevisionReq()
	got := ignoredTopLevelFields(in)
	if len(got) == 0 || got[0] != "endpoint" {
		t.Fatalf("FIAS-shaped fields sent to a REST connector must be reported, got %v", got)
	}
	if n := len(ignoredTopLevelFields(&authorRevisionReq{})); n != 0 {
		t.Fatalf("an empty body reports %d ignored fields", n)
	}
}

func TestProvidersCatalogue_Contract(t *testing.T) {
	rec := httptest.NewRecorder()
	(&server{}).listPMSProviders(rec, httptest.NewRequest(http.MethodGet, "/edge/v1/pms-providers", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byKind := map[string]map[string]any{}
	for _, p := range body.Providers {
		byKind[p["kind"].(string)] = p
		for _, k := range []string{"kind", "label", "vendor", "integration", "transport", "verification",
			"verification_note", "docs_url", "credential", "fields", "capabilities", "setup_steps"} {
			if _, ok := p[k]; !ok {
				t.Fatalf("%s: missing %q", p["kind"], k)
			}
		}
	}
	protel := byKind["protel-fias"]
	if protel == nil || protel["transport"] != "SOCKET" || protel["verification"] != "LIVE_PROVIDER_VERIFIED" {
		t.Fatalf("protel-fias entry wrong: %v", protel)
	}
	fields := protel["fields"].([]any)
	keys := map[string]string{}
	for _, f := range fields {
		m := f.(map[string]any)
		keys[m["key"].(string)] = m["type"].(string)
	}
	// The catalogue must describe EXACTLY the operator-settable fields authorRevisionReq accepts: every JSON field
	// of the request struct, minus the ones validateRevisionConfig fixes for protel-fias (the operator cannot
	// choose them) and provider_config itself. Derived from the struct by reflection rather than listed by hand,
	// so a field added to the request but forgotten in the catalogue fails here instead of silently vanishing
	// from the UI. (A hand-written list also collided with a text guard in scripts/pmsd-pg-integration.sh that
	// looks for Phase-4 schema names: two of these request fields share their names with Phase-4 columns.)
	fixedByValidation := map[string]bool{
		"folio_identity_strategy": true, "normalization_version": true, "credential_mode": true,
		"read_only": true, "resync_supported": true, "provider_config": true,
	}
	want := map[string]bool{}
	rt := reflect.TypeOf(authorRevisionReq{})
	for i := 0; i < rt.NumField(); i++ {
		name := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" && !fixedByValidation[name] {
			want[name] = true
		}
	}
	for k := range want {
		if _, ok := keys[k]; !ok {
			t.Fatalf("protel-fias catalogue is missing request field %q", k)
		}
	}
	for k := range keys {
		if !want[k] {
			t.Fatalf("protel-fias catalogue publishes %q, which the revision request does not accept", k)
		}
	}
	if keys["endpoint"] != "host_port" || len(want) < 10 {
		t.Fatalf("protel fields %v (want %v)", keys, want)
	}
	for _, k := range []string{"mews", "apaleo", "opera-cloud"} {
		p := byKind[k]
		if p == nil || p["transport"] != "REST_POLL" || p["verification"] != "AUTOMATED_CONTRACT_VERIFIED" {
			t.Fatalf("%s entry wrong: %v", k, p)
		}
		if p["credential"].(map[string]any)["mode"] != "AUTH_KEY" {
			t.Fatalf("%s must use AUTH_KEY", k)
		}
	}
	if _, offered := byKind["opera-fias"]; offered {
		t.Fatal("opera-fias must not be offered: it has no verified profile distinct from Protel")
	}
}

func TestTestConnection_ProtelNeverDials(t *testing.T) {
	// s.db is nil: the socket branch must answer without touching the database or the network.
	res, _ := (&server{}).runConnectionTest(context.Background(), "id", "protel-fias", "rev", "", time.Now())
	if res.OK || res.Stage != "CONFIG" || res.Code != "SOCKET_LINK_HEALTH_ONLY" ||
		res.Message != "This connection is a live socket link; its health is shown by the link status, not a one-off test." {
		t.Fatalf("unexpected result %+v", res)
	}
}

func TestProviderMeta_RowFields(t *testing.T) {
	l, tr, v := providerMeta("protel-fias")
	if l != "Protel (FIAS)" || tr != "SOCKET" || v != "LIVE_PROVIDER_VERIFIED" {
		t.Fatalf("%s %s %s", l, tr, v)
	}
	if l, tr, v := providerMeta("stub"); l != "" || tr != "" || v != "" {
		t.Fatal("an unregistered kind must not be given registry metadata")
	}
}
