package main

// THE GUEST-FACING POST-STAY SURFACE.
//
// Three endpoints, and the shape of all three is decided by one rule: the guest never names their own
// subject. There is no Stay parameter, no room parameter, no PMS-interface parameter and no profile
// parameter anywhere on this surface. Every one of them is derived from the DEVICE, which scd resolved
// itself from the packet it received.
//
// That is what keeps post-stay off the room. An endpoint that accepted a room number would answer "is room
// 412 occupied" to anyone who asked; an endpoint that accepted a profile id would answer "is this a real
// post-stay identity". Neither question can be asked here, because neither parameter exists.
//
// UNIFORM NON-SUCCESS. Wrong PIN, no PIN, no post-stay identity for this device, a revoked profile, an
// expired one, a locked-out device and a profile whose episode has moved on all return exactly the same
// status and the same body. The real reason goes to the log, where an operator can see it and a guest cannot.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/poststay"
)

// phase5PostStay is scd's Phase-5 arm. A nil value is inert: while the flags are off the routes are not
// mounted at all, so the surface is ABSENT rather than present-and-refusing.
type phase5PostStay struct {
	srv        *server
	p3         *phase3Auth
	store      *poststay.Store
	ctxs       *authctx.Store
	contextTTL time.Duration
	pinValid   time.Duration
}

func newPhase5PostStay(cfg iamv2.Phase5Config, s *server, p3 *phase3Auth) *phase5PostStay {
	if !cfg.GuestOn() {
		return nil
	}
	return &phase5PostStay{
		srv:        s,
		p3:         p3,
		store:      poststay.New(s.db, s.authThrottle),
		ctxs:       authctx.NewStore(s.db),
		contextTTL: 10 * time.Minute,
		pinValid:   24 * time.Hour,
	}
}

// ---- the uniform envelope --------------------------------------------------

const (
	psOutcomeIssued    = "ISSUED"
	psOutcomeVerified  = "VERIFIED"
	psOutcomeGranted   = "GRANTED"
	psOutcomeUnavailab = "UNAVAILABLE"
)

type psResponse struct {
	Outcome string `json:"outcome"`
	// PIN is present on ISSUED and NOWHERE else. It is the one-time plaintext; scd does not store it, cannot
	// re-derive it, and will never return it again for this generation.
	PIN       string `json:"pin,omitempty"`
	ExpiresAt string `json:"pin_expires_at,omitempty"`
	// AuthContextID/ExpiresIn are present on VERIFIED only.
	AuthContextID string `json:"auth_context_id,omitempty"`
	ExpiresIn     int    `json:"expires_in_seconds,omitempty"`
	// SessionID is present on GRANTED only.
	SessionID string `json:"session_id,omitempty"`
}

// unavailable is the single non-success answer for the whole surface. reason is for the log, never the guest.
//
// It is HTTP 200 with an outcome field, exactly like the Phase-3 auth surface: a 401/403/429 split would
// re-introduce through status codes precisely the distinctions the body refuses to make, and a captive-portal
// client handles a 200 far more predictably than a 4xx.
func (p *phase5PostStay) unavailable(w http.ResponseWriter, reason string) {
	slog.Info("phase5 post-stay: unavailable", "reason", reason)
	writeJSONScd(w, http.StatusOK, psResponse{Outcome: psOutcomeUnavailab})
}

func (p *phase5PostStay) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		p.unavailable(w, "malformed_request")
		return false
	}
	return true
}

// ---- issuance --------------------------------------------------------------

type psIssueReq struct {
	// Device is the ONLY input. There is deliberately no stay, room, interface or profile field: an unknown
	// field is rejected by DisallowUnknownFields, so a client that tries to send one gets the uniform
	// non-success rather than having it ignored.
	Device wireDevice `json:"device"`
}

// issueHandler mints a post-stay PIN for the authenticated guest at this device and returns the plaintext
// ONCE.
//
// A LOST RESPONSE IS NOT RECOVERABLE, and that is a property rather than a gap. pin_revealed_at records that
// this server RETURNED the plaintext; it is not, and must never be read as, proof that the client received
// it. A guest whose connection dropped mid-response does not get the same PIN again — nothing can produce it
// — they get a NEW generation from a reset, which is a different secret and an audited act.
func (p *phase5PostStay) issueHandler(w http.ResponseWriter, r *http.Request) {
	var req psIssueReq
	if !p.decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	dev, err := p.p3.device(ctx, req.Device)
	if err != nil {
		p.unavailable(w, "device_identity: "+err.Error())
		return
	}
	// TRUSTED LINEAGE. The Stay comes from this device's own authorization history, not from the request.
	stay, err := p.store.EligibleStayForDevice(ctx, dev.Tenant, dev.Site, dev.DeviceID)
	if err != nil {
		p.unavailable(w, "no_eligible_stay_for_device")
		return
	}
	out, err := p.store.Issue(ctx, poststay.IssueRequest{
		Tenant: dev.Tenant, Site: dev.Site, Stay: stay, ValidFor: p.pinValid})
	if err != nil {
		// Already has a profile for this episode, stay not eligible, or a database refusal. The guest sees
		// one answer; the operator sees which.
		p.unavailable(w, "issue_refused: "+err.Error())
		return
	}
	slog.Info("phase5 post-stay: PIN issued", "profile", out.Profile, "stay", stay)
	writeJSONScd(w, http.StatusOK, psResponse{
		Outcome: psOutcomeIssued, PIN: out.PIN, ExpiresAt: out.Expires.UTC().Format(time.RFC3339)})
}

// ---- PIN re-authentication -------------------------------------------------

type psVerifyReq struct {
	Device wireDevice `json:"device"`
	PIN    string     `json:"pin"`
}

// verifyHandler is the post-stay PIN re-authentication. Success mints a one-time Auth Context and NEVER a
// session — identical to every other credential method in the system.
func (p *phase5PostStay) verifyHandler(w http.ResponseWriter, r *http.Request) {
	var req psVerifyReq
	if !p.decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	dev, err := p.p3.device(ctx, req.Device)
	if err != nil {
		p.unavailable(w, "device_identity: "+err.Error())
		return
	}
	// An empty PIN is NOT short-circuited. It goes through the same throttle charge and the same
	// constant-work verification as a wrong one, because a cheap "no PIN" answer is a free probe for whether
	// this device has a post-stay identity at all — and free probes are what an attacker automates.
	profile, err := p.store.VerifyForDevice(ctx, poststay.VerifyRequest{
		Tenant: dev.Tenant, Site: dev.Site, PIN: req.PIN,
		Device: dev.DeviceID, IP: ipString(dev)})
	if err != nil {
		switch {
		case errors.Is(err, poststay.ErrThrottled):
			p.unavailable(w, "throttled")
		default:
			p.unavailable(w, "not_authenticable")
		}
		return
	}
	id, err := p.ctxs.IssuePostStay(ctx, authctx.PostStayGrant{
		Tenant: dev.Tenant, Site: dev.Site, Profile: profile,
		Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork,
		TTLSeconds: int(p.contextTTL.Seconds())})
	if err != nil {
		p.unavailable(w, "context_issue: "+err.Error())
		return
	}
	writeJSONScd(w, http.StatusOK, psResponse{
		Outcome: psOutcomeVerified, AuthContextID: id, ExpiresIn: int(p.contextTTL.Seconds())})
}

// ---- zero-price conversion -------------------------------------------------

type psConvertReq struct {
	Device            wireDevice `json:"device"`
	AuthContextID     string     `json:"auth_context_id"`
	PackageRevisionID string     `json:"package_revision_id"`
}

// convertHandler consumes the post-stay context and grants the zero-price POST_STAY package.
//
// The profile and the Stay come from the CONSUMED context, never from the request — the guest names only
// which offered package they want, and the conversion re-validates that choice against the same rules that
// produced the offer.
func (p *phase5PostStay) convertHandler(w http.ResponseWriter, r *http.Request) {
	var req psConvertReq
	if !p.decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	dev, err := p.p3.device(ctx, req.Device)
	if err != nil {
		p.unavailable(w, "device_identity: "+err.Error())
		return
	}
	out, err := p.store.Convert(ctx, poststay.ConvertRequest{
		Tenant: dev.Tenant, Site: dev.Site,
		Context: req.AuthContextID,
		Presenter: authctx.Presenter{Tenant: dev.Tenant, Site: dev.Site,
			Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork},
		PackageRevision: req.PackageRevisionID,
	}, p.ctxs)
	if err != nil {
		p.unavailable(w, "convert_refused: "+err.Error())
		return
	}
	slog.Info("phase5 post-stay: converted", "stay", out.Stay, "entitlement", out.Entitlement)
	writeJSONScd(w, http.StatusOK, psResponse{Outcome: psOutcomeGranted, SessionID: out.Entitlement})
}

func ipString(d deviceIdentity) string {
	if d.IP == nil {
		return ""
	}
	return d.IP.String()
}
