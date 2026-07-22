package main

// PHASE-3 PMS AUTH — the guest-facing vertical slice, wired end to end.
//
// The whole point of this path is that a guest proves WHO THEY ARE only by naming details of a stay, and
// everything that decides ACCESS is derived by the appliance. Nothing in the guest's request body names a
// Stay, an Interface, a package revision's price, a plan, a duration, a device or a network. Those come from
// the resolution, the trusted network view, and the pinned revision. A body that could name them would be a
// body that could ask for someone else's access.
//
// The flow is deliberately TWO steps with a one-time context between them:
//
//	resolve → (VERIFIED) → one-time Auth Context → grant → Entitlement → Session
//
// One step would mean the act of proving identity and the act of granting access were the same transaction,
// so any retry, any double-submit and any network hiccup would either re-prove or re-grant. The Auth Context
// makes the boundary explicit: proving is idempotent per request id, granting consumes exactly once.
//
// TRUST BOUNDARY: this daemon listens on a root-owned unix socket in group stayconnect. A guest cannot reach
// it. portald — which CAN — derives the source IP from the connection (nftables DNAT preserves it) and the MAC
// from the appliance's own neighbour table, and passes them on that internal hop. scd then re-derives the
// guest network from the IP against its own tables, so a forwarded address that is not on a guest network is
// refused regardless of what the hop claimed.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsresolve"
	"github.com/stayconnect/enterprise/data-plane/internal/staygrant"
)

// phase3Auth is scd's Phase-3 arm. A nil value is inert, which is what a dark appliance gets: the routes are
// not mounted at all, so the surface is absent (404) rather than present-and-refusing.
type phase3Auth struct {
	srv      *server
	resolver *pmsresolve.Resolver
	ctxs     *authctx.Store
	grants   *staygrant.Store
	// contextTTL bounds how long a verified identity may sit unused before it has to be proven again.
	contextTTL time.Duration
}

// newPhase3Auth constructs the arm ONLY when the master + PMS-auth flags are on.
func newPhase3Auth(cfg iamv2.PMSConfig, s *server) *phase3Auth {
	if !cfg.AuthOn() {
		return nil
	}
	return &phase3Auth{
		srv:        s,
		resolver:   pmsresolve.NewResolver(s.db),
		ctxs:       authctx.NewStore(s.db),
		grants:     staygrant.New(s.db),
		contextTTL: 10 * time.Minute,
	}
}

// ---- the uniform non-success contract --------------------------------------

// Every failure on this path returns the SAME shape, the same HTTP status and the same body. A guest who
// typed the wrong room, a guest whose stay does not exist, a guest whose evidence matched two stays, and a
// guest whose PMS is unreachable must be indistinguishable — otherwise the endpoint is an oracle for "is room
// 412 occupied", and the differences are exactly what an attacker enumerates.
//
// The real reason is recorded internally (resolution rows, audit, metrics) where operators can see it.
const (
	outcomeVerified    = "VERIFIED"
	outcomeNotVerified = "NOT_VERIFIED"
)

type phase3Response struct {
	Outcome string `json:"outcome"`
	// AuthContextID, ExpiresIn and Offers are present ONLY on VERIFIED.
	AuthContextID string        `json:"auth_context_id,omitempty"`
	ExpiresIn     int           `json:"expires_in_seconds,omitempty"`
	Offers        []phase3Offer `json:"offers,omitempty"`
}

// phase3Offer is a package the SERVER determined this verified stay may be granted. The guest chooses among
// offers; they never name one that was not offered, and the grant re-validates the choice against the same
// rules anyway — an offer is a convenience, never an authorization.
type phase3Offer struct {
	PackageRevisionID string `json:"package_revision_id"`
	Code              string `json:"code"`
	DownKbps          int    `json:"down_kbps"`
	UpKbps            int    `json:"up_kbps"`
}

// notVerified writes the single non-success answer. reason is for the log, never the guest.
func notVerified(w http.ResponseWriter, reason string) {
	slog.Info("phase3 auth: not verified", "reason", reason)
	writeJSONScd(w, http.StatusOK, phase3Response{Outcome: outcomeNotVerified})
}

func writeJSONScd(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- device identity (server-derived) --------------------------------------

type deviceIdentity struct {
	DeviceID     string
	GuestNetwork string
	IP           net.IP
	MAC          net.HardwareAddr
}

type wireDevice struct {
	IP  string `json:"ip"`
	MAC string `json:"mac"`
}

// device resolves the requesting device to durable identity, entirely from the appliance's own view. It is the
// only place a Phase-3 request acquires an identity, and it never consults anything the guest typed.
func (p *phase3Auth) device(ctx context.Context, d wireDevice) (deviceIdentity, error) {
	var out deviceIdentity
	ip := net.ParseIP(strings.TrimSpace(d.IP))
	if ip == nil || ip.To4() == nil {
		return out, errors.New("no usable source address")
	}
	mac, err := net.ParseMAC(strings.TrimSpace(d.MAC))
	if err != nil {
		return out, errors.New("no usable hardware address")
	}
	// The guest network is re-derived HERE from the address, against this appliance's own tables. A
	// forwarded address that belongs to no enabled guest network is refused: Phase-3 resolution is scoped by
	// network, and a request from outside every guest network has no scope to resolve in.
	nc := p.srv.resolveNetwork(ctx, ip)
	if nc.NetworkID == "" {
		return out, errors.New("source address is not on a mapped guest network")
	}
	// The device row is the durable identity every later pin refers to. The conflict target is the table's
	// own uniqueness — (tenant, site, appliance, mac) — not a shorter key that merely looks right: a mismatch
	// there is not a slow path, it is a runtime error on every guest's first request.
	//
	// DO UPDATE rather than DO NOTHING because DO NOTHING returns no row, and this statement exists to
	// return the id; the "update" is a no-op write of the value that was already there.
	var id string
	err = p.srv.db.QueryRow(ctx, `
		INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,$3,$4::macaddr)
		ON CONFLICT (tenant_id, site_id, appliance_id, mac) DO UPDATE SET mac = EXCLUDED.mac
		RETURNING id::text`,
		p.srv.tenID, p.srv.siteID, p.srv.applID, mac.String()).Scan(&id)
	if err != nil {
		return out, err
	}
	return deviceIdentity{DeviceID: id, GuestNetwork: nc.NetworkID, IP: ip, MAC: mac}, nil
}

// ---- resolve ---------------------------------------------------------------

type phase3ResolveReq struct {
	Room              string     `json:"room"`
	LastName          string     `json:"last_name"`
	ReservationNumber string     `json:"reservation_number"`
	RequestID         string     `json:"request_id"`
	Device            wireDevice `json:"device"`
}

// resolveHandler proves the guest's identity STRICTLY across every PMS Interface mapped to their network, and
// on success issues a one-time Auth Context. It grants nothing.
func (p *phase3Auth) resolveHandler(w http.ResponseWriter, r *http.Request) {
	var req phase3ResolveReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		notVerified(w, "malformed_request")
		return
	}
	ctx := r.Context()
	dev, err := p.device(ctx, req.Device)
	if err != nil {
		notVerified(w, "device_identity: "+err.Error())
		return
	}
	room := normalizeRoom(req.Room)
	last := normalizeName(req.LastName)
	res := strings.TrimSpace(req.ReservationNumber)
	if room == "" || (last == "" && res == "") {
		// Incomplete evidence is a non-success like any other: telling the guest WHICH field was missing is
		// a small oracle, and the portal already knows what it asked for.
		notVerified(w, "incomplete_evidence")
		return
	}
	// The request id is recorded as a uuid. Validating its SHAPE here means a malformed one is the ordinary
	// uniform non-success — not a raw PostgreSQL cast error surfaced as "the resolver failed", which would
	// both mislead an operator reading the logs and put database detail in them.
	if !validRequestID(req.RequestID) {
		notVerified(w, "malformed_request_id")
		return
	}

	// The probe evaluates ONE interface's mirrored Stay state. It returns a determinate verdict or an
	// indeterminate one; it never guesses, and the resolver waits for every interface before deciding.
	probe := pmsresolve.ProbeFunc(func(pctx context.Context, ifaceID string) (pmsresolve.CandidateOutcome, string, error) {
		return p.probeInterface(pctx, ifaceID, room, last, res)
	})

	out, err := p.resolver.Resolve(ctx, p.srv.tenID, p.srv.siteID, dev.GuestNetwork, strings.TrimSpace(req.RequestID), probe)
	if err != nil {
		notVerified(w, "resolver: "+err.Error())
		return
	}
	if !out.GuestVisibleSuccess() {
		notVerified(w, "resolution_"+string(out.Resolution)+"_"+out.Reason)
		return
	}

	// A REPLAYED resolution (the same request id submitted twice — a double tap, or a response the guest's
	// phone never received) returns the stored outcome, and the stored row records the Stay, not the
	// interface that produced it. Re-deriving the interface FROM THE STAY is exact: a Stay belongs to exactly
	// one PMS Interface. Without this, every retry of a successful resolution fails to issue a context, and
	// the guest is permanently stuck behind a uniform "not verified" they cannot act on.
	iface := out.InterfaceID
	if iface == "" {
		if iface, err = p.interfaceForStay(ctx, out.Stay); err != nil {
			notVerified(w, "replayed_resolution_interface_unresolvable")
			return
		}
	}

	// The REVISION is pinned server-side from the interface that verified: the guest never names which
	// configuration their access was granted under, and the Auth Context records it so the later grant cannot
	// drift onto a newer one.
	rev, err := p.currentRevision(ctx, iface)
	if err != nil {
		notVerified(w, "no_published_revision")
		return
	}
	id, err := p.ctxs.IssuePMS(ctx, authctx.PMSGrant{
		Tenant: p.srv.tenID, Site: p.srv.siteID,
		Interface: iface, Revision: rev, Stay: out.Stay,
		Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork,
		TTLSeconds: int(p.contextTTL.Seconds()),
	})
	if err != nil {
		notVerified(w, "context_issue: "+err.Error())
		return
	}
	offers, err := p.offers(ctx)
	if err != nil {
		notVerified(w, "offers: "+err.Error())
		return
	}
	if len(offers) == 0 {
		// A verified guest with nothing they may be granted is a CONFIGURATION problem, not an identity one.
		// The guest still gets the uniform answer — they cannot act on the difference — but the operator sees
		// the real reason in the log and in the recorded resolution.
		notVerified(w, "verified_but_no_grantable_package")
		return
	}
	writeJSONScd(w, http.StatusOK, phase3Response{
		Outcome: outcomeVerified, AuthContextID: id, ExpiresIn: int(p.contextTTL.Seconds()), Offers: offers})
}

// offers lists the INCLUDED packages a verified stay may be granted. The predicate is deliberately the SAME
// one staygrant enforces at grant time — current revision only, never a system/grace catalog, inside its
// visibility window, zero price, settlement NOT_REQUIRED. Two predicates that "should" agree are how a portal
// ends up offering something the grant then refuses.
func (p *phase3Auth) offers(ctx context.Context) ([]phase3Offer, error) {
	rows, err := p.srv.db.Query(ctx, `
		SELECT ipr.id::text, ip.code, COALESCE(spr.down_kbps,0), COALESCE(spr.up_kbps,0)
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip
		    ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = ipr.service_plan_revision_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2
		   AND ip.current_revision_id = ipr.id
		   AND ip.is_system IS NOT TRUE
		   AND ipr.package_type <> 'CHECKOUT_GRACE'
		   AND (ipr.visible_from IS NULL OR ipr.visible_from <= now())
		   AND (ipr.visible_until IS NULL OR ipr.visible_until > now())
		   AND ipr.price_minor = 0
		   AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
		 ORDER BY ip.code`, p.srv.tenID, p.srv.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []phase3Offer
	for rows.Next() {
		var o phase3Offer
		if err := rows.Scan(&o.PackageRevisionID, &o.Code, &o.DownKbps, &o.UpKbps); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// validRequestID reports whether s is a canonical 8-4-4-4-12 hex UUID. Deliberately not "close enough": the
// value lands in a uuid column, and anything else is refused before any SQL runs.
func validRequestID(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// probeInterface answers, for ONE interface, whether the evidence identifies exactly one live Stay. Matching
// more than one is AMBIGUOUS_LOCAL rather than a pick: choosing between two guests who share a room number is
// exactly the decision this system must never make on its own.
func (p *phase3Auth) probeInterface(ctx context.Context, ifaceID, room, last, res string) (pmsresolve.CandidateOutcome, string, error) {
	rows, err := p.srv.db.Query(ctx, `
		SELECT s.id::text
		  FROM iam_v2.stays s
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.pms_interface_id=$3
		   AND s.status IN ('IN_HOUSE','POST_STAY_ACTIVE')
		   AND s.normalized_room_number = $4
		   AND ( ($5 <> '' AND EXISTS (SELECT 1 FROM iam_v2.stay_guests g
		                                WHERE g.stay_id = s.id AND g.last_name_norm = $5))
		      OR ($6 <> '' AND s.external_reservation_id = $6) )
		 LIMIT 3`, p.srv.tenID, p.srv.siteID, ifaceID, room, last, res)
	if err != nil {
		// An interface whose state cannot be read is INDETERMINATE, never a determinate "no such guest".
		return pmsresolve.Unavailable, "", err
	}
	defer rows.Close()
	var matches []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return pmsresolve.Unavailable, "", err
		}
		matches = append(matches, id)
	}
	if err := rows.Err(); err != nil {
		return pmsresolve.Unavailable, "", err
	}
	switch len(matches) {
	case 0:
		return pmsresolve.NoMatch, "", nil
	case 1:
		return pmsresolve.Verified, matches[0], nil
	default:
		return pmsresolve.AmbiguousLocal, "", nil
	}
}

// interfaceForStay resolves the one PMS Interface a Stay belongs to, within this appliance's scope.
func (p *phase3Auth) interfaceForStay(ctx context.Context, stay string) (string, error) {
	var iface string
	err := p.srv.db.QueryRow(ctx, `
		SELECT s.pms_interface_id::text FROM iam_v2.stays s
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.id=$3`, p.srv.tenID, p.srv.siteID, stay).Scan(&iface)
	return iface, err
}

// currentRevision returns the interface's published revision — the configuration the access will be pinned to.
func (p *phase3Auth) currentRevision(ctx context.Context, ifaceID string) (string, error) {
	var rev string
	err := p.srv.db.QueryRow(ctx, `
		SELECT r.id::text FROM iam_v2.pms_interface_revisions r
		 WHERE r.tenant_id=$1 AND r.site_id=$2 AND r.pms_interface_id=$3
		 ORDER BY r.revision_no DESC LIMIT 1`,
		p.srv.tenID, p.srv.siteID, ifaceID).Scan(&rev)
	return rev, err
}

// ---- grant -----------------------------------------------------------------

type phase3GrantReq struct {
	AuthContextID string     `json:"auth_context_id"`
	PackageRevID  string     `json:"package_revision_id"`
	Device        wireDevice `json:"device"`
}

type phase3GrantResp struct {
	Outcome       string `json:"outcome"`
	SessionID     string `json:"session_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
}

// grantHandler turns a verified Auth Context into durable access. Auth-Context consumption, Quote, Purchase,
// Entitlement, the device authorization interval AND the Session all commit together or not at all.
//
// THE ORDERING RULE: the Session is created from the Entitlement id the grant returned, inside the same
// transaction. A session that existed before its entitlement — even for microseconds, even in a transaction
// that later commits both — would be a period of network access with nothing authorising it, and every audit
// question afterwards ("what was this device allowed to do at 09:41?") would have no answer.
func (p *phase3Auth) grantHandler(w http.ResponseWriter, r *http.Request) {
	var req phase3GrantReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		notVerified(w, "malformed_request")
		return
	}
	ctx := r.Context()
	dev, err := p.device(ctx, req.Device)
	if err != nil {
		notVerified(w, "device_identity: "+err.Error())
		return
	}

	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		notVerified(w, "begin: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	granted, err := p.grants.GrantTx(ctx, tx, p.srv.tenID, p.srv.siteID, staygrant.Request{
		AuthContextID: strings.TrimSpace(req.AuthContextID),
		Presenter: authctx.Presenter{
			Tenant: p.srv.tenID, Site: p.srv.siteID,
			Device: dev.DeviceID, GuestNetwork: dev.GuestNetwork,
		},
		PackageRevID: strings.TrimSpace(req.PackageRevID),
	})
	if err != nil {
		// Every grant failure is the same uniform non-success: a guest must not be able to tell "that context
		// was already used" from "that package needs payment" from "the stay already has access".
		notVerified(w, "grant: "+err.Error())
		return
	}

	sessionID, err := p.openSessionTx(ctx, tx, granted.EntitlementID, dev)
	if err != nil {
		notVerified(w, "session: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		notVerified(w, "commit: "+err.Error())
		return
	}
	slog.Info("phase3 auth: access granted",
		"stay", granted.Stay, "entitlement", granted.EntitlementID, "session", sessionID)
	writeJSONScd(w, http.StatusOK, phase3GrantResp{
		Outcome: outcomeVerified, SessionID: sessionID, EntitlementID: granted.EntitlementID})
}

// openSessionTx creates the guest's session against an Entitlement that already exists in this transaction.
// The Entitlement id is a parameter, not a lookup: there is no code path here that could create a session
// without one.
func (p *phase3Auth) openSessionTx(ctx context.Context, tx pgx.Tx, entitlement string, dev deviceIdentity) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO iam_v2.sessions
		  (tenant_id, site_id, entitlement_id, device_id, credential_method, state, started, ip, mac, ingress_interface)
		VALUES ($1,$2,$3,$4,'PMS','active',now(),$5::inet,$6::macaddr,$7)
		RETURNING id::text`,
		p.srv.tenID, p.srv.siteID, entitlement, dev.DeviceID,
		dev.IP.String(), dev.MAC.String(), p.bridgeFor(ctx, dev.IP)).Scan(&id)
	return id, err
}

func (p *phase3Auth) bridgeFor(ctx context.Context, ip net.IP) string {
	return p.srv.resolveNetwork(ctx, ip).Bridge
}

// ---- normalization ---------------------------------------------------------
//
// Both sides of a comparison must be normalized the same way or the match silently depends on how the guest
// typed it. The PMS mirror stores normalized values; these functions are the guest-side half of that contract.

func normalizeRoom(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}

func normalizeName(s string) string {
	return strings.ToUpper(strings.TrimSpace(s))
}
