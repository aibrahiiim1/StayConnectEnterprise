//go:build integration

package authctx

// PMS sign-in eligibility follows feed health, not the age of one guest's last event: a PMS says nothing about a
// guest simply staying, and a dead link must not leave stale Stays eligible. Both halves are asserted.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

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

func setFeed(t *testing.T, p *pgxpool.Pool, s fixture, transport, sync, continuity string, live time.Time) {
	t.Helper()
	// updated_at is GREATEST(now(), $5): pir_heartbeat_not_future needs the heartbeat (Go clock) no later than
	// updated_at (database clock), and microseconds of skew violated the CHECK intermittently.
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET transport_status=$2, sync_status=$3, continuity_status=$4,
		    last_connected_at=$5, last_heartbeat_at=$5, updated_at=GREATEST(now(), $5)
		WHERE pms_interface_id=$1`, s.iface, transport, sync, continuity, live); err != nil {
		t.Fatalf("set feed %s/%s: %v", transport, sync, err)
	}
}

// setEvidenceAge backdates occupancy evidence; the version must advance by exactly one
// (p3_stay_lifecycle_guard treats it as a material change).
func setEvidenceAge(t *testing.T, p *pgxpool.Pool, s fixture, at time.Time) {
	t.Helper()
	if _, err := gexec(context.Background(), p, "stay", `UPDATE iam_v2.stays
		SET occupancy_evidence_at=$2, occupancy_evidence_version=occupancy_evidence_version+1
		WHERE id=$1`, s.stay, at); err != nil {
		t.Fatalf("age evidence: %v", err)
	}
}

// A guest who checked in yesterday, on a healthy feed, must still sign in.
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

// A feed unhealthy while connected authorises nobody, and a transport failure authorises nobody without a
// trustworthy mirror. A dropped socket alone does not deny a coherently mirrored Stay (local-first).
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
		// Transport failures on a mirror that never completed a sync (last_complete_sync_at NULL): refused for
		// lack of a local guest list, not because the socket is down. Seeding a complete sync would authorise both.
		{"disconnected, mirror never synchronised", "DISCONNECTED", "IN_SYNC", "CONTINUOUS", now},
		{"transport error, mirror never synchronised", "ERROR", "IN_SYNC", "CONTINUOUS", now},
		{"resync required", "CONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now},
		{"resync in progress", "CONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now},
		{"sync failed", "CONNECTED", "SYNC_FAILED", "CONTINUOUS", now},
		{"gap detected", "CONNECTED", "IN_SYNC", "GAP_DETECTED", now},
		{"continuity never established", "CONNECTED", "IN_SYNC", "UNKNOWN", now},
		// A hung socket that never reported an error must not pass as healthy.
		{"silent beyond heartbeat timeout", "CONNECTED", "IN_SYNC", "CONTINUOUS", now.Add(-2 * time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setFeed(t, p, s, tc.transport, tc.syncSt, tc.continuitySt, tc.live)
			if authorizable(t, p, s) {
				t.Fatalf("a guest was authorised on a %s feed, with no live PMS behind the answer", tc.name)
			}
		})
	}
}

// UNKNOWN continuity (the column default) is not healthy: an interface that never established it authorises
// nobody, while requiring CONTINUOUS costs a healthy interface nothing because IN_SYNC implies it.
func TestIntegration_UnknownContinuityNeverAuthorises(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()

	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "UNKNOWN", now)
	if authorizable(t, p, s) {
		t.Fatal("an interface whose continuity was NEVER established authorised a guest. UNKNOWN is the " +
			"column default, not a clean bill of health: reading it as one authorises on the strength of a " +
			"signal that has never arrived")
	}

	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	if !authorizable(t, p, s) {
		t.Fatal("established continuity must authorise; the UNKNOWN refusal above proves nothing if the " +
			"CONTINUOUS case cannot pass either")
	}
}

// A Stay the PMS silently stopped carrying must not authorise forever on an otherwise healthy interface.
func TestIntegration_EvidenceOlderThanASyncCadenceStopsAuthorising(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)

	// No complete_sync_ms is set, so the 24-hour default plus heartbeat allowance applies (not an unbounded window).
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

// An explicit bound wins; a malformed one falls back to the default rather than widening the window.
func TestIntegration_ExplicitCacheAgeOverridesAndMalformedFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	// Revisions are immutable, so each variant is its own fixture.
	explicit := seedCacheAge(t, p, `600`)
	setFeed(t, p, explicit, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, explicit, now.Add(-2*time.Hour))
	if authorizable(t, p, explicit) {
		t.Fatal("an explicit max_auth_cache_age_seconds must override the derived ceiling")
	}

	malformed := seedCacheAge(t, p, `"not-a-number"`)
	setFeed(t, p, malformed, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	setEvidenceAge(t, p, malformed, now.Add(-2*time.Hour))
	if !authorizable(t, p, malformed) {
		t.Fatal("a malformed max_auth_cache_age_seconds must fall back to the derived ceiling, not be treated " +
			"as zero")
	}
}
