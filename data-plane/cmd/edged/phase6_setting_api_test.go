package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// The setting write takes tenant, site, appliance and operator identity from the server, never the caller:
// a request carrying any of them must be refused, not parsed-and-ignored.

func decodeSetting(body string) error {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var in guestDeviceSettingReq
	return dec.Decode(&in)
}

func TestPhase6SettingWireTypeRefusesEveryIdentity(t *testing.T) {
	for name, body := range map[string]string{
		"tenant_id":       `{"enabled":true,"tenant_id":"11111111-1111-1111-1111-111111111111"}`,
		"site_id":         `{"enabled":true,"site_id":"22222222-2222-2222-2222-222222222222"}`,
		"appliance_id":    `{"enabled":true,"appliance_id":"44444444-4444-4444-4444-444444444444"}`,
		"operator_id":     `{"enabled":true,"operator_id":"55555555-5555-5555-5555-555555555555"}`,
		"changed_by":      `{"enabled":true,"changed_by":"somebody else"}`,
		"operator_label":  `{"enabled":true,"operator_label":"somebody else"}`,
		"actor":           `{"enabled":true,"actor":"somebody else"}`,
		"setting_key":     `{"enabled":true,"setting_key":"something_else"}`,
		"phase_gate":      `{"enabled":true,"phase_gate_enabled":true}`,
		"appliance_scope": `{"enabled":true,"appliance":{"tenant_id":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := decodeSetting(body); err == nil {
				t.Fatalf("the setting wire type ACCEPTED a request carrying %s", name)
			}
		})
	}
}

// The legitimate shape must still decode.
func TestPhase6SettingWireTypeAcceptsOnlyValueAndReason(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`{"enabled":true,"reason":"guests asked for it"}`))
	dec.DisallowUnknownFields()
	var in guestDeviceSettingReq
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("the setting wire type refused its own legitimate shape: %v", err)
	}
	if in.Enabled == nil || !*in.Enabled || in.Reason != "guests asked for it" {
		t.Fatalf("the legitimate shape did not decode: %+v", in)
	}
}

// `enabled` is a pointer so absent differs from false; a PUT that omits it must not turn the capability off.
func TestPhase6SettingDistinguishesAbsentFromFalse(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`{"reason":"forgot the value"}`))
	dec.DisallowUnknownFields()
	var in guestDeviceSettingReq
	if err := dec.Decode(&in); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if in.Enabled != nil {
		t.Fatal("an absent 'enabled' decoded as a value")
	}

	dec = json.NewDecoder(strings.NewReader(`{"enabled":false}`))
	dec.DisallowUnknownFields()
	var off guestDeviceSettingReq
	if err := dec.Decode(&off); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if off.Enabled == nil || *off.Enabled {
		t.Fatal("an explicit false did not decode as false")
	}
}

// The response must keep setting and capability separate ("setting on, capability not deployed"), so an operator is not misled.
func TestPhase6SettingResponseSeparatesTheProductSettingFromTheDeploymentGate(t *testing.T) {
	b, err := json.Marshal(guestDeviceSettingResp{Enabled: true, Changed: true, PhaseGateEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["enabled"] != true {
		t.Fatalf("the product setting is not reported: %s", b)
	}
	v, ok := got["phase_gate_enabled"]
	if !ok {
		t.Fatalf("the response does not report the deployment gate at all: %s", b)
	}
	if v != false {
		t.Fatalf("the deployment gate was reported as %v while it is off", v)
	}
}

// With the Phase-6 admin flag off, the routes are not mounted.
func TestPhase6OperatorSurfaceIsGated(t *testing.T) {
	off, err := iamv2.LoadPhase6ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if off.DeviceAdminOn() {
		t.Fatal("the operator surface is on by default")
	}
	env := map[string]string{iamv2.EnvPhase6Master: "true", iamv2.EnvPhase6DeviceAdmin: "true"}
	on, err := iamv2.LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if !on.DeviceAdminOn() {
		t.Fatal("the operator surface did not open with master+admin on")
	}
	// Enabling the operator surface must not enable the guest one (separate blast radii).
	if on.DeviceGuestOn() {
		t.Fatal("enabling the operator surface also enabled the guest surface")
	}
}
