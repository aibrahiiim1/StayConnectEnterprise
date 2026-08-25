//go:build integration

package authctx

// A DEAD SOCKET IS NOT A DEAD MIRROR.
//
// The Product Owner's rule: a live PMS connection must not be required for a new guest Room sign-in when
// StayConnect already holds trustworthy PMS-derived Stay data locally. Transport availability answers "can I
// hear the PMS right now"; mirror trust answers "do I hold PMS-derived state I have reason to believe". Only
// the second one is a reason to refuse a guest.
//
// These tests pin both sides of that boundary, because a change that implemented only the first half would
// hand out access on a mirror that was never filled — which is worse than the behaviour it replaced, and
// would look identical in a demo.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// establishMirror makes the local mirror trustworthy: a complete sync finished, and none is in flight.
//
// This is the whole of the offline branch's own state. Everything else it requires — ACTIVE, CONTINUOUS,
// pinned revision, evidence within the ceiling — is shared with the live branch and is not relaxed here.
func establishMirror(t *testing.T, p *pgxpool.Pool, s fixture, completedAt time.Time) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET last_complete_sync_at=$2, resync_started_at=NULL, updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1`, s.iface, completedAt); err != nil {
		t.Fatalf("establish mirror: %v", err)
	}
}

// startResync marks a complete sync as in flight — the one state where local Stay data is genuinely
// inconsistent rather than merely stale, because some of the truth has been applied and the rest has not.
func startResync(t *testing.T, p *pgxpool.Pool, s fixture, at time.Time) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET resync_requested_at=$2 - interval '1 minute', resync_started_at=$2,
		    updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1`,
		s.iface, at); err != nil {
		t.Fatalf("start resync: %v", err)
	}
}

// SCENARIO 1 — the live path still works, unchanged. Asserted here as well as in the freshness suite because
// the local-first change rewrote the predicate around it, and a rewrite that broke the connected case while
// fixing the disconnected one would be a strictly worse system.
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

// SCENARIO 2 — THE RULE ITSELF. Transport down, mirror trustworthy, Stay in house: the guest signs in.
//
// Every transport failure mode is exercised, because the rule is about the ABSENCE of a working socket, not
// about one particular way of failing to have one. A predicate written against DISCONNECTED alone would leave
// guests stranded whenever the interface happened to fail as DIAL_FAILED or ERROR instead.
func TestIntegration_TransportDownWithTrustedMirrorAuthorisesLocally(t *testing.T) {
	p := pool(t)
	defer p.Close()
	now := time.Now().UTC()

	for _, transport := range []string{"DISCONNECTED", "ERROR", "UNKNOWN", "CONNECTING"} {
		t.Run(transport, func(t *testing.T) {
			s := seedCacheAge(t, p, "null")
			// sync_status is deliberately RESYNC_REQUIRED, which is what pmsd writes the moment a transport
			// drops — every real outage looks like this. Requiring IN_SYNC on the offline branch would have
			// made the whole rule unreachable in production while passing every test that set it by hand.
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

// SCENARIO 6 — NEVER SYNCHRONISED FAILS CLOSED. No complete sync has ever finished, so there is no mirror to
// fall back on: the tables would answer "no such Stay" for a hotel full of guests, and every guest would be
// refused with the uniform failure while the system reported itself available.
//
// This is the case that makes the whole rule safe, and it is the one an implementation is most likely to get
// wrong, because a fresh interface looks calm — no gap, no error, nothing obviously broken.
func TestIntegration_NeverSynchronisedInterfaceFailsClosedWhileDisconnected(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", now.Add(-time.Hour))
	// last_complete_sync_at deliberately left NULL — the fixture's own default.
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	if authorizable(t, p, s) {
		t.Fatal("an interface that has never completed a sync authorised a guest while disconnected. There " +
			"is no local guest list behind that answer, only an empty table")
	}
}

// SCENARIO 5 — UNRESOLVED CONTINUITY LOSS FAILS CLOSED, whether or not the socket is up.
//
// A detected gap means events went missing. The mirror may be describing a guest who checked out during the
// gap, and no amount of local completeness fixes that: the missing events are exactly the ones that would have
// said so.
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
			establishMirror(t, p, s, now.Add(-time.Hour)) // a complete sync HAS happened — and does not save it
			setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

			if authorizable(t, p, s) {
				t.Fatalf("%s authorised a guest. Missed events are the ones that would have told us the Stay "+
					"had changed, so a complete mirror from before the gap proves nothing about now", tc.name)
			}
		})
	}
}

// A RESYNC IN FLIGHT FAILS CLOSED. Part of the truth has been applied and the rest has not, which is the one
// moment the mirror is inconsistent rather than merely behind. Brief, and worth refusing for.
func TestIntegration_ResyncInFlightFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seedCacheAge(t, p, "null")

	now := time.Now().UTC()
	setFeed(t, p, s, "DISCONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now.Add(-time.Hour))
	establishMirror(t, p, s, now.Add(-6*time.Hour))
	startResync(t, p, s, now.Add(-time.Minute))
	setEvidenceAge(t, p, s, now.Add(-2*time.Hour))

	if authorizable(t, p, s) {
		t.Fatal("a guest was authorised against a mirror that is partway through a complete resync, where " +
			"some Stays have been updated and others still hold the previous generation's state")
	}
}

// SCENARIO 10 — RECONNECT RETURNS TO THE LIVE PATH NATURALLY, with no separate transition to get wrong.
//
// The sequence a real outage follows: healthy, drop, serve locally, resync begins, resync completes,
// reconnected. Authorisation must be continuous across it apart from the deliberate pause while the mirror is
// mid-update, and the interface must end in exactly the state it started in.
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
	step("reconnected, complete resync running", false, func() {
		setFeed(t, p, s, "CONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS", now)
		startResync(t, p, s, now)
	})
	step("resync complete, live feed again", true, func() {
		setFeed(t, p, s, "CONNECTED", "IN_SYNC", "CONTINUOUS", now)
		establishMirror(t, p, s, now)
	})

	// The Stay must come out of the sequence intact: one row, still in house, with its evidence coherent. A
	// resync that duplicated or corrupted Stay state would show up here rather than at the next sign-in.
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

// NO OFFLINE TTL WAS INVENTED, and this proves the claim rather than restating it: the bound that limits
// offline authorisation is the SAME evidence ceiling the connected path has always applied.
//
// While the feed is live each complete sync re-stamps occupancy evidence and the ceiling never bites. While it
// is down nothing re-stamps anything, so each Stay ages out on its own as its evidence crosses the bound. That
// is the offline validity limit, and there is no second number anywhere to keep consistent with it.
func TestIntegration_OfflineValidityIsTheExistingEvidenceCeiling(t *testing.T) {
	p := pool(t)
	defer p.Close()
	now := time.Now().UTC()

	// An explicit max_auth_cache_age_seconds is the operator's lever, so a short one makes the boundary
	// observable in a test without inventing a constant in the predicate.
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
