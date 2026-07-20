//go:build integration

package pmsd

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests require a disposable PostgreSQL 16 with the accepted iam_v2 schema + migration 0010 applied,
// reachable via PHASE3_TEST_DSN. scripts/pmsd-pg-integration.sh builds it. They FAIL (not skip) if the DSN
// is set but the database is unreachable/misbuilt; they skip ONLY when no DSN is configured (local dev).

func integPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set; skipping pmsd PG16 integration (CI sets it)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect PHASE3_TEST_DSN: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PHASE3_TEST_DSN: %v", err)
	}
	if n := scalarInt(t, pool, "SELECT count(*) FROM information_schema.schemata WHERE schema_name='iam_v2'"); n != 1 {
		t.Fatalf("iam_v2 schema not present (schema not built)")
	}
	return pool
}

func scalarInt(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", sql, err)
	}
	return n
}

type seeded struct {
	tenant, site, iface, rev, sg string
	otherTenant                  string
}

// seedScope inserts a tenant/site/interface/revision(+full config)/secret-generation and returns the ids.
func seedScope(t *testing.T, pool *pgxpool.Pool) seeded {
	t.Helper()
	ctx := context.Background()
	var s seeded
	err := pool.QueryRow(ctx, `WITH
	  t AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  ot AS (INSERT INTO public.tenants(id) VALUES (gen_random_uuid()) RETURNING id),
	  si AS (INSERT INTO public.sites(id,tenant_id) SELECT gen_random_uuid(), id FROM t RETURNING id, tenant_id),
	  pi AS (INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,lifecycle_state,current_revision_id)
	         SELECT gen_random_uuid(), si.tenant_id, si.id, 'protel-fias', 'ACTIVE', NULL FROM si RETURNING id,tenant_id,site_id),
	  pr AS (INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1, 'UTC',
	           '{"endpoint":"127.0.0.1:15010","auth":{"read_only":true},"dial_timeout_ms":1000,"read_timeout_ms":1000,"write_timeout_ms":1000,"heartbeat_interval_ms":1000,"heartbeat_timeout_ms":2000,"feed_freshness_ms":60000,"complete_sync_ms":300000,"resync_supported":true}'::jsonb
	         FROM pi RETURNING id, pms_interface_id),
	  sg AS (INSERT INTO iam_v2.pms_interface_secret_generations(id,tenant_id,site_id,pms_interface_id,generation_no,ciphertext,nonce,encryption_key_id,cipher_version)
	         SELECT gen_random_uuid(), pi.tenant_id, pi.site_id, pi.id, 1, '\x00'::bytea, '\x00'::bytea, gen_random_uuid(), 1 FROM pi RETURNING id)
	SELECT (SELECT tenant_id FROM pi)::text, (SELECT site_id FROM pi)::text, (SELECT id FROM pi)::text,
	       (SELECT id FROM pr)::text, (SELECT id FROM sg)::text, (SELECT id FROM ot)::text`).
		Scan(&s.tenant, &s.site, &s.iface, &s.rev, &s.sg, &s.otherTenant)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// set current_revision_id in a SEPARATE statement (a same-statement CTE cannot update the row another
	// CTE just inserted).
	if _, err := pool.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$1 WHERE id=$2`, s.rev, s.iface); err != nil {
		t.Fatalf("seed set current_revision_id: %v", err)
	}
	return s
}

func TestIntegration_AssignmentScopeAndCrossScope(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)

	got, err := repo.ListActiveInterfaces(context.Background(), s.tenant, s.site)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, i := range got {
		if i.ID == s.iface {
			found = true
		}
	}
	if !found {
		t.Fatal("assignment-scoped discovery did not return the seeded interface")
	}
	// cross-scope: the other tenant sees nothing for this site
	other, err := repo.ListActiveInterfaces(context.Background(), s.otherTenant, s.site)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range other {
		if i.ID == s.iface {
			t.Fatal("cross-scope query returned another tenant's interface")
		}
	}
}

func TestIntegration_LoadInterfaceTypedRevisionAndSecret(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)

	_, rev, sg, err := repo.LoadInterface(context.Background(), s.tenant, s.site, s.iface)
	if err != nil {
		t.Fatal(err)
	}
	if err := rev.Validate(); err != nil {
		t.Fatalf("seeded revision must validate: %v -- rev=%+v", err, rev)
	}
	if sg.ID != s.sg || rev.ActiveSecretGenerationID != s.sg {
		t.Fatalf("active secret generation mismatch: sg=%s rev.sg=%s want %s", sg.ID, rev.ActiveSecretGenerationID, s.sg)
	}
	if !rev.ReadOnly {
		t.Fatal("read-only capability must come through as true")
	}
}

func TestIntegration_AtomicGenerationAndStaleCAS(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	req := GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg}

	genA, err := repo.AllocateRuntimeGeneration(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	genB, err := repo.AllocateRuntimeGeneration(context.Background(), req) // restart
	if err != nil {
		t.Fatal(err)
	}
	if genB != genA+1 {
		t.Fatalf("restart must obtain N+1: genA=%d genB=%d", genA, genB)
	}
	base := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface}
	// stale owner A rejected
	ua := TransportUpdate{axisBase: base, Status: TransportConnecting}
	ua.ExpectedGeneration = genA
	if err := repo.UpdateTransport(context.Background(), ua); err != ErrStaleGeneration {
		t.Fatalf("stale owner A must be rejected with ErrStaleGeneration, got %v", err)
	}
	// current owner B accepted
	ub := TransportUpdate{axisBase: base, Status: TransportConnecting}
	ub.ExpectedGeneration = genB
	if err := repo.UpdateTransport(context.Background(), ub); err != nil {
		t.Fatalf("current owner B must update: %v", err)
	}
}

func TestIntegration_IndependentAxisPreservation(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	req := GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg}
	gen, err := repo.AllocateRuntimeGeneration(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	base := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen}
	now := time.Now()
	// establish CONNECTED (with last_connected_at, required by the pir_connected_pins CHECK)
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: base, Status: TransportConnected, LastConnectedAt: &now}); err != nil {
		t.Fatal(err)
	}
	// establish continuity + sync
	if err := repo.UpdateContinuity(context.Background(), ContinuityUpdate{axisBase: base, Status: ContinuityContinuous, LastValidEventAt: &now, LastEventCursor: "cur-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSync(context.Background(), SyncUpdate{axisBase: base, Status: SyncInSync, LastCompleteSyncAt: &now, SyncCursor: "sync-1"}); err != nil {
		t.Fatal(err)
	}
	// a transport heartbeat must NOT erase continuity/sync
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: base, Status: TransportConnected, LastHeartbeatAt: &now}); err != nil {
		t.Fatal(err)
	}
	var cont, syn, cur, scur string
	if err := pool.QueryRow(context.Background(), `SELECT continuity_status, sync_status, COALESCE(last_event_cursor,''), COALESCE(sync_cursor,'')
		FROM iam_v2.pms_interface_runtime WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`,
		s.tenant, s.site, s.iface).Scan(&cont, &syn, &cur, &scur); err != nil {
		t.Fatal(err)
	}
	if cont != "CONTINUOUS" || syn != "IN_SYNC" || cur != "cur-1" || scur != "sync-1" {
		t.Fatalf("heartbeat erased another axis: continuity=%s sync=%s cursor=%s syncCursor=%s", cont, syn, cur, scur)
	}
}

// TestIntegration_AtomicGapAndResync proves §4 against real PostgreSQL: the two-axis gap/resync transition is
// atomic (both axes or neither), a forced failure rolls back both, a stale owner changes neither, a heartbeat
// cannot clear the barrier, and ordinary transport writes preserve the gap/resync state.
func TestIntegration_AtomicGapAndResync(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	req := GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg}
	gen, err := repo.AllocateRuntimeGeneration(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	base := axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen}
	now := time.Now()
	// establish a healthy baseline: CONNECTED + CONTINUOUS + IN_SYNC
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: base, Status: TransportConnected, LastConnectedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateContinuity(context.Background(), ContinuityUpdate{axisBase: base, Status: ContinuityContinuous, LastValidEventAt: &now, LastEventCursor: "cur-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateSync(context.Background(), SyncUpdate{axisBase: base, Status: SyncInSync, LastCompleteSyncAt: &now, SyncCursor: "sync-1"}); err != nil {
		t.Fatal(err)
	}
	read := func() (cont, syn, code string, startedNull bool) {
		var c, y, cd string
		var startedAt *time.Time
		if err := pool.QueryRow(context.Background(), `SELECT continuity_status, sync_status,
			COALESCE(last_sync_failure_code,''), resync_started_at
			FROM iam_v2.pms_interface_runtime WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`,
			s.tenant, s.site, s.iface).Scan(&c, &y, &cd, &startedAt); err != nil {
			t.Fatal(err)
		}
		return c, y, cd, startedAt == nil
	}

	// 1) FORCED FAILURE rolls back both: an already-cancelled context aborts the transaction; neither axis moves.
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	greq := GapResyncRequest{axisBase: base, Reason: CodeEventInvalid}
	if err := repo.MarkGapAndRequireResync(cctx, greq); err == nil {
		t.Fatal("cancelled-context gap/resync must fail")
	}
	if cont, syn, _, _ := read(); cont != "CONTINUOUS" || syn != "IN_SYNC" {
		t.Fatalf("forced failure must roll back BOTH axes, got continuity=%s sync=%s", cont, syn)
	}

	// 2) STALE owner changes NEITHER: wrong generation → ErrStaleGeneration, still CONTINUOUS/IN_SYNC.
	stale := GapResyncRequest{axisBase: axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen + 1}, Reason: CodeEventInvalid}
	if err := repo.MarkGapAndRequireResync(context.Background(), stale); err != ErrStaleGeneration {
		t.Fatalf("stale owner must get ErrStaleGeneration, got %v", err)
	}
	if cont, syn, _, _ := read(); cont != "CONTINUOUS" || syn != "IN_SYNC" {
		t.Fatalf("stale owner must change NEITHER axis, got continuity=%s sync=%s", cont, syn)
	}

	// 3) BOTH axes change together, reason persisted, resync_started_at reset to NULL (coherent).
	if err := repo.MarkGapAndRequireResync(context.Background(), greq); err != nil {
		t.Fatal(err)
	}
	cont, syn, code, startedNull := read()
	if cont != "GAP_DETECTED" || syn != "RESYNC_REQUIRED" {
		t.Fatalf("both axes must move together, got continuity=%s sync=%s", cont, syn)
	}
	if code != CodeEventInvalid.String() {
		t.Fatalf("bounded typed reason must persist, got %q want %q", code, CodeEventInvalid.String())
	}
	if !startedNull {
		t.Fatal("resync_started_at must be reset to NULL on a fresh RESYNC_REQUIRED")
	}

	// 4) A HEARTBEAT (transport axis) must NOT clear the gap/resync barrier. Reuse the early `now` so the
	// heartbeat timestamp is safely <= the DB's updated_at regardless of Go/container clock skew.
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: base, Status: TransportConnected, LastHeartbeatAt: &now}); err != nil {
		t.Fatal(err)
	}
	if cont, syn, _, _ := read(); cont != "GAP_DETECTED" || syn != "RESYNC_REQUIRED" {
		t.Fatalf("heartbeat must not clear the barrier, got continuity=%s sync=%s", cont, syn)
	}

	// 5) An ordinary transport write preserves the gap/resync state (independent axes).
	if err := repo.UpdateTransport(context.Background(), TransportUpdate{axisBase: base, Status: TransportDisconnected, DisconnectedSince: &now, ErrorCode: CodeProtocolLinkEnded}); err != nil {
		t.Fatal(err)
	}
	if cont, syn, _, _ := read(); cont != "GAP_DETECTED" || syn != "RESYNC_REQUIRED" {
		t.Fatalf("normal transport write must preserve gap/resync, got continuity=%s sync=%s", cont, syn)
	}
}

// inbox builds a minimal InboxRow for a seeded scope at the given runtime generation.
func inbox(s seeded, gen int64, identity, eventType string) InboxRow {
	return InboxRow{
		axisBase:              axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen, At: time.Now()},
		ExternalEventIdentity: identity, EventType: eventType,
		ReceivedAt: time.Now(), NormalizationVersion: 1, FingerprintKeyVersion: 1,
		Payload: []byte(`{"rn":"1408","g#":"12345"}`),
	}
}

func publishedGen(t *testing.T, pool *pgxpool.Pool, s seeded) int64 {
	var g int64
	if err := pool.QueryRow(context.Background(),
		`SELECT published_resync_generation FROM iam_v2.pms_interface_runtime WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`,
		s.tenant, s.site, s.iface).Scan(&g); err != nil {
		t.Fatal(err)
	}
	return g
}

// consumableCount mirrors the Increment-4 consumer visibility rule: a row is consumable iff it is LIVE, or a
// RESYNC row whose resync_generation has been published.
func consumableCount(t *testing.T, pool *pgxpool.Pool, s seeded, identity string) int {
	return scalarInt(t, pool, `SELECT count(*) FROM iam_v2.stay_events se
		JOIN iam_v2.pms_interface_runtime r USING (tenant_id, site_id, pms_interface_id)
		WHERE se.tenant_id=$1 AND se.site_id=$2 AND se.pms_interface_id=$3 AND se.external_event_identity=$4
		  AND (se.admission_kind='LIVE' OR se.resync_generation <= r.published_resync_generation)`,
		s.tenant, s.site, s.iface, identity)
}

// TestIntegration_ResyncGenerationAndPublication proves §G: monotonic resync-generation allocation under the
// runtime-generation CAS; staged RESYNC rows are invisible until the atomic publication boundary reaches
// their generation; and a stale owner cannot allocate, stage, admit or publish.
func TestIntegration_ResyncGenerationAndPublication(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	gen, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	if err != nil {
		t.Fatal(err)
	}
	scope := ResyncScope{axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen, At: time.Now()}}

	// monotonic allocation
	g1, err := repo.AllocateResyncGeneration(context.Background(), scope)
	if err != nil || g1 != 1 {
		t.Fatalf("first resync generation = %d err=%v, want 1", g1, err)
	}
	g2, _ := repo.AllocateResyncGeneration(context.Background(), scope)
	if g2 != 2 {
		t.Fatalf("second resync generation = %d, want 2", g2)
	}

	// stale owner cannot allocate/stage/admit/publish
	stale := ResyncScope{axisBase{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, ExpectedGeneration: gen + 5, At: time.Now()}}
	if _, err := repo.AllocateResyncGeneration(context.Background(), stale); err != ErrStaleGeneration {
		t.Fatalf("stale allocate = %v, want ErrStaleGeneration", err)
	}
	staleRow := inbox(s, gen+5, "id-stale", "GI")
	if _, err := repo.AdmitLiveEvent(context.Background(), staleRow); err != ErrStaleGeneration {
		t.Fatalf("stale admit = %v, want ErrStaleGeneration", err)
	}
	if err := repo.PublishResyncGeneration(context.Background(), stale, 1); err != ErrStaleGeneration {
		t.Fatalf("stale publish = %v, want ErrStaleGeneration", err)
	}

	// stage a RESYNC row under generation g2; it must NOT be consumable before publication
	row := inbox(s, gen, "id-roster-1", "GI")
	row.ResyncGeneration = g2
	if _, err := repo.StageResyncEvent(context.Background(), row); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if publishedGen(t, pool, s) != 0 {
		t.Fatal("nothing published yet")
	}
	if n := consumableCount(t, pool, s, "id-roster-1"); n != 0 {
		t.Fatalf("staged RESYNC row must be invisible before publication, consumable=%d", n)
	}

	// publish generation g2 — one atomic row update; now the staged row is consumable
	if err := repo.PublishResyncGeneration(context.Background(), scope, g2); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if publishedGen(t, pool, s) != g2 {
		t.Fatalf("published boundary = %d, want %d", publishedGen(t, pool, s), g2)
	}
	if n := consumableCount(t, pool, s, "id-roster-1"); n != 1 {
		t.Fatalf("published RESYNC row must be consumable, consumable=%d", n)
	}
	// publishing beyond the allocated seq is rejected
	if err := repo.PublishResyncGeneration(context.Background(), scope, g2+10); err != ErrStaleGeneration {
		t.Fatalf("over-publish = %v, want ErrStaleGeneration", err)
	}
}

// TestIntegration_LiveVsResyncIdempotency proves the admission-aware partial unique indexes: LIVE dedups per
// interface; RESYNC dedups per resync generation; the same identity may exist as both LIVE and RESYNC and
// across different resync generations.
func TestIntegration_LiveVsResyncIdempotency(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	gen, _ := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})

	// LIVE idempotency: same identity twice → second rejected
	if _, err := repo.AdmitLiveEvent(context.Background(), inbox(s, gen, "id-A", "GI")); err != nil {
		t.Fatalf("first live admit: %v", err)
	}
	if _, err := repo.AdmitLiveEvent(context.Background(), inbox(s, gen, "id-A", "GI")); err == nil {
		t.Fatal("duplicate LIVE identity must be rejected")
	}
	// same identity as RESYNC (generation 1) is allowed (separate namespace)
	r1 := inbox(s, gen, "id-A", "GI")
	r1.ResyncGeneration = 1
	if _, err := repo.StageResyncEvent(context.Background(), r1); err != nil {
		t.Fatalf("RESYNC with same identity as LIVE must be allowed: %v", err)
	}
	// duplicate within the same resync generation → rejected
	if _, err := repo.StageResyncEvent(context.Background(), r1); err == nil {
		t.Fatal("duplicate identity within one resync generation must be rejected")
	}
	// same identity in a different resync generation → allowed
	r2 := inbox(s, gen, "id-A", "GI")
	r2.ResyncGeneration = 2
	if _, err := repo.StageResyncEvent(context.Background(), r2); err != nil {
		t.Fatalf("same identity in a new resync generation must be allowed: %v", err)
	}
}

// TestIntegration_InboxImmutability proves the admission columns are immutable append-first (the trigger
// rejects mutating admission_kind/resync_generation).
func TestIntegration_InboxImmutability(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	gen, _ := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	id, err := repo.AdmitLiveEvent(context.Background(), inbox(s, gen, "id-imm", "GI"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE iam_v2.stay_events SET admission_kind='RESYNC', resync_generation=9 WHERE id=$1`, id); err == nil {
		t.Fatal("mutating admission columns must be rejected (append-first immutable)")
	}
}

// TestIntegration_SinkResyncLifecycle drives the real workerSink against PostgreSQL through a full
// RequireInitialResync → DS → stage → DE(publish) → live-admit lifecycle and asserts the durable rows and the
// publication boundary land correctly through the real append-only triggers.
func TestIntegration_SinkResyncLifecycle(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	repo := NewPgRepoFromPool(pool)
	gen, err := repo.AllocateRuntimeGeneration(context.Background(), GenerationRequest{TenantID: s.tenant, SiteID: s.site, PMSInterfaceID: s.iface, PinnedRevisionID: s.rev, PinnedSecretGenerationID: s.sg})
	if err != nil {
		t.Fatal(err)
	}
	w := &worker{iface: Interface{TenantID: s.tenant, SiteID: s.site, ID: s.iface}, repo: repo, deps: &Deps{Now: time.Now}}
	sink := &workerSink{w: w, ctx: context.Background(), gen: gen}

	mkEvent := func(identity string) Event {
		return Event{
			InterfaceID: s.iface, RevisionID: s.rev, SecretGenerationID: s.sg, NormalizationVer: 1,
			RecordType: RecGI, ReservationRef: "R" + identity, RoomNumber: "1408",
			SourceEventFingerprint: identity, FingerprintKeyVersion: 1, ExternalEventIdentity: identity,
			SourceEvidenceHash: identity, EvidenceKeyVersion: 1, NormalizedAt: time.Now(), ReceivedAt: time.Now(),
		}
	}
	hex := func(n byte) string { // 64-hex identity
		b := make([]byte, 32)
		for i := range b {
			b[i] = n
		}
		out := make([]byte, 64)
		const h = "0123456789abcdef"
		for i, x := range b {
			out[i*2] = h[x>>4]
			out[i*2+1] = h[x&0xf]
		}
		return string(out)
	}
	ctx := context.Background()

	if err := sink.RequireInitialResync(time.Now()); err != nil {
		t.Fatal(err)
	}
	// live event before publish → held (no durable row)
	if err := sink.OnDomainEvent(ctx, mkEvent(hex(0x11))); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, pool, `SELECT count(*) FROM iam_v2.stay_events WHERE pms_interface_id=$1`, s.iface); n != 0 {
		t.Fatalf("no durable row before publish, got %d", n)
	}
	// DS → stage a roster row
	if err := sink.OnResyncStart(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := sink.OnDomainEvent(ctx, mkEvent(hex(0x22))); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, pool, `SELECT count(*) FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND admission_kind='RESYNC'`, s.iface); n != 1 {
		t.Fatalf("staged RESYNC row count = %d, want 1", n)
	}
	if consumableCount(t, pool, s, hex(0x22)) != 0 {
		t.Fatal("staged row must be invisible before DE")
	}
	// DE → publish; the staged row becomes consumable
	if err := sink.OnResyncComplete(time.Now(), ""); err != nil {
		t.Fatal(err)
	}
	if consumableCount(t, pool, s, hex(0x22)) != 1 {
		t.Fatal("published staged row must be consumable")
	}
	// now a live event is durably admitted
	if err := sink.OnDomainEvent(ctx, mkEvent(hex(0x33))); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, pool, `SELECT count(*) FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND admission_kind='LIVE'`, s.iface); n != 1 {
		t.Fatalf("live admitted row count = %d, want 1", n)
	}
}

func TestIntegration_RealAdvisoryLockCompetition(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	s := seedScope(t, pool)
	key, err := LockKey(s.tenant, s.site, s.iface)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewPgLocker(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := NewPgLocker(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	gotA, err := a.TryLock(context.Background(), key)
	if err != nil || !gotA {
		t.Fatalf("owner A must acquire the lock: got=%v err=%v", gotA, err)
	}
	gotB, err := b.TryLock(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if gotB {
		t.Fatal("competing owner B must NOT acquire the same advisory lock")
	}
	// after A releases, B can acquire
	_ = a.Close()
	gotB2, err := b.TryLock(context.Background(), key)
	if err != nil || !gotB2 {
		t.Fatalf("B must acquire after A releases: got=%v err=%v", gotB2, err)
	}
}

func TestIntegration_ZeroRuntimeGrantsWhileDark(t *testing.T) {
	pool := integPool(t)
	defer pool.Close()
	// no runtime service role (svc_*) may hold any privilege on the runtime/stay tables while DARK
	n := scalarInt(t, pool, `SELECT count(*) FROM information_schema.role_table_grants
		WHERE table_schema='iam_v2' AND grantee LIKE 'svc_%'`)
	if n != 0 {
		t.Fatalf("expected zero svc_* grants on iam_v2 while dark, got %d", n)
	}
}
