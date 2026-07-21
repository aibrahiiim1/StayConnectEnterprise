//go:build integration

package stayengine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/checkout"
)

// commerce seeds the Grace catalog + published config the Checkout Converter needs, and (optionally) the
// canonical Emergency catalog. Returns the grace package revision id.
func commerce(t *testing.T, p *pgxpool.Pool, s scope, bootstrapEmergency bool) string {
	t.Helper()
	ctx := context.Background()
	var pkgRev string
	if err := p.QueryRow(ctx, `WITH
	  sp AS (INSERT INTO iam_v2.service_plans(id,tenant_id,site_id,code,enabled)
	         VALUES (gen_random_uuid(),$1,$2,'grace-plan',true) RETURNING id),
	  spr AS (INSERT INTO iam_v2.service_plan_revisions(id,tenant_id,site_id,service_plan_id,revision_no,down_kbps,up_kbps,max_concurrent_devices,device_limit_policy,time_accounting_mode,data_quota_bytes)
	          SELECT gen_random_uuid(),$1,$2,sp.id,1,4000,1500,2,'REJECT_NEW_DEVICE','VALIDITY_WINDOW',524288000 FROM sp RETURNING id),
	  ip AS (INSERT INTO iam_v2.internet_packages(id,tenant_id,site_id,code,is_system)
	         VALUES (gen_random_uuid(),$1,$2,'grace-pkg',true) RETURNING id),
	  ipr AS (INSERT INTO iam_v2.internet_package_revisions(id,tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,settlement_methods,duration_policy)
	          SELECT gen_random_uuid(),$1,$2,ip.id,1,spr.id,'CHECKOUT_GRACE',0,ARRAY['NOT_REQUIRED']::text[],
	                 '{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600}'::jsonb FROM ip, spr RETURNING id)
	SELECT id::text FROM ipr`, s.tenant, s.site).Scan(&pkgRev); err != nil {
		t.Fatalf("seed commerce: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, pkgRev); err != nil {
		t.Fatalf("pin current revision: %v", err)
	}
	// publish the typed policy through the ONLY legal writer
	if _, err := p.Exec(ctx, `SELECT iam_v2.publish_checkout_grace_config($1,$2,$3,3600,4000,1500,524288000,2,'REJECT_NEW_DEVICE',86400)`,
		s.tenant, s.site, pkgRev); err != nil {
		t.Fatalf("publish grace config: %v", err)
	}
	if bootstrapEmergency {
		if _, err := p.Exec(ctx, `SELECT iam_v2.bootstrap_emergency_grace($1,$2)`, s.tenant, s.site); err != nil {
			t.Fatalf("bootstrap emergency: %v", err)
		}
	}
	return pkgRev
}

// grantEntitlement creates the Stay's ACTIVE Entitlement + its initial history in ONE transaction (the deferred
// coherence constraint forbids an Entitlement committing without history).
func grantEntitlement(t *testing.T, p *pgxpool.Pool, s scope, stayID, pkgRev string, activatedAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var svcRev, purchase, ent string
	if err := tx.QueryRow(ctx, `SELECT service_plan_revision_id::text FROM iam_v2.internet_package_revisions WHERE id=$1`, pkgRev).Scan(&svcRev); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state)
		VALUES ($1,$2,$3,$4,$5,'ADMIN_GRANT',0,'GRANTED') RETURNING id::text`,
		s.tenant, s.site, pkgRev, s.iface, stayID).Scan(&purchase); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id,site_id,stay_id,pms_interface_id,purchase_id,policy_snapshot,service_plan_revision_id,package_revision_id,
		 time_accounting_mode,end_mode,status,window_ends_at)
		VALUES ($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7,'VALIDITY_WINDOW','VALIDITY_WINDOW','ACTIVE',$8) RETURNING id::text`,
		s.tenant, s.site, stayID, s.iface, purchase, svcRev, pkgRev, activatedAt.Add(72*time.Hour)).Scan(&ent); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',$2,'GRANT')`, ent, activatedAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return ent
}

// insertLiveAt is insertLive with an explicit normalized PMS timestamp (the trusted checkout boundary source).
func insertLiveAt(t *testing.T, p *pgxpool.Pool, s scope, identity, eventType, payloadJSON string, ts time.Time) {
	t.Helper()
	if _, err := p.Exec(context.Background(), `INSERT INTO iam_v2.stay_events
		(tenant_id, site_id, pms_interface_id, external_event_identity, event_type, payload, pms_timestamp_utc,
		 admission_kind, admission_runtime_generation, resync_generation, received_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,'LIVE',1,0,now())`,
		s.tenant, s.site, s.iface, identity, eventType, payloadJSON, ts); err != nil {
		t.Fatalf("insert event %s: %v", identity, err)
	}
}

// TestIntegration_CheckoutSliceOneTransaction proves the Stay-Event application and the Checkout conversion are
// ONE physical transaction: the engine does NOT pre-flip the Stay with the server clock; the Converter derives
// the boundary from the SAME event, and the Stay flip, Entitlement termination, Grace creation, event APPLIED
// state, application lineage and audit all commit together.
func TestIntegration_CheckoutSliceOneTransaction(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	// arrival → IN_HOUSE, then an ACTIVE Entitlement granted before the checkout boundary
	insertLive(t, p, s, "E-GI", "GI", pay("R900", "900", "Slice", "Vera", "F900", "260101", "260105"))
	process(t, pr, s)
	var stayID string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R900'`, s.iface).Scan(&stayID); err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().Add(-90 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-2*time.Hour))

	// the GO event carries the trusted normalized checkout timestamp
	insertLiveAt(t, p, s, "E-GO", "GO", pay("R900", "900", "Slice", "Vera", "F900", "260101", "260105"), boundary)
	process(t, pr, s)

	// ONE transaction committed all of it
	var status string
	var effco time.Time
	var posting bool
	var lastEvent string
	if err := p.QueryRow(ctx, `SELECT status, effective_checkout_at, posting_allowed, last_applied_event_id::text
		FROM iam_v2.stays WHERE id=$1`, stayID).Scan(&status, &effco, &posting, &lastEvent); err != nil {
		t.Fatal(err)
	}
	if status != "CHECKED_OUT" || posting {
		t.Fatalf("stay=%s posting=%v, want CHECKED_OUT/false", status, posting)
	}
	// the boundary is the EVENT's normalized timestamp — proving the engine did not pre-flip with now()
	if !effco.Equal(boundary) {
		t.Fatalf("boundary %v != trusted event timestamp %v (engine must not pre-flip with the server clock)", effco, boundary)
	}
	// exact application lineage: the Stay points at the GO event that drove it
	var goEvent, goStatus string
	if err := p.QueryRow(ctx, `SELECT id::text, processing_status FROM iam_v2.stay_events
		WHERE pms_interface_id=$1 AND external_event_identity='E-GO'`, s.iface).Scan(&goEvent, &goStatus); err != nil {
		t.Fatal(err)
	}
	if goStatus != "APPLIED" || lastEvent != goEvent {
		t.Fatalf("event=%s last_applied_event_id=%s want APPLIED + %s", goStatus, lastEvent, goEvent)
	}
	// old Entitlement terminated, exactly one Grace created, audit cites the same event
	var oldStatus, oldReason string
	if err := p.QueryRow(ctx, `SELECT status, COALESCE(terminal_reason,'') FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&oldStatus, &oldReason); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "TERMINATED" || oldReason != "CONVERTED" {
		t.Fatalf("original entitlement %s/%s, want TERMINATED/CONVERTED", oldStatus, oldReason)
	}
	var graceN, auditN int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID).Scan(&graceN)
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1 AND boundary_event_id::text=$2`, stayID, goEvent).Scan(&auditN)
	if graceN != 1 || auditN != 1 {
		t.Fatalf("grace=%d audit-citing-event=%d, want 1/1", graceN, auditN)
	}
}

// TestIntegration_CheckoutSliceRollsBackTogether proves the all-or-nothing property: when the conversion fails
// (Emergency Grace required but its canonical catalog was never bootstrapped), NOTHING commits — the Event stays
// PENDING, the Stay stays IN_HOUSE with no boundary, the Entitlement stays ACTIVE, and no Grace/audit exists.
func TestIntegration_CheckoutSliceRollsBackTogether(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, false) // NO emergency catalog
	// make the configured policy unusable so the conversion must take the Emergency path
	if _, err := p.Exec(ctx, `SELECT iam_v2.publish_checkout_grace_config($1,$2,NULL,3600,4000,1500,524288000,2,'REJECT_NEW_DEVICE',86400)`, s.tenant, s.site); err != nil {
		t.Fatal(err)
	}
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	insertLive(t, p, s, "E-GI2", "GI", pay("R901", "901", "Roll", "Back", "F901", "260101", "260105"))
	process(t, pr, s)
	var stayID string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R901'`, s.iface).Scan(&stayID); err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().Add(-30 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-2*time.Hour))

	insertLiveAt(t, p, s, "E-GO2", "GO", pay("R901", "901", "Roll", "Back", "F901", "260101", "260105"), boundary)
	if _, err := pr.ProcessNext(ctx, s.tenant, s.site, s.iface); err == nil {
		t.Fatal("expected the checkout slice to fail (emergency catalog unavailable)")
	}

	// EVERYTHING rolled back together
	var evStatus string
	_ = p.QueryRow(ctx, `SELECT processing_status FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND external_event_identity='E-GO2'`, s.iface).Scan(&evStatus)
	if evStatus != "PENDING" {
		t.Fatalf("event=%s, want PENDING (application must roll back with the conversion)", evStatus)
	}
	var status string
	var effcoSet bool
	_ = p.QueryRow(ctx, `SELECT status, effective_checkout_at IS NOT NULL FROM iam_v2.stays WHERE id=$1`, stayID).Scan(&status, &effcoSet)
	if status != "IN_HOUSE" || effcoSet {
		t.Fatalf("stay=%s boundary_set=%v, want IN_HOUSE/false", status, effcoSet)
	}
	var entStatus string
	_ = p.QueryRow(ctx, `SELECT status FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&entStatus)
	if entStatus != "ACTIVE" {
		t.Fatalf("entitlement=%s, want ACTIVE (termination must roll back)", entStatus)
	}
	var graceN, auditN int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID).Scan(&graceN)
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, stayID).Scan(&auditN)
	if graceN != 0 || auditN != 0 {
		t.Fatalf("grace=%d audit=%d, want 0/0 after rollback", graceN, auditN)
	}
}

// TestIntegration_CheckoutWithoutConverterFailsClosed proves there is NO legacy Stay-domain-only checkout path:
// a GO event claimed by a Processor with no wired Converter fails closed and leaves the event PENDING and the
// Stay IN_HOUSE (rather than silently establishing an unverified server-clock boundary).
func TestIntegration_CheckoutWithoutConverterFailsClosed(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	commerce(t, p, s, true)
	bare := NewProcessor(p) // deliberately NO converter

	insertLive(t, p, s, "E-GI3", "GI", pay("R902", "902", "No", "Conv", "F902", "260101", "260105"))
	if _, err := bare.ProcessNext(ctx, s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("GI must still apply without a converter: %v", err)
	}
	insertLiveAt(t, p, s, "E-GO3", "GO", pay("R902", "902", "No", "Conv", "F902", "260101", "260105"), time.Now().Add(-time.Hour))
	if _, err := bare.ProcessNext(ctx, s.tenant, s.site, s.iface); err == nil {
		t.Fatal("a GO event without a wired Converter must FAIL CLOSED")
	}
	var evStatus, status string
	var effcoSet bool
	_ = p.QueryRow(ctx, `SELECT processing_status FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND external_event_identity='E-GO3'`, s.iface).Scan(&evStatus)
	_ = p.QueryRow(ctx, `SELECT status, effective_checkout_at IS NOT NULL FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R902'`, s.iface).Scan(&status, &effcoSet)
	if evStatus != "PENDING" || status != "IN_HOUSE" || effcoSet {
		t.Fatalf("fail-closed expected PENDING/IN_HOUSE/no-boundary, got %s/%s/%v", evStatus, status, effcoSet)
	}
}

// TestIntegration_EventOrderingUnderConcurrency proves per-Interface ordered application: with GI and GO for the
// SAME Stay queued together and many processors racing, the GO can never be applied before the GI (which would
// orphan it into MANUAL_REVIEW). Also proves >=24 concurrent integrated processors apply every event exactly
// once with no deadlock and exactly one Grace.
func TestIntegration_EventOrderingUnderConcurrency(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	// queue GI and GO for the same reservation BEFORE any processing, so ordering is genuinely contested
	insertLive(t, p, s, "O-GI", "GI", pay("R910", "910", "Ord", "Er", "F910", "260101", "260105"))
	insertLiveAt(t, p, s, "O-GO", "GO", pay("R910", "910", "Ord", "Er", "F910", "260101", "260105"), time.Now().Add(-time.Hour))

	const n = 24
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				ok, err := pr.ProcessNext(context.Background(), s.tenant, s.site, s.iface)
				if err != nil {
					errs <- err
					return
				}
				if !ok {
					return
				}
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("integrated concurrency did not drain — possible deadlock")
	}
	close(errs)
	for e := range errs {
		t.Fatalf("concurrent integrated processing error: %v", e)
	}

	// ORDER held: the GO applied against an existing Stay (never an orphan MANUAL_REVIEW)
	giStatus, _ := eventOutcome(t, p, s, "O-GI")
	goStatus, goReview := eventOutcome(t, p, s, "O-GO")
	if giStatus != "APPLIED" || goStatus != "APPLIED" {
		t.Fatalf("both events must apply exactly once: GI=%s GO=%s (review=%s)", giStatus, goStatus, goReview)
	}
	var status string
	var stayID string
	if err := p.QueryRow(ctx, `SELECT id::text, status FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R910'`, s.iface).Scan(&stayID, &status); err != nil {
		t.Fatal(err)
	}
	if status != "CHECKED_OUT" {
		t.Fatalf("stay=%s, want CHECKED_OUT (ordered GI then GO)", status)
	}
	// exactly one audit for the episode (no double conversion under 24-way concurrency)
	var auditN int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, stayID).Scan(&auditN)
	if auditN != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", auditN)
	}
}

// TestIntegration_LateStageRollback forces a failure LATE in the conversion — after the boundary is set and the
// original Entitlement has been terminated, the grace Purchase insert hits one_conversion_per_episode — and
// proves the whole slice still rolls back: event PENDING, Stay IN_HOUSE with no boundary, Entitlement ACTIVE.
func TestIntegration_LateStageRollback(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	insertLive(t, p, s, "L-GI", "GI", pay("R920", "920", "Late", "Stage", "F920", "260101", "260105"))
	process(t, pr, s)
	var stayID string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R920'`, s.iface).Scan(&stayID); err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().Add(-time.Hour).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-2*time.Hour))

	// Pre-plant a CHECKOUT_GRACE purchase for THIS episode but NO audit row: the conversion passes its audit
	// idempotency gate, terminates the original Entitlement, and only then collides on
	// purchases.one_conversion_per_episode — a genuine LATE-stage failure.
	if _, err := p.Exec(ctx, `INSERT INTO iam_v2.purchases
		(tenant_id,site_id,package_revision_id,pms_interface_id,stay_id,trigger,amount_minor,state,checkout_episode)
		VALUES ($1,$2,$3,$4,$5,'CHECKOUT_GRACE',0,'GRANTED',1)`, s.tenant, s.site, pkgRev, s.iface, stayID); err != nil {
		t.Fatal(err)
	}

	insertLiveAt(t, p, s, "L-GO", "GO", pay("R920", "920", "Late", "Stage", "F920", "260101", "260105"), boundary)
	if _, err := pr.ProcessNext(ctx, s.tenant, s.site, s.iface); err == nil {
		t.Fatal("expected a LATE-stage conversion failure (duplicate episode purchase)")
	}

	var evStatus, status, entStatus string
	var effcoSet bool
	_ = p.QueryRow(ctx, `SELECT processing_status FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND external_event_identity='L-GO'`, s.iface).Scan(&evStatus)
	_ = p.QueryRow(ctx, `SELECT status, effective_checkout_at IS NOT NULL FROM iam_v2.stays WHERE id=$1`, stayID).Scan(&status, &effcoSet)
	_ = p.QueryRow(ctx, `SELECT status FROM iam_v2.entitlements WHERE id=$1`, ent).Scan(&entStatus)
	if evStatus != "PENDING" || status != "IN_HOUSE" || effcoSet || entStatus != "ACTIVE" {
		t.Fatalf("late-stage rollback incomplete: event=%s stay=%s boundary=%v entitlement=%s", evStatus, status, effcoSet, entStatus)
	}
	var graceN, auditN int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID).Scan(&graceN)
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, stayID).Scan(&auditN)
	if graceN != 0 || auditN != 0 {
		t.Fatalf("late-stage rollback left grace=%d audit=%d, want 0/0", graceN, auditN)
	}
}
