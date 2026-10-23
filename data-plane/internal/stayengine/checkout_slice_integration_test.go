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

// commerce seeds the Grace catalog and config the Checkout Converter needs, optionally the Emergency catalog; returns the grace package revision id.
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
	                 '{"end_mode":"GRACE_AFTER_CHECKOUT","grace_duration_seconds":3600,"policy_version":"CHECKOUT_GRACE_V1"}'::jsonb FROM ip, spr RETURNING id)
	SELECT id::text FROM ipr`, s.tenant, s.site).Scan(&pkgRev); err != nil {
		t.Fatalf("seed commerce: %v", err)
	}
	if _, err := p.Exec(ctx, `UPDATE iam_v2.internet_packages SET current_revision_id=$1
		WHERE id=(SELECT package_id FROM iam_v2.internet_package_revisions WHERE id=$1)`, pkgRev); err != nil {
		t.Fatalf("pin current revision: %v", err)
	}
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

// grantEntitlement creates the Entitlement and its history in one transaction (a deferred constraint forbids one without the other).
func grantEntitlement(t *testing.T, p *pgxpool.Pool, s scope, stayID, pkgRev string, activatedAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`); err != nil {
		t.Fatal(err)
	}
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

// insertLiveAt is insertLive with an explicit normalized PMS timestamp (the trusted checkout boundary).
func insertLiveAt(t *testing.T, p *pgxpool.Pool, s scope, identity, eventType, payloadJSON string, ts time.Time) {
	t.Helper()
	if err := guardedExec(p, stayFamily, `INSERT INTO iam_v2.stay_events
		(tenant_id, site_id, pms_interface_id, external_event_identity, event_type, payload, pms_timestamp_utc,
		 admission_kind, admission_runtime_generation, resync_generation, received_at)
		VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7,'LIVE',1,0,now())`,
		s.tenant, s.site, s.iface, identity, eventType, payloadJSON, ts); err != nil {
		t.Fatalf("insert event %s: %v", identity, err)
	}
}

// The event application and Checkout conversion are one transaction: the engine does not pre-flip the Stay with
// the server clock, and flip, termination, Grace, APPLIED state, lineage and audit commit together.
func TestIntegration_CheckoutSliceOneTransaction(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

	insertLive(t, p, s, "E-GI", "GI", pay("R900", "900", "Slice", "Vera", "F900", "260101", "260105"))
	process(t, pr, s)
	var stayID string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R900'`, s.iface).Scan(&stayID); err != nil {
		t.Fatal(err)
	}
	boundary := time.Now().Add(-90 * time.Minute).Truncate(time.Microsecond)
	ent := grantEntitlement(t, p, s, stayID, pkgRev, boundary.Add(-2*time.Hour))

	insertLiveAt(t, p, s, "E-GO", "GO", pay("R900", "900", "Slice", "Vera", "F900", "260101", "260105"), boundary)
	process(t, pr, s)

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
	if !effco.Equal(boundary) {
		t.Fatalf("boundary %v != trusted event timestamp %v (engine must not pre-flip with the server clock)", effco, boundary)
	}
	var goEvent, goStatus string
	if err := p.QueryRow(ctx, `SELECT id::text, processing_status FROM iam_v2.stay_events
		WHERE pms_interface_id=$1 AND external_event_identity='E-GO'`, s.iface).Scan(&goEvent, &goStatus); err != nil {
		t.Fatal(err)
	}
	if goStatus != "APPLIED" || lastEvent != goEvent {
		t.Fatalf("event=%s last_applied_event_id=%s want APPLIED + %s", goStatus, lastEvent, goEvent)
	}
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

// When the conversion fails (Emergency catalog not bootstrapped) nothing commits: event PENDING, Stay IN_HOUSE, Entitlement ACTIVE, no Grace or audit.
func TestIntegration_CheckoutSliceRollsBackTogether(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pkgRev := commerce(t, p, s, false) // NO emergency catalog
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

// A GO claimed by a Processor with no Converter fails closed (event PENDING, Stay IN_HOUSE) rather than setting an unverified server-clock boundary.
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

// With GI and GO queued together and 24 processors racing, the GO is never applied before the GI (it would
// orphan into MANUAL_REVIEW), every event applies once, and there is no deadlock.
func TestIntegration_EventOrderingUnderConcurrency(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))

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
	var auditN int
	_ = p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, stayID).Scan(&auditN)
	if auditN != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", auditN)
	}
}

// A failure late in the conversion (after termination, the grace Purchase hits one_conversion_per_episode) still
// rolls back the whole slice.
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

	// A pre-planted CHECKOUT_GRACE purchase with no audit row passes the audit gate, terminates the Entitlement,
	// and only then collides on purchases.one_conversion_per_episode.
	if err := guardedExec(p, []string{"commerce_intent"}, `INSERT INTO iam_v2.purchases
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

func paySharers(res, room, folio string, sharers string) string {
	return `{"reservation":"` + res + `","room":"` + room + `","folio":"` + folio +
		`","arrival_raw":"260101","departure_raw":"260105","sharers":[` + sharers + `]}`
}

// Sharing a Stay is legal: several occupants with exactly one primary, which can move across events without duplicating.
func TestIntegration_SharersAndFolios(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pr := NewProcessor(p)

	insertLive(t, p, s, "SH-1", "GI", paySharers("R700", "700", "F700",
		`{"external_guest_id":"G1","first_name":"Ada","last_name":"Byron","is_primary":true},`+
			`{"external_guest_id":"G2","first_name":"Chas","last_name":"Babbage"}`))
	process(t, pr, s)
	var stayID string
	if err := p.QueryRow(ctx, `SELECT id::text FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R700'`, s.iface).Scan(&stayID); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_guests WHERE stay_id=$1`, stayID); n != 2 {
		t.Fatalf("occupants = %d, want 2 (sharing a stay is legal)", n)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_guests WHERE stay_id=$1 AND is_primary`, stayID); n != 1 {
		t.Fatalf("primaries = %d, want exactly 1", n)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_guests WHERE stay_id=$1 AND external_guest_id='G1' AND is_primary`, stayID); n != 1 {
		t.Fatal("the flagged occupant is not the primary")
	}
	// The FO field is not stored (a room charge targets the reservation), so no folio table may exist.
	assertNoFolioTables(t, p)

	insertLive(t, p, s, "SH-2", "GC", paySharers("R700", "700", "F700",
		`{"external_guest_id":"G2","first_name":"Chas","last_name":"Babbage","is_primary":true},`+
			`{"external_guest_id":"G3","first_name":"Mary","last_name":"Somerville"}`))
	process(t, pr, s)
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_guests WHERE stay_id=$1`, stayID); n != 3 {
		t.Fatalf("occupants after the change = %d, want 3", n)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_guests WHERE stay_id=$1 AND is_primary AND external_guest_id='G2'`, stayID); n != 1 {
		t.Fatal("the primary did not move to G2")
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_guests WHERE stay_id=$1 AND is_primary`, stayID); n != 1 {
		t.Fatal("more than one primary after moving it")
	}
}

// A self-contradicting payload goes to MANUAL_REVIEW with a bounded code and writes nothing.
func TestIntegration_ContradictorySharerPayloadGoesToReview(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pr := NewProcessor(p)
	for _, tc := range []struct{ name, ident, sharers, code string }{
		{"two-primaries", "SH-P", `{"external_guest_id":"G1","last_name":"A","is_primary":true},{"external_guest_id":"G2","last_name":"B","is_primary":true}`, CodeSharerTwoPrimary},
		{"duplicate-identity", "SH-D", `{"external_guest_id":"G9","last_name":"A"},{"external_guest_id":"G9","last_name":"B"}`, CodeSharerDuplicate},
	} {
		insertLive(t, p, s, tc.ident, "GI", paySharers("R70"+tc.ident, "701", "F701", tc.sharers))
		process(t, pr, s)
		var status, code string
		if err := p.QueryRow(ctx, `SELECT processing_status, COALESCE(review_code,'') FROM iam_v2.stay_events
			WHERE pms_interface_id=$1 AND external_event_identity=$2`, s.iface, tc.ident).Scan(&status, &code); err != nil {
			t.Fatal(err)
		}
		if status != "MANUAL_REVIEW" || code != tc.code {
			t.Fatalf("%s: status=%s code=%s, want MANUAL_REVIEW/%s", tc.name, status, code, tc.code)
		}
		if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id=$2`,
			s.iface, "R70"+tc.ident); n != 0 {
			t.Fatalf("%s: a contradictory payload created a stay", tc.name)
		}
	}
}

// A room charge targets the reservation, not a folio, so the FO field is ignored: two reservations with the same
// FO both apply normally, with no review code and no source-conflict row.
func TestIntegration_FolioFieldAppliesWithoutFolioSideEffect(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	pr := NewProcessor(p)
	insertLive(t, p, s, "FC-1", "GI", paySharers("R710", "710", "FSHARED", `{"external_guest_id":"G1","last_name":"First","is_primary":true}`))
	process(t, pr, s)
	insertLive(t, p, s, "FC-2", "GI", paySharers("R711", "711", "FSHARED", `{"external_guest_id":"G2","last_name":"Second","is_primary":true}`))
	process(t, pr, s)
	for _, ident := range []string{"FC-1", "FC-2"} {
		if st, code := eventOutcome(t, p, s, ident); st != "APPLIED" || code != "" {
			t.Fatalf("%s: status=%s code=%q, want APPLIED with no review code (an FO field is not a conflict)", ident, st, code)
		}
	}
	for _, res := range []string{"R710", "R711"} {
		if st, lv, _ := stayState(t, p, s, res); st != "IN_HOUSE" || lv != 1 {
			t.Fatalf("%s: status=%s lv=%d, want IN_HOUSE/1", res, st, lv)
		}
	}
	insertLive(t, p, s, "FC-3", "GC", paySharers("R710", "712", "FSHARED", `{"external_guest_id":"G1","last_name":"First","is_primary":true}`))
	process(t, pr, s)
	if st, code := eventOutcome(t, p, s, "FC-3"); st != "APPLIED" || code != "" {
		t.Fatalf("FC-3: status=%s code=%q, want APPLIED", st, code)
	}
	if st, _, room := stayState(t, p, s, "R710"); st != "IN_HOUSE" || room != "712" {
		t.Fatalf("R710 after GC: status=%s room=%s, want IN_HOUSE/712", st, room)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.pms_source_conflicts
		WHERE site_id=$1 AND (interface_a=$2 OR interface_b=$2)`, s.site, s.iface); n != 0 {
		t.Fatalf("source conflicts = %d, want 0 (an FO field never conflicts)", n)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_events
		WHERE pms_interface_id=$1 AND processing_status='MANUAL_REVIEW'`, s.iface); n != 0 {
		t.Fatalf("MANUAL_REVIEW events = %d, want 0", n)
	}
	assertNoFolioTables(t, p)
}

func assertNoFolioTables(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	var folios, stayFolios *string
	if err := p.QueryRow(context.Background(),
		`SELECT to_regclass('iam_v2.folios')::text, to_regclass('iam_v2.stay_folios')::text`).Scan(&folios, &stayFolios); err != nil {
		t.Fatal(err)
	}
	if folios != nil || stayFolios != nil {
		t.Fatal("a folio table exists; migration 0100 drops iam_v2.folios and iam_v2.stay_folios")
	}
}

func countRows(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A room move changes only the room number: Stay identity, episode counter, occupants and reservation survive, since the room is evidence, never identity.
func TestIntegration_F1_RoomMovePreservesTheStay(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	pr := NewProcessor(p)
	insertLive(t, p, s, "F1-GI", "GI", paySharers("R800", "800", "F800",
		`{"external_guest_id":"G1","first_name":"Ada","last_name":"Byron","is_primary":true},`+
			`{"external_guest_id":"G2","first_name":"Chas","last_name":"Babbage"}`))
	process(t, pr, s)
	var stayID string
	var lifecycle int
	if err := p.QueryRow(ctx, `SELECT id::text, lifecycle_version FROM iam_v2.stays
		WHERE pms_interface_id=$1 AND external_reservation_id='R800'`, s.iface).Scan(&stayID, &lifecycle); err != nil {
		t.Fatal(err)
	}
	insertLive(t, p, s, "F1-MOVE", "GC", paySharers("R800", "915", "F800", `{"external_guest_id":"G1","first_name":"Ada","last_name":"Byron","is_primary":true}`))
	process(t, pr, s)

	var movedID, room string
	var movedLifecycle int
	if err := p.QueryRow(ctx, `SELECT id::text, COALESCE(normalized_room_number,''), lifecycle_version FROM iam_v2.stays
		WHERE pms_interface_id=$1 AND external_reservation_id='R800'`, s.iface).Scan(&movedID, &room, &movedLifecycle); err != nil {
		t.Fatal(err)
	}
	if movedID != stayID {
		t.Fatal("a room move created a new Stay — the room is evidence, not identity")
	}
	if room != "915" {
		t.Fatalf("room = %q, want the moved room 915", room)
	}
	if movedLifecycle != lifecycle {
		t.Fatalf("lifecycle_version moved from %d to %d — a room move is not a new episode", lifecycle, movedLifecycle)
	}
	if n := countRows(t, p, `SELECT count(*) FROM iam_v2.stay_guests WHERE stay_id=$1`, stayID); n != 2 {
		t.Fatalf("occupants after the move = %d, want the original 2 preserved", n)
	}
	var resv string
	if err := p.QueryRow(ctx, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1`, stayID).Scan(&resv); err != nil || resv != "R800" {
		t.Fatalf("reservation after the move = %q (%v), want R800 preserved", resv, err)
	}
}

// A replayed LIVE identity is refused by the inbox, and a late event against a departed Stay goes to review
// without touching the Stay's state or lineage.
func TestIntegration_F2_StaleEventIsANoOp(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	s := seed(t, p)
	commerce(t, p, s, true)
	pr := NewProcessorWithCheckout(p, checkout.NewConverter(p))
	insertLive(t, p, s, "F2-GI", "GI", pay("R810", "810", "Stale", "Event", "F810", "260101", "260105"))
	process(t, pr, s)

	// The 'stay' family is opened so the refusal is genuine identity uniqueness, not the writer guard.
	if err := guardedExec(p, stayFamily, `INSERT INTO iam_v2.stay_events
		(tenant_id, site_id, pms_interface_id, external_event_identity, event_type, payload, pms_timestamp_utc,
		 admission_kind, admission_runtime_generation, resync_generation, received_at)
		VALUES ($1,$2,$3,'F2-GI','GI',$4::jsonb,now(),'LIVE',1,0,now())`,
		s.tenant, s.site, s.iface, pay("R810", "999", "Stale", "Event", "F810", "260101", "260105")); err == nil {
		t.Fatal("the durable inbox admitted a replayed LIVE event identity")
	}

	insertLiveAt(t, p, s, "F2-GO", "GO", pay("R810", "810", "Stale", "Event", "F810", "260101", "260105"), time.Now().Add(-time.Hour))
	process(t, pr, s)
	var stayID, room, status, lineage string
	var version int
	if err := p.QueryRow(ctx, `SELECT id::text, COALESCE(normalized_room_number,''), status, last_applied_event_version, last_applied_event_id::text
		FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id='R810'`, s.iface).Scan(&stayID, &room, &status, &version, &lineage); err != nil {
		t.Fatal(err)
	}
	insertLive(t, p, s, "F2-LATE", "GC", pay("R810", "999", "Stale", "Event", "F810", "260101", "260105"))
	process(t, pr, s)

	var afterRoom, afterStatus, afterLineage string
	var afterVersion int
	if err := p.QueryRow(ctx, `SELECT COALESCE(normalized_room_number,''), status, last_applied_event_version, last_applied_event_id::text
		FROM iam_v2.stays WHERE id=$1`, stayID).Scan(&afterRoom, &afterStatus, &afterVersion, &afterLineage); err != nil {
		t.Fatal(err)
	}
	if afterRoom != room || afterStatus != status || afterVersion != version || afterLineage != lineage {
		t.Fatalf("a stale event changed the Stay: room %q->%q status %q->%q version %d->%d lineage %s->%s",
			room, afterRoom, status, afterStatus, version, afterVersion, lineage, afterLineage)
	}
	outcome, code := eventOutcome(t, p, s, "F2-LATE")
	if outcome != "MANUAL_REVIEW" || code != "GC_ON_TERMINAL_STAY" {
		t.Fatalf("late event outcome=%s code=%s, want MANUAL_REVIEW/GC_ON_TERMINAL_STAY", outcome, code)
	}
}

// The database, not in-process loop bookkeeping, prevents double application: processors on separate pools stand
// in for two daemons, and the advisory barrier plus the transactional claim admit one application per event.
func TestIntegration_TwoDaemonProcessesCannotApplyOneEventTwice(t *testing.T) {
	ctx := context.Background()
	p1 := pool(t)
	defer p1.Close()
	p2 := pool(t)
	defer p2.Close()

	s := seed(t, p1)
	commerce(t, p1, s, true)
	daemonA := NewProcessorWithCheckout(p1, checkout.NewConverter(p1))
	daemonB := NewProcessorWithCheckout(p2, checkout.NewConverter(p2))

	insertLive(t, p1, s, "TP-GI", "GI", pay("R930", "930", "Two", "Daemons", "F930", "260101", "260105"))
	insertLiveAt(t, p1, s, "TP-GO", "GO", pay("R930", "930", "Two", "Daemons", "F930", "260101", "260105"),
		time.Now().Add(-time.Hour))

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for _, d := range []*Processor{daemonA, daemonB} {
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(d *Processor) {
				defer wg.Done()
				for {
					ok, err := d.ProcessNext(context.Background(), s.tenant, s.site, s.iface)
					if err != nil {
						errs <- err
						return
					}
					if !ok {
						return
					}
				}
			}(d)
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("two-process application did not drain — possible cross-process deadlock")
	}
	close(errs)
	for e := range errs {
		t.Fatalf("cross-process application error: %v", e)
	}

	giStatus, _ := eventOutcome(t, p1, s, "TP-GI")
	goStatus, goReview := eventOutcome(t, p1, s, "TP-GO")
	if giStatus != "APPLIED" || goStatus != "APPLIED" {
		t.Fatalf("GI=%s GO=%s (review=%s): both events must apply exactly once", giStatus, goStatus, goReview)
	}
	var stayID, status string
	var version int
	if err := p1.QueryRow(ctx, `SELECT id::text, status, last_applied_event_version FROM iam_v2.stays
		WHERE pms_interface_id=$1 AND external_reservation_id='R930'`, s.iface).Scan(&stayID, &status, &version); err != nil {
		t.Fatal(err)
	}
	if status != "CHECKED_OUT" {
		t.Fatalf("stay=%s, want CHECKED_OUT", status)
	}
	if version != 2 {
		t.Fatalf("last_applied_event_version = %d, want exactly 2 (one per event, applied once each)", version)
	}
	if n := countRows(t, p1, `SELECT count(*) FROM iam_v2.checkout_grace_audit WHERE stay_id=$1`, stayID); n != 1 {
		t.Fatalf("checkout audits = %d, want exactly 1", n)
	}
	// The Stay never held an entitlement, so no grace is produced; the audit is the evidence the conversion ran once.
	if n := countRows(t, p1, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND end_mode='GRACE_AFTER_CHECKOUT'`, stayID); n != 0 {
		t.Fatalf("grace entitlements = %d: a Stay with no entitlement at the boundary must earn none", n)
	}
}
