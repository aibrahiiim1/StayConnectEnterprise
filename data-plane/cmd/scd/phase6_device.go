package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/deviceselfservice"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// PHASE-6 GUEST DEVICE SELF-SERVICE — the internal scd surface.
//
// TWO CONTROLS, BOTH REQUIRED, CHECKED IN DIFFERENT PLACES AND AT DIFFERENT TIMES.
//
//   - The Phase-6 DEPLOYMENT GATE decides whether these routes are MOUNTED AT ALL. It is read once at
//     startup, and when it is off newPhase6Devices returns nil and nothing registers a handler — so the
//     routes are ABSENT (404), not present-and-refusing.
//   - The per-appliance PRODUCT SETTING decides whether this hotel offers the feature. It is read from the
//     site database on EVERY request, because an operator turning it off must take effect without a restart,
//     and because reading it locally is what makes the feature work with no Central Control Plane.
//
// Collapsing them would mean that shipping the product control also shipped the activation.
//
// THE SUBJECT IS NEVER IN THE REQUEST. Both handlers accept a device identity and nothing else. The device
// is re-resolved from the source address against this appliance's own tables (phase3Auth.device), and the
// entitlement is whichever live entitlement currently holds an AUTHORIZED binding for that device. There is
// no entitlement, stay, room, pms_interface, profile, tenant or site field anywhere in the wire types, and
// DisallowUnknownFields means a client that invents one gets the uniform non-success rather than having it
// silently ignored.
type phase6Devices struct {
	srv  *server
	p3   *phase3Auth
	svc  *deviceselfservice.Service
	cfg  iamv2.Phase6Config
	appl string // this appliance's own id, from trusted local identity — never from a request
}

// newPhase6Devices returns nil when the deployment gate is off, which is what keeps the routes ABSENT.
// applianceID comes from the SIGNED LOCAL IDENTITY (the assignment/identity store), the same source Central
// binds the licence to. It is passed in rather than read from a request because there is no request in which
// it would be trustworthy: an appliance id a caller can name is an appliance id a caller can change.
func newPhase6Devices(cfg iamv2.Phase6Config, s *server, p3 *phase3Auth, applianceID string) *phase6Devices {
	if !cfg.DeviceGuestOn() {
		return nil
	}
	return &phase6Devices{srv: s, p3: p3, svc: deviceselfservice.New(s.db), cfg: cfg, appl: applianceID}
}

// ---- the uniform envelope --------------------------------------------------

const (
	p6OutcomeListed      = "LISTED"
	p6OutcomeReleased    = "RELEASED"
	p6OutcomeUnavailable = "UNAVAILABLE"
)

// p6Device is one device as the GUEST may see it.
//
// There is no MAC field, and no PMS, stay, room or profile field. A guest identifies their own phone by when
// it was last seen and whether it is online; a MAC would hand every guest a stable network identifier for a
// device on a shared network, and the internal identities are not theirs to see at all.
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

// unavailable is the single non-success answer for the whole surface. reason is for the operator log and is
// never sent to the guest.
//
// EVERY refusal collapses here: the feature being off, no entitlement for this device, a device belonging to
// somebody else, an online device, a throttled caller. That is deliberate — a guest who can tell "not yours"
// from "does not exist" has an oracle, and one who can tell "throttled" from "refused" can measure the
// throttle. HTTP 200 with an outcome field, matching the Phase-3 and Phase-5 surfaces, because a captive
// portal client handles 200 far more predictably than a 4xx.
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

// subject resolves the caller's device AND the entitlement it is authorized under, entirely server-side, and
// checks the per-appliance setting on the way. Every failure returns the same uniform non-success.
func (p *phase6Devices) subject(w http.ResponseWriter, r *http.Request, d wireDevice) (deviceIdentity, string, bool) {
	ctx := r.Context()
	var zero deviceIdentity

	dev, err := p.p3.device(ctx, d)
	if err != nil {
		p.unavailable(w, "device_identity: "+err.Error())
		return zero, "", false
	}

	// THE PRODUCT SETTING, read locally, per request. The appliance id is this process's own — it is not a
	// parameter of anything a guest can send.
	on, err := p.svc.EnabledForAppliance(ctx, dev.Tenant, dev.Site, p.appl)
	if err != nil {
		p.unavailable(w, "setting_unreadable: "+err.Error())
		return zero, "", false
	}
	if !on {
		// Indistinguishable from every other refusal. A guest must not be able to discover that the hotel
		// has the feature but has turned it off.
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

// ---- listing ---------------------------------------------------------------

type p6ListReq struct {
	// Device is the ONLY input. No entitlement, stay, room, interface, profile, tenant or site field exists.
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

// ---- release ---------------------------------------------------------------

type p6ReleaseReq struct {
	Device wireDevice `json:"device"`
	// DeviceID names WHICH of the caller's own devices to release. It is not a subject selector: the
	// entitlement was already resolved from the requesting device, and the durable operation scopes every
	// lookup by it — so an id belonging to another guest matches nothing and returns the same answer as an id
	// that never existed. The device the guest is holding is identified by its address, not by this field.
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
	// The durable policy decides. It re-checks the offline state inside its own lock, enforces the throttle
	// from durable rows, releases exactly once and audits every outcome including this refusal.
	outcome, err := p.svc.Release(r.Context(), ent, target)
	if err != nil {
		p.unavailable(w, "release_failed: "+err.Error())
		return
	}
	if !outcome.Released() {
		// Every refusal reason collapses to one answer. The operator sees which in the durable audit.
		p.unavailable(w, "release_refused: "+string(outcome))
		return
	}
	slog.Info("phase6 device self-service: slot released", "entitlement", ent, "device", target)
	writeJSONScd(w, http.StatusOK, p6Response{Outcome: p6OutcomeReleased})
}
