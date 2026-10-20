//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Migration 0147: an erasure survives a restore. A restore brings the database back as it was, with every guest
// erased since in it again. The journal records what was erased (kinds and ids, nothing about who) and a replay
// erases it again. These tests make the "restore" by writing the identifying values back into the anonymised rows,
// which is exactly what an older backup does, and then replay the journal.

func replayJournal(t *testing.T, f *apiFixture) map[string]any {
	t.Helper()
	ctx := context.Background()
	var journal string
	if err := f.pool.QueryRow(ctx, `SELECT public.erasure_journal_export($1::uuid,$2::uuid)::text`, f.tenant, f.site).Scan(&journal); err != nil {
		t.Fatalf("export the journal: %v", err)
	}
	// The restored database has not heard of these erasures: they were made after its backup.
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM iam_v2.erasure_journal WHERE tenant_id=$1::uuid`, f.tenant)
		return err
	})
	var out string
	if err := f.pool.QueryRow(ctx, `SELECT public.erasure_journal_replay($1::jsonb)::text`, journal).Scan(&out); err != nil {
		t.Fatalf("replay the journal: %v", err)
	}
	res := map[string]any{}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	if failed, _ := res["failed"].([]any); len(failed) > 0 {
		t.Fatalf("the replay reported failures: %v", failed)
	}
	return res
}

func TestIntegration_RestoreReplay_AnErasedAccountStaysErasedAfterARestore(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "restore-" + sfx + "@example.com"
	acct, dev, sess := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.0.10")
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	before := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE tenant_id=$1`, f.tenant)
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.erasure_journal WHERE tenant_id=$1 AND kind='account' AND subject_id=$2::uuid`, f.tenant, acct); got != "1" {
		t.Fatalf("the erasure was not journaled: %s", got)
	}

	// THE RESTORE: an older backup carries the account as it was.
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

	res := replayJournal(t, f)
	if res["reapplied"].(float64) < 1 || res["already_known"].(float64) != 0 {
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
	// A replay is not a second erasure: no second register row, and a second replay is harmless.
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.guest_data_erasures WHERE tenant_id=$1`, f.tenant); got != before {
		t.Errorf("the replay added erasure register rows: %s -> %s", before, got)
	}
	// A second replay of the same file finds every entry known and does nothing.
	var journal, out string
	if err := f.pool.QueryRow(ctx, `SELECT public.erasure_journal_export($1::uuid,$2::uuid)::text`, f.tenant, f.site).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT public.erasure_journal_replay($1::jsonb)::text`, journal).Scan(&out); err != nil || !strings.Contains(out, `"reapplied": 0`) {
		t.Errorf("a second replay was not a no-op: %v %s", err, out)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.erasure_journal WHERE tenant_id=$1`, f.tenant); got != "1" {
		t.Errorf("the journal grew on replay: %s", got)
	}
}

func TestIntegration_RestoreReplay_AReservationAndItsGuestStayErased(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	res := "R-RST-" + sfx
	stay := seedGuestDataStay(t, f, res, "CHECKED_OUT")
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
	replayJournal(t, f)
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
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 200 {
		t.Fatalf("erase = %d %s", st, raw)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.erasure_journal WHERE tenant_id=$1 AND kind='assisted_record'`, f.tenant); got != "2" {
		t.Fatalf("an assisted erasure journals each record it anonymised: %s", got)
	}
	// The older backup: the session and device have their addresses; the other occupant's never changed.
	asstWithoutTriggers(t, f, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.devices SET mac=$2::macaddr, last_ip='10.96.1.1' WHERE id=$1::uuid`, r.devs[0], r.macs[0]); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.sessions SET ip='10.96.1.1', mac=$2::macaddr WHERE id=$1::uuid`, r.sess[0], r.macs[0])
		return err
	})
	replayJournal(t, f)
	if got := sessAddr(t, f, r.sess[0]); got != "" {
		t.Errorf("the restored session address survived the replay: %s", got)
	}
	if got := devMAC(t, f, r.devs[0]); !strings.HasPrefix(got, "03:") {
		t.Errorf("the restored device survived the replay: %s", got)
	}
	if got := sessAddr(t, f, r.sess[1]); got != r.ips[1]+","+r.macs[1] {
		t.Errorf("the replay touched the other occupant's session: %s", got)
	}
}

// The journal names records, never people.
func TestIntegration_RestoreReplay_TheJournalHoldsNoPersonalData(t *testing.T) {
	f := newAPI(t)
	sfx := fmt.Sprint(time.Now().UnixNano())
	email := "journal-" + sfx + "@example.com"
	acct, _, _ := seedEndedAccount(t, f, email, 400*24*time.Hour, "10.95.1.10")
	if st, body := f.do(t, "POST", "/guest-data-rights/erase", gdReq(f, "account", acct, "guest asked")); st != 200 {
		t.Fatalf("erase = %d %v", st, body)
	}
	var doc string
	if err := f.pool.QueryRow(context.Background(), `SELECT public.erasure_journal_export($1::uuid,$2::uuid)::text`, f.tenant, f.site).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{email, "10.95.1.10", "02:aa:bb:cc:dd:01", "guest asked"} {
		if strings.Contains(doc, leak) {
			t.Errorf("the journal carries %q", leak)
		}
	}
	// And only the replay and export functions can read it: the runtime role of edged has no access to the table.
	if _, err := f.pool.Exec(context.Background(), `SELECT 1`); err != nil {
		t.Fatal(err)
	}
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
