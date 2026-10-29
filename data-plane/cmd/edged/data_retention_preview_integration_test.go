//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
)

// AD-67: shortening a period shows what it will remove (counted by the retention functions, rolled back), and is made only on a
// second confirmation of that number.
func TestIntegration_DataRetention_ShorteningShowsItsImpactAndNeedsASecondConfirmation(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	long := map[string]any{"pms_stay_days": 90, "client_activity_days": 365, "inactive_identity_days": 365, "expired_record_days": 7,
		"audit_log_days": 730, "sign_in_attempt_days": 30, "log_days": 14, "staff_note_days": 730}
	short := map[string]any{}
	for k, v := range long {
		short[k] = v
	}
	short["pms_stay_days"] = 30
	// A stay the PMS never closed whose departure was 60 days ago: safe under 90 days, due under 30.
	stay := seedGuestDataStay(t, f, "R-IMPACT-1", "IN_HOUSE")
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stays SET arrival = current_date - 70, departure = current_date - 60 WHERE id=$1::uuid`, stay)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Lengthening to 90 first: no preview is needed and no confirmation.
	if st, b := f.do(t, "PUT", "/data-retention/", map[string]any{"settings": long, "reason": "keep longer", "password": f.password}); st != 200 {
		t.Fatalf("lengthening = %d %v", st, b)
	}

	// The preview counts the stay, and writes nothing.
	st, body := f.do(t, "POST", "/data-retention/preview", map[string]any{"settings": short})
	if st != 200 || body["shortens"] != true || body["additional_records"] != float64(1) {
		t.Fatalf("preview = %d %v", st, body)
	}
	by, _ := body["additional_by_category"].(map[string]any)
	if by["pms_stays"] != float64(1) {
		t.Fatalf("by category = %v", by)
	}
	var ref string
	if err := f.pool.QueryRow(ctx, `SELECT external_reservation_id FROM iam_v2.stays WHERE id=$1::uuid`, stay).Scan(&ref); err != nil || ref != "R-IMPACT-1" {
		t.Fatalf("the preview anonymised the stay: %q (%v)", ref, err)
	}
	var runs int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.data_retention_runs WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&runs)
	var cfg int
	_ = f.pool.QueryRow(ctx, `SELECT pms_stay_days FROM iam_v2.site_data_retention WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&cfg)
	if runs != 0 || cfg != 90 {
		t.Fatalf("the preview left a trace: %d runs, pms_stay_days %d", runs, cfg)
	}

	// The shortening is refused until the number is confirmed, and the refusal carries the impact.
	put := func(extra map[string]any) (int, map[string]any) {
		b := map[string]any{"settings": short, "reason": "shorter policy", "password": f.password}
		for k, v := range extra {
			b[k] = v
		}
		return f.do(t, "PUT", "/data-retention/", b)
	}
	if st, b := put(nil); st != 409 || b["error"] != "impact_confirmation_required" || b["additional_records"] != float64(1) {
		t.Fatalf("shortening without confirmation = %d %v", st, b)
	}
	if st, b := put(map[string]any{"confirm_records": 0}); st != 409 {
		t.Fatalf("shortening with the wrong number = %d %v", st, b)
	}
	if st, b := put(map[string]any{"confirm_records": 1, "password": "wrong"}); st != 401 {
		t.Fatalf("a wrong password passed the confirmation: %d %v", st, b)
	}
	if st, b := put(map[string]any{"confirm_records": 1}); st != 200 {
		t.Fatalf("confirmed shortening = %d %v", st, b)
	}
	var audited int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM public.audit_log WHERE action='data_retention.changed' AND target_id=$1
	      AND (payload->>'impact_confirmed_records')::int = 1`, f.site).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("the confirmed impact is not in the audit: %d (%v)", audited, err)
	}

	// A shortening that would remove nothing more needs no second confirmation.
	shorter := map[string]any{}
	for k, v := range short {
		shorter[k] = v
	}
	shorter["audit_log_days"] = 700
	if st, b := f.do(t, "PUT", "/data-retention/", map[string]any{"settings": shorter, "reason": "tidy", "password": f.password}); st != 200 {
		t.Fatalf("a harmless shortening = %d %v", st, b)
	}
}
