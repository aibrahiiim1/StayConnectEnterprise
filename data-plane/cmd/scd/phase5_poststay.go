package main

// Guest-facing post-stay surface (audit decision AD-36: room number + reservation number, bound to the original stay).
// The guest never names a stay, a PMS interface or a profile. They type the room and the reservation of the stay they
// left (and, from a device that was never on it, the last name on it); scd compares those with the records of the stays
// the device it resolved itself was authorised on (internal/poststay). No PIN is issued, shown, reset or verified.
//
// Uniform non-success: every refusal (a wrong room or reservation, no stay, revoked, expired, locked out, moved-on
// episode) returns the same status and body; the reason goes to the log and, as a closed reason CODE with keyed
// pseudonyms and no guest data, to the audit log (internal/poststay/audit.go).

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/poststay"
)

// phase5PostStay is scd's post-stay arm. A nil value is inert: the routes are not mounted (absent, not refusing).
type phase5PostStay struct {
	srv        *server
	p3         *phase3Auth
	store      *poststay.Store
	ctxs       *authctx.Store
	contextTTL time.Duration
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
	}
}

const (
	psOutcomeVerified  = "VERIFIED"
	psOutcomeGranted   = "GRANTED"
	psOutcomeUnavailab = "UNAVAILABLE"
)

type psResponse struct {
	Outcome string `json:"outcome"`
	// AuthContextID/ExpiresIn are present on VERIFIED only. There is no offer to choose: the stay's one After Check-out
	// entitlement is the only thing a verified device can be let back onto.
	AuthContextID string `json:"auth_context_id,omitempty"`
	ExpiresIn     int    `json:"expires_in_seconds,omitempty"`
	// SessionID/EntitlementID are present on GRANTED only and are different rows.
	SessionID     string `json:"session_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
}

// unavailable is the single non-success answer; reason is for the log only. HTTP 200 with an outcome field,
// since a 401/403/429 split would reintroduce the distinctions the body refuses to make.
func (p *phase5PostStay) unavailable(w http.ResponseWriter, reason string) {
	slog.Info("phase5 post-stay: unavailable", "reason", reason)
	writeJSONScd(w, http.StatusOK, psResponse{Outcome: psOutcomeUnavailab})
}

// refuse is unavailable plus the audit row, for refusals the post-stay store did not already record (Verify records its
// own). code is one of poststay.Refusal*; dev is nil when the device was not resolved.
func (p *phase5PostStay) refuse(w http.ResponseWriter, r *http.Request, code string, dev *deviceIdentity, detail string) {
	ev := poststay.RefusalEvent{Tenant: p.srv.tenID, Code: code}
	if dev != nil {
		ev.Tenant, ev.Device, ev.IP = dev.Tenant, dev.DeviceID, ipString(*dev)
	}
	p.store.AuditRefusal(r.Context(), ev)
	p.unavailable(w, detail)
}

func (p *phase5PostStay) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		p.refuse(w, r, poststay.RefusalMalformed, nil, "malformed_request")
		return false
	}
	return true
}

type psVerifyReq struct {
	Device wireDevice `json:"device"`
	// What the guest typed: the room and the reservation of the stay they left, and nothing else. A surname (or any other
	// field) is refused by the strict decoder like any unknown field.
	Room        string `json:"room"`
	Reservation string `json:"reservation"`
}

// verifyHandler is the post-stay re-authentication; success mints a one-time Auth Context, never a session.
func (p *phase5PostStay) verifyHandler(w http.ResponseWriter, r *http.Request) {
	var req psVerifyReq
	if !p.decode(w, r, &req) {
		return
	}
	// The licence decision every guest method takes first; the refusal keeps the post-stay envelope.
	if code, refused := p.p3.pmsLicenseRefusal(); refused {
		p.refuse(w, r, poststay.RefusalLicense, nil, "license_refused: "+code)
		return
	}
	ctx := r.Context()
	dev, err := p.p3.device(ctx, req.Device)
	if err != nil {
		p.refuse(w, r, poststay.RefusalDeviceUnresolved, nil, "device_identity: "+err.Error())
		return
	}
	// An empty answer is not short-circuited: it takes the same throttle charge and the same comparisons, since a cheap
	// answer would be a free probe for whether this device has a post-stay identity.
	capacity := p.srv.signinCapacity(ctx)
	profile, err := p.store.Verify(ctx, poststay.VerifyRequest{
		Tenant: dev.Tenant, Site: dev.Site, Device: dev.DeviceID, IP: ipString(dev),
		Room: req.Room, Reservation: req.Reservation, Limits: capacity.PostStay})
	if err != nil {
		switch {
		case errors.Is(err, poststay.ErrThrottled):
			p.unavailable(w, "throttled") // Verify already wrote the audit row
		case errors.Is(err, poststay.ErrNotAuthenticable):
			p.unavailable(w, "not_authenticable") // likewise
		default:
			p.refuse(w, r, poststay.RefusalInternal, &dev, "verify_fault: "+err.Error())
		}
		return
	}
	id, err := p.ctxs.IssuePostStay(ctx, authctx.PostStayGrant{
		Tenant: dev.Tenant, Site: dev.Site, Profile: profile,
		Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork,
		TTLSeconds: int(p.contextTTL.Seconds())})
	if err != nil {
		p.refuse(w, r, poststay.RefusalContext, &dev, "context_issue: "+err.Error())
		return
	}
	writeJSONScd(w, http.StatusOK, psResponse{
		Outcome: psOutcomeVerified, AuthContextID: id, ExpiresIn: int(p.contextTTL.Seconds())})
}

type psConvertReq struct {
	Device        wireDevice `json:"device"`
	AuthContextID string     `json:"auth_context_id"`
}

// convertHandler consumes the post-stay context and lets the device back onto the stay's After Check-out entitlement. It
// grants nothing: Profile and Stay come from the consumed context, and the device must hold a place on the entitlement.
func (p *phase5PostStay) convertHandler(w http.ResponseWriter, r *http.Request) {
	var req psConvertReq
	if !p.decode(w, r, &req) {
		return
	}
	// Conversion grants access, so it answers to the licence like the room grant.
	if code, refused := p.p3.pmsLicenseRefusal(); refused {
		p.refuse(w, r, poststay.RefusalLicense, nil, "license_refused: "+code)
		return
	}
	ctx := r.Context()
	dev, err := p.p3.device(ctx, req.Device)
	if err != nil {
		p.refuse(w, r, poststay.RefusalDeviceUnresolved, nil, "device_identity: "+err.Error())
		return
	}
	// A retry of a conversion this device already made returns its session once enforced; the context is one-time,
	// so without this the guest would be stuck behind a consumed context.
	if sid, ent, ok := p.alreadyConverted(ctx, req.AuthContextID, dev); ok {
		if enforced, werr := p.p3.awaitEnforced(ctx, sid); werr != nil || !enforced {
			p.refuse(w, r, poststay.RefusalEnforcement, &dev, "retry_enforcement_not_confirmed")
			return
		}
		writeJSONScd(w, http.StatusOK, psResponse{Outcome: psOutcomeGranted, SessionID: sid, EntitlementID: ent})
		return
	}

	// Conversion and the device's session commit together, as in the room grant: context consumed, entitlement
	// granted, device admitted, licensed slot reserved, session opened PENDING_ENFORCEMENT -- or none of it.
	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		p.refuse(w, r, poststay.RefusalInternal, &dev, "begin: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := p.store.ConvertTx(ctx, tx, poststay.ConvertRequest{
		Tenant: dev.Tenant, Site: dev.Site,
		Context: req.AuthContextID,
		Presenter: authctx.Presenter{Tenant: dev.Tenant, Site: dev.Site,
			Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork},
	}, p.ctxs)
	if err != nil {
		p.refuse(w, r, convertRefusalCode(err), &dev, "convert_refused: "+err.Error())
		return
	}
	// A device that joined an Entitlement it already has a live session on (a retry whose first answer was lost) gets that
	// session back, not a second one.
	sessionID := ""
	if out.Joined {
		sessionID = p.liveSessionTx(ctx, tx, dev.Tenant, dev.Site, out.Entitlement, dev.DeviceID)
	}
	if sessionID == "" {
		if err := reserveLicensedSlot(ctx, tx, p.srv.applID, p.srv.lic.MaxConcurrentOnlineGuests()); err != nil {
			p.refuse(w, r, poststay.RefusalCapacity, &dev, "capacity: "+err.Error())
			return
		}
		sessionID, err = p.p3.openSessionWithMethodTx(ctx, tx, out.Entitlement, dev, postStaySessionMethod)
		if err != nil {
			p.refuse(w, r, poststay.RefusalInternal, &dev, "session: "+err.Error())
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		p.refuse(w, r, poststay.RefusalInternal, &dev, "commit: "+err.Error())
		return
	}
	// Report GRANTED only once the enforcement owner has promoted the session.
	enforced, werr := p.p3.awaitEnforced(ctx, sessionID)
	if werr != nil || !enforced {
		slog.Warn("phase5 post-stay: converted but enforcement not confirmed",
			"session", sessionID, "entitlement", out.Entitlement, "err", werr)
		p.refuse(w, r, poststay.RefusalEnforcement, &dev, "enforcement_not_confirmed")
		return
	}
	slog.Info("phase5 post-stay: converted", "stay", out.Stay, "entitlement", out.Entitlement, "session", sessionID,
		"joined_existing_entitlement", out.Joined)
	writeJSONScd(w, http.StatusOK, psResponse{
		Outcome: psOutcomeGranted, SessionID: sessionID, EntitlementID: out.Entitlement})
}

// convertRefusalCode maps a conversion refusal to its audit code. The guest sees the same answer for all of them.
func convertRefusalCode(err error) string {
	switch {
	case errors.Is(err, poststay.ErrEntitlementNotJoinable):
		return poststay.RefusalNotJoinable
	case errors.Is(err, poststay.ErrNoLineage):
		return poststay.RefusalNoLineage
	case errors.Is(err, authctx.ErrContextInvalid), errors.Is(err, poststay.ErrNotAuthenticable):
		return poststay.RefusalContext
	default:
		return poststay.RefusalInternal
	}
}

// liveSessionTx returns this device's live session on the entitlement, or "".
func (p *phase5PostStay) liveSessionTx(ctx context.Context, tx pgx.Tx, tenant, site, entitlement, device string) string {
	var sid string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.sessions
		 WHERE tenant_id = $1::uuid AND site_id = $2::uuid
		   AND entitlement_id = $3::uuid AND device_id = $4::uuid AND ended IS NULL
		   AND state IN ('active','PENDING_ENFORCEMENT')
		 ORDER BY started DESC LIMIT 1`, tenant, site, entitlement, device).Scan(&sid); err != nil {
		return ""
	}
	return sid
}

// alreadyConverted reports whether this device already turned this context into a live session, with the same
// clauses as alreadyGranted.
func (p *phase5PostStay) alreadyConverted(ctx context.Context, contextID string, dev deviceIdentity) (string, string, bool) {
	if !validRequestID(contextID) {
		return "", "", false
	}
	var sid, ent string
	err := p.srv.db.QueryRow(ctx, `
		SELECT s.id::text, e.id::text
		  FROM iam_v2.auth_contexts c
		  JOIN iam_v2.post_stay_profiles psp
		    ON psp.tenant_id = c.tenant_id AND psp.site_id = c.site_id AND psp.id = c.post_stay_profile_id
		  JOIN iam_v2.entitlements e
		    ON e.tenant_id = psp.tenant_id AND e.site_id = psp.site_id AND e.stay_id = psp.origin_stay_id
		   AND e.status = 'ACTIVE' AND e.end_mode = 'GRACE_AFTER_CHECKOUT'
		  JOIN iam_v2.sessions s ON s.entitlement_id = e.id
		 WHERE c.id = $1::uuid AND c.tenant_id = $2 AND c.site_id = $3 AND c.method = 'POST_STAY_PIN'
		   AND c.consumed_at IS NOT NULL AND c.device_id = $4 AND s.device_id = $4
		   AND s.state IN ('active','PENDING_ENFORCEMENT') AND s.ended IS NULL
		 ORDER BY s.started DESC
		 LIMIT 1`, contextID, dev.Tenant, dev.Site, dev.DeviceID).Scan(&sid, &ent)
	if err != nil {
		return "", "", false
	}
	return sid, ent, true
}

// postStaySessionMethod is the session's credential_method label.
const postStaySessionMethod = "POST_STAY"

func ipString(d deviceIdentity) string {
	if d.IP == nil {
		return ""
	}
	return d.IP.String()
}
