//go:build integration

package stayengine

// OCCUPANCY EVIDENCE IS WHAT MAKES PMS ROOM SIGN-IN POSSIBLE AT ALL.
//
// A guest can type the right room and the right surname, the resolver can match exactly one Stay and record
// VERIFIED, and the sign-in still fails — because minting a PMS Auth Context additionally requires the Stay to
// carry recent, revision-matched occupancy evidence. internal/authctx and iam_v2.issue_or_return_pms_context
// both enforce that; nothing wrote it. Every mirrored Stay had a NULL occupancy_evidence_at, so every verified
// guest was refused with CONTEXT_INVALID behind the uniform failure message.
//
// These tests pin the four things that made that possible, against the real processor and the real triggers:
// that a confirming event stamps evidence at all, that the values come from the event rather than the server
// clock, that a checkout does NOT refresh occupancy, and that the stamped Stay actually satisfies the SQL
// eligibility predicate the authentication path applies.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
)

// seedPinnedRevision publishes a revision and pins the runtime to it, which is the state of any interface that
// has ever connected. The unpinned seed used by the lifecycle tests is a pre-commissioning state.
func seedPinnedRevision(t *testing.T, p *pgxpool.Pool, s scope) string {
	t.Helper()
	var rev string
	if err := p.QueryRow(context.Background(), `WITH r AS (
		  INSERT INTO iam_v2.pms_interface_revisions
		    (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, config)
		  VALUES ($1,$2,$3,1,'Africa/Cairo','{}'::jsonb) RETURNING id)
		SELECT id::text FROM r`, s.tenant, s.site, s.iface).Scan(&rev); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interfaces
		SET current_revision_id=$2 WHERE id=$1`, s.iface, rev); err != nil {
		t.Fatalf("publish revision: %v", err)
	}
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET pinned_revision_id=$2 WHERE pms_interface_id=$1`, s.iface, rev); err != nil {
		t.Fatalf("pin revision: %v", err)
	}
	return rev
}

type evidence struct {
	At           *time.Time
	Version      int
	RevisionID   string
	ClockSuspect bool
}

func readEvidence(t *testing.T, p *pgxpool.Pool, s scope, res string) evidence {
	t.Helper()
	var e evidence
	if err := p.QueryRow(context.Background(), `SELECT occupancy_evidence_at, occupancy_evidence_version,
		       COALESCE(occupancy_revision_id::text,''), COALESCE(occupancy_clock_suspect,false)
		  FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id=$2`,
		s.iface, res).Scan(&e.At, &e.Version, &e.RevisionID, &e.ClockSuspect); err != nil {
		t.Fatalf("read evidence: %v", err)
	}
	return e
}

// insertLiveReceivedAt is insertLive with an explicit arrival time and clock-suspect flag, because the whole point of
// the evidence is that it reflects the record rather than the moment we happened to process it.
func insertLiveReceivedAt(t *testing.T, p *pgxpool.Pool, s scope, identity, eventType, payloadJSON string,
	receivedAt time.Time, clockSuspect bool) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `INSERT INTO iam_v2.stay_events
		(tenant_id, site_id, pms_interface_id, external_event_identity, event_type, payload,
		 admission_kind, admission_runtime_generation, resync_generation, received_at, clock_suspect)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,'LIVE',1,0,$7,$8)`,
		s.tenant, s.site, s.iface, identity, eventType, payloadJSON, receivedAt, clockSuspect); err != nil {
		t.Fatalf("insert event %s: %v", identity, err)
	}
}

// An arrival stamps evidence; a further confirmation refreshes it and advances the version; a checkout does
// not. The version matters because an Auth Context pins the value it was issued under, so a stale context is
// invalidated by the next confirmation rather than surviving on its own timestamp.
func TestIntegration_OccupancyEvidenceStampedOnConfirmationNotCheckout(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	rev := seedPinnedRevision(t, p, s)
	// Wired with the real Converter because this test drives a checkout: the engine refuses to flip a Stay
	// itself, so an unwired Processor cannot reach the assertion that matters here.
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	arrival := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	insertLiveReceivedAt(t, p, s, "ev-gi", "GI", pay("R-EV1", "14332", "Example", "Guest", "F1", "", ""), arrival, false)
	process(t, pr, s)

	e := readEvidence(t, p, s, "R-EV1")
	if e.At == nil {
		t.Fatal("an arrival left the Stay with no occupancy evidence — PMS sign-in cannot succeed for it")
	}
	if !e.At.UTC().Equal(arrival) {
		t.Fatalf("evidence must be stamped from the event, not the server clock: got %s want %s", e.At.UTC(), arrival)
	}
	if e.Version != 1 {
		t.Fatalf("first confirmation should set version 1, got %d", e.Version)
	}
	if e.RevisionID != rev {
		t.Fatalf("evidence must record the pinned revision %s, got %q", rev, e.RevisionID)
	}
	if e.ClockSuspect {
		t.Fatal("a trustworthy event must not produce clock-suspect evidence")
	}

	// A later confirmation for the same Stay refreshes the evidence and advances the version.
	later := arrival.Add(30 * time.Minute)
	insertLiveReceivedAt(t, p, s, "ev-gc", "GC", pay("R-EV1", "14332", "Example", "Guest", "F1", "", ""), later, false)
	process(t, pr, s)

	e2 := readEvidence(t, p, s, "R-EV1")
	if !e2.At.UTC().Equal(later) {
		t.Fatalf("a confirmation must refresh the evidence: got %s want %s", e2.At.UTC(), later)
	}
	if e2.Version != 2 {
		t.Fatalf("every confirmation must advance the version, got %d", e2.Version)
	}

	// A checkout must NOT refresh occupancy. Doing so would keep a departed guest authenticating.
	insertLiveReceivedAt(t, p, s, "ev-go", "GO", pay("R-EV1", "14332", "Example", "Guest", "F1", "", ""),
		later.Add(time.Hour), false)
	process(t, pr, s)

	e3 := readEvidence(t, p, s, "R-EV1")
	if e3.Version != e2.Version || !e3.At.UTC().Equal(later) {
		t.Fatalf("a checkout refreshed occupancy evidence (version %d→%d, at %s): a departed guest would keep "+
			"authenticating", e2.Version, e3.Version, e3.At.UTC())
	}
}

// p3_stay_lifecycle_guard refuses a version change without a material evidence change, so a repeat event
// carrying identical evidence must refresh ingested_at and leave the version alone. Bumping unconditionally
// does not merely record the wrong number — the trigger aborts the transaction and the event stops being
// processed at all.
func TestIntegration_IdenticalEvidenceDoesNotBumpTheVersion(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	seedPinnedRevision(t, p, s)
	pr := NewProcessor(p)

	at := time.Now().UTC().Truncate(time.Second)
	insertLiveReceivedAt(t, p, s, "ev-a", "GI", pay("R-EV4", "14335", "Example", "Guest", "F4", "", ""), at, false)
	process(t, pr, s)
	first := readEvidence(t, p, s, "R-EV4")

	// Same instant, same revision, same clock trust: nothing material has changed.
	insertLiveReceivedAt(t, p, s, "ev-b", "GC", pay("R-EV4", "14335", "Example", "Guest", "F4", "", ""), at, false)
	process(t, pr, s)

	if second := readEvidence(t, p, s, "R-EV4"); second.Version != first.Version {
		t.Fatalf("identical evidence changed the version (%d → %d); the lifecycle guard rejects that and the "+
			"event would fail to apply", first.Version, second.Version)
	}
	if st, _, _ := stayState(t, p, s, "R-EV4"); st != "IN_HOUSE" {
		t.Fatalf("the repeat event did not apply cleanly, Stay is %q", st)
	}
}

// A feed whose timestamp we do not trust must produce evidence that is MARKED suspect, not evidence that is
// silently accepted. Authentication declines clock-suspect evidence, so carrying the flag through is what
// makes that check reachable at all.
func TestIntegration_OccupancyEvidenceCarriesClockSuspect(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	seedPinnedRevision(t, p, s)
	pr := NewProcessor(p)

	insertLiveReceivedAt(t, p, s, "ev-susp", "GI", pay("R-EV2", "14333", "Example", "Guest", "F2", "", ""),
		time.Now().UTC(), true)
	process(t, pr, s)

	if e := readEvidence(t, p, s, "R-EV2"); !e.ClockSuspect {
		t.Fatal("clock_suspect was dropped: untrusted timing would authorise sign-ins as if it were trusted")
	}
}

// The end the guest actually experiences: after a confirming event, the Stay satisfies the same eligibility
// predicate the authentication path applies. This is the assertion that would have caught the defect — the
// lifecycle tests all passed while every real sign-in was refused.
func TestIntegration_StampedStayIsEligibleForAPMSContext(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	rev := seedPinnedRevision(t, p, s)
	pr := NewProcessor(p)

	insertLiveReceivedAt(t, p, s, "ev-elig", "GI", pay("R-EV3", "14334", "Example", "Guest", "F3", "", ""),
		time.Now().UTC(), false)
	process(t, pr, s)

	// Exactly the predicate in iam_v2.issue_or_return_pms_context and internal/authctx, freshness included.
	const maxAgeSeconds = 300
	var eligible bool
	if err := p.QueryRow(context.Background(), `SELECT
		    occupancy_evidence_at IS NOT NULL
		AND occupancy_clock_suspect IS NOT TRUE
		AND occupancy_evidence_version > 0
		AND occupancy_revision_id = $2::uuid
		AND occupancy_evidence_at > now() - make_interval(secs => $3)
		  FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R-EV3'`,
		s.iface, rev, maxAgeSeconds).Scan(&eligible); err != nil {
		t.Fatalf("eligibility probe: %v", err)
	}
	if !eligible {
		t.Fatal("a freshly confirmed Stay is still not eligible for a PMS context: a verified guest would be " +
			"refused with CONTEXT_INVALID after their identity had already matched")
	}
}
