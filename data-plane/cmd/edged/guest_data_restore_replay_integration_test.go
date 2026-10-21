//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

// Migration 0148: an erasure survives a restore. A restore brings the database back as it was, with every guest erased since
// in it again and every request answered since open again. The ledger records what was erased and how requests were closed
// (kinds, ids, references, dates and codes; nothing about who), and replaying it puts all of that right. These tests make the
// "restore" by writing the identifying values back, forgetting the ledger entries after the backup point and the proof rows,
// which is exactly what an older backup is, and then replay the ledger COPY ON THE APPLIANCE (the file), not the database's.

func ledgerSeq(t *testing.T, f *apiFixture) int64 {
	t.Helper()
	var n int64
	if err := f.pool.QueryRow(context.Background(), `SELECT COALESCE(max(seq),0) FROM iam_v2.erasure_ledger`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// replayLedger forgets every ledger entry after base (the backup point) and the erasure proof of this tenant, then applies what
// the primary file holds beyond base.
func replayLedger(t *testing.T, f *apiFixture, base int64) map[string]any {
	t.Helper()
	ctx := context.Background()
	local := f.ledger.ReadLocal()
	if !local.Intact() {
		t.Fatalf("the primary copy is not intact: %+v", local)
	}
	entries := local.Parse.Entries
	if int64(len(entries)) <= base {
		t.Fatalf("the primary copy holds %d entries, the backup point is %d", len(entries), base)
	}
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		for _, q := range []string{
			`DELETE FROM iam_v2.erasure_ledger WHERE seq > $1`,
		} {
			if _, err := tx.Exec(ctx, q, base); err != nil {
				return err
			}
		}
		for _, q := range []string{
			`DELETE FROM iam_v2.erasure_journal WHERE tenant_id=$1::uuid`,
			`DELETE FROM iam_v2.guest_data_erasures WHERE tenant_id=$1::uuid`,
		} {
			if _, err := tx.Exec(ctx, q, f.tenant); err != nil {
				return err
			}
		}
		return nil
	})
	doc, _ := json.Marshal(entries[base:])
	var out string
	if err := f.pool.QueryRow(ctx, `SELECT public.ledger_apply($1::jsonb, false)::text`, string(doc)).Scan(&out); err != nil {
		t.Fatalf("replay the ledger: %v", err)
	}
	res := map[string]any{}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if res["failed"] != nil || res["conflict"] != nil {
		t.Fatalf("the replay reported a problem: %v", res)
	}
	return res
}

func TestIntegration_RestoreReplay_AnErasedAccountStaysErasedAfterARestore(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "restore-" + sfx + "@example.com"
	acct, dev, sess := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.0.10")
	base := ledgerSeq(t, f)
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked"))
	if st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	ref, _ := body["erasure_ref"].(string)
	if got := ledgerSeq(t, f); got != base+1 {
		t.Fatalf("the erasure wrote %d ledger entries, want 1", got-base)
	}
	// The erasure is in BOTH copies outside the database.
	if p := f.ledger.ReadLocal(); !p.Intact() || int64(len(p.Parse.Entries)) != base+1 {
		t.Fatalf("the primary copy does not hold the erasure: %+v", p)
	}
	if info, ok, found, err := f.ledger.ReadExternalHead(ctx); err != nil || !found || !ok || info.Seq != base+1 {
		t.Fatalf("the secondary copy does not hold the erasure: %+v ok=%v found=%v err=%v", info, ok, found, err)
	}

	// THE RESTORE: an older backup carries the account as it was, and has never heard of the erasure.
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		for _, q := range []string{
			`UPDATE iam_v2.guest_access_accounts SET username='` + email + `', display_name='Restored Name', enabled=true WHERE id='` + acct + `'::uuid`,
			`UPDATE iam_v2.devices SET mac='02:aa:bb:cc:dd:01', last_ip='10.95.0.10' WHERE id='` + dev + `'::uuid`,
			`UPDATE iam_v2.sessions SET ip='10.95.0.10', mac='02:aa:bb:cc:dd:01' WHERE id='` + sess + `'::uuid`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return nil
	})
	if got := gdStr(t, f, `SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != email {
		t.Fatalf("the restore simulation did not bring the account back: %s", got)
	}

	res := replayLedger(t, f, base)
	if res["applied"].(float64) != 1 {
		t.Fatalf("nothing was replayed: %v", res)
	}
	for name, q := range map[string]string{
		"account":    `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`,
		"device":     `SELECT (mac::text LIKE '03:%')::text FROM iam_v2.devices WHERE id=$1`,
		"session ip": `SELECT (ip IS NULL AND mac IS NULL)::text FROM iam_v2.sessions WHERE id=$1`,
	} {
		id := map[string]string{"account": acct, "device": dev, "session ip": sess}[name]
		if got := gdStr(t, f, q, id); got != "true" {
			t.Errorf("after the replay the %s is still identifying", name)
		}
	}
	if got := gdStr(t, f, `SELECT (display_name IS NULL)::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != "true" {
		t.Error("the restored display name survived the replay")
	}
	// THE PROOF THE GUEST WAS GIVEN IS BACK: the reference exists again, with its counts, so "ER-..." still means something.
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE erasure_ref=$1 AND tenant_id=$2`, ref, f.tenant); got != "1" {
		t.Errorf("the erasure proof was not recovered: %s rows for %s", got, ref)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.erasure_journal WHERE tenant_id=$1 AND kind='account' AND subject_id=$2::uuid`, f.tenant, acct); got != "1" {
		t.Errorf("the journal row was not recovered: %s", got)
	}
	// A second replay finds every entry known and does nothing.
	doc, _ := json.Marshal(f.ledger.ReadLocal().Parse.Entries[base:])
	var out string
	if err := f.pool.QueryRow(ctx, `SELECT public.ledger_apply($1::jsonb, false)::text`, string(doc)).Scan(&out); err != nil || !strings.Contains(out, `"applied": 0`) || !strings.Contains(out, `"already_known": 1`) {
		t.Errorf("a second replay was not a no-op: %v %s", err, out)
	}
}

func TestIntegration_RestoreReplay_AReservationAndItsGuestStayErased(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	res := "R-RST-" + sfx
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
	base := ledgerSeq(t, f)
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET external_reservation_id=$2, normalized_room_number='101' WHERE id=$1::uuid`, stay, res); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_guests SET first_name_norm='ada', last_name_norm='lovelace', display_name='Ada Lovelace' WHERE stay_id=$1::uuid`, stay)
		return err
	})
	replayLedger(t, f, base)
	if got := gdStr(t, f, `SELECT (NOT external_reservation_id = $2 AND normalized_room_number IS NULL)::text FROM iam_v2.stays WHERE id=$1`, stay, res); got != "true" {
		t.Error("the restored reservation survived the replay")
	}
	if got := gdStr(t, f, `SELECT count(*) FILTER (WHERE display_name IS NOT NULL OR last_name_norm IS NOT NULL)::text FROM iam_v2.stay_guests WHERE stay_id=$1`, stay); got != "0" {
		t.Errorf("a restored guest name survived the replay (%s rows)", got)
	}
}

func TestIntegration_RestoreReplay_AnAssistedErasureIsReplayedRecordByRecord(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "RP3", asstMACs("3c:22:fb:f3:00", 2), "10.96.1")
	id, ref := asstOpen(t, f, asstDefaults("ERASURE", r.macs[:1], r.anchor()))
	rv := asstReviewOf(t, f, id)
	base := ledgerSeq(t, f)
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 200 {
		t.Fatalf("erase = %d %s", st, raw)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.erasure_journal WHERE tenant_id=$1 AND kind='assisted_record'`, f.tenant); got != "2" {
		t.Fatalf("an assisted erasure journals each record it anonymised: %s", got)
	}
	mac := r.macs[0]
	// The older backup: the session and device have their addresses; the other occupant's never changed; the activity log
	// carries the address.
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.devices SET mac=$2::macaddr, last_ip='10.96.1.1' WHERE id=$1::uuid`, r.devs[0], mac); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET ip='10.96.1.1', mac=$2::macaddr WHERE id=$1::uuid`, r.sess[0], mac); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES (now(),$1::uuid,'operator','op@x','device.block','device',$2,jsonb_build_object('mac',$2::text))`, f.tenant, mac)
		return err
	})
	replayLedger(t, f, base)
	if got := sessAddr(t, f, r.sess[0]); got != "" {
		t.Errorf("the restored session address survived the replay: %s", got)
	}
	if got := devMAC(t, f, r.devs[0]); !strings.HasPrefix(got, "03:") {
		t.Errorf("the restored device survived the replay: %s", got)
	}
	if got := sessAddr(t, f, r.sess[1]); got != r.ips[1]+","+r.macs[1] {
		t.Errorf("the replay touched the other occupant's session: %s", got)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND (target_id=$2 OR payload::text ILIKE '%'||$2||'%')`, f.tenant, mac); got != "0" {
		t.Errorf("the device address is still in the activity log after the replay: %s rows", got)
	}
}

// A request answered since the backup is closed again, with who asked removed; a request that did not exist yet comes back closed.
func TestIntegration_RestoreReplay_AnAnsweredRequestIsClosedAgain(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	received := time.Now().UTC().AddDate(0, 0, -3).Format("2006-01-02")
	open := func(who string) (string, string) {
		st, body := f.do(t, "POST", "/guest-data-rights/register", map[string]any{"kind": "ERASURE", "received_at": received, "requester": who})
		if st != 201 {
			t.Fatalf("open = %d %v", st, body)
		}
		return body["id"].(string), body["ref"].(string)
	}
	idA, refA := open("Ada Requester")
	idB, refB := open("Bo Requester")
	base := ledgerSeq(t, f)
	for _, id := range []string{idA, idB} {
		if st, b := f.do(t, "POST", "/guest-data-rights/register/"+id+"/answer", map[string]any{"status": "PARTLY_REFUSED", "outcome": "LEGAL_RETENTION", "note": "invoices kept"}); st != 200 {
			t.Fatalf("answer = %d %v", st, b)
		}
	}
	// THE RESTORE: request A is open again with the requester it had; request B was opened after the backup and is not there.
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_data_request_register SET status='OPEN', outcome_code=NULL, answered_by=NULL, answered_at=NULL,
			requester_ref='Ada Requester', erasure_ref=NULL WHERE id=$1::uuid`, idA); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_data_request_register WHERE id=$1::uuid`, idB)
		return err
	})
	replayLedger(t, f, base)
	if got := gdStr(t, f, `SELECT status||'/'||outcome_code||'/'||requester_ref FROM iam_v2.guest_data_request_register WHERE id=$1`, idA); got != "PARTLY_REFUSED/LEGAL_RETENTION/[removed]" {
		t.Errorf("request %s was not closed again: %s", refA, got)
	}
	if got := gdStr(t, f, `SELECT status||'/'||requester_ref||'/'||ref FROM iam_v2.guest_data_request_register WHERE id=$1`, idB); got != "PARTLY_REFUSED/[removed]/"+refB {
		t.Errorf("request %s was not brought back closed: %s", refB, got)
	}
}

// An assisted request closed since the backup is closed again, which removes its addresses and free text.
func TestIntegration_RestoreReplay_AClosedAssistedRequestDoesNotReopen(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "RP4", asstMACs("3c:22:fb:f5:00", 2), "10.96.4")
	id, _ := asstOpen(t, f, asstDefaults("ERASURE", r.macs[:1], r.anchor()))
	base := ledgerSeq(t, f)
	if st, b := f.do(t, "POST", "/guest-data-rights/assisted/"+id+"/close", map[string]any{"outcome": "CANCELLED", "reason": "the guest withdrew the request"}); st != 200 {
		t.Fatalf("close = %d %v", st, b)
	}
	if got := gdStr(t, f, `SELECT status FROM iam_v2.assisted_guest_requests WHERE id=$1`, id); got != "CANCELLED" {
		t.Fatalf("not cancelled: %s", got)
	}
	// THE RESTORE: the backup has it open, with the addresses and who asked.
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.assisted_guest_requests SET status='OPEN', closed_at=NULL, closed_by=NULL, close_reason=NULL, result=NULL,
			macs=ARRAY[$2]::macaddr[], requester_ref='Cy Requester', evidence_note='saw the passport yesterday' WHERE id=$1::uuid`, id, r.macs[0])
		return err
	})
	replayLedger(t, f, base)
	if got := gdStr(t, f, `SELECT status||'/'||COALESCE(macs::text,'none')||'/'||requester_ref FROM iam_v2.assisted_guest_requests WHERE id=$1`, id); got != "CANCELLED/none/[removed]" {
		t.Errorf("the assisted request was not closed again: %s", got)
	}
}

// The ledger names records, never people.
func TestIntegration_RestoreReplay_TheLedgerHoldsNoPersonalData(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "ledger-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.1.10")
	base := ledgerSeq(t, f)
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	for name, doc := range map[string]string{
		"the database": gdStr(t, f, `SELECT iam_v2.ledger_export($1)::text`, base),
		"the file":     mustJSON(t, f.ledger.ReadLocal().Parse.Entries[base:]),
	} {
		for _, leak := range []string{email, "10.95.1.10", "02:aa:bb:cc:dd:01", "guest asked", "ledger-" + sfx} {
			if strings.Contains(doc, leak) {
				t.Errorf("the ledger in %s carries %q", name, leak)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A ledger entry altered anywhere is refused, and nothing is applied.
func TestIntegration_RestoreReplay_ATamperedEntryIsRefused(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	acct, _, _ := seedEndedAccount(t, f, "tamper-"+sfx+"@example.com", 400*24*time.Hour, "10.95.2.10")
	base := ledgerSeq(t, f)
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	entries := f.ledger.ReadLocal().Parse.Entries[base:]
	forged := make([]ledger.Entry, len(entries))
	copy(forged, entries)
	forged[0].Body = strings.Replace(forged[0].Body, acct, "00000000-0000-4000-8000-000000000000", 1)
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM iam_v2.erasure_ledger WHERE seq > $1`, base)
		return err
	})
	doc, _ := json.Marshal(forged)
	var out string
	if err := f.pool.QueryRow(ctx, `SELECT public.ledger_apply($1::jsonb, false)::text`, string(doc)).Scan(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"applied": 0`) || strings.Contains(out, `"conflict": null`) {
		t.Errorf("a forged entry was applied or not flagged: %s", out)
	}
	if got := ledgerSeq(t, f); got != base {
		t.Errorf("the forged entry entered the ledger (head %d, want %d)", got, base)
	}
}

// ---- fail closed -----------------------------------------------------------------------------------------------------

type failingDest struct {
	ledger.Destination
	failPut bool
}

func (d *failingDest) Put(ctx context.Context, n string, b []byte) error {
	if d.failPut {
		return fmt.Errorf("the disk was unplugged")
	}
	return d.Destination.Put(ctx, n, b)
}

// While the secondary storage cannot be written an erasure is refused and NOTHING is erased; when it returns, the same request works.
func TestIntegration_Ledger_ErasureFailsClosedWhenTheSecondaryStorageIsDown(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "closed-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.3.10")
	fd := &failingDest{Destination: f.ledger.Destination(), failPut: true}
	f.ledgerC.st = ledger.NewStore(ledger.Config{LocalPath: f.ledger.LocalPath, Dest: fd})
	base := ledgerSeq(t, f)
	local := mustRead(t, f.ledger.LocalPath)

	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked"))
	if st != 503 || body["error"] != "ledger_external_unavailable" {
		t.Fatalf("erase = %d %v, want 503 ledger_external_unavailable", st, body)
	}
	if got := gdStr(t, f, `SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != email {
		t.Errorf("the account was erased although the ledger could not be written: %s", got)
	}
	if got := ledgerSeq(t, f); got != base {
		t.Errorf("a ledger entry was left in the database: %d -> %d", base, got)
	}
	if string(mustRead(t, f.ledger.LocalPath)) != string(local) {
		t.Errorf("a ledger entry was left in the primary copy")
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE tenant_id=$1`, f.tenant); got != "0" {
		t.Errorf("an erasure was recorded: %s", got)
	}

	fd.failPut = false // the disk is back: the same request now succeeds
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("after the storage returned: erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != "true" {
		t.Errorf("the account was not erased once the storage returned")
	}
}

// With no secondary storage set up, erasure is refused (409) and says what to do.
func TestIntegration_Ledger_ErasureIsRefusedUntilSecondaryStorageIsSetUp(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "nostore-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.4.10")
	f.ledgerC.st = ledger.NewStore(ledger.Config{LocalPath: f.ledger.LocalPath})
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked"))
	if st != 409 || body["error"] != "ledger_not_configured" || !strings.Contains(fmt.Sprint(body["message"]), "secondary storage") {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != email {
		t.Errorf("the account was erased without the second copy: %s", got)
	}
}

// A reconciliation that refuses (the ledger is not in step) stops the request before anything is touched.
func TestIntegration_Ledger_PrepareRefusalStopsTheRequestEarly(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "prep-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.5.10")
	f.ledgerC.prepareErr = refusalFor(409, "", "", "recovery_needed")
	st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked"))
	if st != 409 || body["error"] != "ledger_recovery_needed" {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT username FROM iam_v2.guest_access_accounts WHERE id=$1`, acct); got != email {
		t.Errorf("the account was erased: %s", got)
	}
	f.ledgerC.prepareErr = nil
}

// A refusal by the database (here: a guest still online) writes nothing to the ledger.
func TestIntegration_Ledger_ARefusedErasureLeavesNoEntry(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	stay := seedGuestDataStay(t, f, "R-REF-"+sfx, "IN_HOUSE")
	base := ledgerSeq(t, f)
	st, _ := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "stay", stay, "guest asked"))
	if st == 200 {
		t.Fatalf("an in-house guest was erased")
	}
	if got := ledgerSeq(t, f); got != base {
		t.Errorf("a refused erasure wrote %d ledger entries", got-base)
	}
	if n := len(f.ledger.ReadLocal().Parse.Entries); int64(n) != base {
		t.Errorf("a refused erasure reached the primary copy (%d entries, want %d)", n, base)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A real value that merely starts with "anon-" is anonymised like any other.
func TestIntegration_RestoreReplay_AnAddressThatStartsWithAnonIsStillAnonymised(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "anon-carl-" + sfx + "@example.com"
	var principal string
	if err := f.pool.QueryRow(ctx, `WITH gp AS (INSERT INTO iam_v2.guest_principals(tenant_id,display_name) VALUES ($1,'Carl') RETURNING id)
		INSERT INTO iam_v2.guest_principal_identities(tenant_id,guest_principal_id,factor_type,factor_issuer,factor_value_norm,verified_at,attrs)
		SELECT $1, gp.id, 'EMAIL', '', $2, now() - interval '400 days', '{}'::jsonb FROM gp RETURNING guest_principal_id::text`,
		f.tenant, email).Scan(&principal); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	// It is offered by the search and erased.
	if st, body := f.do(t, "GET", "/guest-data-rights/search?q=anon-carl-"+sfx, nil); st != 200 || len(body["matches"].([]any)) != 1 {
		t.Fatalf("search = %d %v", st, body)
	}
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "principal", principal, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	if got := gdStr(t, f, `SELECT (factor_value_norm ~ '^anon-[0-9a-f]{32}$')::text FROM iam_v2.guest_principal_identities WHERE guest_principal_id=$1`, principal); got != "true" {
		t.Errorf("an address starting with anon- was taken for an anonymised one and kept")
	}
}
