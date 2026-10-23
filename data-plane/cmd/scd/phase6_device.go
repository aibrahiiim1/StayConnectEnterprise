package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/deviceselfservice"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// Guest device self-service (internal scd surface). Two controls, both required and checked separately:
//   - The deployment gate, read once at startup, decides whether routes are mounted at all (nil means 404).
//   - The per-appliance product setting, read from the site database on every request, decides whether this
//     hotel offers the feature, so turning it off needs no restart and works without Central.
//
// The subject is never in the request: the device is re-resolved from the source address (phase3Auth.device)
// and the entitlement is whichever live one holds an AUTHORIZED binding for it. The wire types carry no
// entitlement/stay/room/profile/tenant/site field and DisallowUnknownFields rejects invented ones.
type phase6Devices struct {
	srv  *server
	p3   *phase3Auth
	svc  *deviceselfservice.Service
	cfg  iamv2.Phase6Config
	appl string // from trusted local identity, never from a request
}

// newPhase6Devices returns nil when the deployment gate is off, keeping the routes absent. applianceID comes
// from the signed local identity, never a request.
func newPhase6Devices(cfg iamv2.Phase6Config, s *server, p3 *phase3Auth, applianceID string) *phase6Devices {
	if !cfg.DeviceGuestOn() {
		return nil
	}
	return &phase6Devices{srv: s, p3: p3, svc: deviceselfservice.New(s.db), cfg: cfg, appl: applianceID}
}

const (
	p6OutcomeListed      = "LISTED"
	p6OutcomeReleased    = "RELEASED"
	p6OutcomeUnavailable = "UNAVAILABLE"
)

// p6Device is one device as the guest may see it. No MAC (a stable identifier on a shared network) and no
// internal identities.
type p6Device struct {
	ID        string `json:"id"`
	LastSeen  string `json:"last_seen,omitempty"`
	Online    bool   `json:"online"`
	Removable bool   `json:"removable"`
}

type p6Response struct {
	Outcome string     `json:"outcome"`
	Devices []p6Device `json:"devices,omitempty"`
}

// unavailable is the single non-success answer; reason is for the operator log only. Every refusal collapses
// here (feature off, no entitlement, someone else's device, online device, throttled): distinguishing "not
// yours" from "does not exist", or "throttled" from "refused", would be an oracle. HTTP 200 with an outcome field.
func (p *phase6Devices) unavailable(w http.ResponseWriter, reason string) {
	slog.Info("phase6 device self-service: unavailable", "reason", reason)
	writeJSONScd(w, http.StatusOK, p6Response{Outcome: p6OutcomeUnavailable})
}

func (p *phase6Devices) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		p.unavailable(w, "malformed_request")
		return false
	}
	return true
}

// subject resolves the caller's device and its authorized entitlement server-side and checks the
// per-appliance setting. Every failure returns the uniform non-success.
func (p *phase6Devices) subject(w http.ResponseWriter, r *http.Request, d wireDevice) (deviceIdentity, string, bool) {
	ctx := r.Context()
	var zero deviceIdentity

	dev, err := p.p3.device(ctx, d)
	if err != nil {
		p.unavailable(w, "device_identity: "+err.Error())
		return zero, "", false
	}

	// The product setting is read locally, per request.
	on, err := p.svc.EnabledForAppliance(ctx, dev.Tenant, dev.Site, p.appl)
	if err != nil {
		p.unavailable(w, "setting_unreadable: "+err.Error())
		return zero, "", false
	}
	if !on {
		// Indistinguishable from other refusals: a guest must not learn the hotel turned the feature off.
		p.unavailable(w, "guest_device_self_service_disabled_on_this_appliance")
		return zero, "", false
	}

	ent, err := p.svc.EntitlementForDevice(ctx, dev.Tenant, dev.Site, dev.DeviceID)
	if err != nil {
		p.unavailable(w, "no_entitlement_for_device: "+err.Error())
		return zero, "", false
	}
	return dev, ent, true
}

type p6ListReq struct {
	// Device is the only input.
	Device wireDevice `json:"device"`
}

func (p *phase6Devices) listHandler(w http.ResponseWriter, r *http.Request) {
	var req p6ListReq
	if !p.decode(w, r, &req) {
		return
	}
	_, ent, ok := p.subject(w, r, req.Device)
	if !ok {
		return
	}
	devices, err := p.svc.ListOwnDevices(r.Context(), ent)
	if err != nil {
		p.unavailable(w, "list_failed: "+err.Error())
		return
	}
	out := make([]p6Device, 0, len(devices))
	for _, d := range devices {
		one := p6Device{ID: d.ID, Online: d.Online, Removable: d.Removable}
		if d.LastSeen != nil {
			one.LastSeen = d.LastSeen.UTC().Format("2006-01-02T15:04:05Z")
		}
		out = append(out, one)
	}
	writeJSONScd(w, http.StatusOK, p6Response{Outcome: p6OutcomeListed, Devices: out})
}

type p6ReleaseReq struct {
	Device wireDevice `json:"device"`
	// DeviceID names which of the caller's own devices to release. It is not a subject selector: lookups are
	// scoped by the resolved entitlement, so another guest's id matches nothing, like an id that never existed.
	DeviceID string `json:"device_id"`
}

func (p *phase6Devices) releaseHandler(w http.ResponseWriter, r *http.Request) {
	var req p6ReleaseReq
	if !p.decode(w, r, &req) {
		return
	}
	target := strings.TrimSpace(req.DeviceID)
	if target == "" {
		p.unavailable(w, "no_target_device")
		return
	}
	_, ent, ok := p.subject(w, r, req.Device)
	if !ok {
		return
	}
	// The durable policy re-checks offline state under its lock, enforces the throttle, releases once and audits every outcome.
	outcome, err := p.svc.Release(r.Context(), ent, target)
	if err != nil {
		p.unavailable(w, "release_failed: "+err.Error())
		return
	}
	if !outcome.Released() {
		// One guest answer; the durable audit records which.
		p.unavailable(w, "release_refused: "+string(outcome))
		return
	}
	slog.Info("phase6 device self-service: slot released", "entitlement", ent, "device", target)
	writeJSONScd(w, http.StatusOK, p6Response{Outcome: p6OutcomeReleased})
}
