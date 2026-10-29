package main

// Guest-facing post-stay surface (audit decision AD-36: room number + reservation number, bound to the original stay).
// The guest never names a stay, a PMS interface or a profile. They type the room and the reservation of the stay they
// left (and, from a device that was never on it, the last name on it); scd compares those with the records of the stays
// the device it resolved itself was authorised on (internal/poststay). No PIN is issued, shown, reset or verified.
//
// Uniform non-success: every refusal (a wrong room or reservation, no stay, revoked, expired, locked out, moved-on
// episode) returns the same status and body; the reason goes only to the log.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

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
	// AuthContextID/ExpiresIn/Offers are present on VERIFIED only (poststay.GrantableOffers).
	AuthContextID string           `json:"auth_context_id,omitempty"`
	ExpiresIn     int              `json:"expires_in_seconds,omitempty"`
	Offers        []poststay.Offer `json:"offers,omitempty"`
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

func (p *phase5PostStay) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		p.unavailable(w, "malformed_request")
		return false
	}
	return true
}

type psVerifyReq struct {
	Device wireDevice `json:"device"`
	// What the guest typed: the room and the reservation of the stay they left; the last name only from a new device.
	Room        string `json:"room"`
	Reservation string `json:"reservation"`
	LastName    string `json:"last_name"`
}

// verifyHandler is the post-stay re-authentication; success mints a one-time Auth Context, never a session.
func (p *phase5PostStay) verifyHandler(w http.ResponseWriter, r *http.Request) {
	var req psVerifyReq
	if !p.decode(w, r, &req) {
		return
	}
	// The licence decision every guest method takes first; the refusal keeps the post-stay envelope.
	if code, refused := p.p3.pmsLicenseRefusal(); refused {
		p.unavailable(w, "license_refused: "+code)
		return
	}
	ctx := r.Context()
	dev, err := p.p3.device(ctx, req.Device)
	if err != nil {
		p.unavailable(w, "device_identity: "+err.Error())
		return
	}
	// An empty answer is not short-circuited: it takes the same throttle charge and the same comparisons, since a cheap
	// answer would be a free probe for whether this device has a post-stay identity.
	profile, err := p.store.Verify(ctx, poststay.VerifyRequest{
		Tenant: dev.Tenant, Site: dev.Site, Device: dev.DeviceID, IP: ipString(dev),
		Room: req.Room, Reservation: req.Reservation, LastName: req.LastName})
	if err != nil {
		switch {
		case errors.Is(err, poststay.ErrThrottled):
			p.unavailable(w, "throttled")
		default:
			p.unavailable(w, "not_authenticable")
		}
		return
	}
	// A site with no grantable post-stay package issues no context (it could be redeemed against nothing).
	offers, err := p.store.GrantableOffers(ctx, dev.Tenant, dev.Site)
	if err != nil {
		p.unavailable(w, "offers_unreadable: "+err.Error())
		return
	}
	if len(offers) == 0 {
		p.unavailable(w, "no_grantable_post_stay_package")
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
		Outcome: psOutcomeVerified, AuthContextID: id, ExpiresIn: int(p.contextTTL.Seconds()), Offers: offers})
}

type psConvertReq struct {
	Device            wireDevice `json:"device"`
	AuthContextID     string     `json:"auth_context_id"`
	PackageRevisionID string     `json:"package_revision_id"`
}

// convertHandler consumes the post-stay context and grants the zero-price POST_STAY package. Profile and Stay
// come from the consumed context; the chosen package is re-validated against the rules that produced the offer.
func (p *phase5PostStay) convertHandler(w http.ResponseWriter, r *http.Request) {
	var req psConvertReq
	if !p.decode(w, r, &req) {
		return
	}
	// Conversion grants access, so it answers to the licence like the room grant.
	if code, refused := p.p3.pmsLicenseRefusal(); refused {
		p.unavailable(w, "license_refused: "+code)
		return
	}
	ctx := r.Context()
	dev, err := p.p3.device(ctx, req.Device)
	if err != nil {
		p.unavailable(w, "device_identity: "+err.Error())
		return
	}
	// A retry of a conversion this device already made returns its session once enforced; the context is one-time,
	// so without this the guest would be stuck behind a consumed context.
	if sid, ent, ok := p.alreadyConverted(ctx, req.AuthContextID, dev); ok {
		if enforced, werr := p.p3.awaitEnforced(ctx, sid); werr != nil || !enforced {
			p.unavailable(w, "retry_enforcement_not_confirmed")
			return
		}
		writeJSONScd(w, http.StatusOK, psResponse{Outcome: psOutcomeGranted, SessionID: sid, EntitlementID: ent})
		return
	}

	// Conversion and the device's session commit together, as in the room grant: context consumed, entitlement
	// granted, device admitted, licensed slot reserved, session opened PENDING_ENFORCEMENT -- or none of it.
	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		p.unavailable(w, "begin: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	out, err := p.store.ConvertTx(ctx, tx, poststay.ConvertRequest{
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
	if err := reserveLicensedSlot(ctx, tx, p.srv.applID, p.srv.lic.MaxConcurrentOnlineGuests()); err != nil {
		p.unavailable(w, "capacity: "+err.Error())
		return
	}
	sessionID, err := p.p3.openSessionWithMethodTx(ctx, tx, out.Entitlement, dev, postStaySessionMethod)
	if err != nil {
		p.unavailable(w, "session: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		p.unavailable(w, "commit: "+err.Error())
		return
	}
	// Report GRANTED only once the enforcement owner has promoted the session.
	enforced, werr := p.p3.awaitEnforced(ctx, sessionID)
	if werr != nil || !enforced {
		slog.Warn("phase5 post-stay: converted but enforcement not confirmed",
			"session", sessionID, "entitlement", out.Entitlement, "err", werr)
		p.unavailable(w, "enforcement_not_confirmed")
		return
	}
	slog.Info("phase5 post-stay: converted", "stay", out.Stay, "entitlement", out.Entitlement, "session", sessionID)
	writeJSONScd(w, http.StatusOK, psResponse{
		Outcome: psOutcomeGranted, SessionID: sessionID, EntitlementID: out.Entitlement})
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
		  FROM iam_v2.purchases pu
		  JOIN iam_v2.auth_contexts c ON c.id = pu.auth_context_id
		  JOIN iam_v2.entitlements e ON e.purchase_id = pu.id
		  JOIN iam_v2.sessions s ON s.entitlement_id = e.id
		 WHERE pu.auth_context_id = $1::uuid AND pu.tenant_id = $2 AND pu.site_id = $3
		   AND pu.trigger = 'POST_STAY_CONVERSION'
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
