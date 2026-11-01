package main

// PMS room sign-in. A guest proves identity only by naming stay details; everything that decides access
// (stay, interface, price, plan, device, network) is derived by the appliance, never named by the request body.
//
// Two steps, so proving is idempotent per request id and granting consumes exactly once:
//
//	resolve → (VERIFIED) → one-time Auth Context → grant → Entitlement → Session
//
// Trust boundary: scd listens on a root-owned unix socket; portald derives source IP and MAC, and scd
// re-derives the guest network from the IP regardless of what the hop claimed.

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
	"github.com/stayconnect/enterprise/data-plane/internal/licstate"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsresolve"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
	"github.com/stayconnect/enterprise/data-plane/internal/staygrant"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
)

// phase3Auth is scd's Phase-3 arm. A nil value is inert: the routes are not mounted (404).
type phase3Auth struct {
	srv      *server
	resolver *pmsresolve.Resolver
	ctxs     *authctx.Store
	grants   *staygrant.Store
	// attempts records every deliberate submission. Nil records nothing.
	attempts *signinattempt.Store
	// protection is the site's guest sign-in rate policy. Nil disables it.
	protection *signinattempt.ProtectionStore
	// contextTTL bounds how long a verified identity may sit unused.
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

// newPhase3Auth constructs the arm only when the master and PMS-auth flags are on.
func newPhase3Auth(cfg iamv2.PMSConfig, s *server) *phase3Auth {
	if !cfg.AuthOn() {
		return nil
	}
	return &phase3Auth{
		srv:      s,
		resolver: pmsresolve.NewResolver(s.db),
		ctxs:     authctx.NewStore(s.db),
		// AGGREGATE_ONLINE_TIME entitlements are created only when the accrual tick is on; otherwise the grant
		// is refused rather than given a budget nothing consumes.
		grants: staygrant.New(s.db).WithAggregateOnlineTime(phase6AggregateOn()),
		// A nil keyring still records attempts, without their sealed half.
		attempts: signinattempt.NewStore(s.db, s.signInAttemptKeyring, signInAttemptDEKID),
		// The policy reads its settings from the database on every decision.
		protection: signinattempt.NewProtectionStore(s.db),
		contextTTL: 10 * time.Minute,
	}
}

// Every failure returns the same shape and status plus one coarse class (re-check details vs system could not
// answer). The class must never become an oracle: anything depending on what the guest typed or on their room is
// one class (internal/signinattempt.GuestClass). The exact reason stays in the resolution row, attempt record and log.
const (
	outcomeVerified    = "VERIFIED"
	outcomeNotVerified = "NOT_VERIFIED"
)

type phase3Response struct {
	Outcome string `json:"outcome"`
	// FailureClass is present on every non-success and is the only thing about the cause that reaches a guest.
	FailureClass string `json:"failure_class,omitempty"`
	// RetryAfterSeconds is present only on RATE_LIMITED: the server's remaining time.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
	// Error is present only on a licence refusal (LICENSE or CAPACITY) and carries the code the other guest
	// methods use; it is an appliance-wide fact, never about a room.
	Error string `json:"error,omitempty"`
	// AuthContextID, ExpiresIn and Offers are present only on VERIFIED.
	AuthContextID string        `json:"auth_context_id,omitempty"`
	ExpiresIn     int           `json:"expires_in_seconds,omitempty"`
	Offers        []phase3Offer `json:"offers,omitempty"`
	// DeviceID and GuestNetworkID are present only on a DEVICE_LIMIT refusal, for portald alone, which never
	// forwards them to the guest.
	DeviceID       string `json:"device_id,omitempty"`
	GuestNetworkID string `json:"guest_network_id,omitempty"`
}

// phase3Offer is a package the server determined this stay may be granted; the grant re-validates it, so an
// offer is never an authorization.
type phase3Offer struct {
	PackageRevisionID string   `json:"package_revision_id"`
	Code              string   `json:"code"`
	DownKbps          int      `json:"down_kbps"`
	UpKbps            int      `json:"up_kbps"`
	PriceMinor        int64    `json:"price_minor"`
	Currency          string   `json:"currency,omitempty"`
	CurrencyExponent  int      `json:"currency_exponent"`
	Methods           []string `json:"methods"`
}

// notVerified writes the non-success answer; only the coarse class reaches the guest, `detail` never does.
func notVerified(w http.ResponseWriter, result signinattempt.Result, detail string) {
	notVerifiedRetryAfter(w, result, detail, 0)
}

// notVerifiedRetryAfter adds the remaining restriction time. Kept separate: a retry hint on a wrong surname
// would tell a guesser how long to wait between guesses.
func notVerifiedRetryAfter(w http.ResponseWriter, result signinattempt.Result, detail string, retryAfter int) {
	slog.Info("phase3 auth: not verified", "result", string(result), "detail", detail)
	writeJSONScd(w, http.StatusOK, phase3Response{
		Outcome:           outcomeNotVerified,
		FailureClass:      string(result.GuestClass()),
		RetryAfterSeconds: retryAfter,
	})
}

// Room sign-in answers to the same licence as every other guest method: licenseGate refuses, and sessions open
// only through reserveLicensedSlot.

// pmsLicenseRefusal is licenseRefusal for room sign-in; PMS is a commercial feature (licstate.FeatPMS).
func (p *phase3Auth) pmsLicenseRefusal() (string, bool) {
	body := p.srv.licenseRefusal(licstate.FeatPMS)
	if body == nil {
		return "", false
	}
	code, _ := body["error"].(string)
	return code, true
}

// notVerifiedLicense writes a licence refusal with the shared refusal code.
func notVerifiedLicense(w http.ResponseWriter, result signinattempt.Result, code, detail string) {
	slog.Info("phase3 auth: refused by the appliance licence", "result", string(result), "code", code, "detail", detail)
	writeJSONScd(w, http.StatusOK, phase3Response{
		Outcome:      outcomeNotVerified,
		FailureClass: string(result.GuestClass()),
		Error:        code,
	})
}

func writeJSONScd(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type deviceIdentity struct {
	Tenant           string
	Site             string
	DeviceID         string
	GuestNetwork     string
	GuestNetworkName string
	IP               net.IP
	MAC              net.HardwareAddr
}

// Three device-identity failures an operator must tell apart: unreadable body (client), address on no mapped
// network (routing/config), store down (ours). The guest sees the same envelope for all three.
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

// device resolves the requesting device to a durable identity from the appliance's own view, never from anything the guest typed.
func (p *phase3Auth) device(ctx context.Context, d wireDevice) (deviceIdentity, error) {
	var out deviceIdentity
	ip := net.ParseIP(strings.TrimSpace(d.IP))
	if ip == nil || ip.To4() == nil {
		return out, fmt.Errorf("%w: no usable source address", errDeviceUnreadable)
	}
	// An empty MAC means the kernel could not answer: a placement failure (technical class). Classing it as
	// malformed (credential class) would tell the guest their details are wrong when typing is not the problem.
	// A non-empty unparseable one is genuinely malformed.
	raw := strings.TrimSpace(d.MAC)
	if raw == "" {
		return out, fmt.Errorf("%w: the source device's hardware address could not be established", errDeviceUnrouted)
	}
	mac, err := net.ParseMAC(raw)
	if err != nil {
		return out, fmt.Errorf("%w: no usable hardware address", errDeviceUnreadable)
	}
	// The guest network is re-derived here from the address; resolution is scoped by network, so an address on
	// no enabled guest network is refused.
	nc := p.srv.resolveNetwork(ctx, ip)
	if nc.NetworkID == "" {
		return out, fmt.Errorf("%w: source address is not on a mapped guest network", errDeviceUnrouted)
	}
	// The conflict target must be the table's real uniqueness (tenant, site, appliance, mac), else every first
	// request errors. DO UPDATE rather than DO NOTHING because DO NOTHING returns no row and the id is needed.
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
	// Session activation admits a device only on a network it has appeared on (iamv2_session_activate.go); a
	// paid room charge activates later, so the appearance must be recorded here or it fails DEVICE_IDENTITY_MISMATCH.
	if _, err := p.srv.db.Exec(ctx, `
		INSERT INTO iam_v2.device_network_appearances (tenant_id, site_id, device_id, guest_network_id, first_seen, last_seen)
		VALUES ($1,$2,$3,$4,now(),now())
		ON CONFLICT (device_id, guest_network_id) DO UPDATE SET last_seen = now()`,
		p.srv.tenID, p.srv.siteID, id, nc.NetworkID); err != nil {
		return out, fmt.Errorf("%w: %v", errDeviceStore, err)
	}
	return deviceIdentity{Tenant: p.srv.tenID, Site: p.srv.siteID, DeviceID: id,
		GuestNetwork: nc.NetworkID, GuestNetworkName: nc.Name, IP: ip, MAC: mac}, nil
}

type phase3ResolveReq struct {
	Room string `json:"room"`
	// Verification is the one value typed under room_any; the server compares it against all three fields.
	Verification      string     `json:"verification"`
	LastName          string     `json:"last_name"`
	FirstName         string     `json:"first_name"`
	ReservationNumber string     `json:"reservation_number"`
	RequestID         string     `json:"request_id"`
	Device            wireDevice `json:"device"`
}

// combineVerification implements room_any: the one typed value is offered to all three comparisons, each
// normalised like its stored column. Never inspect the value's shape (digits do not imply a reservation number).
// This is safe only because probeInterface answers AMBIGUOUS_LOCAL when more than one Stay matches, failing closed.
// An explicit single-field submission always wins.
func combineVerification(verification, last, first, res string) (string, string, string) {
	if v := strings.TrimSpace(verification); v != "" && last == "" && first == "" && res == "" {
		return normalizeName(v), normalizeName(v), v
	}
	return last, first, res
}

// resolveHandler proves identity across every PMS Interface mapped to the guest's network and issues a one-time Auth Context. It grants nothing.
func (p *phase3Auth) resolveHandler(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	ctx := r.Context()

	// The attempt record is opened first and written last (deferred), so every early exit leaves a record.
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
	// Record the source address before judging it, so unrouted-device refusals still carry one; parsed, never copied.
	if parsed := net.ParseIP(strings.TrimSpace(req.Device.IP)); parsed != nil {
		at.DeviceIP = parsed.String()
	}
	if parsedMAC, perr := net.ParseMAC(strings.TrimSpace(req.Device.MAC)); perr == nil {
		at.DeviceMAC = signinattempt.NormalizeMAC(parsedMAC)
	}
	dev, err := p.device(ctx, req.Device)
	if err != nil {
		at.Result = deviceFailure(err)
		notVerified(w, at.Result, "device_identity: "+err.Error())
		return
	}
	at.GuestNetworkID, at.GuestNetworkName = dev.GuestNetwork, dev.GuestNetworkName
	at.DeviceIP, at.DeviceMAC = dev.IP.String(), signinattempt.NormalizeMAC(dev.MAC)
	// The licence is asked first, as on the other guest routes. A refusal is appliance-wide: no room lookup,
	// and it never counts against the device as a wrong credential.
	if code, refused := p.pmsLicenseRefusal(); refused {
		at.Result = signinattempt.LicenseRefused
		at.RequestID = strings.TrimSpace(req.RequestID)
		if !validRequestID(at.RequestID) {
			at.RequestID = ""
		}
		notVerifiedLicense(w, at.Result, code, "license_refused")
		return
	}
	// Room sign-in switched off is refused here as well as hidden. The same read supplies the sign-in mode
	// (phase3_auth_mode.go) so switch and mode come from one record.
	methods, methodsOK := p.srv.guestMethodSwitches(ctx)
	if !methodsOK || !methodSwitchOn(methods, guestMethodPMS) {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "pms_method_disabled")
		return
	}
	if !p.srv.networkOffers(ctx, dev.GuestNetwork, keyPMS) {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "pms_method_not_offered_on_network")
		return
	}

	room := normalizeRoom(req.Room)
	// The site's mode decides which fields may be compared; a field it does not accept is refused below, after the protection gate.
	evidence := applyRoomSignInMode(roomSignInMode(methods), req.Verification,
		normalizeName(req.LastName), normalizeName(req.FirstName), strings.TrimSpace(req.ReservationNumber))
	last, first, res := evidence.Last, evidence.First, evidence.Res

	// Record the raw and normalized value side by side; the difference is often the whole answer at the desk.
	submitted := submittedVerifier(req)
	at.SubmittedRoom = room
	at.VerifierKind = signinattempt.ClassifyVerifier(submitted)
	at.Sensitive.SubmittedVerifier = submitted
	at.Sensitive.NormalizedVerifier = normalizedVerifier(last, first, res)

	if room == "" || (!evidence.Violation && last == "" && first == "" && res == "") {
		// Naming the missing field would be a small oracle.
		at.Result = signinattempt.MalformedSubmission
		notVerified(w, at.Result, "incomplete_evidence")
		return
	}
	// Validate the shape here so a malformed id is the uniform non-success, not a PostgreSQL cast error.
	if !validRequestID(req.RequestID) {
		at.Result = signinattempt.MalformedSubmission
		notVerified(w, at.Result, "malformed_request_id")
		return
	}
	at.RequestID = strings.TrimSpace(req.RequestID)

	// Guest sign-in protection is asked before any evidence is evaluated. It is keyed on the device MAC the
	// appliance read itself, never on the room (a room-scoped throttle would lock out the real guest while the
	// attacker moves on) and never on a client-sent value; the MAC is spoofable (see internal/signinattempt).
	// An error is not an allow: an unreadable gate is a service failure that counts against nobody, because
	// treating it as permission would let one failing query switch the control off.
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
		// Changing the MAC defeats the device rule, so many wrong sign-ins on one network pause room sign-in there;
		// a staff-confirmed guest device is let through. Answered like a device restriction, not counted.
		ngate, nerr := p.protection.CheckNetwork(ctx, p.srv.tenID, p.srv.siteID, dev.GuestNetwork, dev.MAC)
		if nerr != nil {
			at.Result = signinattempt.ServiceUnavailable
			notVerified(w, at.Result, "network_pause_unreadable: "+nerr.Error())
			return
		}
		if ngate.Restricted {
			at.Result = signinattempt.RateLimited
			notVerifiedRetryAfter(w, at.Result, "network_paused", ngate.RemainingSeconds)
			return
		}
	}

	// A request the site's mode does not accept never comes from the real portal. It counts once as a wrong
	// credential (CREDENTIAL_MISMATCH): recording it as malformed, which never counts, would let a client probe
	// the mode for free. No room is looked up; one request is one attempt row, so at most one count.
	if evidence.Violation {
		at.Result = signinattempt.CredentialMismatch
		notVerified(w, at.Result, "evidence_not_accepted_by_room_sign_in_mode")
		return
	}

	// The network's explicit room sign-in policy decides what is consulted, before anything else. NONE offers no room sign-in at all (a voucher or account network);
	// a network nobody has decided about offers it to nobody; DEDICATED asks exactly its one interface and SHARED_STRICT exactly its selected set. A policy that
	// disagrees with the interfaces actually mapped is refused rather than guessed at. These are facts about the network, not a stale feed.
	var mapped int
	policy, perr := p.srv.roomSignInPolicy(ctx, dev.GuestNetwork)
	if perr == nil {
		perr = p.srv.db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_network_pms_map WHERE tenant_id=$1 AND site_id=$2 AND guest_network_id=$3`,
			p.srv.tenID, p.srv.siteID, dev.GuestNetwork).Scan(&mapped)
	}
	if perr != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "mapping: "+perr.Error())
		return
	}
	if why := routingRefusal(policy, mapped); why != "" {
		at.Result = signinattempt.RoutingOrInterfaceFailure
		notVerified(w, at.Result, why)
		return
	}

	// Ask whether the mirror can answer before looking at the room or evaluating evidence: the ordering is a
	// security property (see mirrorStateFor) and keeps "cannot check right now" truthful.
	mirror := p.mirrorStateFor(ctx, dev.GuestNetwork)
	at.PMSTransportStatus = mirror.TransportStatus
	at.MirrorLastCompleteSyncAt, at.MirrorAgeSeconds = mirror.LastCompleteSync, mirror.AgeSeconds
	if !mirror.CanAuthoriseAnyone {
		at.Result = signinattempt.MirrorStaleOrMissingChange
		notVerified(w, at.Result, "mirror_cannot_authorise")
		return
	}

	// The probe returns a determinate or indeterminate verdict per interface and never guesses; `seen` keeps the detail the verdict discards.
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

	// Fold what the probes saw into the record whether or not the resolution succeeded.
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
		// A spent request id signals a stale portal build or a non-portal client; operators need to see that.
		if out.Reason == pmsresolve.ReasonSpentOnRefusal {
			slog.Warn("phase3 auth: a resolution request id was re-used after it had already been refused; "+
				"the submission was evaluated and refused rather than answered from the earlier record",
				"guest_network", dev.GuestNetwork)
		}
		// No matched stay is claimed, but the accepted values of a lone candidate stay: they show what would have matched.
		if matched.MatchedStay == "" {
			at.MatchedStay, at.MatchedGuest = "", ""
		}
		notVerified(w, at.Result, "resolution_"+string(out.Resolution)+"_"+out.Reason)
		return
	}
	at.MatchedStay = out.Stay
	at.MatchedField = matched.MatchedField

	// A replayed resolution stores the Stay, not the interface; a Stay belongs to exactly one interface, so
	// re-derive it, else every retry of a success fails to issue a context.
	iface := out.InterfaceID
	if iface == "" {
		if iface, err = p.interfaceForStay(ctx, out.Stay); err != nil {
			at.Result = signinattempt.ServiceUnavailable
			notVerified(w, at.Result, "replayed_resolution_interface_unresolvable")
			return
		}
	}

	// The revision is pinned server-side so the later grant cannot drift onto a newer configuration.
	at.PMSInterfaceID = iface
	rev, err := p.publishedRevision(ctx, iface)
	if err != nil {
		at.Result = signinattempt.RoutingOrInterfaceFailure
		notVerified(w, at.Result, "no_published_revision")
		return
	}
	// The offer set is what the eligibility engine says this Stay qualifies for, not the whole free catalogue.
	decisions, err := p.offersFor(ctx, out.Stay, iface, dev.GuestNetwork, dev.DeviceID, time.Now())
	if err != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "offers: "+err.Error())
		return
	}
	if len(decisions) == 0 {
		// A configuration or eligibility outcome, not an identity one; kept distinct from wrong surnames.
		at.Result = signinattempt.VerifiedNoEligiblePackage
		notVerified(w, at.Result, "verified_but_no_eligible_package")
		return
	}
	evidenceVersion := decisions[0].EvidenceVersion

	// The Context and its offer set are written together; without the set the grant's "was this offered?" check
	// would weaken to "is this generally grantable?".
	tx, err := p.srv.db.Begin(ctx)
	if err != nil {
		at.Result = signinattempt.ServiceUnavailable
		notVerified(w, at.Result, "begin: "+err.Error())
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// One live context per resolution: a retry returns the existing context instead of minting another.
	var id string
	var reused bool
	if err := tx.QueryRow(ctx, `
		SELECT context_id::text, reused FROM iam_v2.issue_or_return_pms_context(
			$1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid,$6::uuid,$7::uuid,$8::uuid,$9)`,
		p.srv.tenID, p.srv.siteID, iface, rev, out.Stay, dev.DeviceID, dev.GuestNetwork,
		at.RequestID, int(p.contextTTL.Seconds())).Scan(&id, &reused); err != nil {
		// The function refuses a Stay not eligible right now (checked out, no occupancy evidence, superseded revision).
		at.Result = signinattempt.StayNotEligible
		notVerified(w, at.Result, "context_issue: "+err.Error())
		return
	}
	// The controlled writer is idempotent per (context, package), so a retry re-states the same set.
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
	at.Result = signinattempt.Verified

	offers := make([]phase3Offer, 0, len(decisions))
	for _, d := range decisions {
		offers = append(offers, phase3Offer{
			PackageRevisionID: d.PackageRevisionID, Code: d.Code,
			DownKbps: d.DownKbps, UpKbps: d.UpKbps,
			PriceMinor: d.PriceMinor, Currency: d.Currency, CurrencyExponent: d.CurrencyExponent, Methods: d.Methods})
	}
	writeJSONScd(w, http.StatusOK, phase3Response{
		Outcome: outcomeVerified, AuthContextID: id, ExpiresIn: int(p.contextTTL.Seconds()), Offers: offers})
}

// validRequestID reports whether s is a canonical 8-4-4-4-12 hex UUID; anything else is refused before SQL runs.
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

// probeInterface evaluates one interface's mirrored Stay state and records what it saw with the verdict.
//
// Matching is exact against stored normalised identities, no fuzzy matching. More than one live Stay matching
// is AMBIGUOUS_LOCAL, never a pick. Eligibility is applied in Go (not in the WHERE clause) so a checked-out
// room and a nonexistent room stay distinguishable without changing any verdict.
//
// The full multi-word given name ("MARIA DEL CARMEN") must match as a whole; accepting single words would
// admit anyone who guessed a common first name for a room.
func (p *phase3Auth) probeInterface(ctx context.Context, seen *observations, ifaceID, room, last, first, res string) (pmsresolve.CandidateOutcome, string, error) {
	obs := probeObservation{InterfaceID: ifaceID}
	// Each candidate answers for its own mirror. A candidate whose feed cannot be trusted is STALE, never NO_MATCH: on a shared network "this PMS has no such guest"
	// is only worth something when this PMS was actually in a position to say so.
	var feedOK bool
	if err := p.srv.db.QueryRow(ctx, `SELECT iam_v2.p3_interface_can_authorise($1::uuid,$2::uuid,$3::uuid)`, p.srv.tenID, p.srv.siteID, ifaceID).Scan(&feedOK); err != nil {
		obs.Failed = true
		seen.add(obs)
		return pmsresolve.Unavailable, "", err
	}
	if !feedOK {
		obs.Failed = true
		seen.add(obs)
		return pmsresolve.Stale, "", nil
	}
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
		 -- ELIGIBLE STAYS FIRST. The window is bounded and a busy room accumulates departed stays; without an
		 -- order the window is whatever the scan returns first, and on PRE-LIVE that was 16 departed stays with
		 -- the in-house guest outside it (refused as STAY_NOT_ELIGIBLE). Departed stays still fill the rest of
		 -- the window, so "the room exists" keeps its meaning.
		 ORDER BY (s.status IN ('IN_HOUSE','POST_STAY_ACTIVE')) DESC, s.id
		 LIMIT 16`, p.srv.tenID, p.srv.siteID, ifaceID, room, last, first, res)
	if err != nil {
		// Unreadable is indeterminate, never "no such guest".
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
		// Fixed precedence keeps the recorded field deterministic when one value equals two accepted ones.
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
	// Only with exactly one eligible stay is it unambiguous whose values would have been accepted.
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

// routingRefusal returns why a network's room sign-in policy refuses before any interface is consulted, or "" when the policy and the interfaces it names agree.
// A network with no policy row keeps the one meaning it always had: exactly one mapped interface is that interface, anything else is not decided.
func routingRefusal(policy string, mapped int) string {
	switch policy {
	case "NONE":
		return "network_room_sign_in_policy_none"
	case "DEDICATED":
		if mapped != 1 {
			return "network_policy_dedicated_mapping_mismatch"
		}
	case "SHARED_STRICT":
		if mapped < 2 || mapped > pmsresolve.MaxSharedCandidates {
			return "network_policy_shared_mapping_mismatch"
		}
	case "":
		switch {
		case mapped == 0:
			return "network_has_no_pms_mapping"
		case mapped > 1:
			return "network_has_undecided_multi_pms_mapping"
		}
	default:
		return "network_policy_unknown"
	}
	return ""
}

// classifyRefusal maps the resolver verdict plus probe observations to the operator-facing reason. The test
// order matters: "nothing matched" covers absent room, ineligible stay and mismatch, and a wrong order gives a confident wrong diagnosis.
func classifyRefusal(out pmsresolve.Result, roomExists bool, eligible int, anyProbeFailed bool) signinattempt.Result {
	if out.Reason == pmsresolve.ReasonSpentOnRefusal {
		return signinattempt.SpentRequestID
	}
	switch out.Resolution {
	case pmsresolve.ResAmbiguous:
		return signinattempt.AmbiguousRoomCandidates
	case pmsresolve.ResIndeterminate:
		if out.Reason == "UNMAPPED" || out.Reason == "CANDIDATE_CAP_EXCEEDED" || out.Reason == "CANDIDATE_NOT_ACTIVE" {
			// Not anything the guest typed.
			return signinattempt.RoutingOrInterfaceFailure
		}
		return signinattempt.ServiceUnavailable
	case pmsresolve.ResNoMatch:
		switch {
		case anyProbeFailed:
			// An unanswered probe is never "your details are wrong".
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

// submittedVerifier returns the one value the guest typed, unnormalized so a trailing space stays visible to operators.
func submittedVerifier(req phase3ResolveReq) string {
	for _, v := range []string{req.Verification, req.LastName, req.FirstName, req.ReservationNumber} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// normalizedVerifier returns the value the comparison used; the name fields normalise identically.
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

// publishedRevision returns the interface's published revision via the publication pointer, never
// max(revision_no): a higher revision is often an unapproved draft. Fails closed on no published revision, a
// pointer outside this tenant/site/interface, or a non-ACTIVE interface.
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

type phase3GrantReq struct {
	AuthContextID string     `json:"auth_context_id"`
	PackageRevID  string     `json:"package_revision_id"`
	Device        wireDevice `json:"device"`
	// Method is the acquisition method chosen for this offer: "" / NOT_REQUIRED for an included package,
	// PMS_POSTING (room charge) or ONLINE_PAYMENT (card) for a priced one (phase3_paid.go).
	Method string `json:"method,omitempty"`
	// ReturnBase is the portal origin a card provider sends the client back to.
	ReturnBase string `json:"return_base,omitempty"`
}

type phase3GrantResp struct {
	Outcome       string `json:"outcome"`
	SessionID     string `json:"session_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
}

// grantHandler turns a verified Auth Context into durable access; context consumption, Quote, Purchase,
// Entitlement, device authorization and Session commit together or not at all. The Session is created from
// the Entitlement id the grant returned, inside the same transaction, so access never exists without authority.
func (p *phase3Auth) grantHandler(w http.ResponseWriter, r *http.Request) {
	var req phase3GrantReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		notVerified(w, signinattempt.MalformedSubmission, "malformed_request")
		return
	}
	ctx := r.Context()

	// The grant completes the resolve's attempt row via the same request id; a second row would double-count sign-ins.
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

	// The licence is re-checked: it can change between resolve and grant.
	if code, refused := p.pmsLicenseRefusal(); refused {
		grantResult = signinattempt.LicenseRefused
		notVerifiedLicense(w, grantResult, code, "license_refused_at_grant")
		return
	}

	// Already-granted case: the reply can be lost after commit, and a retry would find the context consumed and
	// refuse a guest who already has access. So a retry from the same device returns the session its earlier
	// consumption produced; the device identity is derived server-side, so another device knowing the id gets nothing.
	if sid, ent, ok := p.alreadyGranted(ctx, strings.TrimSpace(req.AuthContextID), dev); ok {
		// Success depends on the kernel, so wait as the fresh path does.
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

	// "Was this package offered to this Stay?" is the authorisation question; "is it grantable?" is not enough,
	// or a guest could name any free package whose eligibility rules they fail. Runs in the grant transaction
	// with the offer row locked FOR UPDATE so a concurrent consumption cannot slip in.
	var offeredTier *int
	var offerEvidence int64
	// Locks via iam_v2.lock_auth_context_offer, not an inline FOR UPDATE: a row lock needs UPDATE privilege,
	// which would let scd rewrite the tier, evidence version and expiry validated below.
	err = tx.QueryRow(ctx,
		`SELECT matched_tier_order, evidence_version
		   FROM iam_v2.lock_auth_context_offer($1,$2,$3::uuid,$4::uuid)`,
		p.srv.tenID, p.srv.siteID, strings.TrimSpace(req.AuthContextID),
		strings.TrimSpace(req.PackageRevID)).Scan(&offeredTier, &offerEvidence)
	if err != nil {
		// Only "no such offer" is an authorisation answer; permission errors, dead connections and timeouts are
		// internal failures and must not be reported as "not offered".
		if errors.Is(err, pgx.ErrNoRows) {
			// A stay-eligibility fact, not an identity one.
			grantResult = signinattempt.VerifiedNoEligiblePackage
			notVerified(w, grantResult, "package_not_offered_to_this_context")
			return
		}
		slog.Error("phase3 grant: offer lock failed", "err", err,
			"context", strings.TrimSpace(req.AuthContextID))
		notVerified(w, signinattempt.ServiceUnavailable, "offer_lock_unavailable")
		return
	}
	// The evidence must still be what the offer was decided under; a moved, re-rated or checked-out Stay is a different subject.
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
		grantResult = signinattempt.StayNotEligible
		notVerified(w, grantResult, "stay_evidence_changed_since_the_offer")
		return
	}

	// A paid choice creates a quote, purchase and required settlement; no access until the PMS or card provider confirms payment.
	if m := strings.TrimSpace(req.Method); m != "" && m != "NOT_REQUIRED" {
		req.Method = m
		res, ok := p.paidPurchase(w, r, tx, req, dev, offeredTier)
		grantResult = res
		if ok {
			grantResult = signinattempt.Verified
		}
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
		// Every grant failure is uniform to the guest; the operator record keeps that an already-entitled stay is an eligibility fact.
		if errors.Is(err, staygrant.ErrAlreadyEntitled) {
			grantResult = signinattempt.StayNotEligible
		}
		// A full device limit is neither a wrong room nor a fault; it gets its own sentence so the guest can disconnect another device.
		if strings.Contains(err.Error(), "MAX_DEVICES_REACHED") {
			slog.Info("phase3 auth: device limit reached for the stay's entitlement", "detail", err.Error())
			writeJSONScd(w, http.StatusOK, phase3Response{Outcome: outcomeNotVerified, FailureClass: "DEVICE_LIMIT",
				DeviceID: dev.DeviceID, GuestNetworkID: dev.GuestNetwork})
			return
		}
		notVerified(w, grantResult, "grant: "+err.Error())
		return
	}

	// A device that signs in again while online keeps its open session, but only at the same address on the same bridge: the firewall authorizes an address, so
	// a session left at the old address would be reported as verified while the new one is not admitted. At a new address the old session is ended through the
	// controlled writer (as activation does) and a fresh one is opened on the same entitlement.
	var sessionID string
	if granted.Joined {
		live, lerr := p.srv.liveSessionsOfDevice(ctx, tx, granted.EntitlementID, dev.DeviceID)
		if lerr != nil {
			notVerified(w, signinattempt.ServiceUnavailable, "sessions: "+lerr.Error())
			return
		}
		bridge := p.bridgeFor(ctx, dev.IP)
		for _, ls := range live {
			if sessionID == "" && ls.ip == dev.IP.String() && ls.iface == bridge {
				sessionID = ls.id
				continue
			}
			if _, eerr := p.srv.endSessionControlled(ctx, tx, ls.id, endReasonAddressChanged); eerr != nil {
				notVerified(w, signinattempt.ServiceUnavailable, "end old session: "+eerr.Error())
				return
			}
		}
	}
	if sessionID == "" {
		// Every new session takes a licensed concurrent-guest slot, the same reservation vouchers and accounts
		// use, so they contend for one capacity. A refusal rolls the whole grant back (context stays unconsumed).
		if err := reserveLicensedSlot(ctx, tx, p.srv.applID, p.srv.lic.MaxConcurrentOnlineGuests()); err != nil {
			var capErr *licenseCapacityError
			if errors.As(err, &capErr) {
				grantResult = signinattempt.LicenseCapacityReached
				notVerifiedLicense(w, grantResult, "LICENSE_CAPACITY_REACHED",
					fmt.Sprintf("licensed capacity %d reached (%d online)", capErr.Limit, capErr.Current))
				return
			}
			notVerified(w, signinattempt.ServiceUnavailable, "capacity: "+err.Error())
			return
		}
		sessionID, err = p.openSessionTx(ctx, tx, granted.EntitlementID, dev)
	}
	if err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "session: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		notVerified(w, signinattempt.ServiceUnavailable, "commit: "+err.Error())
		return
	}
	// Nothing is enforced in the kernel yet; wait for the enforcement owner before claiming the guest is connected.
	enforced, werr := p.awaitEnforced(ctx, sessionID)
	if werr != nil || !enforced {
		// The grant is recoverable: a retry from the same device returns this Session once active.
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

// alreadyGranted answers whether this device already turned this Auth Context into a live session. Every clause is load-bearing:
//
//	consumed_at IS NOT NULL — an unconsumed context must take the real path;
//	c.device_id = dev       — issued to this device, so a stolen context id is not enough;
//	s.device_id = dev       — the session is this device's, not merely the Stay's;
//	s.state IN (active, PENDING_ENFORCEMENT) — an ended session is not resurrected; PENDING recovers a retry
//	                          arriving while enforcement converges.
//
// It reads outside the grant transaction: a lock-free fast path, so retries do not serialise behind the grants they duplicate.
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
		   AND host(s.ip) = $5 AND COALESCE(s.ingress_interface, '') = $6
		   AND s.state IN ('active','PENDING_ENFORCEMENT') AND s.ended IS NULL
		 ORDER BY s.started DESC
		 LIMIT 1`,
		authContextID, p.srv.tenID, p.srv.siteID, dev.DeviceID, dev.IP.String(), p.bridgeFor(ctx, dev.IP)).Scan(&sid, &ent)
	if err != nil {
		return "", "", false
	}
	return sid, ent, true
}

// openSessionTx creates the session against an Entitlement from this transaction. It opens it as
// PENDING_ENFORCEMENT, never 'active': nothing is enforced yet, and netd promotes it through
// iam_v2.activate_session_enforcement once the tc class and nft authorization are proven.
func (p *phase3Auth) openSessionTx(ctx context.Context, tx pgx.Tx, entitlement string, dev deviceIdentity) (string, error) {
	return p.openSessionWithMethodTx(ctx, tx, entitlement, dev, "PMS")
}

// openSessionWithMethodTx is openSessionTx with the caller's credential label (e.g. POST_STAY).
func (p *phase3Auth) openSessionWithMethodTx(ctx context.Context, tx pgx.Tx, entitlement string, dev deviceIdentity, method string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO iam_v2.sessions
		  (tenant_id, site_id, entitlement_id, device_id, credential_method, state, started, ip, mac, ingress_interface)
		VALUES ($1,$2,$3,$4,$8,'PENDING_ENFORCEMENT',now(),$5::inet,$6::macaddr,$7)
		RETURNING id::text`,
		p.srv.tenID, p.srv.siteID, entitlement, dev.DeviceID,
		dev.IP.String(), dev.MAC.String(), p.bridgeFor(ctx, dev.IP), method).Scan(&id)
	if err != nil {
		return id, err
	}
	// Tell the plan producer (acctd) at commit, as the voucher and account path does (iamv2_session_activate.go). Without it a
	// Room sign-in waited for acctd's next period, and on the real CHR appliance enforcement was confirmed 2.4 s after the
	// commit: after portald's 2.5 s budget, so a guest who was in fact online was told the stay could not be verified
	// (2026-11-01). Delivered only if this transaction commits; the periodic submission stays the fallback.
	_, err = tx.Exec(ctx, `SELECT pg_notify('onegate_session_pending', $1)`, id)
	return id, err
}

// The enforcement wait must exceed one reconciliation cycle yet fit the caller's budget. portald passes its
// response-time budget as a context deadline, so the wait is derived from it minus a reserve (scd answers
// before portald stops listening); enforcementDeadlineMax applies only when there is no deadline.
const (
	enforcementDeadlineMax = 8 * time.Second
	enforcementReserve     = 150 * time.Millisecond
	// enforcementPoll stays short relative to the producer's one-second cadence.
	enforcementPoll = 100 * time.Millisecond
	// enforcementPollWithNotify is the safety-net poll while the activation notification is connected (AD-52).
	enforcementPollWithNotify = time.Second
)

// enforcementDeadlineFor derives the wait from the caller's budget.
func enforcementDeadlineFor(ctx context.Context, now time.Time) time.Time {
	deadline := now.Add(enforcementDeadlineMax)
	if d, ok := ctx.Deadline(); ok {
		if reserved := d.Add(-enforcementReserve); reserved.Before(deadline) {
			deadline = reserved
		}
	}
	return deadline
}

// awaitEnforced blocks until the Session is network-active or the deadline passes, so guest-visible success
// is truthful. It polls the Session row (the enforcement owner's confirmed result) rather than asking netd,
// needing no privileged channel.
func (p *phase3Auth) awaitEnforced(ctx context.Context, sessionID string) (bool, error) {
	deadline := enforcementDeadlineFor(ctx, time.Now())
	// AD-52: wake when the database says the session became active; the poll is the fallback and the only mechanism while the
	// notification connection is down. Registered BEFORE the first read, so an activation between the read and the wait is not missed.
	var wake <-chan struct{}
	poll := enforcementPoll
	if w := p.srv.waiters; w != nil {
		ch, release := w.wait(sessionID)
		defer release()
		wake = ch
		if w.up.Load() {
			poll = enforcementPollWithNotify
		}
	}
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
		case <-wake:
		case <-time.After(poll):
		}
	}
}

func (p *phase3Auth) bridgeFor(ctx context.Context, ip net.IP) string {
	return p.srv.resolveNetwork(ctx, ip).Bridge
}

// Both sides of a comparison must normalise identically; the single implementation is internal/namenorm.

func normalizeRoom(s string) string { return namenorm.Room(s) }

func normalizeName(s string) string { return namenorm.Name(s) }
