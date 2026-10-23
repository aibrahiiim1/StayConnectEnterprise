//go:build integration

package main

// Readiness mirrors the feed half of iam_v2.p3_feed_authorizes (ACTIVE, CONNECTED, IN_SYNC, CONTINUOUS, pinned
// to the published Revision, heard within that Revision's own heartbeat_timeout_ms) and excludes the
// Stay-specific half. It is decided server-side because a client hardcoding the 300-second default gets any
// other timeout wrong; the two non-default tests below matter most.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

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

// setHeartbeatTimeout publishes a fresh Revision (Revisions are immutable) and points the interface at it.
func (f *apiFixture) setHeartbeatTimeout(t *testing.T, iface string, ms int) string {
	t.Helper()
	ctx := context.Background()
	var rev string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.pms_interface_revisions
		  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,
		   config,normalization_version)
		SELECT gen_random_uuid(),$1,$2,$3::uuid,
		       COALESCE((SELECT max(revision_no) FROM iam_v2.pms_interface_revisions
		                  WHERE pms_interface_id=$3::uuid),0)+1,
		       'Europe/Berlin','RESERVATION',
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

func TestIntegration_API_RoomAuthReadyWhenTheFeedIsHealthy(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	f.runtimeState(t, iface, rev1, "CONNECTED", "IN_SYNC", "CONTINUOUS", ptrTime(time.Now()))

	ready, reason := f.readiness(t, iface)
	if !ready || reason != "" {
		t.Fatalf("a healthy feed must serve Room auth, got ready=%v reason=%q", ready, reason)
	}
}

// A heartbeat_timeout_ms far above the default: quiet for over 300 seconds and still healthy by its own configuration.
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

// A heartbeat_timeout_ms below the default: a gap a 300-second rule would wave through is a silent feed here.
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

// An absent heartbeat_timeout_ms falls back to the 300-second default, as iam_v2.p3_cfg_secs does.
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

// Each remaining clause of the feed rule has its own bounded code, so the surface can say more than "unavailable".
func TestIntegration_API_RoomAuthReportsEachUnhealthyClause(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name                        string
		transport, sync, continuity string
		want                        string
	}{
		// Transport failure is judged by the mirror, and this fixture never completed a sync, so the answer is
		// "no local guest list"; transport_status and disconnected_since still report the transport.
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

// A runtime pinned to a Revision that is no longer published cannot authenticate: the connector runs stale configuration.
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

// An interface switched off serves nobody and is reported as a lifecycle state, not a connection fault.
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

// An interface with no runtime row has never connected: not-ready rather than an error, with
// CONTINUITY_NOT_ESTABLISHED (not a transport reason) since it was never commissioned.
func TestIntegration_API_RoomAuthHandlesAnInterfaceThatNeverConnected(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t) // no runtime row seeded

	ready, reason := f.readiness(t, iface)
	if ready || reason != roomAuthContinuityNone {
		t.Fatalf("an interface that never connected must not serve Room auth; got ready=%v reason=%q",
			ready, reason)
	}
}

// Local-first: when the socket drops but the mirror is intact, guests keep signing in and the operator must not be told otherwise.
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

// While the transport is down a resync in flight does not close room sign-in: the published roster is untouched
// until a replacement completes and p3_feed_authorizes authorises from it, so readiness must not report "down".
func TestIntegration_API_TransportDownDuringResyncStaysReady(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t)
	f.runtimeState(t, iface, rev1, "DISCONNECTED", "RESYNC_IN_PROGRESS", "CONTINUOUS",
		ptrTime(time.Now().Add(-time.Hour)))
	f.completeSync(t, iface, time.Now().Add(-6*time.Hour))
	f.beginResync(t, iface, time.Now().Add(-time.Minute))

	ready, reason := f.readiness(t, iface)
	if !ready || reason == roomAuthResyncInFlight {
		t.Fatalf("a disconnected interface with a published roster and a resync in flight must stay ready "+
			"(the mirror authorises); got ready=%v reason=%q", ready, reason)
	}
}

// completeSync is the durable evidence that a local mirror exists at all.
func (f *apiFixture) completeSync(t *testing.T, iface string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET last_complete_sync_at=$2, resync_started_at=NULL, updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1::uuid`, iface, at); err != nil {
		t.Fatalf("record complete sync: %v", err)
	}
}

// beginResync marks a sync in flight, the one state where the mirror is inconsistent rather than merely stale.
func (f *apiFixture) beginResync(t *testing.T, iface string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET resync_requested_at=$2::timestamptz - interval '1 minute', resync_started_at=$2,
		    updated_at=GREATEST(now(), $2)
		WHERE pms_interface_id=$1::uuid`,
		iface, at); err != nil {
		t.Fatalf("begin resync: %v", err)
	}
}
