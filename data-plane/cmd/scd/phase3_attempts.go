package main

// What scd records about a guest sign-in, and how it classifies what happened. Classification keeps the distinctions an
// operator needs (room not in the mirror, no stay, stay ineligible, several candidates, feed unable to answer) that the narrow
// resolver verdict discards. Recording leaves one row per deliberate submission, best-effort: a guest's access must never
// depend on diagnostics about it.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

const (
	signInAttemptDEKFile = "signin_attempts_dek.key"
	// encryption_key_id is a uuid column, so the DEK id is a fixed UUID. Stable by contract: changing it makes every row sealed under the old id unreadable, with nothing to reissue.
	signInAttemptDEKID = "0a5c1e00-0000-4000-8000-0000000000d2"
)

// loadSignInAttemptKeyring does not fail closed when the key is absent, unlike the voucher DEK: a missing key only means the
// credential half of a diagnostic record cannot be sealed, and refusing to start would take a property's guest internet
// offline over an audit key file. scd runs, records attempts with the sensitive columns NULL, and says so once, loudly.
func loadSignInAttemptKeyring(secretsDir string) signinattempt.Keyring {
	path := filepath.Join(secretsDir, signInAttemptDEKFile)
	key, err := localkeys.LoadExistingKey(path)
	if err != nil {
		slog.Warn("guest sign-in attempts will be recorded WITHOUT the submitted and accepted values: "+
			"the sealing key is unavailable (run keybootstrap at deploy)", "path", path, "err", err)
		return nil
	}
	return signinattempt.MapKeyring{signInAttemptDEKID: key}
}

// probeObservation is everything one interface learned about the submitted room beyond the resolver's verdict, collected alongside it so the decision core keeps its narrow interface.
type probeObservation struct {
	InterfaceID string
	// RoomExists is true when any stay carries this room number on this interface, whatever its status: it separates a wrong room number from a name spelled differently from the PMS.
	RoomExists    bool
	EligibleStays int
	Matches       int
	MatchedStay   string
	MatchedGuest  string
	MatchedField  signinattempt.MatchedField
	// CandidateStay is the stay to show accepted values for when nothing matched: with exactly one eligible stay on the room there is no ambiguity.
	CandidateStay string
	// Failed records a probe that could not answer: an unreadable interface is indeterminate, never a determinate "no such guest".
	Failed bool
}

// observations collects what each interface saw; the resolver probes concurrently, so it is mutex-guarded (a race would corrupt the operator's answer).
type observations struct {
	mu   sync.Mutex
	seen []probeObservation
}

func (o *observations) add(p probeObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, p)
}

func (o *observations) summarise() (roomExists bool, eligible int, matched probeObservation, anyFailed bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, p := range o.seen {
		roomExists = roomExists || p.RoomExists
		eligible += p.EligibleStays
		anyFailed = anyFailed || p.Failed
		if p.Matches == 1 && matched.MatchedStay == "" {
			matched = p
		}
	}
	if matched.MatchedStay == "" {
		// No match: fall back to the single unambiguous candidate so the panel shows what would have been accepted; with several candidates show none, since picking one invents an expectation.
		var candidates []probeObservation
		for _, p := range o.seen {
			if p.CandidateStay != "" {
				candidates = append(candidates, p)
			}
		}
		if len(candidates) == 1 {
			matched = candidates[0]
		}
	}
	return
}

// mirrorState is the feed's condition at the instant of the attempt, captured then rather than reconstructed from a runtime row that has moved on.
type mirrorState struct {
	TransportStatus    string
	LastCompleteSync   *time.Time
	AgeSeconds         *int64
	CanAuthoriseAnyone bool
}

// mirrorStateFor answers the site-wide question: can the local mirror authorise anybody right now?
// The ordering is the security property: it is asked before any room is looked up and used to refuse with
// MIRROR_STALE_OR_MISSING_CHANGE before evidence is evaluated, otherwise a per-room signal wearing a site-wide label would
// let anyone in the lobby enumerate occupied rooms.
// p3_feed_authorizes is asked with now() as the evidence timestamp, which makes its freshness term trivially true, so it is
// strictly more permissive than the real per-stay check: if it says no, no stay could have passed.
// It goes through a scoped reader because svc_scd deliberately holds no privilege on pms_interface_runtime (the role being
// authorised must not read the feed health it is authorised against); querying it directly answered NO and refused every guest.
func (p *phase3Auth) mirrorStateFor(ctx context.Context, guestNetwork string) mirrorState {
	var st mirrorState
	row := p.srv.db.QueryRow(ctx, `
		SELECT transport_status, last_complete_sync_at, can_authorise
		  FROM iam_v2.p3_guest_network_mirror_state($1::uuid,$2::uuid,$3::uuid)`,
		p.srv.tenID, p.srv.siteID, guestNetwork)
	var can *bool
	if err := row.Scan(&st.TransportStatus, &st.LastCompleteSync, &can); err != nil {
		// Unreadable feed state is not evidence that the feed is healthy. The caller treats a false
		// CanAuthoriseAnyone with no recorded reason as a service condition, not as the guest's fault.
		slog.Warn("phase3 auth: could not read PMS mirror state for the attempt record", "err", err)
		return st
	}
	if can != nil {
		st.CanAuthoriseAnyone = *can
	}
	if st.LastCompleteSync != nil {
		age := int64(time.Since(*st.LastCompleteSync).Seconds())
		st.AgeSeconds = &age
	}
	return st
}

// acceptedValues reads the identity a stay carries: the primary guest's normalized first and family names and the
// reservation identifier. The normalized values are the accepted ones (the comparison is made against them; the PMS's
// mixed-case spelling would tell a guest to type something that fails). It also counts other guests whose names would have
// been accepted, so an operator does not turn away a legitimate sharer.
func (p *phase3Auth) acceptedValues(ctx context.Context, stayID string) (first, family, reservation, guestID string, others int) {
	if stayID == "" {
		return
	}
	err := p.srv.db.QueryRow(ctx, `
		SELECT COALESCE(g.first_name_norm,''), COALESCE(g.last_name_norm,''),
		       COALESCE(s.external_reservation_id,''), COALESCE(g.id::text,''),
		       GREATEST(0, (SELECT count(*) FROM iam_v2.stay_guests o WHERE o.stay_id = s.id) - 1)
		  FROM iam_v2.stays s
		  LEFT JOIN LATERAL (
		      SELECT gg.id, gg.first_name_norm, gg.last_name_norm
		        FROM iam_v2.stay_guests gg
		       WHERE gg.stay_id = s.id
		       ORDER BY gg.is_primary DESC NULLS LAST, gg.id
		       LIMIT 1
		  ) g ON TRUE
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.id=$3`,
		p.srv.tenID, p.srv.siteID, stayID).Scan(&first, &family, &reservation, &guestID, &others)
	if err != nil {
		slog.Warn("phase3 auth: could not read the accepted values for the attempt record", "err", err)
	}
	return
}

// recordAttempt runs on its own context: the request context is cancelled when the guest's browser gives up or portald's
// budget expires, and an abandoned attempt is the one an operator most wants to see. Every failure is logged and swallowed;
// the guest has already been answered.
func (p *phase3Auth) recordAttempt(at signinattempt.Attempt, started time.Time) {
	if p.attempts == nil {
		return
	}
	at.LatencyMS = time.Since(started).Milliseconds()
	if at.Result == "" {
		// A path that returned without classifying itself is a defect here; record it as an internal fault rather than attributing it to the guest.
		at.Result = signinattempt.ServiceUnavailable
		slog.Warn("phase3 auth: an attempt ended with no recorded result", "request", at.RequestID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := p.attempts.Record(ctx, at); err != nil {
		slog.Warn("phase3 auth: the sign-in attempt could not be recorded", "err", err, "result", at.Result)
		// The policy counts attempt rows, so an unrecorded attempt did not happen as far as the threshold is concerned.
		return
	}
	p.applyProtection(ctx, at)
}

// applyProtection runs after the attempt row exists: the rolling count is derived from the rows, so reporting a failure
// earlier would undercount. A success clears the counter rather than adding to it.
func (p *phase3Auth) applyProtection(ctx context.Context, at signinattempt.Attempt) {
	if p.protection == nil || at.DeviceMAC == "" {
		return
	}
	mac, err := net.ParseMAC(at.DeviceMAC)
	if err != nil {
		return
	}
	switch {
	case at.Result.Succeeded():
		if err := p.protection.NoteSuccess(ctx, p.srv.tenID, p.srv.siteID, mac); err != nil {
			slog.Warn("phase3 auth: a successful sign-in did not clear its device counter", "err", err)
		}
	case at.Result.CountsAsCredentialFailure():
		restricted, expires, count, err := p.protection.NoteFailure(ctx, p.srv.tenID, p.srv.siteID, mac,
			at.GuestNetworkID, at.GuestNetworkName, at.SubmittedRoom)
		if err != nil {
			slog.Warn("phase3 auth: a wrong credential was not counted", "err", err)
			return
		}
		if restricted {
			// INFO so an operator can find when the property started refusing a device, without the room or anything typed.
			slog.Info("phase3 auth: guest sign-in restricted for a device after repeated wrong credentials",
				"failures", count, "expires_at", expires, "guest_network", at.GuestNetworkName)
		}
		paused, until, n, err := p.protection.NoteNetworkFailure(ctx, p.srv.tenID, p.srv.siteID,
			at.GuestNetworkID, at.GuestNetworkName)
		if err != nil {
			slog.Warn("phase3 auth: a wrong room sign-in was not counted for its network", "err", err)
			return
		}
		if paused {
			// WARN: many wrong room sign-ins from many devices looks like guessing.
			slog.Warn("phase3 auth: room sign-in paused on a guest network after many wrong room sign-ins",
				"failures", n, "until", until, "guest_network", at.GuestNetworkName)
		}
	}
}

// completeAttempt stamps what the grant produced onto the attempt the resolve wrote, keyed by the shared request id. Best-effort like recordAttempt.
func (p *phase3Auth) completeAttempt(requestID string, result signinattempt.Result, entitlementID, sessionID string) {
	if p.attempts == nil || requestID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := p.attempts.Complete(ctx, p.srv.tenID, p.srv.siteID, requestID, result, entitlementID, sessionID); err != nil {
		slog.Warn("phase3 auth: the sign-in attempt could not be completed", "err", err, "result", result)
	}
}

// requestIDForContext finds the resolution request id an Auth Context was issued under, so the grant completes the resolve's attempt rather than starting a second record.
func (p *phase3Auth) requestIDForContext(ctx context.Context, authContextID string) string {
	if authContextID == "" {
		return ""
	}
	var id string
	if err := p.srv.db.QueryRow(ctx, `
		SELECT COALESCE(resolution_request_id::text,'') FROM iam_v2.auth_contexts
		 WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`,
		p.srv.tenID, p.srv.siteID, authContextID).Scan(&id); err != nil {
		return ""
	}
	return id
}

// signInAttemptCredentialsHandler returns the sealed half of one attempt in clear. It lives in scd because scd is root behind
// a unix socket and the only holder of the sealing key; putting the key in edged would let the process answering HTTP decrypt
// every guest credential. Which operator may ask is settled by edged against the site role matrix; restating it here would
// be a second copy of a boundary that must have one.
func (p *phase3Auth) signInAttemptCredentialsHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttemptID string `json:"attempt_id"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || strings.TrimSpace(req.AttemptID) == "" {
		writeJSONScd(w, http.StatusBadRequest, map[string]any{"error": "attempt_id is required"})
		return
	}
	sensitive, ok, err := p.attempts.OpenSensitive(r.Context(), p.srv.tenID, p.srv.siteID,
		strings.TrimSpace(req.AttemptID))
	if err != nil {
		slog.Error("phase3 auth: a sealed sign-in attempt could not be opened", "err", err)
		writeJSONScd(w, http.StatusInternalServerError, map[string]any{"error": "unavailable"})
		return
	}
	// No row and a row with no sealed half answer the same on purpose: distinguishing them would let a caller probe for attempts it may not see.
	if !ok {
		writeJSONScd(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	writeJSONScd(w, http.StatusOK, map[string]any{
		"available":                   true,
		"submitted_verifier":          sensitive.SubmittedVerifier,
		"normalized_verifier":         sensitive.NormalizedVerifier,
		"accepted_first_name":         sensitive.AcceptedFirstName,
		"accepted_family_name":        sensitive.AcceptedFamilyName,
		"accepted_reservation_number": sensitive.AcceptedReservationNumber,
		"additional_accepted_guests":  sensitive.AdditionalAcceptedGuests,
	})
}
