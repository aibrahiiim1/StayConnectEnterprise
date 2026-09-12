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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsresolve"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
	"github.com/stayconnect/enterprise/data-plane/internal/staygrant"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
)

// phase3Auth is scd's Phase-3 arm. A nil value is inert, which is what a dark appliance gets: the routes are
// not mounted at all, so the surface is absent (404) rather than present-and-refusing.
type phase3Auth struct {
	srv      *server
	resolver *pmsresolve.Resolver
	ctxs     *authctx.Store
	grants   *staygrant.Store
	// attempts records what happened on every deliberate submission. Nil is inert: the arm keeps working and
	// simply records nothing, which is what a build without a database pool gets.
	attempts *signinattempt.Store
	// protection is the site's guest sign-in rate policy. Nil is inert for the same reason.
	protection *signinattempt.ProtectionStore
	// contextTTL bounds how long a verified identity may sit unused before it has to be proven again.
	contextTTL time.Duration
}

// phase6AggregateOn reads the Phase-6 aggregate capability from the environment, failing CLOSED on an
// unreadable or incoherent flag pair: a configuration nobody can parse must not be read as permission.
func phase6AggregateOn() bool {
	cfg, err := iamv2.LoadPhase6ConfigFromEnv(os.Getenv)
	if err != nil {
		slog.Error("phase6 configuration is unreadable; aggregate acquisition stays OFF", "err", err)
		return false
	}
	return cfg.AggregateTimeOn()
}

// newPhase3Auth constructs the arm ONLY when the master + PMS-auth flags are on.
func newPhase3Auth(cfg iamv2.PMSConfig, s *server) *phase3Auth {
	if !cfg.AuthOn() {
		return nil
	}
	return &phase3Auth{
		srv:      s,
		resolver: pmsresolve.NewResolver(s.db),
		ctxs:     authctx.NewStore(s.db),
		// PHASE 6 (DARK by default): may this appliance create NEW entitlements in AGGREGATE_ONLINE_TIME
		// mode? Only when the flag that also turns on the accrual tick is on. Off means the grant is
		// refused rather than silently created with a budget nothing would ever consume.
		grants: staygrant.New(s.db).WithAggregateOnlineTime(phase6AggregateOn()),
		// The recorder is constructed with whatever keyring the daemon loaded. A nil keyring is not a failure
		// here: attempts are still recorded, without their sealed half, and the operator is told so.
		attempts: signinattempt.NewStore(s.db, s.signInAttemptKeyring, signInAttemptDEKID),
		// The policy reads its own numbers from the site database on every decision, so an operator changing
		// them in Hotel Admin takes effect on the next submission with no restart, rebuild or deployment.
		protection: signinattempt.NewProtectionStore(s.db),
		contextTTL: 10 * time.Minute,
	}
}

// ---- the uniform non-success contract --------------------------------------

// Every failure on this path returns the SAME shape and the same HTTP status, and it carries exactly ONE
// extra thing: a coarse CLASS saying whether the guest should re-check what they typed or whether the system
// could not answer. Nothing else about the cause crosses this boundary.
//
// THE CLASS IS NEW AND IT IS NARROW. The Product Owner asked for it because the old single sentence was
// useless in both directions: it told a guest whose details were right to re-check them, and it told a guest
// who merely mistyped to contact Reception. What it must NOT become is an oracle, so the mapping from the
// exact internal result to this class lives in internal/signinattempt.GuestClass and is built on one rule —
// anything that depends on what the guest typed, or on what the mirror holds about THEIR room, is one class;
// only conditions that are true for every guest on the site right now are the other. "No such room", "wrong
// name", "checked out" and "two candidates" are therefore all the same answer, exactly as before.
//
// The exact reason is recorded internally — the resolution row, the sign-in attempt record, the log — where
// operators can see it and guests cannot.
const (
	outcomeVerified    = "VERIFIED"
	outcomeNotVerified = "NOT_VERIFIED"
)

type phase3Response struct {
	Outcome string `json:"outcome"`
	// FailureClass is one of signinattempt's four guest classes, present on every non-success. It is the ONLY
	// thing about the cause that leaves this daemon towards a guest.
	FailureClass string `json:"failure_class,omitempty"`
	// RetryAfterSeconds is present only on a RATE_LIMITED refusal. It is the SERVER's remaining time, which
	// is what the guest counts down; a browser that ignores it gains nothing, because the same gate refuses
	// the next submission.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
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

// notVerified writes the non-success answer for an exact internal result. The result is for the log and the
// attempt record; only its coarse class reaches the guest, and `detail` never leaves this process.
func notVerified(w http.ResponseWriter, result signinattempt.Result, detail string) {
	notVerifiedRetryAfter(w, result, detail, 0)
}

// notVerifiedRetryAfter is the same answer carrying the server's remaining restriction time. It exists as a
// separate entry point so that every other refusal keeps the exact shape it had: a retry hint on a wrong
// surname would tell a guesser how long to wait between guesses.
func notVerifiedRetryAfter(w http.ResponseWriter, result signinattempt.Result, detail string, retryAfter int) {
	slog.Info("phase3 auth: not verified", "result", string(result), "detail", detail)
	writeJSONScd(w, http.StatusOK, phase3Response{
		Outcome:           outcomeNotVerified,
		FailureClass:      string(result.GuestClass()),
		RetryAfterSeconds: retryAfter,
	})
}

func writeJSONScd(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// ---- device identity (server-derived) --------------------------------------

type deviceIdentity struct {
	Tenant           string
	Site             string
	DeviceID         string
	GuestNetwork     string
	GuestNetworkName string
	IP               net.IP
	MAC              net.HardwareAddr
}

// The three ways deriving a device identity can fail are three DIFFERENT answers for an operator, and used
// to be one. A body the server cannot read is the client's fault; an address on no mapped guest network is a
// routing or configuration fault; a database that will not answer is ours. The guest sees a uniform envelope
// in all three cases, and the attempt record does not.
var (
	errDeviceUnreadable = errors.New("device identity: unreadable")
	errDeviceUnrouted   = errors.New("device identity: not on a mapped guest network")
	errDeviceStore      = errors.New("device identity: store unavailable")
)

// deviceFailure classifies a device error into the structured result an operator reads.
func deviceFailure(err error) signinattempt.Result {
	switch {
	case errors.Is(err, errDeviceUnrouted):
		return signinattempt.RoutingOrInterfaceFailure
	case errors.Is(err, errDeviceStore):
		return signinattempt.ServiceUnavailable
	default:
		return signinattempt.MalformedSubmission
	}
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
		return out, fmt.Errorf("%w: no usable source address", errDeviceUnreadable)
	}
	mac, err := net.ParseMAC(strings.TrimSpace(d.MAC))
	if err != nil {
		return out, fmt.Errorf("%w: no usable hardware address", errDeviceUnreadable)
	}
	// The guest network is re-derived HERE from the address, against this appliance's own tables. A
	// forwarded address that belongs to no enabled guest network is refused: Phase-3 resolution is scoped by
	// network, and a request from outside every guest network has no scope to resolve in.
	nc := p.srv.resolveNetwork(ctx, ip)
	if nc.NetworkID == "" {
		return out, fmt.Errorf("%w: source address is not on a mapped guest network", errDeviceUnrouted)
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
		return out, fmt.Errorf("%w: %v", errDeviceStore, err)
	}
	return deviceIdentity{Tenant: p.srv.tenID, Site: p.srv.siteID, DeviceID: id,
		GuestNetwork: nc.NetworkID, GuestNetworkName: nc.Name, IP: ip, MAC: mac}, nil
}

// ---- resolve ---------------------------------------------------------------

type phase3ResolveReq struct {
	Room string `json:"room"`
	// Verification carries the ONE value a guest typed under the combined room_any mode, where the portal does
	// not know or ask which kind of identifier it is. The server compares it against all three PMS-derived
	// fields; see the handler.
	Verification      string     `json:"verification"`
	LastName          string     `json:"last_name"`
	FirstName         string     `json:"first_name"`
	ReservationNumber string     `json:"reservation_number"`
	RequestID         string     `json:"request_id"`
	Device            wireDevice `json:"device"`
}

// combineVerification implements the room_any combined mode: the guest typed ONE value and was never asked
// what kind of identifier it is, so the server offers it to all three comparisons at once.
//
// Each copy is normalised the way its own stored column was — as a name for the name columns, as typed for the
// reservation id. NOTHING HERE INSPECTS THE VALUE'S SHAPE. Guessing "digits mean a reservation number" is the
// legacy "either" behaviour this mode exists to replace: it submitted a surname containing a digit as a
// reservation number and failed for the guest who owned that name, and it is wrong in the other direction for
// any property whose reservation numbers are alphabetic.
//
// Fanning out to three comparisons widens what matches, which is only safe because of what happens next:
// probeInterface ORs the three and answers AMBIGUOUS_LOCAL whenever more than one Stay matches, so a value
// that is simultaneously one guest's surname and another guest's reservation number in the same room FAILS
// CLOSED rather than picking one. Combined mode is therefore a transport change, not a matching change.
//
// An explicit single-field submission always wins, so a site on room_lastname keeps comparing exactly one
// field and nothing about its behaviour moves.
func combineVerification(verification, last, first, res string) (string, string, string) {
	if v := strings.TrimSpace(verification); v != "" && last == "" && first == "" && res == "" {
		return normalizeName(v), normalizeName(v), v
	}
	return last, first, res
}

// resolveHandler proves the guest's identity STRICTLY across every PMS Interface mapped to their network, and
// on success issues a one-time Auth Context. It grants nothing.
func (p *phase3Auth) resolveHandler(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	ctx := r.Context()

	// THE ATTEMPT RECORD IS OPENED FIRST AND WRITTEN LAST, whatever happens in between.
	//
	// The deferred write is the whole reason every refusal now leaves something behind. The exits below are
	// numerous and several are reached before a resolver, a stay or even a network exists — a malformed body,
	// a device on no mapped network, a database that will not answer — and those were exactly the attempts
	// that used to vanish. Setting a field and returning is all any branch has to remember to do.
	at := signinattempt.Attempt{TenantID: p.srv.tenID, SiteID: p.srv.siteID, OccurredAt: started}
	defer func() { p.recordAttempt(at, started) }()

	var req phase3ResolveReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		at.Result = signinattempt.MalformedSubmission
		notVerified(w, at.Result, "malformed_request")
		return
	}
	dev, err := p.device(ctx, req.Device)
	if err != nil {
		at.Result = deviceFailure(err)
		notVerified(w, at.Result, "device_identity: "+err.Error())
		return
	}
	at.GuestNetworkID, at.GuestNetworkName = dev.GuestNetwork, dev.GuestNetworkName
	at.DeviceIP, at.DeviceMAC = dev.IP.String(), signinattempt.NormalizeMAC(dev.MAC)

	room := normalizeRoom(req.Room)
	last := normalizeName(req.LastName)
	first := normalizeName(req.FirstName)
	res := strings.TrimSpace(req.ReservationNumber)

	last, first, res = combineVerification(req.Verification, last, first, res)

	// WHAT THE GUEST TYPED, recorded before anything is decided about it. The raw value and the normalized
	// form sit side by side because the difference between them is frequently the whole answer at the desk:
	// a trailing space and a wrong name are the same refusal once both sides are trimmed.
	submitted := submittedVerifier(req)
	at.SubmittedRoom = room
	at.VerifierKind = signinattempt.ClassifyVerifier(submitted)
	at.Sensitive.SubmittedVerifier = submitted
	at.Sensitive.NormalizedVerifier = normalizedVerifier(last, first, res)

	if room == "" || (last == "" && first == "" && res == "") {
		// Incomplete evidence is a non-success like any other: telling the guest WHICH field was missing is
		// a small oracle, and the portal already knows what it asked for.
		at.Result = signinattempt.MalformedSubmission
		notVerified(w, at.Result, "incomplete_evidence")
		return
	}
	// The request id is recorded as a uuid. Validating its SHAPE here means a malformed one is the ordinary
	// uniform non-success — not a raw PostgreSQL cast error surfaced as "the resolver failed", which would
	// both mislead an operator reading the logs and put database detail in them.
	if !validRequestID(req.RequestID) {
		at.Result = signinattempt.MalformedSubmission
		notVerified(w, at.Result, "malformed_request_id")
		return
	}
	at.RequestID = strings.TrimSpace(req.RequestID)

	// GUEST SIGN-IN PROTECTION, asked before any evidence is evaluated.
	//
	// THIS REPLACED A ROOM-SCOPED THROTTLE. The previous call charged the appliance's generic durable throttle
	// with the ROOM as one of its scopes, and the Product Owner ruled that out for a reason worth recording:
	// restricting a room number locks out the guest who actually lives there while the attacker — who chose
	// that number — simply moves to the next one. It made a denial of service against any guest trivial. The
	// address was no better, because a guest network NATs and an address is a floor.
	//
	// The device is what is doing the guessing, and the MAC used here is the one the APPLIANCE read from its
	// own neighbour table — never a value the client sent, so no cookie, request id or reopened page moves it.
	// It is not unspoofable and is not claimed to be; see internal/signinattempt for the honest limit.
	//
	// AN ERROR IS NOT AN ALLOW. A gate that cannot be read is a service failure, answered with the technical
	// message — which also does not count as a wrong credential, so a broken database cannot restrict anybody
	// either. The alternative, treating an error as permission to proceed, would make one failing query the
	// way to switch the control off.
	if p.protection != nil {
		gate, gerr := p.protection.Check(ctx, p.srv.tenID, p.srv.siteID, dev.MAC)
		if gerr != nil {
			at.Result = signinattempt.ServiceUnavailable
			notVerified(w, at.Result, "protection_gate_unreadable: "+gerr.Error())
			return
		}
		if gate.Restricted {
			at.Result = signinattempt.RateLimited
			notVerifiedRetryAfter(w, at.Result, "restricted", gate.RemainingSeconds)
			return
		}
	}

	// CAN THE MIRROR ANSWER FOR ANYBODY? Asked BEFORE the room is looked at, and refused on before the
	// evidence is evaluated at all. The ordering is the security property rather than an optimisation — see
	// mirrorStateFor — and it is also what lets a guest be told "we cannot check right now" truthfully,
	// instead of being told to re-check a surname that was never compared against anything.
	mirror := p.mirrorStateFor(ctx, dev.GuestNetwork)
	at.PMSTransportStatus = mirror.TransportStatus
	at.MirrorLastCompleteSyncAt, at.MirrorAgeSeconds = mirror.LastCompleteSync, mirror.AgeSeconds
	if !mirror.CanAuthoriseAnyone {
		at.Result = signinattempt.MirrorStaleOrMissingChange
		notVerified(w, at.Result, "mirror_cannot_authorise")
		return
	}

	// The probe evaluates ONE interface's mirrored Stay state. It returns a determinate verdict or an
	// indeterminate one; it never guesses, and the resolver waits for every interface before deciding. The
	// collector rides alongside and keeps the detail the verdict necessarily discards.
	var seen observations
	probe := pmsresolve.ProbeFunc(func(pctx context.Context, ifaceID string) (pmsresolve.CandidateOutcome, string, error) {
		return p.probeInterface(pctx, &seen, ifaceID, room, last, first, res)
	})

	out, err := p.resolver.Resolve(ctx, p.srv.tenID, p.srv.siteID, dev.GuestNetwork, at.RequestID, probe)
	if err != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "resolver: "+err.Error())
		return
	}

	// What the probes saw, folded into the record whether or not the resolution succeeded. On a refusal this
	// IS the answer; on a success it is the corroboration.
	roomExists, eligible, matched, anyFailed := seen.summarise()
	at.RoomInMirror, at.EligibleStayCandidates = roomExists, eligible
	at.PMSInterfaceID = matched.InterfaceID
	if stay := firstNonEmpty(matched.MatchedStay, matched.CandidateStay); stay != "" {
		f, fam, resv, guestID, others := p.acceptedValues(ctx, stay)
		at.Sensitive.AcceptedFirstName = f
		at.Sensitive.AcceptedFamilyName = fam
		at.Sensitive.AcceptedReservationNumber = resv
		at.Sensitive.AdditionalAcceptedGuests = others
		at.MatchedStay, at.MatchedGuest = stay, guestID
	}

	if !out.GuestVisibleSuccess() {
		at.Result = classifyRefusal(out, roomExists, eligible, anyFailed)
		// One refusal reason is not like the others. A spent request id means the CLIENT re-used an id it had
		// already had refused — which the portal no longer does — so it is the signature of a stale portal
		// build or of something that is not the portal, and an operator looking at a guest who "cannot sign
		// in" needs to see that rather than another indistinguishable NOT_VERIFIED.
		if out.Reason == pmsresolve.ReasonSpentOnRefusal {
			slog.Warn("phase3 auth: a resolution request id was re-used after it had already been refused; "+
				"the submission was evaluated and refused rather than answered from the earlier record",
				"guest_network", dev.GuestNetwork)
		}
		// Nothing matched, so the record must not claim a matched stay. The accepted values gathered above
		// from a LONE candidate stay stay in place deliberately: they are what WOULD have been accepted, and
		// that comparison is the reason an operator opens this screen.
		if matched.MatchedStay == "" {
			at.MatchedStay, at.MatchedGuest = "", ""
		}
		notVerified(w, at.Result, "resolution_"+string(out.Resolution)+"_"+out.Reason)
		return
	}
	at.MatchedStay = out.Stay
	at.MatchedField = matched.MatchedField

	// A REPLAYED resolution (the same request id submitted twice — a double tap, or a response the guest's
	// phone never received) returns the stored outcome, and the stored row records the Stay, not the
	// interface that produced it. Re-deriving the interface FROM THE STAY is exact: a Stay belongs to exactly
	// one PMS Interface. Without this, every retry of a successful resolution fails to issue a context, and
	// the guest is permanently stuck behind a uniform "not verified" they cannot act on.
	iface := out.InterfaceID
	if iface == "" {
		if iface, err = p.interfaceForStay(ctx, out.Stay); err != nil {
			at.Result = signinattempt.ServiceUnavailable
			notVerified(w, at.Result, "replayed_resolution_interface_unresolvable")
			return
		}
	}

	// The REVISION is pinned server-side from the interface that verified: the guest never names which
	// configuration their access was granted under, and the Auth Context records it so the later grant cannot
	// drift onto a newer one.
	at.PMSInterfaceID = iface
	rev, err := p.publishedRevision(ctx, iface)
	if err != nil {
		at.Result = signinattempt.RoutingOrInterfaceFailure
		notVerified(w, at.Result, "no_published_revision")
		return
	}
	// THE OFFER SET the real eligibility engine says this verified Stay qualifies for — not the site's whole
	// free catalogue. Two Stays verified a second apart can legitimately get different answers.
	decisions, err := p.offersFor(ctx, out.Stay, iface, time.Now())
	if err != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "offers: "+err.Error())
		return
	}
	if len(decisions) == 0 {
		// A verified guest with nothing they qualify for is a CONFIGURATION or eligibility outcome, not an
		// identity one — and it is recorded as its own result rather than folded in with wrong surnames,
		// because the two need opposite responses from whoever is helping the guest.
		at.Result = signinattempt.VerifiedNoEligiblePackage
		notVerified(w, at.Result, "verified_but_no_eligible_package")
		return
	}
	evidenceVersion := decisions[0].EvidenceVersion

	// The Context and the offer set it authorises are written TOGETHER. A Context that existed for even an
	// instant without its offer set would be redeemable against nothing, and the grant's "was this offered?"
	// check would have to fall back to "is this generally grantable?" — the exact weakening it replaces.
	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "begin: "+err.Error())
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
		at.RequestID, int(p.contextTTL.Seconds())).Scan(&id, &reused); err != nil {
		// The context function refuses a Stay that is not eligible RIGHT NOW — checked out, without occupancy
		// evidence, pinned to a superseded revision. That is a different fact from "the name was wrong", and
		// it is recorded as one instead of joining the same undifferentiated refusal.
		at.Result = signinattempt.StayNotEligible
		notVerified(w, at.Result, "context_issue: "+err.Error())
		return
	}
	// A reused context already carries the offer set it was issued with; the controlled writer is idempotent
	// per (context, package), so a retry re-states the same set rather than widening it.
	if err := p.recordOfferSet(ctx, tx, id, evidenceVersion, decisions,
		time.Now().Add(p.contextTTL)); err != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "offer_record: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "commit: "+err.Error())
		return
	}
	// The identity proof stands. Whether the guest ends up online depends on the grant that follows, which
	// records its own attempt against the same request id.
	at.Result = signinattempt.Verified

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

// probeInterface evaluates ONE interface's mirrored Stay state for the submitted evidence, and records what
// it SAW alongside the verdict it returns.
//
// Each identifier is matched against a field the PMS actually populated on this Stay — last name and first
// name from stay_guests, reservation from the Stay itself. There is no fuzzy matching and nothing is
// inferred: a value either equals a stored, normalised identity or it does not. Matching more than one live
// Stay is AMBIGUOUS_LOCAL rather than a pick, because choosing between two guests who share a room number is
// exactly the decision this system must never make on its own.
//
// WHY THE STATUS FILTER MOVED OUT OF THE WHERE CLAUSE. It used to select only stays that may authenticate, so
// a room with a checked-out guest and a room that does not exist produced the identical empty result — and
// therefore the identical refusal, with nothing anywhere to tell them apart. The query now returns every stay
// on the room and the ELIGIBILITY IS APPLIED IN GO, which changes no verdict (an ineligible stay is still
// never matched) and turns one undifferentiated failure into three distinguishable ones.
//
// The multi-word given name is worth stating explicitly because it looks like a bug and is the contract: the
// PMS sends "FAMILY, FULL GIVEN NAME" and the mirror stores the whole given-name field in one column, so
// first_name_norm may be "MARIA DEL CARMEN" and only that complete value matches. Individual words within it
// are not accepted, and that is deliberate — accepting "MARIA" would admit anyone who guessed a common first
// name for a room.
func (p *phase3Auth) probeInterface(ctx context.Context, seen *observations, ifaceID, room, last, first, res string) (pmsresolve.CandidateOutcome, string, error) {
	obs := probeObservation{InterfaceID: ifaceID}
	rows, err := p.srv.db.Query(ctx, `
		SELECT s.id::text,
		       s.status IN ('IN_HOUSE','POST_STAY_ACTIVE') AS eligible,
		       ($5 <> '' AND EXISTS (SELECT 1 FROM iam_v2.stay_guests g
		                              WHERE g.stay_id = s.id AND g.last_name_norm = $5))  AS family_hit,
		       ($6 <> '' AND EXISTS (SELECT 1 FROM iam_v2.stay_guests g
		                              WHERE g.stay_id = s.id AND g.first_name_norm = $6)) AS first_hit,
		       ($7 <> '' AND s.external_reservation_id = $7)                               AS reservation_hit
		  FROM iam_v2.stays s
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.pms_interface_id=$3
		   AND s.normalized_room_number = $4
		 LIMIT 16`, p.srv.tenID, p.srv.siteID, ifaceID, room, last, first, res)
	if err != nil {
		// An interface whose state cannot be read is INDETERMINATE, never a determinate "no such guest".
		obs.Failed = true
		seen.add(obs)
		return pmsresolve.Unavailable, "", err
	}
	defer rows.Close()

	var matches []string
	var eligibleStays []string
	for rows.Next() {
		var id string
		var eligible, familyHit, firstHit, reservationHit bool
		if err := rows.Scan(&id, &eligible, &familyHit, &firstHit, &reservationHit); err != nil {
			obs.Failed = true
			seen.add(obs)
			return pmsresolve.Unavailable, "", err
		}
		obs.RoomExists = true
		if !eligible {
			continue
		}
		eligibleStays = append(eligibleStays, id)
		if !(familyHit || firstHit || reservationHit) {
			continue
		}
		matches = append(matches, id)
		// WHICH FIELD ADMITTED THEM, in a fixed precedence so the recorded answer is deterministic when one
		// typed value happens to equal two accepted ones. Order follows how a guest is usually asked: family
		// name, then given name, then the reservation identifier.
		switch {
		case familyHit:
			obs.MatchedField = signinattempt.MatchedFamilyName
		case firstHit:
			obs.MatchedField = signinattempt.MatchedFirstName
		default:
			obs.MatchedField = signinattempt.MatchedReservationNumber
		}
	}
	if err := rows.Err(); err != nil {
		obs.Failed = true
		seen.add(obs)
		return pmsresolve.Unavailable, "", err
	}

	obs.EligibleStays = len(eligibleStays)
	obs.Matches = len(matches)
	if len(matches) == 1 {
		obs.MatchedStay = matches[0]
	} else {
		obs.MatchedField = signinattempt.MatchedNone
	}
	// With exactly one eligible stay on the room there is no ambiguity about whose values WOULD have been
	// accepted, so the comparison panel can show them. With several, it shows none: picking one would be
	// inventing an expectation the guest never had.
	if len(eligibleStays) == 1 {
		obs.CandidateStay = eligibleStays[0]
	}
	seen.add(obs)

	switch len(matches) {
	case 0:
		return pmsresolve.NoMatch, "", nil
	case 1:
		return pmsresolve.Verified, matches[0], nil
	default:
		return pmsresolve.AmbiguousLocal, "", nil
	}
}

// classifyRefusal turns the resolver's narrow verdict plus what the probes saw into the exact structured
// reason an operator reads.
//
// THE ORDER OF THESE TESTS IS THE ANSWER. "Nothing matched" is the same verdict whether the room is absent,
// present but ineligible, or present with candidates the value did not match, and those are three different
// conversations at the desk. Getting them in the wrong order would produce a confident, wrong diagnosis —
// worse than the single undifferentiated refusal this replaces.
func classifyRefusal(out pmsresolve.Result, roomExists bool, eligible int, anyProbeFailed bool) signinattempt.Result {
	if out.Reason == pmsresolve.ReasonSpentOnRefusal {
		return signinattempt.SpentRequestID
	}
	switch out.Resolution {
	case pmsresolve.ResAmbiguous:
		return signinattempt.AmbiguousRoomCandidates
	case pmsresolve.ResIndeterminate:
		if out.Reason == "UNMAPPED" || out.Reason == "CANDIDATE_CAP_EXCEEDED" {
			// The guest network maps to no usable interface, or to implausibly many. Neither is anything the
			// guest typed.
			return signinattempt.RoutingOrInterfaceFailure
		}
		return signinattempt.ServiceUnavailable
	case pmsresolve.ResNoMatch:
		switch {
		case anyProbeFailed:
			// A probe that could not answer must never be reported as "your details are wrong": we did not
			// ask, so we do not know.
			return signinattempt.ServiceUnavailable
		case !roomExists:
			return signinattempt.RoomNotInMirror
		case eligible == 0:
			return signinattempt.StayNotEligible
		default:
			return signinattempt.CredentialMismatch
		}
	default:
		return signinattempt.ServiceUnavailable
	}
}

// submittedVerifier returns the ONE value the guest typed, exactly as they typed it.
//
// Whichever sign-in mode the site runs, a guest fills in one box. room_any sends it as `verification`; the
// explicit modes send it in the field they asked for. Recording the raw string is what makes the operator's
// comparison honest — normalize it here and a trailing space becomes invisible, which is one of the failures
// this screen exists to explain.
func submittedVerifier(req phase3ResolveReq) string {
	for _, v := range []string{req.Verification, req.LastName, req.FirstName, req.ReservationNumber} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// normalizedVerifier returns the value the comparison actually used. The name fields are normalized
// identically, so either serves; the reservation identifier is trim-only and is the fallback.
func normalizedVerifier(last, first, res string) string {
	return firstNonEmpty(last, first, res)
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
		notVerified(w, signinattempt.MalformedSubmission, "malformed_request")
		return
	}
	ctx := r.Context()

	// THE GRANT COMPLETES AN ATTEMPT; IT DOES NOT START ONE.
	//
	// One deliberate Connect submission is one row, and the resolve already wrote it. This call is the second
	// half of the same submission, so what it learns — the entitlement and session the guest ended up with, or
	// the fact that a proved identity could not be granted access — is stamped onto that row through the same
	// request id. Writing a second row here would double-count every successful sign-in and make "how many
	// guests failed today" unanswerable.
	requestID := p.requestIDForContext(ctx, strings.TrimSpace(req.AuthContextID))
	grantResult := signinattempt.ServiceUnavailable
	var grantedEntitlement, grantedSession string
	defer func() { p.completeAttempt(requestID, grantResult, grantedEntitlement, grantedSession) }()

	dev, err := p.device(ctx, req.Device)
	if err != nil {
		grantResult = deviceFailure(err)
		notVerified(w, grantResult, "device_identity: "+err.Error())
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
		// The rows exist. Success still depends on the KERNEL, so this path waits exactly as the fresh one
		// does: a retry arriving while enforcement is still converging must not be told it is connected.
		enforced, werr := p.awaitEnforced(ctx, sid)
		if werr != nil || !enforced {
			slog.Warn("phase3 auth: retry found an existing grant whose enforcement is not confirmed",
				"session", sid, "err", werr)
			notVerified(w, signinattempt.ServiceUnavailable, "enforcement_not_confirmed")
			return
		}
		grantResult, grantedEntitlement, grantedSession = signinattempt.Verified, ent, sid
		slog.Info("phase3 auth: returning the session an earlier grant already created",
			"session", sid, "entitlement", ent)
		writeJSONScd(w, http.StatusOK, phase3GrantResp{Outcome: outcomeVerified, SessionID: sid, EntitlementID: ent})
		return
	}

	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "begin: "+err.Error())
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
	// The lock goes through iam_v2.lock_auth_context_offer (migration 0057) rather than an inline
	// SELECT ... FOR UPDATE. PostgreSQL requires UPDATE privilege for a row lock, and granting scd UPDATE on
	// auth_context_offers would let it rewrite the matched tier, the evidence version and the expiry — the
	// exact fields validated below. The function hands over the lock and nothing else.
	err = tx.QueryRow(ctx,
		`SELECT matched_tier_order, evidence_version
		   FROM iam_v2.lock_auth_context_offer($1,$2,$3::uuid,$4::uuid)`,
		p.srv.tenID, p.srv.siteID, strings.TrimSpace(req.AuthContextID),
		strings.TrimSpace(req.PackageRevID)).Scan(&offeredTier, &offerEvidence)
	if err != nil {
		// ONLY "no such offer" is an authorisation answer. Everything else — a permission error, a dead
		// connection, a timeout — is an internal failure, and reporting it as "not offered" is how the first
		// real Room Login on this appliance spent a diagnosis cycle looking like a data problem. The guest
		// still sees the uniform envelope either way; the difference is entirely in what the operator is told.
		if errors.Is(err, pgx.ErrNoRows) {
			// The context is real but this package was never offered to it. That is a stay-eligibility fact,
			// not an identity one, and an operator needs to see the difference.
			grantResult = signinattempt.VerifiedNoEligiblePackage
			notVerified(w, grantResult, "package_not_offered_to_this_context")
			return
		}
		slog.Error("phase3 grant: offer lock failed", "err", err,
			"context", strings.TrimSpace(req.AuthContextID))
		notVerified(w, signinattempt.ServiceUnavailable, "offer_lock_unavailable")
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
		notVerified(w, signinattempt.ServiceUnavailable, "stay_evidence_unreadable")
		return
	}
	if nowEvidence != offerEvidence {
		// The stay moved room, changed rate or checked out between the offer and the grant. The identity was
		// proved against facts that no longer hold.
		grantResult = signinattempt.StayNotEligible
		notVerified(w, grantResult, "stay_evidence_changed_since_the_offer")
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
		// Every grant failure is the same uniform non-success to the GUEST: they must not be able to tell
		// "that context was already used" from "that package needs payment" from "the stay already has
		// access". The operator's record keeps the distinction that matters most of those — a stay that
		// already holds access is an eligibility fact, not an internal fault.
		if errors.Is(err, staygrant.ErrAlreadyEntitled) {
			grantResult = signinattempt.StayNotEligible
		}
		notVerified(w, grantResult, "grant: "+err.Error())
		return
	}

	sessionID, err := p.openSessionTx(ctx, tx, granted.EntitlementID, dev)
	if err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "session: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "commit: "+err.Error())
		return
	}
	// The rows are durable, but the guest is NOT online yet: nothing has been enforced in the kernel. Wait for
	// the enforcement owner to confirm both halves before telling the guest they are connected.
	enforced, werr := p.awaitEnforced(ctx, sessionID)
	if werr != nil || !enforced {
		// Durable state is intact and the grant is recoverable — the guest's retry (same device, same
		// context) returns this exact Session as soon as it is genuinely active. What must not happen is
		// claiming success now: that is the "connected" screen on a network that carries no packets.
		slog.Warn("phase3 auth: grant committed but enforcement not confirmed",
			"session", sessionID, "entitlement", granted.EntitlementID, "err", werr)
		notVerified(w, signinattempt.ServiceUnavailable, "enforcement_not_confirmed")
		return
	}
	grantResult, grantedEntitlement, grantedSession = signinattempt.Verified, granted.EntitlementID, sessionID
	slog.Info("phase3 auth: access granted and enforced",
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
//	s.state IN (active,        — a closed session is not access. A guest whose session was ended (checkout,
//	  PENDING_ENFORCEMENT)        revocation, an operator action) gets the uniform refusal, not a resurrection.
//	                            PENDING_ENFORCEMENT is included so a retry that arrives while enforcement is
//	                            still converging recovers THAT session instead of minting a second grant.
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
		   AND s.state IN ('active','PENDING_ENFORCEMENT') AND s.ended IS NULL
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
//
// It opens the Session as PENDING_ENFORCEMENT, NOT 'active'.
//
// At this instant nothing has been enforced: no accountable class exists and the guest is not authorized at
// the packet gate. Writing 'active' here would be a false statement about the kernel — and the one every
// other component trusts. PENDING_ENFORCEMENT says exactly what is true: the grant is durable and recoverable,
// and this guest SHOULD be enforced. netd, the enforcement owner, promotes it to 'active' through
// iam_v2.activate_session_enforcement once the accountable tc class and the nft authorization are both proven.
func (p *phase3Auth) openSessionTx(ctx context.Context, tx pgx.Tx, entitlement string, dev deviceIdentity) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO iam_v2.sessions
		  (tenant_id, site_id, entitlement_id, device_id, credential_method, state, started, ip, mac, ingress_interface)
		VALUES ($1,$2,$3,$4,'PMS','PENDING_ENFORCEMENT',now(),$5::inet,$6::macaddr,$7)
		RETURNING id::text`,
		p.srv.tenID, p.srv.siteID, entitlement, dev.DeviceID,
		dev.IP.String(), dev.MAC.String(), p.bridgeFor(ctx, dev.IP)).Scan(&id)
	return id, err
}

// THE ENFORCEMENT WAIT, AND WHOSE CLOCK IT RUNS ON.
//
// This wait has to exceed one reconciliation cycle — the enforcement owner converges on its own schedule, so
// a grant that commits just after a tick genuinely cannot be enforced for most of a second — while staying
// inside the budget of whoever is waiting on the other end.
//
// It used to be a flat 8 seconds, which was wrong in a way that a fixed number cannot be right: the caller is
// portald, portald runs every Phase-3 request under a uniform response-time budget, and it passes that budget
// down as a context deadline. A wait longer than the caller's deadline is not a longer wait — the context is
// already cancelled, so it is a wait that ends immediately, with an error, on the very requests it was meant
// to protect. The 8 seconds were unreachable; the effective wait was whatever portald had left.
//
// So the deadline is DERIVED from the caller's, with a small reserve so scd answers before its caller stops
// listening rather than at the same instant. enforcementDeadlineMax applies only when there is no deadline to
// derive from (a direct operator call, a test), and bounds the wait so nothing can pin a request forever.
const (
	enforcementDeadlineMax = 8 * time.Second
	enforcementReserve     = 150 * time.Millisecond
	// enforcementPoll is the granularity at which durable state is re-read. It is short relative to the
	// producer's one-second cadence, so the time between "the owner promoted this Session" and "the guest is
	// told" is a small fraction of the budget rather than a quarter of it.
	enforcementPoll = 100 * time.Millisecond
)

// enforcementDeadlineFor derives this request's wait from the caller's own budget.
func enforcementDeadlineFor(ctx context.Context, now time.Time) time.Time {
	deadline := now.Add(enforcementDeadlineMax)
	if d, ok := ctx.Deadline(); ok {
		if reserved := d.Add(-enforcementReserve); reserved.Before(deadline) {
			deadline = reserved
		}
	}
	return deadline
}

// awaitEnforced blocks until the Session is genuinely network-active, or the deadline passes.
//
// This is what makes guest-visible success truthful. Before this existed the handler returned VERIFIED as
// soon as the rows committed, so the portal told the guest they were online while the kernel had not yet
// authorized a single packet — the guest would tap "Connect", see success, and have no internet.
//
// It polls durable state rather than asking netd, deliberately: the Session row is the one place where the
// enforcement owner's confirmed result is recorded, and reading it means scd needs no privileged channel and
// no second opinion about what is in force.
func (p *phase3Auth) awaitEnforced(ctx context.Context, sessionID string) (bool, error) {
	deadline := enforcementDeadlineFor(ctx, time.Now())
	for {
		var state string
		var ended *time.Time
		err := p.srv.db.QueryRow(ctx,
			`SELECT state, ended FROM iam_v2.sessions WHERE id=$1::uuid AND tenant_id=$2 AND site_id=$3`,
			sessionID, p.srv.tenID, p.srv.siteID).Scan(&state, &ended)
		if err != nil {
			return false, err
		}
		if ended != nil || state == "ended" || state == "closed" {
			// Enforcement failed and the session was closed, or it was revoked while we waited. Either way
			// this guest is not online, and saying so is the only honest answer.
			return false, nil
		}
		if state == "active" {
			return true, nil
		}
		if !time.Now().Before(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(enforcementPoll):
		}
	}
}

func (p *phase3Auth) bridgeFor(ctx context.Context, ip net.IP) string {
	return p.srv.resolveNetwork(ctx, ip).Bridge
}

// ---- normalization ---------------------------------------------------------
//
// Both sides of a comparison must be normalized the same way or the match silently depends on how the guest
// typed it. The PMS mirror stores normalized values; these functions are the guest-side half of that contract.

// These now DELEGATE. They used to hold their own copy of the transformation, which is how the write side
// was able to drift away from them unnoticed -- the contract was asserted in a comment here and implemented
// nowhere else. There is one implementation, in internal/namenorm, and both sides import it.

func normalizeRoom(s string) string { return namenorm.Room(s) }

func normalizeName(s string) string { return namenorm.Name(s) }
