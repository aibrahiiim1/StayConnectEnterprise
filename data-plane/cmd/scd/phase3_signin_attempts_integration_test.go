//go:build integration

package main

// Guest sign-in attempts through scd's real handlers: every refusal leaves exactly one row with its reason
// and comparison. No real guest data; all names, rooms and reservations are invented.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

// testSealingKeyID and withSealingKey install a known key; without one the daemon records attempts with a
// NULL sensitive half and the encryption proofs would silently assert nothing.
const testSealingKeyID = "0a5c1e00-0000-4000-8000-0000000000d2"

func withSealingKey(t *testing.T, f *authFixture) {
	t.Helper()
	kr := signinattempt.MapKeyring{testSealingKeyID: bytes.Repeat([]byte{0x5e}, 32)}
	f.srv.signInAttemptKeyring = kr
	f.p3.attempts = signinattempt.NewStore(f.pool, kr, testSealingKeyID)
}

type attemptRow struct {
	ID            string
	Result        string
	MatchedField  string
	Room          string
	RoomInMirror  *bool
	Eligible      *int
	RequestID     string
	VerifierKind  string
	SealedLen     int
	Latency       *int64
	SessionID     string
	EntitlementID string
	Network       string
	Transport     string
}

func lastAttempt(t *testing.T, f *authFixture) attemptRow {
	t.Helper()
	var a attemptRow
	if err := f.pool.QueryRow(context.Background(), `
		SELECT id::text, result, COALESCE(matched_field,''), COALESCE(submitted_room,''), room_in_mirror,
		       eligible_stay_candidates, COALESCE(request_id::text,''), verifier_kind,
		       COALESCE(length(sensitive_ciphertext),0), latency_ms, COALESCE(session_id::text,''),
		       COALESCE(entitlement_id::text,''), COALESCE(guest_network_name,''),
		       COALESCE(pms_transport_status,'')
		  FROM iam_v2.sign_in_attempts
		 WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY occurred_at DESC LIMIT 1`, f.tenant, f.site).
		Scan(&a.ID, &a.Result, &a.MatchedField, &a.Room, &a.RoomInMirror, &a.Eligible, &a.RequestID,
			&a.VerifierKind, &a.SealedLen, &a.Latency, &a.SessionID, &a.EntitlementID, &a.Network,
			&a.Transport); err != nil {
		t.Fatalf("no attempt was recorded at all: %v", err)
	}
	return a
}

func attemptCount(t *testing.T, f *authFixture) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM iam_v2.sign_in_attempts WHERE tenant_id=$1 AND site_id=$2`,
		f.tenant, f.site).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// seedGivenName gives the stay a multi-word given name in one field, as a PMS "FAMILY, FULL GIVEN NAME" is mirrored.
func seedGivenName(t *testing.T, f *authFixture, given string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE iam_v2.stay_guests SET first_name_norm=$2 WHERE stay_id=$1 AND is_primary`,
		f.stay, given); err != nil {
		t.Fatalf("seed the given name: %v", err)
	}
}

// soleOccupant moves the second stay off room 412, leaving one eligible stay.
func soleOccupant(t *testing.T, f *authFixture) {
	t.Helper()
	if _, err := controlledExec(context.Background(), f.pool, "stay",
		`UPDATE iam_v2.stays SET normalized_room_number='777' WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		f.tenant, f.site, f.otherStay); err != nil {
		t.Fatalf("move the second stay: %v", err)
	}
}

// The complete stored given name authenticates; individual words do not (a security property: one word
// would admit anyone who guessed a common first name).
func TestIntegration_SignInAttempts_TheCompleteGivenNameAuthenticatesAndItsWordsDoNot(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	seedGivenName(t, f, "MARIA DEL CARMEN")

	for _, word := range []string{"Maria", "Del", "Carmen", "Maria Del"} {
		_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", word, f.reqID(t, 0)))
		if res.Outcome == outcomeVerified {
			t.Fatalf("%q — one word of a multi-word given name authenticated; anyone who guessed a common "+
				"first name could sign in to that room", word)
		}
		if got := lastAttempt(t, f).Result; got != string(signinattempt.CredentialMismatch) {
			t.Fatalf("%q recorded %s, want CREDENTIAL_MISMATCH: the room and an eligible stay both exist", word, got)
		}
	}

	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Maria Del Carmen", f.reqID(t, 1)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("the complete given name was refused: %+v", res)
	}
	if a := lastAttempt(t, f); a.MatchedField != string(signinattempt.MatchedFirstName) {
		t.Fatalf("matched_field = %q, want FIRST_NAME", a.MatchedField)
	}
}

// The record says which accepted value (family name, reservation number) admitted the guest.
func TestIntegration_SignInAttempts_FamilyNameAndReservationNumberEachAuthenticate(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	for _, c := range []struct{ value, wantField string }{
		{"Okonkwo", string(signinattempt.MatchedFamilyName)},
		{"RES-4001", string(signinattempt.MatchedReservationNumber)},
	} {
		_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", c.value, f.reqID(t, 0)))
		if res.Outcome != outcomeVerified {
			t.Fatalf("%q did not authenticate: %+v", c.value, res)
		}
		a := lastAttempt(t, f)
		if a.Result != string(signinattempt.Verified) || a.MatchedField != c.wantField {
			t.Fatalf("%q recorded %s/%s, want VERIFIED/%s", c.value, a.Result, a.MatchedField, c.wantField)
		}
	}
}

// A wrong value, a room the mirror does not hold and an ineligible stay are three recorded results but one
// answer to the guest.
func TestIntegration_SignInAttempts_MismatchAbsentRoomAndIneligibleStayAreDistinct(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Nottheguest", f.reqID(t, 1)))
	if res.Outcome == outcomeVerified {
		t.Fatal("a wrong value verified")
	}
	mismatch := lastAttempt(t, f)
	if mismatch.Result != string(signinattempt.CredentialMismatch) {
		t.Fatalf("a wrong value on a real room recorded %s, want CREDENTIAL_MISMATCH", mismatch.Result)
	}
	if mismatch.RoomInMirror == nil || !*mismatch.RoomInMirror {
		t.Fatal("the record does not say the room was found, which is the fact that distinguishes this case")
	}
	if mismatch.Eligible == nil || *mismatch.Eligible == 0 {
		t.Fatal("the record does not count the eligible stays on that room")
	}

	_, res = post(t, f.p3.resolveHandler, f.verifyBody("905", "Okonkwo", f.reqID(t, 2)))
	if res.Outcome == outcomeVerified {
		t.Fatal("a room absent from the mirror verified")
	}
	absent := lastAttempt(t, f)
	if absent.Result != string(signinattempt.RoomNotInMirror) {
		t.Fatalf("an absent room recorded %s, want ROOM_NOT_IN_MIRROR", absent.Result)
	}
	if absent.RoomInMirror == nil || *absent.RoomInMirror {
		t.Fatal("the record claims an absent room was found")
	}

	if _, err := controlledExec(ctx, f.pool, "stay",
		// posting_allowed=false as internal/checkout writes it: the CHECK runs before the AFTER trigger.
		`UPDATE iam_v2.stays SET status='CHECKED_OUT', posting_allowed=false, effective_checkout_at=now()
		  WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site); err != nil {
		t.Fatalf("check the stays out: %v", err)
	}
	_, res = post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 3)))
	if res.Outcome == outcomeVerified {
		t.Fatal("a checked-out stay verified")
	}
	ineligible := lastAttempt(t, f)
	if ineligible.Result != string(signinattempt.StayNotEligible) {
		t.Fatalf("a checked-out stay recorded %s, want STAY_NOT_ELIGIBLE", ineligible.Result)
	}

	// All three look identical to the guest, or the lobby could enumerate occupied and departed rooms.
	for _, r := range []signinattempt.Result{
		signinattempt.CredentialMismatch, signinattempt.RoomNotInMirror, signinattempt.StayNotEligible,
	} {
		if r.GuestClass() != signinattempt.GuestCredential {
			t.Fatalf("%s reaches the guest as %s; the three refusals are distinguishable from outside", r, r.GuestClass())
		}
	}
}

// A room's history never hides its current guest: the probe reads a bounded window of stays, which departed
// stays must not fill.
func TestIntegration_SignInAttempts_RoomHistoryNeverHidesTheInHouseStay(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	// Compact the table first so rows land in write order rather than in space earlier tests freed.
	if _, err := f.pool.Exec(ctx, `VACUUM FULL iam_v2.stays`); err != nil {
		t.Fatalf("compact stays: %v", err)
	}
	// Twenty departed stays then the current one, ids and reservations sorting after the history, so any
	// plan meets the history first.
	if _, err := controlledExec(ctx, f.pool, "stay", `
		INSERT INTO iam_v2.stays(id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
		                         normalized_room_number,status,posting_allowed,effective_checkout_at,lifecycle_version,
		                         last_applied_event_version,occupancy_evidence_at,occupancy_ingested_at,occupancy_revision_id,
		                         occupancy_normalization_version,occupancy_clock_suspect,occupancy_evidence_version)
		SELECT overlay(gen_random_uuid()::text PLACING CASE WHEN g=21 THEN 'ffffffff' ELSE '00000000' END FROM 1 FOR 8)::uuid,
		       $1,$2,$3,'RES-777-'||lpad(g::text,2,'0'),'STAY-777-'||lpad(g::text,2,'0'),'777',
		       CASE WHEN g=21 THEN 'IN_HOUSE' ELSE 'CHECKED_OUT' END, false,
		       CASE WHEN g=21 THEN NULL ELSE now() END, 1, 0,
		       CASE WHEN g=21 THEN o.occupancy_evidence_at END, CASE WHEN g=21 THEN o.occupancy_ingested_at END,
		       CASE WHEN g=21 THEN o.occupancy_revision_id END, CASE WHEN g=21 THEN o.occupancy_normalization_version END,
		       CASE WHEN g=21 THEN o.occupancy_clock_suspect END, CASE WHEN g=21 THEN o.occupancy_evidence_version ELSE 0 END
		  FROM generate_series(1,21) g, iam_v2.stays o WHERE o.id=$4::uuid ORDER BY g`,
		f.tenant, f.site, f.iface, f.stay); err != nil {
		t.Fatalf("seed the room: %v", err)
	}
	if _, err := controlledExec(ctx, f.pool, "stay", `
		INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
		SELECT tenant_id,site_id,pms_interface_id,id,'ADEBAYO',true FROM iam_v2.stays
		 WHERE tenant_id=$1 AND site_id=$2 AND normalized_room_number='777'`, f.tenant, f.site); err != nil {
		t.Fatalf("seed the room's guests: %v", err)
	}
	// An unordered window of 16 would hold none of the current stay.
	inWindow := -1
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status='IN_HOUSE') FROM (SELECT status FROM iam_v2.stays
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3 AND normalized_room_number='777' LIMIT 16) w`,
		f.tenant, f.site, f.iface).Scan(&inWindow); err != nil {
		t.Fatalf("read the unordered window: %v", err)
	}
	if inWindow != 0 {
		t.Fatalf("fixture: could not place the current stay after the history in scan order, so this test would prove nothing")
	}

	_, res := post(t, f.p3.resolveHandler, f.verifyBody("777", "Adebayo", f.reqID(t, 1)))
	got := lastAttempt(t, f)
	if res.Outcome != outcomeVerified {
		t.Fatalf("the in-house guest was refused (%s, %d eligible counted) behind the room's history", got.Result, deref(got.Eligible))
	}
}

func deref(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

// Every deliberate submission leaves exactly one row, including those failing before a resolver or network exists.
func TestIntegration_SignInAttempts_EverySubmissionLeavesExactlyOneRecord(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	before := attemptCount(t, f)
	submissions := []struct {
		name string
		body map[string]any
		want signinattempt.Result
	}{
		{"a wrong value", f.verifyBody("412", "Nottheguest", f.reqID(t, 1)), signinattempt.CredentialMismatch},
		{"an absent room", f.verifyBody("905", "Okonkwo", f.reqID(t, 2)), signinattempt.RoomNotInMirror},
		{"no evidence at all", f.verifyBody("412", "", f.reqID(t, 3)), signinattempt.MalformedSubmission},
		{"a malformed request id", f.verifyBody("412", "Okonkwo", "not-a-uuid"), signinattempt.MalformedSubmission},
		{"a device on no guest network", map[string]any{
			"room": "412", "verification": "Okonkwo", "request_id": f.reqID(t, 4),
			"device": map[string]string{"ip": "192.0.2.10", "mac": f.net.mac}},
			signinattempt.RoutingOrInterfaceFailure},
		{"an unusable hardware address", map[string]any{
			"room": "412", "verification": "Okonkwo", "request_id": f.reqID(t, 5),
			"device": map[string]string{"ip": f.net.guestIP, "mac": "not-a-mac"}},
			signinattempt.MalformedSubmission},
		{"the correct value", f.verifyBody("412", "Okonkwo", f.reqID(t, 6)), signinattempt.Verified},
	}
	for _, s := range submissions {
		post(t, f.p3.resolveHandler, s.body)
		if got := lastAttempt(t, f).Result; got != string(s.want) {
			t.Errorf("%s recorded %s, want %s", s.name, got, s.want)
		}
	}
	if got := attemptCount(t, f) - before; got != len(submissions) {
		t.Fatalf("%d submissions produced %d records, want one each", len(submissions), got)
	}
}

// A successful sign-in is one row: the grant completes the row the resolve wrote.
func TestIntegration_SignInAttempts_AGrantCompletesTheRecordItDoesNotDuplicateIt(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	before := attemptCount(t, f)
	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 1)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}
	grant := postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)
	if grant.Outcome != outcomeVerified || grant.SessionID == "" {
		t.Fatalf("setup: the grant produced no access: %+v", grant)
	}

	if got := attemptCount(t, f) - before; got != 1 {
		t.Fatalf("one sign-in produced %d records, want 1", got)
	}
	a := lastAttempt(t, f)
	if a.SessionID != grant.SessionID || a.EntitlementID != grant.EntitlementID {
		t.Fatalf("the record does not name the access it produced: session=%q entitlement=%q", a.SessionID, a.EntitlementID)
	}
	if a.Latency == nil || *a.Latency < 0 {
		t.Fatal("the record carries no usable latency")
	}
	if a.Network == "" {
		t.Fatal("the record does not name the guest network, which is a column the operator list shows")
	}
}

// The correlation id is the one the portal minted, on success and failure alike.
func TestIntegration_SignInAttempts_CorrelationIdsAreUsableOnSuccessAndFailure(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	for _, c := range []struct{ name, value string }{
		{"failure", "Nottheguest"},
		{"success", "Okonkwo"},
	} {
		id := f.reqID(t, 0)
		post(t, f.p3.resolveHandler, f.verifyBody("412", c.value, id))
		if got := lastAttempt(t, f).RequestID; got != id {
			t.Errorf("%s: recorded correlation id %q, want the submitted %q", c.name, got, id)
		}
	}
}

// The sealed half round-trips and the ciphertext on disk carries none of it in clear.
func TestIntegration_SignInAttempts_CredentialsAreEncryptedAtRestAndRoundTrip(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	withSealingKey(t, f)
	ctx := context.Background()
	seedGivenName(t, f, "MARIA DEL CARMEN")
	// Move the second stay away so only one stay's values could have been accepted.
	soleOccupant(t, f)

	post(t, f.p3.resolveHandler, f.verifyBody("412", "  Nottheguest ", f.reqID(t, 1)))
	a := lastAttempt(t, f)
	if a.SealedLen == 0 {
		t.Fatal("nothing was sealed; the comparison panel would be empty")
	}

	// (1) the bytes on disk contain neither what was typed nor what would have been accepted
	var raw []byte
	if err := f.pool.QueryRow(ctx,
		`SELECT sensitive_ciphertext FROM iam_v2.sign_in_attempts WHERE id=$1::uuid`, a.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"Nottheguest", "NOTTHEGUEST", "OKONKWO", "MARIA DEL CARMEN", "RES-4001"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("the stored ciphertext contains %q in the clear", secret)
		}
	}

	// (2) it opens and carries both typed and accepted values
	s, ok, err := f.p3.attempts.OpenSensitive(ctx, f.tenant, f.site, a.ID)
	if err != nil || !ok {
		t.Fatalf("the sealed half did not open: %v (ok=%v)", err, ok)
	}
	if s.SubmittedVerifier != "  Nottheguest " {
		t.Fatalf("submitted verifier = %q — the RAW value must survive, or a trailing space is invisible", s.SubmittedVerifier)
	}
	if s.NormalizedVerifier != "NOTTHEGUEST" {
		t.Fatalf("normalized verifier = %q", s.NormalizedVerifier)
	}
	if s.AcceptedFamilyName != "OKONKWO" || s.AcceptedFirstName != "MARIA DEL CARMEN" || s.AcceptedReservationNumber != "RES-4001" {
		t.Fatalf("the accepted values are wrong: %+v", s)
	}

	// (3) an id from another site does not open, and errors no differently from one that never existed
	other := newAuthFixture(t)
	withSealingKey(t, other)
	if _, ok, err := f.p3.attempts.OpenSensitive(ctx, other.tenant, other.site, a.ID); err != nil || ok {
		t.Fatalf("another site opened this attempt: ok=%v err=%v", ok, err)
	}
}

// No expected values are invented when the mirror holds no stay for the room.
func TestIntegration_SignInAttempts_AnAbsentRoomRecordsNoAcceptedValues(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	withSealingKey(t, f)

	post(t, f.p3.resolveHandler, f.verifyBody("905", "Okonkwo", f.reqID(t, 1)))
	a := lastAttempt(t, f)
	s, ok, err := f.p3.attempts.OpenSensitive(context.Background(), f.tenant, f.site, a.ID)
	if err != nil || !ok {
		t.Fatalf("the sealed half did not open: %v", err)
	}
	if s.AcceptedFirstName != "" || s.AcceptedFamilyName != "" || s.AcceptedReservationNumber != "" {
		t.Fatalf("expected values were invented for a room the mirror does not hold: %+v", s)
	}
	if s.SubmittedVerifier == "" {
		t.Fatal("what the guest typed was not recorded, which is the one thing that IS known here")
	}
}

// PMS offline: a guest in the local mirror still signs in, and the record carries the mirror's age.
func TestIntegration_SignInAttempts_OfflineSignInIsRecordedWithTheMirrorAge(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	takeInterfaceOffline(t, f, "DIAL_FAILED")

	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 1)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("a mirrored stay could not sign in while the PMS was down: %+v", res)
	}
	a := lastAttempt(t, f)
	if a.Transport != "DISCONNECTED" {
		t.Fatalf("pms_transport_status = %q, want DISCONNECTED — the record must capture the state at the time", a.Transport)
	}
	var age *int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT mirror_age_seconds FROM iam_v2.sign_in_attempts WHERE id=$1::uuid`, a.ID).Scan(&age); err != nil {
		t.Fatal(err)
	}
	if age == nil || *age <= 0 {
		t.Fatal("the mirror age was not recorded, so 'how stale was the roster' cannot be answered later")
	}
}

// A mirror that can authorise nobody is a site-wide condition and answers the guest TECHNICALLY.
func TestIntegration_SignInAttempts_AMirrorThatCannotAuthoriseIsTechnical(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	if _, err := f.pool.Exec(context.Background(), `UPDATE iam_v2.pms_interface_runtime
		SET transport_status='DISCONNECTED', sync_status='RESYNC_REQUIRED', continuity_status='CONTINUOUS',
		    last_complete_sync_at=NULL, updated_at=now() WHERE tenant_id=$1`, f.tenant); err != nil {
		t.Fatal(err)
	}
	rec, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 1)))
	if res.Outcome == outcomeVerified {
		t.Fatal("a never-synchronised interface authorised a guest")
	}
	if res.FailureClass != string(signinattempt.GuestTechnical) {
		t.Fatalf("failure_class = %q, want TECHNICAL: nothing this guest typed could have helped", res.FailureClass)
	}
	if a := lastAttempt(t, f); a.Result != string(signinattempt.MirrorStaleOrMissingChange) {
		t.Fatalf("recorded %s, want MIRROR_STALE_OR_MISSING_CHANGE", a.Result)
	}
	if rec.Code != 200 {
		t.Fatalf("status %d — a distinct status is itself a signal", rec.Code)
	}
}

// An ordinary mismatch is never TECHNICAL.
func TestIntegration_SignInAttempts_AnOrdinaryMismatchIsClassifiedAsCredential(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	for _, c := range []struct{ name, room, value string }{
		{"wrong value on a real room", "412", "Nottheguest"},
		{"a room the mirror does not hold", "905", "Okonkwo"},
	} {
		_, res := post(t, f.p3.resolveHandler, f.verifyBody(c.room, c.value, f.reqID(t, 1)))
		if res.FailureClass != string(signinattempt.GuestCredential) {
			t.Errorf("%s: failure_class = %q, want CREDENTIAL", c.name, res.FailureClass)
		}
	}
}

// The thirty-day retention boundary, asserted on both sides; the sweep must touch no stays, guests,
// entitlements, sessions or audit log.
func TestIntegration_SignInAttempts_PurgeRemovesOnlyExpiredAttempts(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	// One successful sign-in gives the sweep stays, entitlements and sessions to leave alone.
	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 1)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}
	postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)

	type placed struct {
		name string
		age  time.Duration
		keep bool
	}
	cases := []placed{
		{"just inside", signinattempt.Retention - time.Hour, true},
		{"one minute inside", signinattempt.Retention - time.Minute, true},
		{"one minute outside", signinattempt.Retention + time.Minute, false},
	}
	ids := map[string]string{}
	for _, c := range cases {
		post(t, f.p3.resolveHandler, f.verifyBody("412", "Wrong-"+strings.ReplaceAll(c.name, " ", "-"), f.reqID(t, 1)))
		a := lastAttempt(t, f)
		ids[c.name] = a.ID
		if _, err := f.pool.Exec(ctx,
			`UPDATE iam_v2.sign_in_attempts SET occurred_at = now() - $2::interval WHERE id=$1::uuid`,
			a.ID, fmt.Sprintf("%d seconds", int(c.age.Seconds()))); err != nil {
			t.Fatal(err)
		}
	}

	staysBefore := countRows(t, f, `SELECT count(*) FROM iam_v2.stays WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site)
	guestsBefore := countRows(t, f, `SELECT count(*) FROM iam_v2.stay_guests WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site)
	entsBefore := countRows(t, f, `SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site)
	sessBefore := countRows(t, f, `SELECT count(*) FROM iam_v2.sessions WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site)
	resolutionsBefore := countRows(t, f, `SELECT count(*) FROM iam_v2.auth_resolutions WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site)

	if _, err := f.p3.attempts.Purge(ctx, time.Now()); err != nil {
		t.Fatalf("purge: %v", err)
	}

	for _, c := range cases {
		n := countRows(t, f, `SELECT count(*) FROM iam_v2.sign_in_attempts WHERE id=$1::uuid`, ids[c.name])
		if c.keep && n != 1 {
			t.Errorf("%s (%s old) was purged; the boundary is thirty days, not less", c.name, c.age)
		}
		if !c.keep && n != 0 {
			t.Errorf("%s (%s old) survived the purge", c.name, c.age)
		}
	}

	for _, c := range []struct {
		name   string
		before int
		query  string
	}{
		{"stays", staysBefore, `SELECT count(*) FROM iam_v2.stays WHERE tenant_id=$1 AND site_id=$2`},
		{"stay guests", guestsBefore, `SELECT count(*) FROM iam_v2.stay_guests WHERE tenant_id=$1 AND site_id=$2`},
		{"entitlements", entsBefore, `SELECT count(*) FROM iam_v2.entitlements WHERE tenant_id=$1 AND site_id=$2`},
		{"sessions", sessBefore, `SELECT count(*) FROM iam_v2.sessions WHERE tenant_id=$1 AND site_id=$2`},
		{"auth resolutions", resolutionsBefore, `SELECT count(*) FROM iam_v2.auth_resolutions WHERE tenant_id=$1 AND site_id=$2`},
	} {
		if got := countRows(t, f, c.query, f.tenant, f.site); got != c.before {
			t.Errorf("the retention sweep changed %s from %d to %d; it must touch its own table and nothing else",
				c.name, c.before, got)
		}
	}
}

// Nothing outside this tenant and site is recorded or readable, even to a caller holding an attempt id.
func TestIntegration_SignInAttempts_AreConfinedToTheirTenantAndSite(t *testing.T) {
	f := newAuthFixture(t)
	other := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	withSealingKey(t, f)
	withSealingKey(t, other)

	post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 1)))
	mine := lastAttempt(t, f)

	if n := countRows(t, other, `SELECT count(*) FROM iam_v2.sign_in_attempts WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`,
		other.tenant, other.site, mine.ID); n != 0 {
		t.Fatal("another site can see this attempt by id")
	}
	if _, ok, _ := other.p3.attempts.OpenSensitive(context.Background(), other.tenant, other.site, mine.ID); ok {
		t.Fatal("another site opened this attempt's sealed credentials")
	}
}

// With more than one stay on the room no expected values are shown; printing one would be a confident, wrong answer.
func TestIntegration_SignInAttempts_AmbiguousRoomShowsNoExpectedValues(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	withSealingKey(t, f)

	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Shared", f.reqID(t, 1)))
	if res.Outcome == outcomeVerified {
		t.Fatal("an ambiguous match verified")
	}
	a := lastAttempt(t, f)
	if a.Result != string(signinattempt.AmbiguousRoomCandidates) {
		t.Fatalf("recorded %s, want AMBIGUOUS_ROOM_CANDIDATES", a.Result)
	}
	s, ok, err := f.p3.attempts.OpenSensitive(context.Background(), f.tenant, f.site, a.ID)
	if err != nil || !ok {
		t.Fatalf("the sealed half did not open: %v", err)
	}
	if s.AcceptedFamilyName != "" || s.AcceptedFirstName != "" || s.AcceptedReservationNumber != "" {
		t.Fatalf("one of two candidates was presented as THE expected value: %+v", s)
	}
	if s.SubmittedVerifier != "Shared" {
		t.Fatalf("what the guest typed was not recorded: %q", s.SubmittedVerifier)
	}
}

// Raw credentials never reach a log: one "reason" string carrying the submitted value would copy guest data
// into every journal and CI transcript. The whole default logger is captured across a complete sign-in
// (wrong value, correct value, grant) and every byte is searched.
func TestIntegration_SignInAttempts_NoCredentialValueIsEverLogged(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	withSealingKey(t, f)
	soleOccupant(t, f)
	seedGivenName(t, f, "MARIA DEL CARMEN")

	var captured bytes.Buffer
	restore := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&captured, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(restore)

	post(t, f.p3.resolveHandler, f.verifyBody("412", "Nottheguest", f.reqID(t, 1)))
	post(t, f.p3.resolveHandler, f.verifyBody("905", "Maria Del Carmen", f.reqID(t, 2)))
	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Maria Del Carmen", f.reqID(t, 3)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: the correct value did not verify: %+v", res)
	}
	postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)

	logged := strings.ToLower(captured.String())
	if logged == "" {
		t.Fatal("nothing was logged at all, so this test would pass for the wrong reason")
	}
	for _, secret := range []string{
		"nottheguest",
		"maria del carmen",
		"okonkwo",
		"res-4001",
	} {
		if strings.Contains(logged, secret) {
			t.Errorf("a credential value reached the log: %q", secret)
		}
	}
	// The log must stay useful: operators grep the structured result.
	if !strings.Contains(logged, "credential_mismatch") {
		t.Error("the log carries no structured result, so it is neither safe nor useful")
	}
}
