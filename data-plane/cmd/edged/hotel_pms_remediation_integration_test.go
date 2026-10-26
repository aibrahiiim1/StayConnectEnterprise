//go:build integration

package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedReviewMessage records a PMS message about room/reservation that the engine placed in MANUAL_REVIEW.
func (f *apiFixture) seedReviewMessage(t *testing.T, iface, reservation, room string, receivedAgo time.Duration) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO iam_v2.stay_events(tenant_id,site_id,pms_interface_id,external_event_identity,event_type,
			  payload,received_at)
			VALUES ($1,$2,$3::uuid,$4,'GC',jsonb_build_object('reservation',$5::text,'room',$6::text),now() - $7::interval)
			RETURNING id::text`, f.tenant, f.site, iface, fmt.Sprintf("REV-%d", time.Now().UnixNano()),
			reservation, room, fmt.Sprintf("%d seconds", int(receivedAgo.Seconds()))).Scan(&id); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE iam_v2.stay_events SET processing_status='MANUAL_REVIEW',
			review_code='GC_UNKNOWN_STAY', processed_at=now() WHERE id=$1::uuid`, id)
		return err
	}); err != nil {
		t.Fatalf("seed review message: %v", err)
	}
	return id
}

func (f *apiFixture) seedStay(t *testing.T, iface, reservation, room, guest string) string {
	t.Helper()
	ctx := context.Background()
	var stay string
	if err := controlled(ctx, f.pool, []string{"stay"}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO iam_v2.stays(tenant_id,site_id,pms_interface_id,external_reservation_id,
			  external_stay_identity,normalized_room_number,status,arrival,departure)
			VALUES ($1,$2,$3::uuid,$4,$4,$5,'IN_HOUSE',current_date - 2,current_date + 2)
			RETURNING id::text`, f.tenant, f.site, iface, reservation, room).Scan(&stay); err != nil {
			return err
		}
		if guest == "" {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,display_name,last_name_norm,is_primary)
			VALUES ($1,$2,$3::uuid,$4::uuid,$5,lower($5),true)`, f.tenant, f.site, iface, stay, guest)
		return err
	}); err != nil {
		t.Fatalf("seed stay: %v", err)
	}
	return stay
}

func (f *apiFixture) auditTargetCount(t *testing.T, action, target string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM public.audit_log
		WHERE action=$1 AND target_id=$2`, action, target).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *apiFixture) undecided(t *testing.T) float64 {
	t.Helper()
	st, body := f.do(t, http.MethodGet, "/pms-events/", nil)
	if st != http.StatusOK {
		t.Fatalf("pms-events = %d %v", st, body)
	}
	return body["summary"].(map[string]any)["undecided"].(float64)
}

func TestIntegration_API_ReviewMessageIsAcknowledgedOrLinkedOnce(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	stay := f.seedStay(t, iface, "RES-LINK-1", "312", "")
	other := f.seedStay(t, iface, "RES-OTHER", "313", "")
	ack := f.seedReviewMessage(t, iface, "RES-NONE", "999", time.Minute)
	link := f.seedReviewMessage(t, iface, "RES-LINK-1", "312", time.Minute)
	if got := f.undecided(t); got != 2 {
		t.Fatalf("undecided before = %v, want 2", got)
	}

	if st, _ := f.do(t, http.MethodPost, "/pms-event-decisions/"+ack+"/acknowledge",
		map[string]any{"reason": "duplicate of a message already handled"}); st != http.StatusUnauthorized {
		t.Fatalf("acknowledge without password = %d, want 401", st)
	}
	if st, _ := f.do(t, http.MethodPost, "/pms-event-decisions/"+ack+"/acknowledge",
		map[string]any{"password": f.password}); st != http.StatusBadRequest {
		t.Fatalf("acknowledge without reason = %d, want 400", st)
	}
	if st, body := f.do(t, http.MethodPost, "/pms-event-decisions/"+ack+"/acknowledge",
		map[string]any{"password": f.password, "reason": "test message from the PMS vendor"}); st != http.StatusOK {
		t.Fatalf("acknowledge = %d %v", st, body)
	}
	if st, _ := f.do(t, http.MethodPost, "/pms-event-decisions/"+ack+"/acknowledge",
		map[string]any{"password": f.password, "reason": "again"}); st != http.StatusConflict {
		t.Fatalf("a second decision = %d, want 409", st)
	}
	if f.auditTargetCount(t, "pms_event.acknowledge", ack) != 1 {
		t.Fatal("the acknowledgement was not audited")
	}

	// Link: the candidates are the re-matchable stays only; another stay is refused.
	st, cands := f.do(t, http.MethodGet, "/pms-event-decisions/"+link+"/candidates", nil)
	if st != http.StatusOK {
		t.Fatalf("candidates = %d %v", st, cands)
	}
	data := cands["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["stay_id"] != stay {
		t.Fatalf("candidates = %v, want only the stay with the same reservation", data)
	}
	if st, _ := f.do(t, http.MethodPost, "/pms-event-decisions/"+link+"/link",
		map[string]any{"password": f.password, "reason": "front desk confirmed", "stay_id": other}); st != http.StatusUnprocessableEntity {
		t.Fatalf("linking to a non-matching stay = %d, want 422", st)
	}
	if st, body := f.do(t, http.MethodPost, "/pms-event-decisions/"+link+"/link",
		map[string]any{"password": f.password, "reason": "front desk confirmed", "stay_id": stay}); st != http.StatusOK {
		t.Fatalf("link = %d %v", st, body)
	}
	if got := f.undecided(t); got != 0 {
		t.Fatalf("undecided after two decisions = %v, want 0", got)
	}
	var status string
	_ = f.pool.QueryRow(context.Background(), `SELECT status FROM iam_v2.stays WHERE id=$1::uuid`, stay).Scan(&status)
	if status != "IN_HOUSE" {
		t.Fatalf("linking a message changed the stay to %s", status)
	}
	if st, _ := f.do(t, http.MethodPost, "/pms-event-decisions/not-a-uuid/acknowledge",
		map[string]any{"password": f.password, "reason": "x x x"}); st != http.StatusBadRequest {
		t.Fatalf("malformed id = %d, want 400", st)
	}
}

func TestIntegration_API_DeskAndViewerCannotDecideReviewMessages(t *testing.T) {
	for _, role := range []string{"site_viewer", "front_office_operator"} {
		f := newAPI(t, role)
		iface, _, _ := f.seedInterface(t)
		ev := f.seedReviewMessage(t, iface, "R1", "1", time.Minute)
		if st, _ := f.do(t, http.MethodPost, "/pms-event-decisions/"+ev+"/acknowledge",
			map[string]any{"password": f.password, "reason": "not mine to decide"}); st != http.StatusForbidden {
			t.Fatalf("%s acknowledge = %d, want 403", role, st)
		}
	}
}

func TestIntegration_API_ReviewExpiryAppliesOnlyAfterItIsSwitchedOn(t *testing.T) {
	f := newAPI(t, "hotel_it_manager")
	iface, _, _ := f.seedInterface(t)
	old := f.seedReviewMessage(t, iface, "R-OLD", "1", 40*24*time.Hour)

	st, cur := f.do(t, http.MethodGet, "/pms-event-decisions/settings", nil)
	if st != http.StatusOK || cur["enabled"] != false || cur["expire_after_days"].(float64) != 30 {
		t.Fatalf("default setting = %d %v, want off / 30 days", st, cur)
	}
	if st, _ := f.do(t, http.MethodPut, "/pms-event-decisions/settings", map[string]any{
		"expected_config_version": 0, "enabled": true, "expire_after_days": 400,
		"reason": "out of bounds", "password": f.password}); st != http.StatusBadRequest {
		t.Fatalf("400 days = %d, want 400", st)
	}
	if st, body := f.do(t, http.MethodPut, "/pms-event-decisions/settings", map[string]any{
		"expected_config_version": 0, "enabled": true, "expire_after_days": 1,
		"reason": "messages older than a day need no decision", "password": f.password}); st != http.StatusOK {
		t.Fatalf("enable = %d %v", st, body)
	}
	if st, _ := f.do(t, http.MethodPut, "/pms-event-decisions/settings", map[string]any{
		"expected_config_version": 0, "enabled": false, "expire_after_days": 1,
		"reason": "stale version", "password": f.password}); st != http.StatusConflict {
		t.Fatalf("stale version = %d, want 409", st)
	}
	// The setting's start is moved three days back -- the only clock this test can move -- so a message
	// received two days ago counts as having arrived after it was switched on.
	fresh := f.seedReviewMessage(t, iface, "R-NEW", "2", 0)
	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_review_expiry_settings
		SET applies_from = now() - interval '3 days' WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// fresh was received now(): not yet due. Make a due one inside the window.
	due := f.seedReviewMessage(t, iface, "R-DUE", "3", 2*24*time.Hour)
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT iam_v2.pms_review_expire_due($1,$2,500)`, f.tenant, f.site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expired %d messages, want exactly the one received after the setting and past the period", n)
	}
	var disp string
	if err := f.pool.QueryRow(ctx, `SELECT disposition FROM iam_v2.pms_case_resolutions WHERE stay_event_id=$1::uuid`, due).Scan(&disp); err != nil || disp != "EXPIRED_UNDECIDED" {
		t.Fatalf("the due message: %v %q", err, disp)
	}
	for _, id := range []string{old, fresh} {
		var c int
		_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.pms_case_resolutions WHERE stay_event_id=$1::uuid`, id).Scan(&c)
		if c != 0 {
			t.Fatalf("message %s was expired; only messages received after the setting and past the period may be", id)
		}
	}
	if f.auditTargetCount(t, "pms_event.expiry_setting_changed", f.site) != 1 {
		t.Fatal("the setting change was not audited")
	}
}

func TestIntegration_API_OpeningRosterReconciliationWritesNothing(t *testing.T) {
	f := newAPI(t, "hotel_it_manager")
	iface, rev1, _ := f.seedInterface(t)
	f.runtimeState(t, iface, rev1, "CONNECTED", "IN_SYNC", "CONTINUOUS", ptrTime(time.Now()))
	runs := func() int {
		var n int
		_ = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.pms_roster_reconciliation_runs
			WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&n)
		return n
	}
	for i := 0; i < 3; i++ {
		if st, body := f.do(t, http.MethodGet, "/pms-roster-reconciliation/", nil); st != http.StatusOK {
			t.Fatalf("open = %d %v", st, body)
		}
	}
	if n := runs(); n != 0 {
		t.Fatalf("opening the page wrote %d run rows (H-13)", n)
	}
	if st, body := f.do(t, http.MethodPost, "/pms-roster-reconciliation/preview",
		map[string]any{"pms_interface_id": iface}); st != http.StatusOK {
		t.Fatalf("dry run = %d %v", st, body)
	}
	if n := runs(); n != 1 {
		t.Fatalf("an explicit dry run wrote %d rows, want 1", n)
	}
	st, body := f.do(t, http.MethodGet, "/pms-roster-reconciliation/", nil)
	if st != http.StatusOK || body["last_run_mode"] != "DRY_RUN" {
		t.Fatalf("the page does not show the last run: %v", body)
	}
}

func TestIntegration_API_ViewerCannotRunADryRun(t *testing.T) {
	f := newAPI(t, "front_office_operator")
	if st, _ := f.do(t, http.MethodPost, "/pms-roster-reconciliation/preview", map[string]any{}); st != http.StatusForbidden {
		t.Fatalf("a read-only role ran a dry run: %d", st)
	}
}

func TestIntegration_API_RosterSettingsNeedStepUpVersionAndReason(t *testing.T) {
	f := newAPI(t, "hotel_it_manager")
	base := map[string]any{"roster_trust_min": 40, "reason": "smaller property"}
	if st, _ := f.do(t, http.MethodPut, "/pms-roster-reconciliation/settings", base); st != http.StatusBadRequest {
		t.Fatalf("no version = %d, want 400", st)
	}
	base["expected_config_version"] = 0
	if st, _ := f.do(t, http.MethodPut, "/pms-roster-reconciliation/settings", base); st != http.StatusUnauthorized {
		t.Fatalf("no password = %d, want 401", st)
	}
	base["password"] = f.password
	if st, body := f.do(t, http.MethodPut, "/pms-roster-reconciliation/settings", base); st != http.StatusOK {
		t.Fatalf("save = %d %v", st, body)
	}
	if st, _ := f.do(t, http.MethodPut, "/pms-roster-reconciliation/settings", base); st != http.StatusConflict {
		t.Fatalf("stale version = %d, want 409", st)
	}
	base["expected_config_version"] = 1
	base["roster_trust_min"] = 0
	if st, body := f.do(t, http.MethodPut, "/pms-roster-reconciliation/settings", base); st != http.StatusBadRequest {
		t.Fatalf("out of bounds = %d %v, want 400", st, body)
	}
	if f.auditTargetCount(t, "pms_roster_reconciliation.settings_changed", f.site) != 1 {
		t.Fatal("the settings change was not audited exactly once")
	}
}

func TestIntegration_API_ViewerDoesNotSeeTheRoomCredential(t *testing.T) {
	admin := newAPI(t)
	iface, _, _ := admin.seedInterface(t)
	admin.seedStay(t, iface, "RES-SECRET-77", "770", "Lindqvist")

	viewer := newAPIIn(t, admin.tenant, "site_viewer")
	// The viewer fixture is a second site under the same tenant; point it at the admin's site.
	viewer.app.siteID = admin.site
	st, raw := viewer.doRaw(t, http.MethodGet, "/pms-stays/", nil)
	if st != http.StatusOK {
		t.Fatalf("viewer stays = %d %s", st, raw)
	}
	for _, secret := range []string{"RES-SECRET-77", "Lindqvist"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("the viewer's stays response carries %q", secret)
		}
	}
	if !strings.Contains(raw, `"identity_hidden":true`) || !strings.Contains(raw, "770") {
		t.Fatalf("the viewer should still see the room and be told identity is hidden: %s", raw)
	}
	st, page := viewer.getWithHeader(t, "/pms-stays/", map[string]string{staySearchHeader: "Lindqvist"})
	if st != http.StatusOK || page["summary"].(map[string]any)["total"].(float64) != 0 {
		t.Fatalf("a viewer could find a stay by guest name: %v", page)
	}
	_, adminRaw := admin.doRaw(t, http.MethodGet, "/pms-stays/", nil)
	if !strings.Contains(adminRaw, "RES-SECRET-77") || !strings.Contains(adminRaw, "Lindqvist") {
		t.Fatal("site_admin must still see the guest's name and reservation")
	}
}

func TestIntegration_API_PublishThatTakesRoomChargeOffNeedsConfirmation(t *testing.T) {
	f := newAPI(t)
	iface, rev1, _ := f.seedInterface(t) // rev1 is published with posting target RESERVATION
	var unset string
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO iam_v2.pms_interface_revisions
		  (tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config,normalization_version)
		VALUES ($1,$2,$3::uuid,3,'Europe/Berlin','UNSET','{}'::jsonb,1) RETURNING id::text`,
		f.tenant, f.site, iface).Scan(&unset); err != nil {
		t.Fatal(err)
	}
	req := map[string]any{"revision_id": unset, "expected_revision_id": rev1,
		"reason_code": "ENDPOINT_CHANGE", "password": f.password}
	st, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", req)
	if st != http.StatusConflict || body["error"] != "room_charge_reset_unconfirmed" {
		t.Fatalf("unconfirmed publish = %d %v, want 409 room_charge_reset_unconfirmed", st, body)
	}
	var cur string
	_ = f.pool.QueryRow(context.Background(), `SELECT current_revision_id::text FROM iam_v2.pms_interfaces WHERE id=$1::uuid`, iface).Scan(&cur)
	if cur != rev1 {
		t.Fatal("a refused publish changed the published revision")
	}
	req["confirm_room_charge_reset"] = true
	if st, body := f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", req); st != http.StatusOK {
		t.Fatalf("confirmed publish = %d %v", st, body)
	}
}

func (f *apiFixture) createAuthorPublish(t *testing.T, label, endpoint string) (iface, rev string, publishStatus int, publishBody map[string]any) {
	t.Helper()
	st, body := f.do(t, http.MethodPost, "/pms-interfaces/", map[string]any{"connector_kind": "protel-fias", "display_label": label})
	if st != http.StatusCreated {
		t.Fatalf("create %q = %d %v", label, st, body)
	}
	iface = body["id"].(string)
	st, body = f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/revisions", validRevisionBody(endpoint))
	if st != http.StatusCreated {
		t.Fatalf("author %q = %d %v", label, st, body)
	}
	rev = body["revision_id"].(string)
	publishStatus, publishBody = f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/publish", map[string]any{
		"revision_id": rev, "expected_revision_id": "", "reason_code": "INITIAL_SETUP", "password": f.password})
	return
}

func (f *apiFixture) activate(t *testing.T, iface string) (int, map[string]any) {
	t.Helper()
	return f.do(t, http.MethodPost, "/pms-interfaces/"+iface+"/lifecycle", map[string]any{
		"state": "ACTIVE", "reason_code": "COMMISSIONING", "password": f.password})
}

func TestIntegration_API_TwoIndependentProtelFIASSystemsCanBeConfigured(t *testing.T) {
	f := newAPI(t)
	f.seedInterface(t)
	a, revA, st, b := f.createAuthorPublish(t, "Hotel A PMS", "pms-a.local:5010")
	if st != http.StatusOK {
		t.Fatalf("publish A = %d %v", st, b)
	}
	c, revC, st, b := f.createAuthorPublish(t, "Hotel B PMS", "pms-b.local:5010")
	if st != http.StatusOK {
		t.Fatalf("publish B on a different PMS = %d %v", st, b)
	}
	for _, id := range []string{a, c} {
		if st, b := f.activate(t, id); st != http.StatusOK {
			t.Fatalf("activate %s = %d %v", id, st, b)
		}
	}
	var fpA, fpC string
	_ = f.pool.QueryRow(context.Background(), `SELECT source_fingerprint FROM iam_v2.pms_interface_revisions WHERE id=$1::uuid`, revA).Scan(&fpA)
	_ = f.pool.QueryRow(context.Background(), `SELECT source_fingerprint FROM iam_v2.pms_interface_revisions WHERE id=$1::uuid`, revC).Scan(&fpC)
	if fpA == "" || fpA == fpC {
		t.Fatalf("each system needs its own source identity: %q %q", fpA, fpC)
	}
	if n := f.count(t, `SELECT count(*) FROM iam_v2.pms_interfaces WHERE tenant_id=$1 AND connector_kind='protel-fias' AND lifecycle_state='ACTIVE'`, f.tenant); n < 3 {
		t.Fatalf("%d active Protel FIAS interfaces, want at least 3", n)
	}
}

func TestIntegration_API_TheSamePhysicalPMSCannotBeOwnedTwice(t *testing.T) {
	f := newAPI(t)
	a, _, st, b := f.createAuthorPublish(t, "Owner", "Shared-PMS.local:5010")
	if st != http.StatusOK {
		t.Fatalf("publish owner = %d %v", st, b)
	}
	dup, dupRev, st, b := f.createAuthorPublish(t, "Duplicate", " shared-pms.LOCAL:5010 ")
	if st != http.StatusConflict || b["error"] != "pms_source_in_use" {
		t.Fatalf("publishing a second connection to the same PMS = %d %v, want 409 pms_source_in_use", st, b)
	}
	if strings.Contains(fmt.Sprint(b), "shared-pms") {
		t.Fatalf("the refusal leaks the endpoint: %v", b)
	}
	var cur *string
	_ = f.pool.QueryRow(context.Background(), `SELECT current_revision_id::text FROM iam_v2.pms_interfaces WHERE id=$1::uuid`, dup).Scan(&cur)
	if cur != nil {
		t.Fatal("a refused publication still changed the interface")
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2::uuid WHERE id=$1::uuid`, dup, dupRev); err != nil {
		t.Fatal(err)
	}
	if st, b := f.activate(t, dup); st != http.StatusConflict || b["error"] != "pms_source_in_use" {
		t.Fatalf("activating a duplicate source = %d %v, want 409 pms_source_in_use", st, b)
	}

	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interfaces SET lifecycle_state='DECOMMISSIONED' WHERE id=$1::uuid`, a); err != nil {
		t.Fatal(err)
	}
	if st, b := f.activate(t, dup); st != http.StatusOK {
		t.Fatalf("a decommissioned owner must release the source: %d %v", st, b)
	}
}

func TestIntegration_API_DuplicateSourceIsDetected(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	mk := func(label string) string {
		var iface, rev string
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interfaces(tenant_id,site_id,connector_kind,display_label,lifecycle_state)
			VALUES ($1,$2,'protel-fias',$3,'AUTH_DISABLED') RETURNING id::text`, f.tenant, f.site, label).Scan(&iface); err != nil {
			t.Fatal(err)
		}
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.pms_interface_revisions
			(tenant_id,site_id,pms_interface_id,revision_no,source_timezone,posting_target_model,config,normalization_version,source_fingerprint)
			VALUES ($1,$2,$3::uuid,1,'UTC','UNSET','{}'::jsonb,1,'same-physical-source') RETURNING id::text`,
			f.tenant, f.site, iface).Scan(&rev); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$2::uuid WHERE id=$1::uuid`, iface, rev); err != nil {
			t.Fatal(err)
		}
		return iface
	}
	mk("Old link")
	mk("New link")
	st, body := f.do(t, http.MethodGet, "/pms-source-conflicts/", nil)
	if st != http.StatusOK {
		t.Fatalf("conflicts = %d %v", st, body)
	}
	list := body["conflicts"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["detected"] != true {
		t.Fatalf("conflicts = %v, want one detected duplicate source", list)
	}
}

func TestIntegration_API_TransferAcceptsTheReservationNumber(t *testing.T) {
	f := newAPI(t)
	iface, _, _ := f.seedInterface(t)
	f.seedStay(t, iface, "RES-FROM-1", "101", "")
	st, body := f.do(t, http.MethodPost, "/stay-transfers/preview",
		map[string]any{"from_stay_id": "RES-FROM-1", "to_stay_id": "RES-NOWHERE"})
	if st != http.StatusNotFound || body["error"] != "to_stay_not_found" {
		t.Fatalf("unknown destination reference = %d %v, want 404 to_stay_not_found", st, body)
	}
	st, body = f.do(t, http.MethodPost, "/stay-transfers/preview",
		map[string]any{"from_stay_id": "RES-NOWHERE", "to_stay_id": "RES-FROM-1"})
	if st != http.StatusNotFound || body["error"] != "from_stay_not_found" {
		t.Fatalf("unknown source reference = %d %v", st, body)
	}
	// Both resolve: the reference reaches the transfer rules (same interface -> a room move, not a transfer).
	f.seedStay(t, iface, "RES-TO-1", "102", "")
	st, body = f.do(t, http.MethodPost, "/stay-transfers/preview",
		map[string]any{"from_stay_id": "RES-FROM-1", "to_stay_id": "RES-TO-1"})
	if st >= 500 {
		t.Fatalf("a resolvable reference answered %d %v", st, body)
	}
}

func TestIntegration_API_MalformedIdsAreNotFound(t *testing.T) {
	f := newAPI(t)
	for _, path := range []string{"/pms-stays/not-a-uuid", "/pms-interfaces/not-a-uuid", "/pms-interfaces/not-a-uuid/revisions",
		"/pms-interfaces/not-a-uuid/health"} {
		if st, _ := f.do(t, http.MethodGet, path, nil); st != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", path, st)
		}
	}
	if st, _ := f.do(t, http.MethodDelete, "/pms-routing/not-a-uuid", nil); st != http.StatusNotFound {
		t.Fatalf("DELETE routing with a malformed id = %d, want 404", st)
	}
}
