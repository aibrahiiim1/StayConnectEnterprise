//go:build integration

package authctx

// PMS SIGN-IN ELIGIBILITY FOLLOWS FEED HEALTH, NOT THE AGE OF THE LAST EVENT ABOUT ONE GUEST.
//
// The rule these tests pin replaced one that was wrong in both directions at once. It required each Stay's own
// occupancy evidence to be younger than max_auth_cache_age_seconds — unset everywhere, so 300 seconds. A PMS
// says nothing about a guest who is simply staying in their room, so a real guest could sign in for five
// minutes after check-in and never again. And when the link died, every Stay kept its stored timestamp and
// stayed eligible for five more minutes with no feed behind it at all.
//
// Both halves are asserted here, because a change that fixed only the first would look correct in a demo and
// would quietly hand out access on a dead interface.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// authorizable asks the shared predicate exactly as the auth path does.
func authorizable(t *testing.T, p *pgxpool.Pool, s fixture) bool {
	t.Helper()
	var ok bool
	if err := p.QueryRow(context.Background(),
		`SELECT iam_v2.p3_feed_authorizes($1,$2,$3,$5,(SELECT occupancy_evidence_at FROM iam_v2.stays WHERE id=$4))`,
		s.tenant, s.site, s.iface, s.stay, s.rev).Scan(&ok); err != nil {
		t.Fatalf("p3_feed_authorizes: %v", err)
	}
	return ok
}

// setFeed drives the runtime axes to a named state.
func setFeed(t *testing.T, p *pgxpool.Pool, s fixture, transport, sync, continuity string, live time.Time) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET transport_status=$2, sync_status=$3, continuity_status=$4,
		    last_connected_at=$5, last_heartbeat_at=$5, updated_at=now()
		WHERE pms_interface_id=$1`, s.iface, transport, sync, continuity, live); err != nil {
		t.Fatalf("set feed %s/%s: %v", transport, sync, err)
	}
}

// setEvidenceAge backdates the Stay's occupancy evidence.
//
// The version is incremented because p3_stay_lifecycle_guard requires it: moving occupancy_evidence_at is a
// MATERIAL evidence change and must advance the version by exactly one. A helper that tried to age the
// timestamp quietly would be rejected by the database, which is the guard doing its job.
func setEvidenceAge(t *testing.T, p *pgxpool.Pool, s fixture, at time.Time) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.stays
		SET occupancy_evidence_at=$2, occupancy_evidence_version=occupancy_evidence_version+1
		WHERE id=$1`, s.stay, at); err != nil {
		t.Fatalf("age evidence: %v", err)
	}
}

// THE BUG THE PRODUCT OWNER HIT. A guest who checked in yesterday, on a healthy feed, must still sign in.
// Under the old rule this was false after five minutes and Room sign-in was unusable for every real guest.
func TestIntegration_HealthyFeedAuthorisesAnUnchangedOvernightStay(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, s, now.Add(-20*time.Hour)) // checked in yesterday, nothing has changed since

	if !authorizable(t, p, s) {
		t.Fatal("a guest who checked in yesterday cannot sign in on a healthy, connected, in-sync feed. " +
			"Silence from a healthy PMS means the Stay is unchanged, not that the evidence has decayed")
	}
}

// The other half. A dead feed authorises NOBODY, immediately — not for a further grace period on stored
// evidence that nothing is maintaining any more.
func TestIntegration_DeadFeedAuthorisesNobody(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()

	for _, tc := range []struct {
		name                            string
		transport, syncSt, continuitySt string
		live                            time.Time
	}{
		{"disconnected", "DISCONNECTED", "IN_SYNC", "CONTINUOUS", now},
		{"error", "ERROR", "IN_SYNC", "CONTINUOUS", now},
		{"resync required", "CONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now},
		{"resync in progress", "CONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now},
		{"sync failed", "CONNECTED", "SYNC_FAILED", "CONTINUOUS", now},
		{"gap detected", "CONNECTED", "IN_SYNC", "GAP_DETECTED", now},
		// Connected and in sync on paper, but nothing has been heard for far longer than the heartbeat
		// timeout: a hung socket that never reported an error must not pass as a healthy feed.
		{"silent beyond heartbeat timeout", "CONNECTED", "IN_SYNC", "CONTINUOUS", now.Add(-2 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Only the FEED varies. The evidence stays exactly as seeded — fresh, coherent, revision-matched —
			// so each refusal below is attributable to feed health and nothing else.
			setFeed(t, p, s, tc.transport, tc.syncSt, tc.continuitySt, tc.live)
			if authorizable(t, p, s) {
				t.Fatalf("a guest was authorised on a %s feed, with no live PMS behind the answer", tc.name)
			}
		})
	}
}

// The absolute ceiling still exists. A Stay the PMS has silently stopped carrying must not authorise forever
// just because the interface is healthy for other guests.
func TestIntegration_EvidenceOlderThanASyncCadenceStopsAuthorising(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)

	// The fixture config sets no complete_sync_ms, so the 24-hour default applies, plus the heartbeat
	// allowance. An absent key must produce that default rather than an unbounded window.
	setEvidenceAge(t, p, s, now.Add(-23*time.Hour))
	if !authorizable(t, p, s) {
		t.Fatal("evidence within one complete-sync cadence must still authorise on a healthy feed")
	}
	setEvidenceAge(t, p, s, now.Add(-30*time.Hour))
	if authorizable(t, p, s) {
		t.Fatal("a Stay the PMS has not confirmed for longer than a full sync cadence must stop authorising, " +
			"however healthy the interface is for everyone else")
	}
}

// An explicit operator-set bound still wins, and a malformed one still fails closed to the default rather
// than widening the window.
func TestIntegration_ExplicitCacheAgeOverridesAndMalformedFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	// Revisions are immutable, so each variant is its own fixture rather than an edit.
	explicit := seedCacheAge(t, p, `600`) // ten minutes, explicitly chosen
	setFeed(t, p, explicit, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, explicit, now.Add(-2*time.Hour))
	if authorizable(t, p, explicit) {
		t.Fatal("an explicit max_auth_cache_age_seconds must override the derived ceiling")
	}

	malformed := seedCacheAge(t, p, `"not-a-number"`) // must NOT widen anything, and must not reject everything
	setFeed(t, p, malformed, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, malformed, now.Add(-2*time.Hour))
	if !authorizable(t, p, malformed) {
		t.Fatal("a malformed max_auth_cache_age_seconds must fall back to the derived ceiling, not be treated " +
			"as zero")
	}
}
