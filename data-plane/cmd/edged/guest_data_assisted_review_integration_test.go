//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Regressions for the independent review of migration 0146 (2026-10-20): each of these was a way a record of another
// person, or of nobody in particular, could be offered to the requester, or a way the activity log could keep what
// the check refuses.

// asstWithoutTriggers runs fn in a transaction with triggers off: a history row a guard would make awkward to build.
func asstWithoutTriggers(t *testing.T, f *apiFixture, fn func(tx pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// H1: the PMS matched an attempt on the stated address to a guest who is not the stated one.
func TestIntegration_Assisted_AnAttemptTheMatchedToAnotherGuestIsNotTheStatedGuests(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "RV1", asstMACs("3c:22:fb:e1:00", 1), "10.91.1")
	other := seedAsstRoom(t, f, "RV1B", nil, "10.91.12")
	ada := gdStr(t, f, `SELECT id::text FROM iam_v2.stay_guests WHERE stay_id=$1`, r.stay)
	bob := gdStr(t, f, `SELECT id::text FROM iam_v2.stay_guests WHERE stay_id=$1`, other.stay)
	var forBob, forAda string
	for _, x := range []struct {
		guest string
		dst   *string
	}{{bob, &forBob}, {ada, &forAda}} {
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind,submitted_room,device_mac,device_ip,matched_stay_id,matched_guest_id)
			VALUES ($1,$2,now() - interval '4 days','VERIFIED_NO_ELIGIBLE_PACKAGE','FULL_NAME','101',$3::macaddr,'10.91.1.9',$4::uuid,$5::uuid) RETURNING id::text`,
			f.tenant, f.site, r.macs[0], r.stay, x.guest).Scan(x.dst); err != nil {
			t.Fatalf("seed attempt: %v", err)
		}
	}
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	if c := rv.find("sign_in_attempt", forBob); c == nil || c.Class != "CONFLICTING" {
		t.Errorf("the attempt the PMS matched to another guest = %+v", c)
	}
	// The address was used by someone the stated guest is not: nothing keyed by it or given by the room is theirs.
	if c := rv.find("session", r.sess[0]); c == nil || c.Class != "CONFLICTING" || c.Reason != "device_used_by_another_person" {
		t.Errorf("the room's session on an address another guest used = %+v", c)
	}
	if len(rv.selectable()) != 0 {
		t.Fatalf("selectable: %v", rv.selectable())
	}
	if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("sign_in_attempt", forBob)}); st != 409 {
		t.Errorf("hand-over of the other guest's attempt = %d %s", st, raw)
	}
	// Without it, the stated guest's own attempt is theirs to claim.
	if _, err := f.pool.Exec(ctx, `DELETE FROM iam_v2.sign_in_attempts WHERE id=$1::uuid`, forBob); err != nil {
		t.Skipf("sign-in attempts cannot be removed here: %v", err)
	}
	id2, _ := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	if c := asstReviewOf(t, f, id2).find("sign_in_attempt", forAda); c == nil || c.Class != "CLAIMABLE" {
		t.Errorf("the stated guest's own attempt = %+v", c)
	}
}

// H2: one address, two device rows. The answer is about the address.
func TestIntegration_Assisted_AnAddressWithTwoDeviceRowsIsJudgedWhole(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	a := seedAsstRoom(t, f, "RV2A", asstMACs("3c:22:fb:e2:00", 1), "10.91.2")
	b := seedAsstRoom(t, f, "RV2B", nil, "10.91.3")
	// The same address, seen by a second appliance, registered to the other reservation only (no session).
	var d2 string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac, first_seen, last_seen, last_ip)
		VALUES ($1,$2,gen_random_uuid(),$3::macaddr, now() - interval '5 days', now() - interval '2 days', '10.91.3.1'::inet) RETURNING id::text`,
		f.tenant, f.site, a.macs[0]).Scan(&d2); err != nil {
		t.Fatalf("seed second row: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices (tenant_id, site_id, entitlement_id, device_id) VALUES ($1,$2,$3::uuid,$4::uuid)`,
		f.tenant, f.site, b.room, d2); err != nil {
		t.Fatalf("register to the other reservation: %v", err)
	}
	// Whatever order the rows are read in, the answer is the same.
	for i := 0; i < 6; i++ {
		id, ref := asstOpen(t, f, asstDefaults("ACCESS", a.macs, a.anchor()))
		rv := asstReviewOf(t, f, id)
		if len(rv.selectable()) != 0 {
			t.Fatalf("run %d: something is selectable on an address another reservation registered a device row for: %+v", i, rv.Records)
		}
		if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("device", d2)}); st != 409 {
			t.Fatalf("run %d: the other reservation's device row was handed over: %d %s", i, st, raw)
		}
	}
}

// H3: past use leaves a trace even when no session or binding remains.
func TestIntegration_Assisted_PastUseByAnotherAccessLeavesTheAddressUnclaimable(t *testing.T) {
	for name, seed := range map[string]func(t *testing.T, f *apiFixture, otherEnt, dev string){
		"an authorisation that was released": func(t *testing.T, f *apiFixture, otherEnt, dev string) {
			asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `INSERT INTO iam_v2.entitlement_device_authorizations(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at,deauthorized_at)
					VALUES ($1,$2,$3::uuid,$4::uuid,1,now() - interval '4 days', now() - interval '3 days')`, f.tenant, f.site, otherEnt, dev)
				return err
			})
		},
		"a slot action by staff": func(t *testing.T, f *apiFixture, otherEnt, dev string) {
			asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `INSERT INTO iam_v2.device_slot_actions(tenant_id,site_id,entitlement_id,device_id,actor,operator_label,action,outcome,reason,acted_at)
					VALUES ($1,$2,$3::uuid,$4::uuid,'STAFF','desk','RELEASE','OK','guest asked', now() - interval '3 days')`, f.tenant, f.site, otherEnt, dev)
				return err
			})
		},
		"a release the guest made": func(t *testing.T, f *apiFixture, otherEnt, dev string) {
			asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `INSERT INTO iam_v2.guest_device_actions(tenant_id,site_id,entitlement_id,device_id,action,outcome,acted_at)
					VALUES ($1,$2,$3::uuid,$4::uuid,'RELEASE','OK', now() - interval '3 days')`, f.tenant, f.site, otherEnt, dev)
				return err
			})
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newAPI(t)
			a := seedAsstRoom(t, f, "RV3A", asstMACs("3c:22:fb:e3:00", 1), "10.91.4")
			b := seedAsstRoom(t, f, "RV3B", nil, "10.91.5")
			id0, _ := asstOpen(t, f, asstDefaults("ACCESS", a.macs, a.anchor()))
			if len(asstReviewOf(t, f, id0).selectable()) != 2 {
				t.Fatal("before the other use the session and the device are claimable")
			}
			seed(t, f, b.room, a.devs[0])
			id, ref := asstOpen(t, f, asstDefaults("ACCESS", a.macs, a.anchor()))
			rv := asstReviewOf(t, f, id)
			if c := rv.find("session", a.sess[0]); c == nil || c.Class != "CONFLICTING" {
				t.Errorf("the session after the address was used by another access = %+v", c)
			}
			if c := rv.of("device"); len(c) != 1 || c[0].Class != "CONFLICTING" || c[0].Selectable {
				t.Errorf("the device = %+v", c)
			}
			if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("device", a.devs[0])}); st != 409 {
				t.Errorf("hand-over of the device = %d %s", st, raw)
			}
		})
	}
}

// H4: a sign-in pass in force is not anonymised, which would end it early.
func TestIntegration_Assisted_APassInForceIsNotErased(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "RV4", asstMACs("3c:22:fb:e4:00", 1), "10.91.6")
	var pause, pass string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_signin_network_pauses(tenant_id,site_id,guest_network_id,started_at,expires_at,failure_count)
		VALUES ($1,$2,gen_random_uuid(), now() - interval '2 days', now() + interval '5 days', 30) RETURNING id::text`, f.tenant, f.site).Scan(&pause); err != nil {
		t.Fatalf("seed pause: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_signin_device_passes(pause_id,tenant_id,site_id,device_mac,granted_at,granted_by,reason)
		VALUES ($1::uuid,$2,$3,$4::macaddr, now() - interval '2 days','desk','confirmed guest') RETURNING id::text`, pause, f.tenant, f.site, r.macs[0]).Scan(&pass); err != nil {
		t.Fatalf("seed pass: %v", err)
	}
	// For a hand-over it may be selected; for an erasure it is live.
	ida, refa := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	if c := asstReviewOf(t, f, ida).find("sign_in_pass", pass); c == nil || !c.Selectable {
		t.Errorf("the pass for a hand-over = %+v", c)
	}
	_ = refa
	ide, refe := asstOpen(t, f, asstDefaults("ERASURE", r.macs, r.anchor()))
	rve := asstReviewOf(t, f, ide)
	if c := rve.find("sign_in_pass", pass); c == nil || c.Selectable || c.Block == nil || *c.Block != "live" {
		t.Fatalf("a pass in force for an erasure = %+v", c)
	}
	if st, raw := asstAct(f, t, "erase", ide, rve, refe, []map[string]any{sel("sign_in_pass", pass)}); st != 409 || !strings.Contains(raw, "blocked_live") {
		t.Errorf("erase of a pass in force = %d %s", st, raw)
	}
	if got := gdStr(t, f, `SELECT device_mac::text FROM iam_v2.guest_signin_device_passes WHERE id=$1::uuid`, pass); got != r.macs[0] {
		t.Errorf("the pass changed: %s", got)
	}
}

// H5: a reason the check refuses is never written to the activity log.
func TestIntegration_Assisted_ARefusedReasonNeverReachesTheActivityLog(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "RV5", asstMACs("3c:22:fb:e5:00", 1), "10.91.7")
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", r.macs, r.anchor()))
	rv := asstReviewOf(t, f, id)
	for verb, reason := range map[string]string{
		"export": "phone " + r.macs[0] + " shown at the desk",
		"close":  "Ada Lovelace withdrew",
	} {
		body := map[string]any{"password": f.password, "reason": reason, "confirm": ref, "signature": rv.Signature, "selected": rv.selectable(), "outcome": "CANCELLED"}
		if st, raw := f.doRaw(t, "POST", "/guest-data-rights/assisted/"+id+"/"+verb, body); st != 400 {
			t.Errorf("%s with a reason that names the guest or the address = %d %s", verb, st, raw)
		}
	}
	for _, leak := range []string{r.macs[0], "Lovelace"} {
		if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND payload::text ILIKE '%'||$2||'%'`, f.tenant, leak); got != "0" {
			t.Errorf("the activity log holds %q from a refused reason (%s entries)", leak, got)
		}
	}
	if got := asstStatus(t, f, id)["status"]; got != "OPEN" {
		t.Errorf("a refused close must leave the request open: %v", got)
	}
}

// H6: a record keyed only by the address says nothing about the stated context when nothing ties the two.
func TestIntegration_Assisted_AnAddressNothingTiesToTheContextIsLeftAlone(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "RV6", nil, "10.91.8")
	mac := "3c:22:fb:e6:00:01"
	var attempt, restr string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind,submitted_room,device_mac,device_ip)
		VALUES ($1,$2,now() - interval '4 days','CREDENTIAL_MISMATCH','UNKNOWN','202',$3::macaddr,'10.91.8.9') RETURNING id::text`, f.tenant, f.site, mac).Scan(&attempt); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_signin_restrictions(tenant_id,site_id,device_mac,guest_network_name,last_submitted_room,restricted_at,expires_at,
		  failure_count,reason,released_at,released_by,release_reason)
		VALUES ($1,$2,$3::macaddr,'Guest','202',now() - interval '4 days',now() - interval '4 days' + interval '1 hour',5,'FAILED_CREDENTIAL_THRESHOLD',
		  now() - interval '4 days' + interval '30 minutes','op-1','released') RETURNING id::text`, f.tenant, f.site, mac).Scan(&restr); err != nil {
		t.Fatalf("seed restriction: %v", err)
	}
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", []string{mac}, r.anchor()))
	rv := asstReviewOf(t, f, id)
	for _, x := range []struct{ kind, id string }{{"sign_in_attempt", attempt}, {"sign_in_restriction", restr}} {
		if c := rv.find(x.kind, x.id); c == nil || c.Class != "UNVERIFIABLE" || c.Reason != "not_tied_to_the_stated_context" {
			t.Errorf("%s on an address nothing ties to the stay = %+v", x.kind, c)
		}
	}
	if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("sign_in_attempt", attempt)}); st != 409 {
		t.Errorf("hand-over = %d %s", st, raw)
	}
}

// Two requests on an address that has no device row still run one after the other.
func TestIntegration_Assisted_TwoRequestsOnAnAddressWithNoDeviceRowRunOnce(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "RV7", nil, "10.91.9")
	mac := "3c:22:fb:e7:00:01"
	var attempt string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind,submitted_room,device_mac,device_ip,matched_stay_id)
		VALUES ($1,$2,now() - interval '4 days','VERIFIED_NO_ELIGIBLE_PACKAGE','FULL_NAME','101',$3::macaddr,'10.91.9.9',$4::uuid) RETURNING id::text`,
		f.tenant, f.site, mac, r.stay).Scan(&attempt); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	type req struct {
		id, ref string
		rv      asstReview
	}
	var reqs []req
	for i := 0; i < 4; i++ {
		id, ref := asstOpen(t, f, asstDefaults("ERASURE", []string{mac}, r.anchor()))
		rv := asstReviewOf(t, f, id)
		if c := rv.find("sign_in_attempt", attempt); c == nil || c.Class != "CLAIMABLE" {
			t.Fatalf("the attempt the PMS matched to the stated stay = %+v", c)
		}
		reqs = append(reqs, req{id, ref, rv})
	}
	var wg sync.WaitGroup
	codes := make([]int, len(reqs))
	for i := range reqs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i], _ = asstAct(f, t, "erase", reqs[i].id, reqs[i].rv, reqs[i].ref, reqs[i].rv.selectable())
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == 200 {
			ok++
		} else if c != 409 {
			t.Errorf("unexpected status %v", codes)
		}
	}
	if ok != 1 {
		t.Errorf("exactly one of four concurrent erasures of one attempt must run: %v", codes)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE subject_kind='assisted' AND tenant_id=$1`, f.tenant); got != "1" {
		t.Errorf("erasures recorded: %s", got)
	}
}

// What a hand-over would contain is part of what was reviewed.
func TestIntegration_Assisted_ARecordThatChangedAfterTheReviewStopsTheHandOver(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "RV8", nil, "10.91.10")
	mac := "3c:22:fb:e8:00:01"
	var attempt string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sign_in_attempts(tenant_id,site_id,occurred_at,result,verifier_kind,submitted_room,device_mac,device_ip,matched_stay_id)
		VALUES ($1,$2,now() - interval '4 days','VERIFIED_NO_ELIGIBLE_PACKAGE','FULL_NAME','101',$3::macaddr,'10.91.10.9',$4::uuid) RETURNING id::text`,
		f.tenant, f.site, mac, r.stay).Scan(&attempt); err != nil {
		t.Fatalf("seed attempt: %v", err)
	}
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", []string{mac}, r.anchor()))
	rv := asstReviewOf(t, f, id)
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sign_in_attempts SET device_ip='10.91.10.77' WHERE id=$1::uuid`, attempt)
		return err
	})
	if st, raw := asstAct(f, t, "export", id, rv, ref, rv.selectable()); st != 409 || !strings.Contains(raw, "changed") {
		t.Errorf("hand-over of a record that changed after the review = %d %s", st, raw)
	}
}

// Address spellings.
func TestIntegration_Assisted_AddressSpellings(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "RV9", asstMACs("3c:22:fb:e9:00", 1), "10.91.11")
	for in, want := range map[string]int{
		"3C:22:FB:E9:00:01": 201, "3c-22-fb-e9-00-01": 201, "3c22fbe90001": 201,
		"3c22-fbe9-0001": 400, "3c:22-fb:e9-00:01": 400, "3c22fbe9000": 400, "3c:22:fb:e9:00:0g": 400,
	} {
		o := asstDefaults("ACCESS", []string{in}, r.anchor())
		if st, body := f.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != want {
			t.Errorf("%q: %d %v, want %d", in, st, body, want)
		}
	}
	// Whatever the spelling, the request is about the one address.
	o := asstDefaults("ACCESS", []string{"3C-22-FB-E9-00-01", "3c22fbe90001", "3c:22:fb:e9:00:01"}, r.anchor())
	id, _ := asstOpen(t, f, o)
	if got := asstStatus(t, f, id)["addresses"]; fmt.Sprint(got) != "[3c:22:fb:e9:00:01]" {
		t.Errorf("addresses = %v", got)
	}
	_ = time.Now
}
