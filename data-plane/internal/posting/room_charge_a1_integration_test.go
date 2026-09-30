//go:build integration

package posting

// PHASE-0 AMENDMENT A1 (D46), on the FULL appliance schema: a room charge targets the RESERVATION (RN + G#).
//
//   - G# is stable and pinned at purchase; RN is mutable and refreshed immediately before each attempt;
//   - pmsd's authorisation refuses a command whose room is no longer the reservation's (NOT_SENT, not a failure);
//   - once written, an attempt is immutable: its PA or timeout decides, and nothing is resent automatically;
//   - a PA is "not posted" only for a code the vendor confirmed on the interface, otherwise UNKNOWN;
//   - confirmed NP / NG / NR block the stay as the contract says, NA / RY do not, and no operator can lift a
//     Protel block; UNKNOWN blocks until its review;
//   - one unresolved room charge per stay; terminal charges do not prevent the next purchase;
//   - a stay that stops being postable before sending ends the charge as definitely not posted.
//
// Set ROOMCHARGE_TEST_DSN to a database built by scripts/sitedb-dev.sh. No test here reaches a PMS: every
// transport is an in-process script, and the one that plays pmsd asks the database exactly what pmsd asks.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---- helpers -------------------------------------------------------------------------------------------

// rcWorker is the pool the posting engine runs on: the real least-privilege worker login (svc_posting) when
// ROOMCHARGE_WORKER_DSN is set, so these tests prove the worker's grants suffice; the admin pool otherwise.
func rcWorker(t *testing.T, admin *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("ROOMCHARGE_WORKER_DSN") == "" {
		return admin
	}
	return rcPool(t, "ROOMCHARGE_WORKER_DSN")
}

func confirmAnswer(t *testing.T, p *pgxpool.Pool, s rcScope, code string) {
	t.Helper()
	mustExec(t, p, `SELECT iam_v2.pms_answer_confirmation_record($1,$2,$3,$4,'CONFIRM',
		'Vendor confirmed by e-mail on 2026-09-29, reference FIAS-AS-2026-01.','test confirmation',$5)`,
		s.tenant, s.site, s.iface, code, s.op)
}

// moveRoom is what an applied GC does to the stay: the SAME reservation, a new current room.
func moveRoom(t *testing.T, p *pgxpool.Pool, s rcScope, room string) {
	t.Helper()
	rcGuarded(t, p, "stay", `UPDATE iam_v2.stays SET normalized_room_number=$2 WHERE id=$1 RETURNING id::text`, s.stay, room)
	if n := scan1[int](t, p, `SELECT count(*)::int FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id=$2`,
		s.iface, s.reservation); n != 1 {
		t.Fatalf("a room move never creates a second stay for the reservation: %d", n)
	}
}

// freshEvidence is what an applied guest record (or resync record) stamps: new occupancy evidence, now.
func freshEvidence(t *testing.T, p *pgxpool.Pool, s rcScope) {
	t.Helper()
	rcGuarded(t, p, "stay", `UPDATE iam_v2.stays SET occupancy_evidence_at = clock_timestamp(), occupancy_ingested_at = now(),
		occupancy_revision_id = $2::uuid, occupancy_normalization_version = 1, occupancy_clock_suspect = false,
		occupancy_evidence_version = occupancy_evidence_version + 1 WHERE id=$1 RETURNING id::text`, s.stay, s.rev)
}

func stayPermission(t *testing.T, p *pgxpool.Pool, s rcScope) (bool, string) {
	t.Helper()
	var ok bool
	var reason *string
	if err := p.QueryRow(context.Background(), `SELECT posting_allowed, posting_block_reason FROM iam_v2.stays WHERE id=$1`,
		s.stay).Scan(&ok, &reason); err != nil {
		t.Fatal(err)
	}
	if reason == nil {
		return ok, ""
	}
	return ok, *reason
}

func activeBlocks(t *testing.T, p *pgxpool.Pool, s rcScope) []string {
	t.Helper()
	rows, err := p.Query(context.Background(),
		`SELECT reason FROM iam_v2.stay_posting_blocks WHERE stay_id=$1 AND cleared_at IS NULL ORDER BY reason`, s.stay)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		_ = rows.Scan(&r)
		out = append(out, r)
	}
	return out
}

type attemptRow struct {
	no                int
	rn, g, outcome    string
	pnumber, paStatus string
}

func attempts(t *testing.T, p *pgxpool.Pool, posting string) []attemptRow {
	t.Helper()
	rows, err := p.Query(context.Background(), `SELECT attempt_no, rn, g_number, outcome, p_number, coalesce(pa_as_status,'')
		FROM iam_v2.posting_attempts WHERE internal_posting_id=$1 ORDER BY attempt_no`, posting)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []attemptRow
	for rows.Next() {
		var a attemptRow
		if err := rows.Scan(&a.no, &a.rn, &a.g, &a.outcome, &a.pnumber, &a.paStatus); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

// pmsdLike is a transport that behaves as pmsd does at the first-byte boundary: before "writing", it runs an
// optional hook (e.g. the PMS moving the guest), then asks the database the exact question pmsd asks
// (p4_posting_command_authorised). A refusal is answered NOT_TRANSMITTED with pmsd's reason; an authorised
// command is "written" and answered by answer (nil answer = no PA: UNKNOWN).
type pmsdLike struct {
	db       *pgxpool.Pool
	iface    string
	before   func(n int) // n = 1-based call number
	answer   func(pn int64) (*PA, error)
	mu       sync.Mutex
	calls    int
	written  []string
	verdicts []string
}

func (m *pmsdLike) SendPS(ctx context.Context, _ string, pn int64, body string) (*PA, error) {
	m.mu.Lock()
	m.calls++
	n := m.calls
	m.mu.Unlock()
	if m.before != nil {
		m.before(n)
	}
	sum := sha256.Sum256([]byte(body))
	var verdict string
	if err := m.db.QueryRow(ctx, `SELECT iam_v2.p4_posting_command_authorised($1::uuid,$2,$3)`,
		m.iface, strconv.FormatInt(pn, 10), hex.EncodeToString(sum[:])).Scan(&verdict); err != nil {
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "authorisation unavailable", notSent: true, reason: "AUTHORISATION_UNAVAILABLE"}
	}
	m.mu.Lock()
	m.verdicts = append(m.verdicts, verdict)
	m.mu.Unlock()
	if verdict != "AUTHORISED" {
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "pmsd wrote nothing: " + verdict, notSent: true, reason: verdict}
	}
	m.mu.Lock()
	m.written = append(m.written, body)
	m.mu.Unlock()
	if m.answer == nil {
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "no conclusive answer: ANSWER_TIMEOUT"}
	}
	return m.answer(pn)
}

func (m *pmsdLike) writtenBodies() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.written...)
}

func okAnswer(pn int64) (*PA, error) { return &PA{PNumber: pn, AS: "OK"}, nil }

func answerWith(as string) func(int64) (*PA, error) {
	return func(pn int64) (*PA, error) { return &PA{PNumber: pn, AS: as}, nil }
}

// ---- E14 / E5: posting permission is evaluated by the database ------------------------------------------

func TestA1_PostingPermissionIsEvaluatedNotAssumed(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	if ok, reason := stayPermission(t, admin, s); !ok || reason != "" {
		t.Fatalf("an in-house stay with a reservation and no block is postable: %v %q", ok, reason)
	}
	// ADMIN_BLOCK is the only block an operator sets or clears.
	if r := scan1[string](t, admin, `SELECT iam_v2.p4_admin_posting_block($1,$2,$3,'SET','front desk hold',$4)`,
		s.tenant, s.site, s.stay, s.op); r != "SET" {
		t.Fatalf("set: %s", r)
	}
	if ok, reason := stayPermission(t, admin, s); ok || reason != "ADMIN_BLOCK" {
		t.Fatalf("an administrative block stops room charge: %v %q", ok, reason)
	}
	if _, err := admin.Exec(context.Background(), `SELECT iam_v2.p4_create_room_charge_posting($1,$2,$3)`, s.tenant, s.site, s.settle); err == nil ||
		!strings.Contains(err.Error(), "POSTING_NOT_ALLOWED") {
		t.Fatalf("a blocked stay is not charged: %v", err)
	}
	if r := scan1[string](t, admin, `SELECT iam_v2.p4_admin_posting_block($1,$2,$3,'CLEAR','hold released',$4)`,
		s.tenant, s.site, s.stay, s.op); r != "CLEARED" {
		t.Fatalf("clear: %s", r)
	}
	if ok, _ := stayPermission(t, admin, s); !ok {
		t.Fatal("clearing the administrative block restores posting")
	}
}

// THE OFFER-TIME FRESHNESS READER (migrations 0101/0102). scd asks it, as svc_scd, before offering Room charge:
// true for a fresh, onboarded interface, false once the feed is not fresh. The first version read "no block"
// (NULL) as a block, so Room charge was never offered on PRE-LIVE.
func TestA1_OfferFreshnessReaderAnswersFreshAndStale(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	if !scan1[bool](t, admin, `SELECT iam_v2.p4_room_charge_interface_fresh($1,$2,$3)`, s.tenant, s.site, s.iface) {
		t.Fatal("a fresh, onboarded interface reads as not fresh, so Room charge would never be offered")
	}
	mustExec(t, admin, `UPDATE iam_v2.pms_interface_runtime SET transport_status='DISCONNECTED' WHERE pms_interface_id=$1`, s.iface)
	if scan1[bool](t, admin, `SELECT iam_v2.p4_room_charge_interface_fresh($1,$2,$3)`, s.tenant, s.site, s.iface) {
		t.Fatal("a disconnected interface reads as fresh")
	}
	if scan1[bool](t, admin, `SELECT iam_v2.p4_room_charge_interface_fresh($1,$2,gen_random_uuid())`, s.tenant, s.site) {
		t.Fatal("an interface outside the scope reads as fresh")
	}
}

// ---- E18 room-move races -------------------------------------------------------------------------------

// Case 1: the room moves BEFORE the purchase. The posting pins the reservation; the attempt carries the room
// the reservation has now.
func TestA1_RoomMoveBeforePurchase(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	moveRoom(t, admin, s, "205")
	posting := createRoomCharge(t, admin, s)
	if g := scan1[string](t, admin, `SELECT g_number FROM iam_v2.pms_postings WHERE id=$1`, posting); g != s.reservation {
		t.Fatalf("the posting pins the reservation: %s", g)
	}
	tr := &pmsdLike{db: admin, iface: s.iface, answer: okAnswer}
	if out, err := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr).RunOnce(context.Background(), s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("run: %+v %v", out, err)
	}
	if w := tr.writtenBodies(); len(w) != 1 || !strings.HasPrefix(w[0], "PS|RN205|G#"+s.reservation+"|") {
		t.Fatalf("the PS carries the current room and the reservation: %q", w)
	}
}

// Case 2: the room moves AFTER the purchase and BEFORE the attempt is created. The purchase is NOT failed; the
// attempt is built with the new room and the same reservation.
func TestA1_RoomMoveAfterPurchaseBeforeAttempt(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	posting := createRoomCharge(t, admin, s)
	moveRoom(t, admin, s, "205")
	tr := &pmsdLike{db: admin, iface: s.iface, answer: okAnswer}
	out, err := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr).RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if err != nil || out.Result != "POSTED" {
		t.Fatalf("a room move before the attempt does not fail the purchase: %+v %v", out, err)
	}
	a := attempts(t, admin, posting)
	if len(a) != 1 || a[0].rn != "205" || a[0].g != s.reservation || a[0].outcome != "ACKED" {
		t.Fatalf("one attempt, RN205 + the same G#: %+v", a)
	}
	if st, pu, n := rcState(t, admin, s); st != "SETTLED" || pu != "GRANTED" || n != 1 {
		t.Fatalf("settled and granted: %s %s %d", st, pu, n)
	}
}

// Case 3: the attempt is built for RN1421/G#, then the guest moves (GC) to RN205 before the socket write.
// pmsd refuses the prepared command (ROOM_CHANGED): the RN1421 PS is never written, the attempt is NOT_SENT,
// and the next attempt carries RN205 and the same G#. That is not a retry: nothing was transmitted.
func TestA1_RoomMoveAfterAttemptBeforeWrite(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	posting := createRoomCharge(t, admin, s)
	tr := &pmsdLike{db: admin, iface: s.iface, answer: okAnswer, before: func(n int) {
		if n == 1 {
			moveRoom(t, admin, s, "205") // the GC lands between attempt creation and the write
		}
	}}
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr)
	out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if err != nil || out.Result != "NOT_SENT" || out.NotSentReason != "ROOM_CHANGED" {
		t.Fatalf("the stale command is refused before the write: %+v %v", out, err)
	}
	if len(tr.writtenBodies()) != 0 {
		t.Fatalf("the RN1421 PS must never be written: %q", tr.writtenBodies())
	}
	if st, _, _ := rcState(t, admin, s); st != "IN_PROGRESS" {
		t.Fatalf("NOT_SENT is not a PMS failure and not UNKNOWN; the charge is still in progress: %s", st)
	}
	out, err = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if err != nil || out.Result != "POSTED" {
		t.Fatalf("the fresh attempt is sent: %+v %v", out, err)
	}
	a := attempts(t, admin, posting)
	if len(a) != 2 || a[0].rn != "1421" || a[0].outcome != "NOT_SENT" || a[1].rn != "205" || a[1].outcome != "ACKED" ||
		a[0].g != s.reservation || a[1].g != s.reservation || a[0].pnumber == a[1].pnumber {
		t.Fatalf("attempt 1 NOT_SENT with RN1421, attempt 2 ACKED with RN205, same G#, distinct P#: %+v", a)
	}
	if w := tr.writtenBodies(); len(w) != 1 || !strings.HasPrefix(w[0], "PS|RN205|G#"+s.reservation+"|") {
		t.Fatalf("exactly one PS reached the PMS, with RN205: %q", w)
	}
	var reason string
	if err := admin.QueryRow(context.Background(), `SELECT e.detail->>'reason' FROM iam_v2.posting_attempt_events e
		JOIN iam_v2.posting_attempts a ON a.id=e.posting_attempt_id WHERE a.internal_posting_id=$1 AND a.attempt_no=1
		AND e.event_type='NOT_SENT'`, posting).Scan(&reason); err != nil || reason != "ROOM_CHANGED" {
		t.Fatalf("the pre-send stale outcome is audited with its reason: %q %v", reason, err)
	}
}

// Case 5: the PS is written, then the guest moves and no PA arrives. RN205 is never sent automatically; the
// original attempt stays authoritative (UNKNOWN), and UNKNOWN blocks any new room charge until review.
func TestA1_RoomMoveAfterWriteBeforePA(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	posting := createRoomCharge(t, admin, s)
	var tr *pmsdLike
	tr = &pmsdLike{db: admin, iface: s.iface, answer: func(pn int64) (*PA, error) {
		moveRoom(t, admin, s, "205") // after the write, before any PA
		return nil, &handoffError{code: ErrTransportUnavailable, msg: "no conclusive answer: ANSWER_TIMEOUT"}
	}}
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr)
	out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if out.Result != "UNKNOWN" || CodeOf(err) != ErrUnknownTerminal {
		t.Fatalf("a written command without a PA is UNKNOWN: %+v %v", out, err)
	}
	for i := 0; i < 3; i++ {
		_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	}
	if w := tr.writtenBodies(); len(w) != 1 || !strings.HasPrefix(w[0], "PS|RN1421|") {
		t.Fatalf("nothing is resent, least of all for RN205: %q", w)
	}
	a := attempts(t, admin, posting)
	if len(a) != 1 || a[0].rn != "1421" || a[0].outcome != "UNKNOWN" {
		t.Fatalf("the original attempt stays authoritative and its RN is never rewritten: %+v", a)
	}
	if ok, reason := stayPermission(t, admin, s); ok || reason != "POSTING_UNRESOLVED" {
		t.Fatalf("UNKNOWN blocks the stay: %v %q", ok, reason)
	}
	addRoomChargePurchase(t, admin, &s)
	if _, err := admin.Exec(context.Background(), `SELECT iam_v2.p4_create_room_charge_posting($1,$2,$3)`, s.tenant, s.site, s.settle); err == nil {
		t.Fatal("no new room charge while one is UNKNOWN")
	}
}

// Case 6: after the PA, a room move changes nothing about the posted attempt; a later purchase uses the new room.
func TestA1_RoomMoveAfterPAAndSequentialPurchase(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	first := createRoomCharge(t, admin, s)
	tr := &pmsdLike{db: admin, iface: s.iface, answer: okAnswer}
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr)
	if out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("first: %+v %v", out, err)
	}
	moveRoom(t, admin, s, "205")
	if a := attempts(t, admin, first); a[0].rn != "1421" {
		t.Fatalf("a historical attempt's room is never rewritten: %+v", a)
	}
	addRoomChargePurchase(t, admin, &s)
	second := createRoomCharge(t, admin, s) // terminal first charge: a sequential purchase is allowed (E16)
	if out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("second: %+v %v", out, err)
	}
	if a := attempts(t, admin, second); len(a) != 1 || a[0].rn != "205" || a[0].g != s.reservation {
		t.Fatalf("the later purchase uses the new room and the same reservation: %+v", a)
	}
}

// ---- E16: one unresolved room charge per stay ------------------------------------------------------------

func TestA1_OneUnresolvedChargePerStay(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	first := s
	createRoomCharge(t, admin, first)
	addRoomChargePurchase(t, admin, &s)
	if _, err := admin.Exec(context.Background(), `SELECT iam_v2.p4_create_room_charge_posting($1,$2,$3)`, s.tenant, s.site, s.settle); err == nil ||
		!strings.Contains(err.Error(), "ROOM_CHARGE_UNRESOLVED") {
		t.Fatalf("a second room charge while the first is in progress is refused: %v", err)
	}
	if open := scan1[bool](t, admin, `SELECT iam_v2.p4_stay_room_charge_open($1)`, s.stay); !open {
		t.Fatal("the stay reports its open room charge (the offer is withheld)")
	}
	// The first concludes (PA=OK): the second purchase is now admitted.
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), &pmsdLike{db: admin, iface: s.iface, answer: okAnswer})
	if out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface); err != nil || out.Result != "POSTED" {
		t.Fatalf("first: %+v %v", out, err)
	}
	createRoomCharge(t, admin, s)
}

// ---- E15 / E17: the answer decides, conservatively --------------------------------------------------------

func TestA1_UnconfirmedRefusalIsUnknown(t *testing.T) {
	for _, code := range []string{"NP", "NG", "NR", "NA", "RY", "UR"} {
		t.Run(code, func(t *testing.T) {
			admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
			s := seedRoomCharge(t, admin)
			posting := createRoomCharge(t, admin, s)
			e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), &pmsdLike{db: admin, iface: s.iface, answer: answerWith(code)})
			out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
			if out.Result != "UNKNOWN" || CodeOf(err) != ErrUnknownTerminal {
				t.Fatalf("an unconfirmed %s is UNKNOWN: %+v %v", code, out, err)
			}
			if a := attempts(t, admin, posting); a[0].outcome != "UNKNOWN" || a[0].paStatus != code {
				t.Fatalf("recorded as UNKNOWN with the status it carried: %+v", a)
			}
			if st, pu, _ := rcState(t, admin, s); st != "MANUAL_REVIEW" || pu != "MANUAL_REVIEW" {
				t.Fatalf("manual review: %s %s", st, pu)
			}
			if b := activeBlocks(t, admin, s); len(b) != 1 || b[0] != "POSTING_UNRESOLVED" {
				t.Fatalf("POSTING_UNRESOLVED blocks the stay: %v", b)
			}
		})
	}
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	if _, err := admin.Exec(context.Background(), `SELECT iam_v2.pms_answer_confirmation_record($1,$2,$3,'UR','CONFIRM',
		'Vendor confirmed by e-mail on 2026-09-29, reference FIAS-AS-2026-01.','x',$4)`, s.tenant, s.site, s.iface, s.op); err == nil {
		t.Fatal("UR can never be confirmed as not posted")
	}
}

func TestA1_ConfirmedAnswersHaveTheirStayEffect(t *testing.T) {
	cases := []struct {
		code   string
		blocks []string
		resync bool
	}{
		{"NP", []string{"PMS_NO_POST"}, false},
		{"NG", []string{"PMS_DATA_SUSPECT"}, true},
		{"NR", []string{"PMS_DATA_SUSPECT"}, true},
		{"NA", nil, false},
		{"RY", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
			s := seedRoomCharge(t, admin)
			confirmAnswer(t, admin, s, tc.code)
			createRoomCharge(t, admin, s)
			tr := &pmsdLike{db: admin, iface: s.iface, answer: answerWith(tc.code)}
			e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr)
			out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
			if err != nil || out.Result != "REJECTED" {
				t.Fatalf("a confirmed %s is a definite refusal: %+v %v", tc.code, out, err)
			}
			if st, pu, n := rcState(t, admin, s); st != "FAILED" || pu != "FAILED" || n != 0 {
				t.Fatalf("the purchase fails and nothing is granted: %s %s %d", st, pu, n)
			}
			for i := 0; i < 2; i++ {
				_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
			}
			if len(tr.writtenBodies()) != 1 {
				t.Fatalf("a refusal is never retried automatically: %d", len(tr.writtenBodies()))
			}
			if b := activeBlocks(t, admin, s); strings.Join(b, ",") != strings.Join(tc.blocks, ",") {
				t.Fatalf("stay effect of %s: want %v, got %v", tc.code, tc.blocks, b)
			}
			requested := scan1[bool](t, admin, `SELECT resync_command_id IS NOT NULL FROM iam_v2.pms_interface_runtime WHERE pms_interface_id=$1`, s.iface)
			if requested != tc.resync {
				t.Fatalf("resync requested for %s: want %v, got %v", tc.code, tc.resync, requested)
			}
			// No block: a later new purchase may be attempted.
			if tc.blocks == nil {
				addRoomChargePurchase(t, admin, &s)
				createRoomCharge(t, admin, s)
			}
		})
	}
}

// No operator can lift a Protel block to permit another charge (A1 rule 9). NP is cleared only by explicit
// Protel data (the feed carries none, so it stays); NG/NR data-suspect is cleared by FRESH valid data.
func TestA1_ProtelBlocksAreNotOperatorClearable(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	confirmAnswer(t, admin, s, "NP")
	createRoomCharge(t, admin, s)
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), &pmsdLike{db: admin, iface: s.iface, answer: answerWith("NP")})
	_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if r := scan1[string](t, admin, `SELECT iam_v2.p4_admin_posting_block($1,$2,$3,'CLEAR','please let them charge',$4)`,
		s.tenant, s.site, s.stay, s.op); r != "NOT_BLOCKED" {
		t.Fatalf("the operator path clears only ADMIN_BLOCK: %s", r)
	}
	_, err := admin.Exec(context.Background(), `UPDATE iam_v2.stay_posting_blocks SET cleared_at=now(), cleared_by_source='OPERATOR',
		cleared_by=$2, cleared_reason='bypass' WHERE stay_id=$1 AND reason='PMS_NO_POST'`, s.stay, s.op)
	if err == nil {
		t.Fatal("even a direct write cannot clear PMS_NO_POST as an operator")
	}
	freshEvidence(t, admin, s)
	if ok, reason := stayPermission(t, admin, s); ok || reason != "PMS_NO_POST" {
		t.Fatalf("fresh guest data without an explicit posting permission does not lift NP: %v %q", ok, reason)
	}
}

func TestA1_DataSuspectClearsOnFreshValidData(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	confirmAnswer(t, admin, s, "NG")
	createRoomCharge(t, admin, s)
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), &pmsdLike{db: admin, iface: s.iface, answer: answerWith("NG")})
	_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if ok, reason := stayPermission(t, admin, s); ok || reason != "PMS_DATA_SUSPECT" {
		t.Fatalf("NG marks the data suspect: %v %q", ok, reason)
	}
	if r := scan1[string](t, admin, `SELECT iam_v2.p4_admin_posting_block($1,$2,$3,'CLEAR','please',$4)`,
		s.tenant, s.site, s.stay, s.op); r != "NOT_BLOCKED" {
		t.Fatalf("an operator cannot lift PMS_DATA_SUSPECT: %s", r)
	}
	freshEvidence(t, admin, s) // a fresh guest/resync record for the same reservation, IN_HOUSE, room resolvable
	if ok, reason := stayPermission(t, admin, s); !ok || reason != "" {
		t.Fatalf("fresh valid data clears the suspect block: %v %q", ok, reason)
	}
}

// ---- E4: the stay stops being postable before sending ----------------------------------------------------

func TestA1_AbortBeforeSendWhenTheStayIsNoLongerPostable(t *testing.T) {
	for name, change := range map[string]func(t *testing.T, p *pgxpool.Pool, s rcScope){
		"checked out": func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			rcGuarded(t, p, "stay", `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false,
				effective_checkout_at=now() WHERE id=$1 RETURNING id::text`, s.stay)
		},
		"administratively blocked": func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			mustExec(t, p, `SELECT iam_v2.p4_admin_posting_block($1,$2,$3,'SET','hold',$4)`, s.tenant, s.site, s.stay, s.op)
		},
		"stale PMS data": func(t *testing.T, p *pgxpool.Pool, s rcScope) {
			mustExec(t, p, `UPDATE iam_v2.pms_interface_runtime SET transport_status='DISCONNECTED' WHERE pms_interface_id=$1`, s.iface)
		},
	} {
		t.Run(name, func(t *testing.T) {
			admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
			s := seedRoomCharge(t, admin)
			posting := createRoomCharge(t, admin, s)
			change(t, admin, s)
			tr := &pmsdLike{db: admin, iface: s.iface, answer: okAnswer}
			out, err := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr).RunOnce(context.Background(), s.tenant, s.site, s.iface)
			if err != nil || out.Result != "ABORTED" {
				t.Fatalf("definitely not posted: %+v %v", out, err)
			}
			if len(tr.writtenBodies()) != 0 || len(attempts(t, admin, posting)) != 0 {
				t.Fatal("nothing is built or written")
			}
			if st, pu, n := rcState(t, admin, s); st != "FAILED" || pu != "FAILED" || n != 0 {
				t.Fatalf("the purchase fails and nothing is granted: %s %s %d", st, pu, n)
			}
			if n := scan1[int](t, admin, `SELECT count(*)::int FROM iam_v2.posting_presend_aborts WHERE posting_id=$1`, posting); n != 1 {
				t.Fatalf("the abort is recorded: %d", n)
			}
		})
	}
}

// A charge that may have been transmitted is never aborted: a reviewed retry whose stay then checks out goes
// back to review instead.
func TestA1_ReviewedRetryIsNeverAbortedAfterTransmission(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	posting := createRoomCharge(t, admin, s)
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), &pmsdLike{db: admin, iface: s.iface})
	_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface) // UNKNOWN
	ver := scan1[int](t, admin, `SELECT COALESCE((SELECT review_version FROM iam_v2.posting_review_state WHERE posting_id=$1),0)`, posting)
	mustExec(t, admin, `SELECT iam_v2.record_posting_review_action($1,'CONFIRM_NOT_POSTED_RETRY',$2,'folio verified clean',
		'{"pms_reference":"checked at front office","source":"FO","observed_at":"2026-09-29T10:00:00Z"}'::jsonb,$3,NULL)`, posting, s.op, ver)
	mustExec(t, admin, `SELECT iam_v2.p4_posting_review_apply($1,$2,$3)`, s.tenant, s.site, posting)
	rcGuarded(t, admin, "stay", `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false,
		effective_checkout_at=now() WHERE id=$1 RETURNING id::text`, s.stay)
	out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if out.Result != "DECLINED" || err == nil {
		t.Fatalf("returned to review, not aborted: %+v %v", out, err)
	}
	if n := scan1[int](t, admin, `SELECT count(*)::int FROM iam_v2.posting_presend_aborts WHERE posting_id=$1`, posting); n != 0 {
		t.Fatal("a possibly-transmitted charge is never aborted")
	}
	if ob := scan1[string](t, admin, `SELECT state FROM iam_v2.posting_outbox WHERE posting_id=$1`, posting); ob != "HELD_RECOVERY" {
		t.Fatalf("held for review: %s", ob)
	}
}

// The reviewed retry is not stopped by its OWN unresolved block (it is by any other).
func TestA1_ReviewedRetryIsNotBlockedByItsOwnUnresolvedBlock(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	posting := createRoomCharge(t, admin, s)
	first := true
	tr := &pmsdLike{db: admin, iface: s.iface, answer: func(pn int64) (*PA, error) {
		if first {
			first = false
			return nil, errors.New("connection torn after write")
		}
		return &PA{PNumber: pn, AS: "OK"}, nil
	}}
	e := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr)
	_, _ = e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	moveRoom(t, admin, s, "205") // the guest moved while the charge was under review
	ver := scan1[int](t, admin, `SELECT COALESCE((SELECT review_version FROM iam_v2.posting_review_state WHERE posting_id=$1),0)`, posting)
	mustExec(t, admin, `SELECT iam_v2.record_posting_review_action($1,'CONFIRM_NOT_POSTED_RETRY',$2,'folio verified clean',
		'{"pms_reference":"checked at front office","source":"FO","observed_at":"2026-09-29T10:00:00Z"}'::jsonb,$3,NULL)`, posting, s.op, ver)
	mustExec(t, admin, `SELECT iam_v2.p4_posting_review_apply($1,$2,$3)`, s.tenant, s.site, posting)
	out, err := e.RunOnce(context.Background(), s.tenant, s.site, s.iface)
	if err != nil || out.Result != "POSTED" {
		t.Fatalf("the one authorised attempt is sent: %+v %v", out, err)
	}
	a := attempts(t, admin, posting)
	if len(a) != 2 || a[0].rn != "1421" || a[0].outcome != "UNKNOWN" || a[1].rn != "205" || a[1].outcome != "ACKED" {
		t.Fatalf("attempt 1 keeps RN1421 (UNKNOWN); the authorised attempt carries the current room: %+v", a)
	}
	if b := activeBlocks(t, admin, s); len(b) != 0 {
		t.Fatalf("the unresolved block clears when the charge concludes: %v", b)
	}
	if st, pu, n := rcState(t, admin, s); st != "SETTLED" || pu != "GRANTED" || n != 1 {
		t.Fatalf("settled and granted: %s %s %d", st, pu, n)
	}
}

// pmsd's authorisation, directly: a command whose stay checked out, or whose reservation or room no longer
// matches, is refused before any byte (it is what makes case 3 true in production).
func TestA1_PmsdAuthorisationFollowsTheReservation(t *testing.T) {
	admin := rcPool(t, "ROOMCHARGE_TEST_DSN")
	s := seedRoomCharge(t, admin)
	createRoomCharge(t, admin, s)
	verdicts := []string{}
	tr := &pmsdLike{db: admin, iface: s.iface, before: func(int) {
		rcGuarded(t, admin, "stay", `UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false,
			effective_checkout_at=now() WHERE id=$1 RETURNING id::text`, s.stay)
	}}
	out, _ := NewEngine(transmitCfg, NewRepo(rcWorker(t, admin)), tr).RunOnce(context.Background(), s.tenant, s.site, s.iface)
	verdicts = append(verdicts, tr.verdicts...)
	if out.Result != "NOT_SENT" || len(verdicts) != 1 || verdicts[0] != "STAY_NOT_POSTABLE" || len(tr.writtenBodies()) != 0 {
		t.Fatalf("a checkout between attempt and write refuses the command unwritten: %+v %v", out, verdicts)
	}
}
