package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// PHASE-6 OPERATOR SURFACE — the identity boundary, treated as security rather than wiring.
//
// The setting write takes four identities and a caller may supply NONE of them: tenant and site from this
// process's signed assignment, the appliance from its signed identity, the operator id and label from the
// authenticated session. This asserts the wire contract that makes that true — a request carrying any of
// them must be REFUSED rather than parsed-and-ignored, because a silently dropped identity field looks
// exactly like an honoured one from the caller's side.

func decodeSetting(body string) error {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	var in guestDeviceSettingReq
	return dec.Decode(&in)
}

// Every identity an operator might try to redirect the write with must be refused by the wire type.
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

// The legitimate shape must still decode, or the test above would pass for a type that accepts nothing.
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

// `enabled` is a POINTER so that absent is distinguishable from false. A PUT that forgot the field must not
// read as a request to turn a guest-facing capability off.
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

// THE TWO CONTROLS MUST NOT BE CONFLATED IN THE RESPONSE. The screen has to be able to say "the setting is
// on, and the capability is still not deployed", because an operator who turns something on, sees it
// confirmed, and believes guests can use it has been misled by the product.
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

// The operator surface is itself gated: with the Phase-6 admin flag off, the routes are not mounted.
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
	// ...and enabling the OPERATOR surface must not enable the GUEST one. They are separate flags because
	// they are separate blast radii: a property that wants the setting screen has not thereby asked to serve
	// device management to guests.
	if on.DeviceGuestOn() {
		t.Fatal("enabling the operator surface also enabled the guest surface")
	}
}
