//go:build integration

package main

// ROOM-AUTH FEED READINESS IS DECIDED HERE, FROM EACH INTERFACE'S OWN REVISION.
//
// The health read reports whether an interface can currently serve Room authentication. It mirrors the feed
// half of iam_v2.p3_feed_authorizes — ACTIVE, CONNECTED, IN_SYNC, CONTINUOUS, pinned to the published
// Revision, and heard from within that Revision's own heartbeat_timeout_ms — and deliberately excludes the
// Stay-specific half, because occupancy evidence belongs to one guest's Stay rather than to the feed.
//
// The heartbeat bound is why this must be answered server-side at all. Hotel Admin was re-implementing the
// rule with the 300-second DEFAULT hardcoded, so an interface configured with any other timeout was described
// to an operator using a number that interface does not use. The tests that matter most below are the two
// non-default ones: a long timeout must keep a quiet feed READY, and a short one must call a recently-seen
// feed SILENT. A client cannot get either right, which is the whole argument for moving the decision.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// runtimeState puts the interface's runtime row into an exact state. Every axis is named at each call site so
// a test reads as the situation it is describing.
func (f *apiFixture) runtimeState(
	t *testing.T, iface, pinnedRev, transport, sync, continuity string, lastSeen *time.Time,
) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `
		INSERT INTO iam_v2.pms_interface_runtime
		  (tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode,
		   published_resync_generation, pinned_revision_id, transport_status, sync_status, continuity_status,
		   last_connected_at, last_heartbeat_at)
		VALUES ($1,$2,$3::uuid,1,'NONE',0,NULLIF($4,'')::uuid,$5,$6,$7,$8,$8)
		ON CONFLICT (tenant_id, site_id, pms_interface_id) DO UPDATE SET
		  pinned_revision_id=EXCLUDED.pinned_revision_id, transport_status=EXCLUDED.transport_status,
		  sync_status=EXCLUDED.sync_status, continuity_status=EXCLUDED.continuity_status,
		  last_connected_at=EXCLUDED.last_connected_at, last_heartbeat_at=EXCLUDED.last_heartbeat_at,
		  updated_at=now()`,
		f.tenant, f.site, iface, pinnedRev, transport, sync, continuity, lastSeen); err != nil {
		t.Fatalf("set runtime state: %v", err)
	}
}

// setHeartbeatTimeout publishes a fresh Revision carrying an explicit heartbeat_timeout_ms and points the
// interface at it. Revisions are immutable, so a different bound means a different Revision — which is also
// how a real property changes one.
func (f *apiFixture) setHeartbeatTimeout(t *testing.T, iface string, ms int) string {
	t.Helper()
	ctx := context.Background()
	var rev string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.pms_interface_revisions
		  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,folio_identity_strategy,
		   config,normalization_version)
		SELECT gen_random_uuid(),$1,$2,$3::uuid,
		       COALESCE((SELECT max(revision_no) FROM iam_v2.pms_interface_revisions
		                  WHERE pms_interface_id=$3::uuid),0)+1,
		       'Europe/Berlin','UNIQUE_PER_STAY',
		       jsonb_build_object('host','pms.local','heartbeat_timeout_ms',$4::int),1
		RETURNING id::text`, f.tenant, f.site, iface, ms).Scan(&rev); err != nil {
		t.Fatalf("author revision with heartbeat_timeout_ms=%d: %v", ms, err)
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.pms_interfaces SET current_revision_id=$2::uuid WHERE id=$1::uuid`, iface, rev); err != nil {
		t.Fatalf("publish revision: %v", err)
	}
	return rev
}

func (f *apiFixture) readiness(t *testing.T, iface string) (bool, string) {
	t.Helper()
	code, raw := f.doRaw(t, http.MethodGet, "/pms-interfaces/"+iface+"/health", nil)
	if code != 200 {
		t.Fatalf("health read returned %d: %s", code, raw)
	}
	var out struct {
		Health struct {
			Ready  bool   `json:"room_auth_ready"`
			Reason string `json:"room_auth_reason"`
		} `json:"health"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode health: %v — %s", err, raw)
	}
	return out.Health.Ready, out.Health.Reason
}

func ptrTime(t time.Time) *time.Time { return &t }

// A fully healthy feed serves Room authentication.
func TestIntegration_API_RoomAuthReadyWhenTheFeedIsHealthy(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	f.runtimeState(t, iface, rev1, "CONNECTED", "IN_SYNC", "CONTINUOUS", ptrTime(time.Now()))

	ready, reason := f.readiness(t, iface)
	if !ready || reason != "" {
		t.Fatalf("a healthy feed must serve Room auth, got ready=%v reason=%q", ready, reason)
	}
}

// THE CASE A CLIENT CANNOT GET RIGHT (1): a Revision whose heartbeat_timeout_ms is far ABOVE the default. The
// feed has been quiet for longer than 300 seconds and is still healthy by its own configuration.
func TestIntegration_API_RoomAuthHonoursALongerThanDefaultHeartbeatTimeout(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	rev := f.setHeartbeatTimeout(t, iface, 3_600_000) // one hour
	f.runtimeState(t, iface, rev, "CONNECTED", "IN_SYNC", "CONTINUOUS",
		ptrTime(time.Now().Add(-20*time.Minute))) // past the 300s default, inside this Revision's bound

	ready, reason := f.readiness(t, iface)
	if !ready {
		t.Fatalf("a feed quiet for 20 minutes under a one-hour heartbeat timeout is healthy by its own "+
			"configuration, but readiness said %q — the default is being applied instead of the Revision", reason)
	}
}

// THE CASE A CLIENT CANNOT GET RIGHT (2): a Revision whose heartbeat_timeout_ms is BELOW the default. A gap a
// 300-second rule would wave through is a silent feed by this interface's own configuration.
func TestIntegration_API_RoomAuthHonoursAShorterThanDefaultHeartbeatTimeout(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	rev := f.setHeartbeatTimeout(t, iface, 30_000) // thirty seconds
	f.runtimeState(t, iface, rev, "CONNECTED", "IN_SYNC", "CONTINUOUS",
		ptrTime(time.Now().Add(-2*time.Minute))) // inside the 300s default, well past this Revision's bound

	ready, reason := f.readiness(t, iface)
	if ready {
		t.Fatal("a feed quiet for two minutes under a thirty-second heartbeat timeout is silent, but readiness " +
			"said it was fine — the Revision's bound is being ignored in favour of the default")
	}
	if reason != roomAuthFeedSilent {
		t.Fatalf("reason = %q, want %q", reason, roomAuthFeedSilent)
	}
}

// An absent heartbeat_timeout_ms falls back to the documented 300-second default — the same fallback
// iam_v2.p3_cfg_secs applies for the authentication path.
func TestIntegration_API_RoomAuthFallsBackToTheDefaultTimeout(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t) // rev1 carries no heartbeat_timeout_ms
	f.runtimeState(t, iface, rev1, "CONNECTED", "IN_SYNC", "CONTINUOUS",
		ptrTime(time.Now().Add(-10*time.Minute)))

	ready, reason := f.readiness(t, iface)
	if ready || reason != roomAuthFeedSilent {
		t.Fatalf("with no configured timeout the 300s default applies, so a ten-minute silence is silent; "+
			"got ready=%v reason=%q", ready, reason)
	}
}

// Each remaining clause of the feed rule, reported with its own bounded code so an operator surface can say
// something specific rather than "unavailable".
func TestIntegration_API_RoomAuthReportsEachUnhealthyClause(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name                        string
		transport, sync, continuity string
		want                        string
	}{
		// A transport failure is judged by the MIRROR now, and this fixture has never completed a sync, so
		// the honest answer is that there is no local guest list — not that the socket is down. The
		// transport's own state is still reported alongside, in transport_status and disconnected_since.
		{"disconnected, mirror never synchronised", "DISCONNECTED", "IN_SYNC", "CONTINUOUS", roomAuthNeverSynced},
		{"not in sync", "CONNECTED", "RESYNC_REQUIRED", "CONTINUOUS", roomAuthNotInSync},
		{"gap detected", "CONNECTED", "IN_SYNC", "GAP_DETECTED", roomAuthContinuityGap},
		{"continuity never established", "CONNECTED", "IN_SYNC", "UNKNOWN", roomAuthContinuityNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAPI(t)
			iface, rev1, _ := f.seedInterface(t)
			f.runtimeState(t, iface, rev1, tc.transport, tc.sync, tc.continuity, ptrTime(now))
			ready, reason := f.readiness(t, iface)
			if ready {
				t.Fatalf("%s must not serve Room auth", tc.name)
			}
			if reason != tc.want {
				t.Fatalf("reason = %q, want %q", reason, tc.want)
			}
		})
	}
}

// A runtime pinned to a Revision that is no longer the published one cannot authenticate: Phase 3 compares
// the pin against the published Revision, and a mismatch means the connector is running configuration the
// product has moved on from.
func TestIntegration_API_RoomAuthRefusesAnUnpinnedRevision(t *testing.T) {
	f := newAPI(t)
	iface, _, rev2 := f.seedInterface(t) // rev1 is published; rev2 is not
	f.runtimeState(t, iface, rev2, "CONNECTED", "IN_SYNC", "CONTINUOUS", ptrTime(time.Now()))

	ready, reason := f.readiness(t, iface)
	if ready || reason != roomAuthRevisionUnpin {
		t.Fatalf("a runtime pinned to an unpublished Revision must not serve Room auth; got ready=%v reason=%q",
			ready, reason)
	}
}

// An interface that is switched off serves nobody, whatever its axes say. Reported as a lifecycle state so an
// operator is not sent to investigate a connection that is deliberately absent.
func TestIntegration_API_RoomAuthRefusesANonActiveInterface(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	f.runtimeState(t, iface, rev1, "CONNECTED", "IN_SYNC", "CONTINUOUS", ptrTime(time.Now()))
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.pms_interfaces SET lifecycle_state='AUTH_DISABLED' WHERE id=$1::uuid`, iface); err != nil {
		t.Fatalf("disable interface: %v", err)
	}

	ready, reason := f.readiness(t, iface)
	if ready || reason != roomAuthNotActive {
		t.Fatalf("a non-ACTIVE interface must not serve Room auth; got ready=%v reason=%q", ready, reason)
	}
}

// An interface with no runtime row at all has never connected. It must read as not-ready rather than as an
// error, and the reason must be one the operator can act on.
//
// The reason is CONTINUITY_NOT_ESTABLISHED rather than anything about the transport, and that is the more
// honest answer: nothing has ever arrived from this PMS. Naming the socket would suggest a connection problem
// to investigate, when what is actually true is that the interface has never been commissioned.
func TestIntegration_API_RoomAuthHandlesAnInterfaceThatNeverConnected(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t) // no runtime row seeded

	ready, reason := f.readiness(t, iface)
	if ready || reason != roomAuthContinuityNone {
		t.Fatalf("an interface that never connected must not serve Room auth; got ready=%v reason=%q",
			ready, reason)
	}
}

// THE OPERATOR MUST NOT BE TOLD GUESTS CANNOT SIGN IN WHEN THEY CAN.
//
// This is the readiness half of the Product Owner's local-first rule, and it is the half a hotel actually
// experiences: the socket drops, and the front desk needs to know whether that is their problem. If the mirror
// is intact it is not — guests carry on signing in — and saying otherwise invites staff to start handing out
// workarounds for a problem nobody has.
func TestIntegration_API_TransportDownWithTrustedMirrorStaysReady(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	f.runtimeState(t, iface, rev1, "DISCONNECTED", "RESYNC_REQUIRED", "CONTINUOUS",
		ptrTime(time.Now().Add(-3*time.Hour)))
	f.completeSync(t, iface, time.Now().Add(-4*time.Hour))

	ready, reason := f.readiness(t, iface)
	if !ready {
		t.Fatalf("a disconnected interface with a complete local mirror reported Room sign-in unavailable "+
			"(reason %q). Guests can sign in; telling staff otherwise is worse than saying nothing", reason)
	}
}

// The other half of the same surface: while the transport is down, a resync in flight IS a real reason, and it
// gets its own code so the operator can be told it is temporary rather than sent to investigate.
func TestIntegration_API_TransportDownDuringResyncReportsResyncInFlight(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	f.runtimeState(t, iface, rev1, "DISCONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS",
		ptrTime(time.Now().Add(-time.Hour)))
	f.completeSync(t, iface, time.Now().Add(-6*time.Hour))
	f.beginResync(t, iface, time.Now().Add(-time.Minute))

	ready, reason := f.readiness(t, iface)
	if ready || reason != roomAuthResyncInFlight {
		t.Fatalf("a mirror partway through a refresh must report %q; got ready=%v reason=%q",
			roomAuthResyncInFlight, ready, reason)
	}
}

// completeSync records that a full sync finished — the durable evidence that a local mirror exists at all.
func (f *apiFixture) completeSync(t *testing.T, iface string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET last_complete_sync_at=$2, resync_started_at=NULL, updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1::uuid`, iface, at); err != nil {
		t.Fatalf("record complete sync: %v", err)
	}
}

// beginResync marks a complete sync as in flight, which is the one state where the mirror is inconsistent
// rather than merely stale.
func (f *apiFixture) beginResync(t *testing.T, iface string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET resync_requested_at=$2 - interval '1 minute', resync_started_at=$2,
		    updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1::uuid`,
		iface, at); err != nil {
		t.Fatalf("begin resync: %v", err)
	}
}
