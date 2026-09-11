//go:build integration

package main

// GUEST SIGN-IN ATTEMPTS, end to end, against a real PostgreSQL.
//
// These are the proofs the Product Owner asked for, and they are written against the handlers scd actually
// serves rather than against the pieces underneath. What the pieces cannot show is the thing that matters
// here: that a refusal which used to leave nothing behind now leaves exactly one row, carrying the reason an
// operator needs and the comparison that settles the conversation at the desk.
//
// NO REAL GUEST DATA APPEARS ANYWHERE IN THIS FILE. Every name, room and reservation identifier is invented
// fixture data.

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

// testSealingKeyID and withSealingKey give the fixture the appliance-local key scd would load at startup.
//
// The production loader reads a 0600 file that a disposable test database has no reason to carry, and the
// daemon deliberately keeps running without it — so a fixture that did nothing here would record every
// attempt with its sensitive half NULL and the encryption proofs would silently assert nothing. Installing a
// known key makes the round trip, the ciphertext inspection and the owner binding real.
const testSealingKeyID = "0a5c1e00-0000-4000-8000-0000000000d2"

func withSealingKey(t *testing.T, f *authFixture) {
	t.Helper()
	kr := signinattempt.MapKeyring{testSealingKeyID: bytes.Repeat([]byte{0x5e}, 32)}
	f.srv.signInAttemptKeyring = kr
	f.p3.attempts = signinattempt.NewStore(f.pool, kr, testSealingKeyID)
}

// attemptRow is what the test reads back out of the table.
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

// seedGivenName gives the fixture's stay a MULTI-WORD given name in one field, which is how a PMS sending
// "FAMILY NAME, FULL GIVEN NAME" is mirrored.
func seedGivenName(t *testing.T, f *authFixture, given string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		UPDATE iam_v2.stay_guests SET first_name_norm=$2 WHERE stay_id=$1 AND is_primary`,
		f.stay, given); err != nil {
		t.Fatalf("seed the given name: %v", err)
	}
}

// soleOccupant moves the fixture's SECOND stay off room 412, leaving exactly one eligible stay on it.
func soleOccupant(t *testing.T, f *authFixture) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE iam_v2.stays SET normalized_room_number='777' WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		f.tenant, f.site, f.otherStay); err != nil {
		t.Fatalf("move the second stay: %v", err)
	}
}

// THE MULTI-WORD GIVEN NAME. The Product Owner confirmed the contract: the COMPLETE stored field
// authenticates, and individual words inside it do not. Both halves are asserted, because the second is the
// one that looks like a bug to somebody reading the code later and is in fact the security property —
// accepting one word would admit anyone who guessed a common first name for a room.
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

// The family name and the reservation number are the other two accepted values, and the record says WHICH
// one admitted the guest — an operator who cannot tell them apart cannot explain the next failure.
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

// THE THREE REFUSALS THAT USED TO BE ONE.
//
// A wrong value on a real room, a room the mirror does not hold, and a room whose stay is no longer eligible
// produced the identical empty answer and the identical recorded outcome. They are three different
// conversations at the desk and they are now three different recorded results — while remaining, deliberately,
// one single answer to the guest.
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

	// Check the stays out. The room still exists in the mirror; no stay on it may authenticate.
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.stays SET status='CHECKED_OUT', effective_checkout_at=now()
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

	// ...AND ALL THREE LOOK IDENTICAL TO THE GUEST. The distinction is the operator's; giving it to the guest
	// would let anyone in the lobby enumerate which rooms are occupied and which have departed.
	for _, r := range []signinattempt.Result{
		signinattempt.CredentialMismatch, signinattempt.RoomNotInMirror, signinattempt.StayNotEligible,
	} {
		if r.GuestClass() != signinattempt.GuestCredential {
			t.Fatalf("%s reaches the guest as %s; the three refusals are distinguishable from outside", r, r.GuestClass())
		}
	}
}

// EVERY DELIBERATE SUBMISSION LEAVES EXACTLY ONE ROW, including the ones that fail before a resolver, a stay
// or even a network exists. Those are precisely the attempts that used to vanish.
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

// A SUCCESSFUL SIGN-IN IS ONE ROW, NOT TWO. Identity is proved in one call and access granted in the next;
// they are two halves of ONE deliberate submission, so the grant completes the row the resolve wrote rather
// than starting its own. Double-counting here would make "how many guests failed today" unanswerable.
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

// THE CORRELATION ID ties a guest's report to the row, on success and on failure alike, and it is the id the
// portal minted for that submission rather than something invented server-side.
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

// THE SEALED HALF ROUND-TRIPS THROUGH THE DATABASE, and the ciphertext on disk carries none of it in clear.
func TestIntegration_SignInAttempts_CredentialsAreEncryptedAtRestAndRoundTrip(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	withSealingKey(t, f)
	ctx := context.Background()
	seedGivenName(t, f, "MARIA DEL CARMEN")
	// The fixture deliberately puts TWO stays on room 412 so that ambiguity can be exercised elsewhere. This
	// test is about the ordinary case, where the room has one stay and there is therefore no question about
	// whose values would have been accepted, so the second one is moved out of the way.
	soleOccupant(t, f)

	post(t, f.p3.resolveHandler, f.verifyBody("412", "  Nottheguest ", f.reqID(t, 1)))
	a := lastAttempt(t, f)
	if a.SealedLen == 0 {
		t.Fatal("nothing was sealed; the comparison panel would be empty")
	}

	// (1) the bytes on disk contain neither what was typed nor what would have been accepted.
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

	// (2) it opens, and it carries both what the guest typed and what the property would have accepted.
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

	// (3) an id from another site does not open, and does not error differently from an id that never existed.
	other := newAuthFixture(t)
	withSealingKey(t, other)
	if _, ok, err := f.p3.attempts.OpenSensitive(ctx, other.tenant, other.site, a.ID); err != nil || ok {
		t.Fatalf("another site opened this attempt: ok=%v err=%v", ok, err)
	}
}

// NO EXPECTED VALUES ARE INVENTED when the mirror holds no stay for the room. The panel must be able to say
// "there is nothing here" rather than show blanks that read as withheld data.
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

// PMS OFFLINE: a guest whose stay is in the local mirror still signs in, and the record carries the mirror's
// age so an operator can see how far behind the roster was.
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

// A MIRROR THAT CAN AUTHORISE NOBODY is recorded as the site-wide condition it is, and answers the guest
// TECHNICALLY — not "check your details", which for this guest would be advice that cannot help.
func TestIntegration_SignInAttempts_AMirrorThatCannotAuthoriseIsTechnical(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()

	// Never synchronised: there is no published roster to answer from, for anybody.
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

// AN ORDINARY MISMATCH IS NEVER TECHNICAL. The mirror image of the test above, and the Product Owner's rule
// stated directly at the wire.
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

// THE THIRTY-DAY BOUNDARY, asserted on both sides of it, and asserted to touch nothing else.
//
// A retention sweep that reached into stays, guests, entitlements, sessions or the general audit log would be
// destroying evidence rather than expiring it, so the counts around it are part of the test.
func TestIntegration_SignInAttempts_PurgeRemovesOnlyExpiredAttempts(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	ctx := context.Background()

	// One successful sign-in, so there are stays, entitlements and sessions for the sweep to leave alone.
	_, res := post(t, f.p3.resolveHandler, f.verifyBody("412", "Okonkwo", f.reqID(t, 1)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("setup: %+v", res)
	}
	postGrant(t, f, res.AuthContextID, res.Offers[0].PackageRevisionID)

	// Three attempts placed either side of the boundary, to the minute.
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

// NOTHING OUTSIDE THIS TENANT AND SITE IS RECORDED OR READABLE. The confinement is asserted at the record,
// not only at the response: an attempt in one site must be invisible from another even to a caller holding
// its id.
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

// WITH MORE THAN ONE STAY ON THE ROOM, NO EXPECTED VALUES ARE SHOWN.
//
// The panel exists to say "you typed X, we would have accepted Y". When two guests share a room there is no
// single Y, and printing one of them would put a name on the operator's screen that the guest never had to
// match — a confident, wrong answer, which is worse than the honest absence.
func TestIntegration_SignInAttempts_AmbiguousRoomShowsNoExpectedValues(t *testing.T) {
	f := newAuthFixture(t)
	defer f.startEnforcementOwner(t)()
	withSealingKey(t, f)

	// The fixture seeds two IN_HOUSE stays on room 412, and "Shared" is a surname both carry.
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
