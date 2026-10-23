package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// Guest device surface wire contract (no database needed): a request carrying entitlement_id, stay_id, room,
// pms_interface_id, profile_id, tenant_id or site_id must be refused, not parsed and ignored. decodeInto
// mirrors the handlers' strictness, so relaxing DisallowUnknownFields fails here.
func decodeInto(body string, dst any) error {
	dec := json.NewDecoder(strings.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func TestPhase6GuestWireTypesRefuseEverySubjectIdentity(t *testing.T) {
	spoofs := map[string]string{
		"entitlement_id":   `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"entitlement_id":"11111111-1111-1111-1111-111111111111"}`,
		"stay_id":          `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"stay_id":"11111111-1111-1111-1111-111111111111"}`,
		"room":             `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"room":"101"}`,
		"pms_interface_id": `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"pms_interface_id":"11111111-1111-1111-1111-111111111111"}`,
		"profile_id":       `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"profile_id":"11111111-1111-1111-1111-111111111111"}`,
		"tenant_id":        `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"tenant_id":"11111111-1111-1111-1111-111111111111"}`,
		"site_id":          `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"site_id":"11111111-1111-1111-1111-111111111111"}`,
		"guest_id":         `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"guest_id":"11111111-1111-1111-1111-111111111111"}`,
		"subject":          `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"subject":"someone-else"}`,
		"appliance_id":     `{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"appliance_id":"11111111-1111-1111-1111-111111111111"}`,
	}
	for name, body := range spoofs {
		t.Run("list/"+name, func(t *testing.T) {
			var req p6ListReq
			if err := decodeInto(body, &req); err == nil {
				t.Fatalf("the LIST wire type ACCEPTED a request carrying %s", name)
			}
		})
		t.Run("release/"+name, func(t *testing.T) {
			var req p6ReleaseReq
			// a release legitimately carries device_id
			withTarget := strings.Replace(body, `"device":{`, `"device_id":"22222222-2222-2222-2222-222222222222","device":{`, 1)
			if err := decodeInto(withTarget, &req); err == nil {
				t.Fatalf("the RELEASE wire type ACCEPTED a request carrying %s", name)
			}
		})
	}
}

// The legitimate shapes must still decode, or the test above would pass for a type accepting nothing.
func TestPhase6GuestWireTypesAcceptOnlyTheLegitimateShape(t *testing.T) {
	var l p6ListReq
	if err := decodeInto(`{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"}}`, &l); err != nil {
		t.Fatalf("the LIST wire type refused its own legitimate shape: %v", err)
	}
	if l.Device.IP != "10.90.0.5" {
		t.Fatalf("device identity did not decode: %+v", l.Device)
	}
	var rl p6ReleaseReq
	if err := decodeInto(
		`{"device":{"ip":"10.90.0.5","mac":"02:00:00:00:00:01"},"device_id":"22222222-2222-2222-2222-222222222222"}`,
		&rl); err != nil {
		t.Fatalf("the RELEASE wire type refused its own legitimate shape: %v", err)
	}
	if rl.DeviceID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("target device did not decode: %q", rl.DeviceID)
	}
}

// With the deployment gate off the constructor returns nil: the path is absent (404), not a refusing handler.
func TestPhase6RoutesAreAbsentWhileTheGateIsOff(t *testing.T) {
	off, err := iamv2.LoadPhase6ConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got := newPhase6Devices(off, nil, nil, "appliance-1"); got != nil {
		t.Fatal("the guest device surface was constructed while the deployment gate was OFF")
	}

	env := map[string]string{
		iamv2.EnvPhase6Master:      "true",
		iamv2.EnvPhase6DeviceGuest: "true",
	}
	on, err := iamv2.LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	// A minimal server suffices: the constructor only stores the pool handle.
	if got := newPhase6Devices(on, &server{}, nil, "appliance-1"); got == nil {
		t.Fatal("the guest device surface was NOT constructed while the deployment gate was ON")
	}
}

// The master flag alone is not the guest surface; the device child flag is also required.
func TestPhase6MasterAloneDoesNotMountTheGuestSurface(t *testing.T) {
	env := map[string]string{iamv2.EnvPhase6Master: "true"}
	cfg, err := iamv2.LoadPhase6ConfigFromEnv(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if got := newPhase6Devices(cfg, &server{}, nil, "appliance-1"); got != nil {
		t.Fatal("the master flag alone constructed the guest device surface")
	}
}

func TestPhase6RefusalBodyCarriesNoReason(t *testing.T) {
	p := &phase6Devices{}
	w := httptest.NewRecorder()
	p.unavailable(w, "guest_device_self_service_disabled_on_this_appliance")

	if w.Code != http.StatusOK {
		t.Fatalf("a refusal used status %d; the surface answers 200 with an outcome, like Phase 3 and 5", w.Code)
	}
	body := w.Body.String()
	var resp p6Response
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("refusal body is not the uniform envelope: %v", err)
	}
	if resp.Outcome != p6OutcomeUnavailable {
		t.Fatalf("refusal outcome is %q", resp.Outcome)
	}
	if len(resp.Devices) != 0 {
		t.Fatal("a refusal carried devices")
	}
	// the internal reason must not reach the wire
	for _, leak := range []string{"disabled", "appliance", "entitlement", "throttl", "online", "not_found"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Fatalf("the refusal body leaks %q: %s", leak, body)
		}
	}
}

func TestPhase6GuestDeviceProjectionHasNoMACOrInternalIdentity(t *testing.T) {
	b, err := json.Marshal(p6Device{ID: "dev-1", LastSeen: "2026-08-15T00:00:00Z", Online: true, Removable: false})
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ToLower(string(b))
	for _, forbidden := range []string{"mac", "stay", "room", "pms", "profile", "entitlement", "tenant", "site"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("the guest device projection exposes %q: %s", forbidden, body)
		}
	}
}
