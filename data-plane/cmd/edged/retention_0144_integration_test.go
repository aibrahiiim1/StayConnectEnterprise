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

// Retention keeps what is still in use, and the nightly backup uses only the current hotel's keys.

func runRetention(t *testing.T, f *apiFixture) {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT o_category FROM iam_v2.run_data_retention($1::uuid,$2::uuid,500)`, f.tenant, f.site)
	if err != nil {
		t.Fatalf("run_data_retention: %v", err)
	}
	rows.Close()
}

// An account or device whose age was never recorded is counted from the floor, not taken for an infinitely old
// one; an arrival in the future keeps an account.
func TestIntegration_Retention0144_UnknownAgeIsNotOld(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	account := func(name, createdAt, validFrom string) string {
		var id string
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, created_at, valid_from)
			VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash',`+createdAt+`,`+validFrom+`) RETURNING id::text`,
			f.tenant, f.site, name+"-"+sfx+"@example.com").Scan(&id); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		return id
	}
	unknown := account("unknown", "NULL", "NULL")
	future := account("future", "now() - interval '400 days'", "now() + interval '5 days'")
	old := account("old", "now() - interval '400 days'", "NULL")
	var device string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,gen_random_uuid(),'02:aa:bb:cc:dd:61'::macaddr) RETURNING id::text`, f.tenant, f.site).Scan(&device); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	runRetention(t, f)
	for name, c := range map[string]struct{ id, want string }{
		"unknown age kept":      {unknown, "false"},
		"future arrival kept":   {future, "false"},
		"old unused anonymised": {old, "true"},
	} {
		if got := gdStr(t, f, `SELECT (username LIKE 'anon-%')::text FROM iam_v2.guest_access_accounts WHERE id=$1`, c.id); got != c.want {
			t.Errorf("%s: anonymised = %s, want %s", name, got, c.want)
		}
	}
	if got := gdStr(t, f, `SELECT mac::text FROM iam_v2.devices WHERE id=$1`, device); got != "02:aa:bb:cc:dd:61" {
		t.Errorf("a device never seen was anonymised: %s", got)
	}
}

// An unlinked review message past the stay retention is closed as undecided, then blanked; one whose reservation
// is still open in the mirror is left alone.
func TestIntegration_Retention0144_AReviewMessageIsGivenUpNotLeftBlank(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())
	openRes := "R-OPEN-" + sfx
	stay := seedGuestDataStay(t, f, openRes, "IN_HOUSE")
	iface := gdStr(t, f, `SELECT pms_interface_id::text FROM iam_v2.stays WHERE id=$1`, stay)
	review := func(res string) string {
		var ev string
		if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.stay_events(tenant_id,site_id,pms_interface_id,external_event_identity,
				  event_type,processing_status,payload,received_at)
				VALUES ($1,$2,$3::uuid,'EV-RV-'||$4,'GUEST_CHANGE','PENDING',jsonb_build_object('reservation',$4::text,'last_name','Review'),
				        now() - interval '40 days') RETURNING id::text`, f.tenant, f.site, iface, res).Scan(&ev); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='MANUAL_REVIEW', review_code='UNMATCHED',
				processed_at=now() - interval '40 days' WHERE id=$1::uuid`, ev)
			return err
		}); err != nil {
			t.Fatalf("seed review message: %v", err)
		}
		return ev
	}
	gone := review("R-GONE-" + sfx)
	stillOpen := review(openRes)
	runRetention(t, f)
	if got := gdStr(t, f, `SELECT coalesce((SELECT disposition||'/'||evidence_kind FROM iam_v2.pms_case_resolutions WHERE stay_event_id=$1),'none')
		|| ' ' || (SELECT payload::text FROM iam_v2.stay_events WHERE id=$1)`, gone); got != `EXPIRED_UNDECIDED/DATA_RETENTION {"anonymised": true}` {
		t.Errorf("an expired review message = %q, want closed as undecided and blanked", got)
	}
	if got := gdStr(t, f, `SELECT coalesce((SELECT disposition FROM iam_v2.pms_case_resolutions WHERE stay_event_id=$1),'none')
		|| ' ' || (SELECT payload->>'reservation' FROM iam_v2.stay_events WHERE id=$1)`, stillOpen); got != "none "+openRes {
		t.Errorf("a review message about an open stay = %q, want untouched", got)
	}
}

// A staff account created already disabled is dated, so the disabled-account period applies to it.
func TestIntegration_Retention0144_AStaffAccountCreatedDisabledIsDated(t *testing.T) {
	f := newAPI(t)
	var at *time.Time
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO public.operators (email, status) VALUES ($1,'disabled') RETURNING disabled_at`,
		"disabled-"+fmt.Sprint(time.Now().UnixNano())+"@staff.example").Scan(&at); err != nil {
		t.Fatalf("insert disabled operator: %v", err)
	}
	if at == nil || time.Since(*at) > time.Minute {
		t.Fatalf("disabled_at = %v, want now", at)
	}
}

// The nightly backup (no site named) never uses a key holder whose site the appliance no longer serves: a
// previous owner's holders change neither the recipients nor whether the list answers.
func TestIntegration_Backup0144_TheNightlyIgnoresAPreviousOwnersKeys(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	list := func() string {
		rows, err := f.pool.Query(ctx, `SELECT k FROM public.backup_key_recipients() k`)
		if err != nil {
			return "error: " + err.Error()
		}
		defer rows.Close()
		var keys []string
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			keys = append(keys, k)
		}
		if err := rows.Err(); err != nil {
			return "error: " + err.Error()
		}
		return strings.Join(keys, ",")
	}
	before := list()
	key := "age1" + strings.Repeat("q", 58)
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.backup_key_holders
		  (tenant_id, site_id, label, public_key, status, loss_acknowledged, created_by, confirmed_at)
		VALUES (gen_random_uuid(), gen_random_uuid(), 'Previous hotel', $1, 'ACTIVE', true, 'test', now())`, key); err != nil {
		t.Fatalf("seed a previous owner's holder: %v", err)
	}
	if after := list(); after != before || strings.Contains(after, key) {
		t.Fatalf("a previous owner's key changed the nightly recipients: before %q, after %q", before, after)
	}
}
