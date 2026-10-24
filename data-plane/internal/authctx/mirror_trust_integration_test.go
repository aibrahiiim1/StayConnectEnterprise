//go:build integration

package authctx

// A dead socket is not a dead mirror: Room sign-in must not require a live PMS connection when trustworthy
// local Stay data exists. Only mirror trust, not transport availability, is a reason to refuse a guest.
// Both sides are pinned, or access would be handed out on a mirror that was never filled.

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// establishMirror makes the mirror trustworthy: a complete sync finished and none is in flight.
func establishMirror(t *testing.T, p *pgxpool.Pool, s fixture, completedAt time.Time) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET last_complete_sync_at=$2, resync_started_at=NULL, updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1`, s.iface, completedAt); err != nil {
		t.Fatalf("establish mirror: %v", err)
	}
}

// startResync marks a sync in flight: local data is inconsistent, not merely stale.
func startResync(t *testing.T, p *pgxpool.Pool, s fixture, at time.Time) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET resync_requested_at=$2::timestamptz - interval '1 minute', resync_started_at=$2,
		    updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1`,
		s.iface, at); err != nil {
		t.Fatalf("start resync: %v", err)
	}
}

// The live path still works.
func TestIntegration_ConnectedFeedWithTrustedMirrorStillAuthorises(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	now := time.Now().UTC()
	setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
	establishMirror(t, p, s, now.Add(-2*time.Hour))
	setEvidenceAge(t, p, s, now.Add(-6*time.Hour))

	if !authorizable(t, p, s) {
		t.Fatal("a healthy connected feed stopped authorising an in-house guest. The local-mirror branch was " +
			"added alongside the live branch, not in place of it")
	}
}

// Transport down, mirror trustworthy, Stay in house: the guest signs in. Every transport failure mode is
// exercised, not just DISCONNECTED.
func TestIntegration_TransportDownWithTrustedMirrorAuthorisesLocally(t *testing.T) {
	p := pool(t)
	defer p.Close()
	now := time.Now().UTC()

	for _, transport := range []string{"DISCONNECTED", "ERROR", "UNKNOWN", "CONNECTING"} {
		t.Run(transport, func(t *testing.T) {
			s := seedCacheAge(t, p, "null")
			// sync_status is RESYNC_REQUIRED, which pmsd writes the moment a transport drops; requiring IN_SYNC
			// offline would make the rule unreachable in production.
			setFeed(t, p, s, transport, "RESYNC_REQUIRED", "CONTINUOUS", now.Add(-3*time.Hour))
			establishMirror(t, p, s, now.Add(-4*time.Hour))
			setEvidenceAge(t, p, s, now.Add(-5*time.Hour))

			if !authorizable(t, p, s) {
				t.Fatalf("transport %s refused an in-house guest whose Stay is mirrored completely and "+
					"coherently. The socket being down is not evidence that the local guest list is wrong",
					transport)
			}
		})
	}
}

// Never synchronised fails closed: with no complete sync there is no mirror, and a fresh interface looks calm.
func TestIntegration_NeverSynchronisedInterfaceFailsClosedWhileDisconnected(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now.Add(-time.Hour))
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	if authorizable(t, p, s) {
		t.Fatal("an interface that has never completed a sync authorised a guest while disconnected. There " +
			"is no local guest list behind that answer, only an empty table")
	}
}

// Unresolved continuity loss fails closed whether or not the socket is up: a gap may hide a checkout.
func TestIntegration_ContinuityLossFailsClosedRegardlessOfTransport(t *testing.T) {
	p := pool(t)
	defer p.Close()
	now := time.Now().UTC()

	for _, tc := range []struct{ name, transport, continuity string }{
		{"gap while disconnected", "DISCONNECTED", "GAP_DETECTED"},
		{"gap while connected", "CONNECTED", "GAP_DETECTED"},
		{"continuity never established while disconnected", "DISCONNECTED", "UNKNOWN"},
		{"continuity never established while connected", "CONNECTED", "UNKNOWN"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := seedCacheAge(t, p, "null")
			setFeed(t, p, s, tc.transport, "IN_SYNC", tc.continuity, now)
			establishMirror(t, p, s, now.Add(-time.Hour))
			setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

			if authorizable(t, p, s) {
				t.Fatalf("%s authorised a guest. Missed events are the ones that would have told us the Stay "+
					"had changed, so a complete mirror from before the gap proves nothing about now", tc.name)
			}
		})
	}
}

// A resync in flight fails closed: the mirror is inconsistent, not merely behind.
func TestIntegration_ResyncInFlightKeepsTheLastGoodRoster(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now.Add(-time.Hour))
	establishMirror(t, p, s, now.Add(-6*time.Hour))
	startResync(t, p, s, now.Add(-time.Minute))
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	if !authorizable(t, p, s) {
		t.Fatal("a resync that has merely STARTED withdrew a roster that is published, whole and two hours " +
			"old. This is the defect that refused Room authentication for four days: pmsd set " +
			"resync_started_at, the dial failed, and the field was never cleared")
	}
}

// A resync that began and failed leaves the old roster as the only one, so it still authorises within the cache age.
func TestIntegration_FailedResyncDoesNotInvalidateThePublishedRoster(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "259200") // seventy-two hours

	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now.Add(-71*time.Hour))
	establishMirror(t, p, s, now.Add(-71*time.Hour))
	startResync(t, p, s, now.Add(-70*time.Hour)) // began 70 hours ago and never finished
	setEvidenceAge(t, p, s, now.Add(-71*time.Hour))

	if !authorizable(t, p, s) {
		t.Fatal("a roster 71 hours old was refused inside a 72-hour window because a failed resync attempt " +
			"was on the record")
	}
}

// One hour later the same roster is outside the cache age and nothing authorises.
func TestIntegration_TheSeventyTwoHourWindowStillExpires(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "259200")

	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now.Add(-73*time.Hour))
	establishMirror(t, p, s, now.Add(-73*time.Hour))
	startResync(t, p, s, now.Add(-72*time.Hour))
	setEvidenceAge(t, p, s, now.Add(-73*time.Hour))

	if authorizable(t, p, s) {
		t.Fatal("evidence older than the configured 72-hour cache age authorised a guest")
	}
}

// Events of an unpublished generation are unclaimable: they neither block nor contribute to authorisation.
func TestIntegration_AnUnpublishedGenerationNeitherBlocksNorAuthorises(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now.Add(-time.Hour))
	establishMirror(t, p, s, now.Add(-6*time.Hour))
	startResync(t, p, s, now.Add(-time.Minute))
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	if _, err := gexec(context.Background(), p, "stay", `INSERT INTO iam_v2.stay_events
		(id,tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,
		 processing_status,admission_kind,resync_generation,sequence_version,received_at)
		SELECT gen_random_uuid(),$1,$2,$3,'partial-1','GO','{}'::jsonb,'PENDING','RESYNC',
		       published_resync_generation + 1, 1, now()
		  FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$3`,
		s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("stage the arriving generation: %v", err)
	}

	if !authorizable(t, p, s) {
		t.Fatal("an unpublished generation's event blocked authorisation against the roster that IS published")
	}

	// A claimable pending event still blocks: the published roster is then behind.
	if _, err := gexec(context.Background(), p, "stay", `INSERT INTO iam_v2.stay_events
		(id,tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,
		 processing_status,admission_kind,resync_generation,sequence_version,received_at)
		VALUES (gen_random_uuid(),$1,$2,$3,'claimable-1','GO','{}'::jsonb,'PENDING','LIVE',0,1,now())`,
		s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("stage the claimable event: %v", err)
	}
	if authorizable(t, p, s) {
		t.Fatal("a claimable PENDING event no longer blocks: the materialization-readiness rule was lost")
	}
}

// Outage sequence: healthy, drop, serve locally, reconnect, resync runs and completes. Authorisation is
// continuous throughout (D69), and the interface ends as it started.
func TestIntegration_OutageAndReconnectSequence(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	setEvidenceAge(t, p, s, now.Add(-4*time.Hour))

	step := func(name string, want bool, mutate func()) {
		t.Helper()
		mutate()
		if got := authorizable(t, p, s); got != want {
			t.Fatalf("%s: authorised=%v, want %v", name, got, want)
		}
	}

	step("healthy and connected", true, func() {
		setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
		establishMirror(t, p, s, now.Add(-2*time.Hour))
	})
	step("socket drops, mirror intact", true, func() {
		setFeed(t, p, s, "DISCONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now.Add(-time.Minute))
	})
	step("reconnected, complete resync running: the published roster keeps answering (D69)", true, func() {
		setFeed(t, p, s, "CONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now)
		startResync(t, p, s, now)
	})
	step("resync complete, live feed again", true, func() {
		setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
		establishMirror(t, p, s, now)
	})

	// The Stay must come out intact: one row, still in house, evidence coherent.
	var rows int
	var status string
	if err := p.QueryRow(context.Background(),
		`SELECT count(*), max(status) FROM iam_v2.stays WHERE id=$1`, s.stay).Scan(&rows, &status); err != nil {
		t.Fatalf("post-sequence stay read: %v", err)
	}
	if rows != 1 || status != "IN_HOUSE" {
		t.Fatalf("after outage and resync the Stay is rows=%d status=%q, want exactly one IN_HOUSE row",
			rows, status)
	}
}

// Offline authorisation is bounded by the same evidence ceiling as the connected path: with the feed down
// nothing re-stamps evidence, so each Stay ages out on its own. There is no separate offline TTL.
func TestIntegration_OfflineValidityIsTheExistingEvidenceCeiling(t *testing.T) {
	p := pool(t)
	defer p.Close()
	now := time.Now().UTC()

	// A short explicit max_auth_cache_age_seconds makes the boundary observable.
	s := seedCacheAge(t, p, `"3600"`)
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now.Add(-2*time.Hour))
	establishMirror(t, p, s, now.Add(-3*time.Hour))

	setEvidenceAge(t, p, s, now.Add(-30*time.Minute))
	if !authorizable(t, p, s) {
		t.Fatal("evidence inside the configured ceiling was refused while offline")
	}
	setEvidenceAge(t, p, s, now.Add(-90*time.Minute))
	if authorizable(t, p, s) {
		t.Fatal("evidence older than the configured ceiling was authorised while offline. The ceiling IS the " +
			"offline validity limit; if it does not bite here, an outage authorises forever")
	}
}

// D69: a CONNECTED feed whose guest list is being refreshed (or whose refresh failed) keeps authorising from the
// roster already published, exactly as a disconnected one does.
func TestIntegration_ConnectedResyncKeepsTheLastGoodRoster(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	establishMirror(t, p, s, now.Add(-30*time.Minute))
	setEvidenceAge(t, p, s, now.Add(-time.Hour))

	for _, syncSt := range []string{"RESYNC_REQUIRED", "RESYNC_IN_PROGRESS", "SYNC_FAILED", "UNKNOWN"} {
		t.Run(syncSt, func(t *testing.T) {
			setFeed(t, p, s, "CONNECTED", syncSt, "CONTINUOUS", now)
			if syncSt == "RESYNC_IN_PROGRESS" {
				startResync(t, p, s, now)
			}
			if !authorizable(t, p, s) {
				t.Fatalf("a connected %s feed refused a guest on a roster that is published, whole and 30 minutes old", syncSt)
			}
		})
	}
}

// What still fails closed while a connected resync runs.
func TestIntegration_ConnectedResyncStillFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	now := time.Now().UTC()

	t.Run("never synchronised", func(t *testing.T) {
		s := seedCacheAge(t, p, "null")
		setFeed(t, p, s, "CONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now)
		setEvidenceAge(t, p, s, now.Add(-time.Minute))
		if authorizable(t, p, s) {
			t.Fatal("a resync on an interface that never completed one authorised a guest")
		}
	})
	t.Run("continuity gap", func(t *testing.T) {
		s := seedCacheAge(t, p, "null")
		establishMirror(t, p, s, now.Add(-time.Hour))
		setFeed(t, p, s, "CONNECTED", "RESYNC_IN_PROGRESS", "GAP_DETECTED", now)
		setEvidenceAge(t, p, s, now.Add(-time.Hour))
		if authorizable(t, p, s) {
			t.Fatal("a continuity gap during a resync authorised a guest")
		}
	})
	t.Run("evidence older than the cache age", func(t *testing.T) {
		s := seedCacheAge(t, p, "259200")
		establishMirror(t, p, s, now.Add(-73*time.Hour))
		setFeed(t, p, s, "CONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now)
		setEvidenceAge(t, p, s, now.Add(-73*time.Hour))
		if authorizable(t, p, s) {
			t.Fatal("evidence older than the 72-hour cache age authorised a guest during a connected resync")
		}
	})
	t.Run("connected, in sync but silent", func(t *testing.T) {
		s := seedCacheAge(t, p, "null")
		establishMirror(t, p, s, now.Add(-time.Hour))
		setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now.Add(-2*time.Hour))
		setEvidenceAge(t, p, s, now.Add(-time.Hour))
		if authorizable(t, p, s) {
			t.Fatal("a hung IN_SYNC link authorised a guest: only a resync may fall back to the last good roster")
		}
	})
}

// A reconnect storm: the link drops and comes back repeatedly, each reconnect starting a full resync, with
// partial generations arriving and a check-out reported live mid-resync. Authorisation never lapses on the
// published roster, the partial generation never counts, and the live check-out blocks until it is applied.
func TestIntegration_ReconnectStormKeepsRoomSignInOnTheLastGoodRoster(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")
	now := time.Now().UTC()
	establishMirror(t, p, s, now.Add(-10*time.Minute))
	setEvidenceAge(t, p, s, now.Add(-10*time.Minute))
	ctx := context.Background()

	for i := 1; i <= 8; i++ {
		setFeed(t, p, s, "DISCONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now.Add(-time.Second))
		if !authorizable(t, p, s) {
			t.Fatalf("drop %d: the roster stopped answering while the link was down", i)
		}
		setFeed(t, p, s, "CONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now)
		startResync(t, p, s, now)
		if _, err := gexec(ctx, p, "stay", `INSERT INTO iam_v2.stay_events
			(id,tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,
			 processing_status,admission_kind,resync_generation,sequence_version,received_at)
			SELECT gen_random_uuid(),$1,$2,$3,'storm-partial-'||$4,'GI','{}'::jsonb,'PENDING','RESYNC',
			       published_resync_generation + 1, 1, now()
			  FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$3`,
			s.tenant, s.site, s.iface, strconv.Itoa(i)); err != nil {
			t.Fatalf("stage partial generation %d: %v", i, err)
		}
		if !authorizable(t, p, s) {
			t.Fatalf("reconnect %d: a connected resync with a partial generation arriving refused the published roster", i)
		}
	}

	if _, err := gexec(ctx, p, "stay", `INSERT INTO iam_v2.stay_events
		(id,tenant_id,site_id,pms_interface_id,external_event_identity,event_type,payload,
		 processing_status,admission_kind,resync_generation,sequence_version,received_at)
		VALUES (gen_random_uuid(),$1,$2,$3,'storm-live-checkout','GO','{}'::jsonb,'PENDING','LIVE',0,1,now())`,
		s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("stage the live check-out: %v", err)
	}
	if authorizable(t, p, s) {
		t.Fatal("a live check-out not yet applied did not block sign-in during the resync")
	}
}
