package main

// WHAT SCD RECORDS ABOUT A GUEST SIGN-IN, AND HOW IT DECIDES WHAT HAPPENED.
//
// Two jobs live here, and they are the same job seen from two sides.
//
// The first is CLASSIFICATION. The resolver answers a narrow question — does this evidence identify exactly
// one eligible Stay — and its vocabulary is correspondingly narrow. An operator at the desk needs a wider
// answer: was the room even in the mirror, was there a stay on it, was the stay eligible, did more than one
// candidate match, or was the property's feed unable to answer for anybody. Those distinctions are all
// visible at probe time and were simply being discarded. This file keeps them.
//
// The second is RECORDING. Every deliberate submission leaves one row, including the ones that used to leave
// nothing at all because they failed before any resolver was reached. The write is best-effort by
// construction: a guest's access must never depend on the diagnostics about it.

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

const (
	signInAttemptDEKFile = "signin_attempts_dek.key"
	// encryption_key_id is a uuid column, so the DEK id is a fixed UUID rather than a readable label. Stable
	// by contract: changing it makes every row sealed under the old id unreadable, and unlike a voucher there
	// is nothing to reissue — the record simply stops answering the question it exists for.
	signInAttemptDEKID = "0a5c1e00-0000-4000-8000-0000000000d2"
)

// loadSignInAttemptKeyring loads the sealing key, and DOES NOT fail closed when it is absent.
//
// That is the opposite of the voucher DEK's behaviour and the difference is deliberate. A missing voucher key
// means scd cannot issue a credential, so refusing to start is the only safe answer. A missing key here means
// only that the credential half of a DIAGNOSTIC record cannot be sealed. Refusing to start would take a
// property's guest internet offline because an audit feature's key file was not created — trading a working
// product for a complete log. So scd runs, records every attempt with the sensitive columns NULL, and says so
// once, loudly, where an operator will see it.
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

// ---- what one interface saw -------------------------------------------------

// probeObservation is everything ONE interface learned about the submitted room, beyond the verdict the
// resolver needs. The resolver's Probe contract deliberately stays as it is — a verdict and a Stay id — and
// these are collected alongside it, so the decision core keeps its narrow, testable interface while the
// operator still gets an answer.
type probeObservation struct {
	InterfaceID string
	// RoomExists is true when ANY stay carries this room number on this interface, whatever its status. It is
	// what separates "you have the wrong room number" from "your name is spelled differently from the PMS",
	// which are different conversations and used to be the same refusal.
	RoomExists bool
	// EligibleStays counts the stays on that room that are in a state that may authenticate.
	EligibleStays int
	// Matches counts the eligible stays the submitted value actually matched.
	Matches int
	// MatchedStay / MatchedGuest / MatchedField describe the single match, when there was exactly one.
	MatchedStay  string
	MatchedGuest string
	MatchedField signinattempt.MatchedField
	// CandidateStay is the stay to show accepted values for when the evidence matched nothing: with exactly
	// one eligible stay on the room there is no ambiguity about which values WOULD have been accepted.
	CandidateStay string
	// Failed records a probe that could not answer. An interface whose state cannot be read is indeterminate,
	// never a determinate "no such guest", and the recorded result must say so.
	Failed bool
}

// observations collects what each interface saw. The resolver probes concurrently, so this is mutex-guarded
// rather than a plain map — a data race here would corrupt the operator's answer, not merely lose it.
type observations struct {
	mu   sync.Mutex
	seen []probeObservation
}

func (o *observations) add(p probeObservation) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seen = append(o.seen, p)
}

// summarise folds the per-interface observations into the fields the attempt record carries.
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
		// No match: fall back to the single unambiguous candidate, so the panel can still show what WOULD
		// have been accepted. With more than one candidate it deliberately shows none — picking one would be
		// inventing an expectation the guest never had.
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

// ---- the mirror's own state -------------------------------------------------

// mirrorState is the feed's condition at the instant of the attempt, recorded so that "the mirror was four
// days old" is a fact captured at the time rather than reconstructed later from a runtime row that has since
// moved on.
type mirrorState struct {
	TransportStatus    string
	LastCompleteSync   *time.Time
	AgeSeconds         *int64
	CanAuthoriseAnyone bool
}

// mirrorStateFor reads the feed condition for the interfaces mapped to this guest network, and answers the
// SITE-WIDE question: can the local mirror authorise ANYBODY right now?
//
// THE ORDERING IS THE SECURITY PROPERTY. This is asked BEFORE any room is looked up, and the answer is used
// to refuse with MIRROR_STALE_OR_MISSING_CHANGE before the evidence is evaluated at all. A "the mirror cannot
// answer" response produced only when a room was NOT found would be a per-room signal wearing a site-wide
// label, and would let anyone in the lobby enumerate occupied rooms by watching which ones answered
// differently.
//
// p3_feed_authorizes is asked with now() as the evidence timestamp on purpose: that makes its freshness term
// trivially true, so what remains is exactly the feed-health half — connected-and-in-sync, or a published
// roster that still stands. It is therefore strictly more permissive than the real per-stay check, which is
// what makes it safe to refuse on: if it says no, no stay could have passed either.
func (p *phase3Auth) mirrorStateFor(ctx context.Context, guestNetwork string) mirrorState {
	var st mirrorState
	row := p.srv.db.QueryRow(ctx, `
		SELECT COALESCE(max(rt.transport_status), ''),
		       max(rt.last_complete_sync_at),
		       bool_or(iam_v2.p3_feed_authorizes(pi.tenant_id, pi.site_id, pi.id, pi.current_revision_id, now()))
		  FROM iam_v2.guest_network_pms_map m
		  JOIN iam_v2.pms_interfaces pi
		    ON pi.tenant_id=m.tenant_id AND pi.site_id=m.site_id AND pi.id=m.pms_interface_id
		   AND pi.lifecycle_state='ACTIVE'
		  LEFT JOIN iam_v2.pms_interface_runtime rt
		    ON rt.tenant_id=pi.tenant_id AND rt.site_id=pi.site_id AND rt.pms_interface_id=pi.id
		 WHERE m.tenant_id=$1 AND m.site_id=$2 AND m.guest_network_id=$3`,
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

// ---- the values that would have been accepted -------------------------------

// acceptedValues reads the identity a stay actually carries: the primary guest's normalized first and family
// names and the stay's reservation identifier.
//
// THE NORMALIZED VALUES ARE THE ACCEPTED ONES. They are what the comparison is made against, so they are what
// belongs in a comparison panel; showing the PMS's own mixed-case spelling instead would put a value on the
// screen that is NOT what the server requires, which is how an operator ends up telling a guest to type
// something that will not work.
//
// It also counts the other guests on the stay whose names would have been accepted too. Without that count an
// operator reads the panel as "only this one name works" and turns away a legitimate sharer.
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

// ---- writing the record -----------------------------------------------------

// recordAttempt writes the attempt the handler assembled.
//
// IT RUNS ON ITS OWN CONTEXT, not the request's. The request context is cancelled the moment the guest's
// browser gives up or portald's response-time budget expires — and an abandoned attempt is precisely the one
// an operator most wants to see afterwards. Writing on the request context would drop exactly those.
//
// Every failure here is logged and swallowed. The guest has already been answered by the time this runs, and
// nothing about their access depends on it.
func (p *phase3Auth) recordAttempt(at signinattempt.Attempt, started time.Time) {
	if p.attempts == nil {
		return
	}
	at.LatencyMS = time.Since(started).Milliseconds()
	if at.Result == "" {
		// A path that returned without classifying itself is a defect in this file, not a guest problem. It is
		// recorded as an internal fault rather than silently dropped or, worse, attributed to the guest.
		at.Result = signinattempt.ServiceUnavailable
		slog.Warn("phase3 auth: an attempt ended with no recorded result", "request", at.RequestID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := p.attempts.Record(ctx, at); err != nil {
		slog.Warn("phase3 auth: the sign-in attempt could not be recorded", "err", err, "result", at.Result)
	}
}

// completeAttempt stamps what the grant produced onto the attempt the resolve already wrote, keyed by the
// request id the two calls share. Best-effort for the same reason as recordAttempt.
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

// requestIDForContext finds the resolution request id an Auth Context was issued under, so the grant can
// complete the attempt the resolve wrote instead of starting a second record for one guest's one submission.
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
