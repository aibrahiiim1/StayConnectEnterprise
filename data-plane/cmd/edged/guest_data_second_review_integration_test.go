//go:build integration

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Findings of the second adversarial review of migration 0147.

// A stay the PMS never closed, with occupancy evidence, is anonymised by retention (it used to abort the whole run).
func TestIntegration_SecondReview_AnOpenStayWithEvidenceIsAnonymised(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-SR1-"+sfx, "IN_HOUSE")
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.pms_interface_revisions(id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
			SELECT gen_random_uuid(),tenant_id,site_id,pms_interface_id,9000+(random()*900)::int,'UTC','{}' FROM iam_v2.stays WHERE id=$1::uuid`, stay); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET occupancy_evidence_at=now()-interval '10 days', occupancy_ingested_at=now()-interval '10 days',
			occupancy_revision_id=(SELECT id FROM iam_v2.pms_interface_revisions WHERE pms_interface_id=stays.pms_interface_id LIMIT 1),
			occupancy_normalization_version=1, occupancy_clock_suspect=false, occupancy_evidence_version=1 WHERE id=$1::uuid`, stay)
		return err
	})
	if _, err := f.pool.Exec(ctx, `SELECT iam_v2.anonymise_stay($1::uuid)`, stay); err != nil {
		t.Fatalf("anonymise an open stay with evidence: %v", err)
	}
	if got := gdStr(t, f, `SELECT iam_v2.is_anon_token(external_reservation_id)::text FROM iam_v2.stays WHERE id=$1`, stay); got != "true" {
		t.Errorf("the stay was not anonymised: %s", got)
	}
}

// The request register keeps neither the requester nor the reason for an extension once the request is answered, and
// the extension reason names no one.
func TestIntegration_SecondReview_TheRegisterKeepsNoFreeTextAfterTheAnswer(t *testing.T) {
	f := newAPI(t)
	received := time.Now().UTC().Format("2006-01-02")
	st, body := f.do(t, "POST", "/guest-data-rights/register", map[string]any{"kind": "ERASURE", "received_at": received, "requester": "Ola Registrant"})
	if st != 201 {
		t.Fatalf("open = %d %v", st, body)
	}
	id := body["id"].(string)
	if st, _ := f.do(t, "POST", "/guest-data-rights/register/"+id+"/extend", map[string]any{"reason": "jane.roe@example.com asked for more time"}); st == 200 {
		t.Errorf("an extension reason with an email address was accepted")
	}
	if st, b := f.do(t, "POST", "/guest-data-rights/register/"+id+"/extend", map[string]any{"reason": "complex, several stays"}); st != 200 {
		t.Fatalf("extend = %d %v", st, b)
	}
	if st, b := f.do(t, "POST", "/guest-data-rights/register/"+id+"/answer", map[string]any{"status": "ANSWERED"}); st != 200 {
		t.Fatalf("answer = %d %v", st, b)
	}
	if got := gdStr(t, f, `SELECT coalesce(extension_reason,'-') FROM iam_v2.guest_data_request_register WHERE id=$1`, id); got != "-" {
		t.Errorf("the extension reason survived the answer: %q", got)
	}
}

// A row that carries only a PMS guest number is another guest: the named guest's erasure leaves it alone.
func TestIntegration_SecondReview_ACoOccupantWithOnlyAPmsNumberIsNotErasedWithTheNamedGuest(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-SR3-"+sfx, "CHECKED_OUT")
	guestRow(t, f, stay, "G-NAMED-"+sfx, "NELL", "NAMED")
	other := addGuest(t, f, stay, "G-OTHER-"+sfx, "x", "y")
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_guests SET first_name_norm=NULL,last_name_norm=NULL,display_name=NULL WHERE id=$1::uuid`, other)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export", gdReq(f, "stay", stay, "guest asked"))
	if st == 200 && strings.Contains(raw, "G-OTHER-"+sfx) {
		t.Errorf("the other occupant's PMS number is in the named guest's export")
	}
	f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked"))
	if got := gdStr(t, f, `SELECT coalesce(external_guest_id,'-') FROM iam_v2.stay_guests WHERE id=$1`, other); got != "G-OTHER-"+sfx {
		t.Errorf("the other occupant's row was erased with the named guest: %q", got)
	}
}

// A replayed assisted erasure removes the device address from the activity log, as the request itself did.
func TestIntegration_SecondReview_AReplayedAssistedErasureCleansTheActivityLog(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "SR4", asstMACs("3c:22:fb:f4:00", 2), "10.96.2")
	id, ref := asstOpen(t, f, asstDefaults("ERASURE", r.macs[:1], r.anchor()))
	rv := asstReviewOf(t, f, id)
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 200 {
		t.Fatalf("erase = %d %s", st, raw)
	}
	mac := r.macs[0]
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.devices SET mac=$2::macaddr, last_ip='10.96.2.1' WHERE id=$1::uuid`, r.devs[0], mac); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES (now(),$1::uuid,'operator','op@x','device.block','device',$2,jsonb_build_object('mac',$2::text))`, f.tenant, mac)
		return err
	})
	replayJournal(t, f)
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND (target_id=$2 OR payload::text ILIKE '%'||$2||'%')`, f.tenant, mac); got != "0" {
		t.Errorf("the device address is still in the activity log after the replay: %s rows", got)
	}
}

// A staff member's email is scrubbed from a table another role owns (the staff run used to fail on it).
func TestIntegration_SecondReview_AStaffLabelIsScrubbedWhoeverOwnsTheTable(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	email := fmt.Sprintf("Departed%d@Hotel.Example", time.Now().UnixNano())
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.system_network_audit(actor, action, target) VALUES ($1, 'x', 'wan')`, email); err != nil {
		t.Skipf("system_network_audit has another shape here: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `SELECT public.scrub_staff_label_public($1, 'removed-0123456789ab@staff.invalid')`, email); err != nil {
		t.Fatalf("scrub: %v", err)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.system_network_audit WHERE lower(actor)=lower($1)`, email); got != "0" {
		t.Errorf("the label survived: %s", got)
	}
}
