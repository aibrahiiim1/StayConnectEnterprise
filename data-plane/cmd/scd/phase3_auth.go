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
	Tenant       string
	Site         string
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
	return deviceIdentity{Tenant: p.srv.tenID, Site: p.srv.siteID,
		DeviceID: id, GuestNetwork: nc.NetworkID, IP: ip, MAC: mac}, nil
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
	rev, err := p.publishedRevision(ctx, iface)
	if err != nil {
		notVerified(w, "no_published_revision")
		return
	}
	// THE OFFER SET the real eligibility engine says this verified Stay qualifies for — not the site's whole
	// free catalogue. Two Stays verified a second apart can legitimately get different answers.
	decisions, err := p.offersFor(ctx, out.Stay, iface, time.Now())
	if err != nil {
		notVerified(w, "offers: "+err.Error())
		return
	}
	if len(decisions) == 0 {
		// A verified guest with nothing they qualify for is a CONFIGURATION or eligibility outcome, not an
		// identity one. The guest gets the uniform answer either way — they cannot act on the difference —
		// but the operator sees the real reason in the log and in the recorded resolution.
		notVerified(w, "verified_but_no_eligible_package")
		return
	}
	evidenceVersion := decisions[0].EvidenceVersion

	// The Context and the offer set it authorises are written TOGETHER. A Context that existed for even an
	// instant without its offer set would be redeemable against nothing, and the grant's "was this offered?"
	// check would have to fall back to "is this generally grantable?" — the exact weakening it replaces.
	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		notVerified(w, "begin: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ONE LIVE CONTEXT PER RESOLUTION. A retry — the guest's second tap, or a response their phone never
	// received — returns the context this resolution already has rather than minting another. Five taps used
	// to leave five independently redeemable credentials for a single identity proof.
	var id string
	var reused bool
	if err := tx.QueryRow(ctx, `
		SELECT context_id::text, reused FROM iam_v2.issue_or_return_pms_context(
			$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9)`,
		p.srv.tenID, p.srv.siteID, iface, rev, out.Stay, dev.DeviceID, dev.GuestNetwork,
		strings.TrimSpace(req.RequestID), int(p.contextTTL.Seconds())).Scan(&id, &reused); err != nil {
		notVerified(w, "context_issue: "+err.Error())
		return
	}
	// A reused context already carries the offer set it was issued with; the controlled writer is idempotent
	// per (context, package), so a retry re-states the same set rather than widening it.
	if err := p.recordOfferSet(ctx, tx, id, evidenceVersion, decisions,
		time.Now().Add(p.contextTTL)); err != nil {
		notVerified(w, "offer_record: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		notVerified(w, "commit: "+err.Error())
		return
	}

	offers := make([]phase3Offer, 0, len(decisions))
	for _, d := range decisions {
		offers = append(offers, phase3Offer{
			PackageRevisionID: d.PackageRevisionID, Code: d.Code,
			DownKbps: d.DownKbps, UpKbps: d.UpKbps})
	}
	writeJSONScd(w, http.StatusOK, phase3Response{
		Outcome: outcomeVerified, AuthContextID: id, ExpiresIn: int(p.contextTTL.Seconds()), Offers: offers})
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

// publishedRevision returns the interface's PUBLISHED revision — the one the operator made current.
//
// Not max(revision_no). A higher-numbered revision is routinely a DRAFT: somebody is mid-way through
// configuring a connector change and has not published it. Pinning that would authenticate guests against a
// configuration nobody approved, and the Auth Context would record it as the authority for their access.
// The publication pointer is the only statement of what is live, so it is the only thing read here.
//
// Fails closed on every ambiguity: no published revision, a pointer that leaves this tenant/site/interface,
// or an interface that is not ACTIVE. Each of those means "we cannot say which configuration is authoritative",
// and a guest must not be admitted on an unanswerable question.
func (p *phase3Auth) publishedRevision(ctx context.Context, ifaceID string) (string, error) {
	var rev string
	err := p.srv.db.QueryRow(ctx, `
		SELECT r.id::text
		  FROM iam_v2.pms_interfaces i
		  JOIN iam_v2.pms_interface_revisions r
		    ON r.tenant_id = i.tenant_id AND r.site_id = i.site_id
		   AND r.pms_interface_id = i.id AND r.id = i.current_revision_id
		 WHERE i.tenant_id=$1 AND i.site_id=$2 AND i.id=$3
		   AND i.lifecycle_state='ACTIVE'`,
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

	// THE ALREADY-GRANTED CASE, answered before anything is locked.
	//
	// A grant is one transaction, but the ANSWER to it crosses a network hop, and that hop can be lost after
	// the transaction commits: portald abandons the call at its response-time budget, the guest closes the
	// page, a proxy drops the connection. The rows are durable and correct; only the reply is gone. If the
	// retry then went down the normal path it would find the Auth Context consumed and refuse — leaving the
	// guest permanently unable to obtain a session they already have, on a network that is already carrying
	// their traffic. That is the worst of the possible outcomes: real access the guest is told they lack.
	//
	// So a retry from THE SAME DEVICE against a context that device already consumed returns the session that
	// consumption produced. The device identity is the safety: it is derived from the connection and the
	// appliance's neighbour table (never from the body), so this cannot hand one guest's session to another
	// device that happens to know a context id.
	if sid, ent, ok := p.alreadyGranted(ctx, strings.TrimSpace(req.AuthContextID), dev); ok {
		slog.Info("phase3 auth: returning the session an earlier grant already created",
			"session", sid, "entitlement", ent)
		writeJSONScd(w, http.StatusOK, phase3GrantResp{Outcome: outcomeVerified, SessionID: sid, EntitlementID: ent})
		return
	}

	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		notVerified(w, "begin: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// THE OFFER CHECK. "Is this package grantable?" and "was this package offered to THIS verified Stay?" are
	// different questions, and only the second is authorisation. Without this a guest could name any other
	// free package on the site — one whose eligibility rules they do not satisfy — and the generic
	// grantability checks would all pass.
	//
	// It runs inside the SAME transaction as the grant and takes the offer row FOR UPDATE, so a concurrent
	// consumption cannot slip between the check and the grant.
	var offeredTier *int
	var offerEvidence int64
	err = tx.QueryRow(ctx, `
		SELECT o.matched_tier_order, o.evidence_version
		  FROM iam_v2.auth_context_offers o
		 WHERE o.tenant_id=$1 AND o.site_id=$2 AND o.auth_context_id=$3::uuid
		   AND o.package_revision_id=$4::uuid AND o.expires_at > now()
		 FOR UPDATE`,
		p.srv.tenID, p.srv.siteID, strings.TrimSpace(req.AuthContextID),
		strings.TrimSpace(req.PackageRevID)).Scan(&offeredTier, &offerEvidence)
	if err != nil {
		notVerified(w, "package_not_offered_to_this_context")
		return
	}
	// The evidence must still be the evidence the offer was decided under. A Stay that moved room, changed
	// rate or checked out since the offer was made is a different subject, and honouring an offer computed
	// against the old facts would grant something the guest no longer qualifies for.
	var nowEvidence int64
	if err := tx.QueryRow(ctx, `
		SELECT s.occupancy_evidence_version FROM iam_v2.stays s
		  JOIN iam_v2.auth_contexts c ON c.stay_id = s.id
		 WHERE c.id=$1::uuid AND c.tenant_id=$2 AND c.site_id=$3`,
		strings.TrimSpace(req.AuthContextID), p.srv.tenID, p.srv.siteID).Scan(&nowEvidence); err != nil {
		notVerified(w, "stay_evidence_unreadable")
		return
	}
	if nowEvidence != offerEvidence {
		notVerified(w, "stay_evidence_changed_since_the_offer")
		return
	}

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

// alreadyGranted answers "did THIS device already turn THIS Auth Context into a live session?".
//
// Every clause is load-bearing:
//
//	consumed_at IS NOT NULL   — an unconsumed context has produced nothing, and must go down the real path;
//	c.device_id = dev         — the context was issued TO this device, so a stolen context id is not enough;
//	s.device_id = dev         — the session belongs to this device, not merely to the same Stay. Two devices
//	                            in one room hold two contexts and must hold two sessions;
//	s.state = 'active'        — a closed session is not access. A guest whose session was ended (checkout,
//	                            revocation, an operator action) gets the uniform refusal, not a resurrection.
//
// It reads outside the grant transaction on purpose: it is a read-only fast path, and taking locks to answer
// "you already have this" would serialise retries behind the very grants they are duplicating.
func (p *phase3Auth) alreadyGranted(ctx context.Context, authContextID string, dev deviceIdentity) (string, string, bool) {
	if authContextID == "" {
		return "", "", false
	}
	var sid, ent string
	err := p.srv.db.QueryRow(ctx, `
		SELECT s.id::text, e.id::text
		  FROM iam_v2.auth_contexts c
		  JOIN iam_v2.entitlements e ON e.stay_id = c.stay_id
		                            AND e.tenant_id = c.tenant_id AND e.site_id = c.site_id
		  JOIN iam_v2.sessions s ON s.entitlement_id = e.id
		 WHERE c.id = $1::uuid AND c.tenant_id = $2 AND c.site_id = $3
		   AND c.consumed_at IS NOT NULL
		   AND c.device_id = $4 AND s.device_id = $4
		   AND s.state = 'active'
		 ORDER BY s.started DESC
		 LIMIT 1`,
		authContextID, p.srv.tenID, p.srv.siteID, dev.DeviceID).Scan(&sid, &ent)
	if err != nil {
		return "", "", false
	}
	return sid, ent, true
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
