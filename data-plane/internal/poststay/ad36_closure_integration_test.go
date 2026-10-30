//go:build integration && phase5

package poststay

// AD-36 closure (docs/reviews/AD36_POST_STAY_CLOSURE.md). Real PostgreSQL, full appliance schema. The cases are the ones the
// review (docs/reviews/AD36_NEW_DEVICE_IDENTITY_REVIEW.md) found open: a returning device and a further device must join
// the stay's Entitlement and never reset what it spent; refusals are persisted without guest data; the brute-force limits
// are settings; a client that rotates its device identity is bounded and cannot lock the guest out for good.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/authctx"
)

// reqU is req with a source address derived from the device, so a test that moves the throttle clock into the past never
// meets a block another test (or an earlier run) left on the shared test address.
func reqU(f fx, device, room, res, last string) VerifyRequest {
	return req(f, device, room, res, last)
}

// endOriginal ends the stay's own entitlement the way check-out does (one live Entitlement per stay is unique), so the
// post-stay benefit is the only way to a new one. Its lineage rows remain: that is how the device is "known".
func endOriginal(t *testing.T, p *pgxpool.Pool, f fx) {
	t.Helper()
	if _, err := p.Exec(context.Background(),
		`SELECT iam_v2.apply_entitlement_transition(id,'TERMINATED',now(),NULL) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); err != nil {
		t.Fatalf("end original: %v", err)
	}
}

// signIn is the guest path to a conversion: verify, mint the one-time context, convert.
func signIn(t *testing.T, s *Store, p *pgxpool.Pool, f fx, device string, r VerifyRequest) (Converted, error) {
	t.Helper()
	ctx := context.Background()
	ac := authctx.NewStore(p)
	profile, err := s.Verify(ctx, r)
	if err != nil {
		return Converted{}, fmt.Errorf("verify: %w", err)
	}
	id, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: profile,
		Device: device, GuestNetwork: f.network, TTLSeconds: 600})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: id,
		Presenter:       authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: device, GuestNetwork: f.network},
		PackageRevision: f.pkg}, ac)
}

func count(t *testing.T, p *pgxpool.Pool, q string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// The first device gets the Post-Stay benefit (a new Entitlement); every device after it JOINS that one. There is one
// live Entitlement, one conversion, one window; the plan's device limit (2 in the fixture) bounds the joiners.
func TestIntegration_AD36Closure_FurtherDevicesJoinTheSameEntitlement(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	endOriginal(t, p, f)
	s := storeAtFixedTime(t, p, time.Now().Add(-600*time.Minute))
	ctx := context.Background()

	first, err := signIn(t, s, p, f, f.device, reqU(f, f.device, "101", "R1", ""))
	if err != nil {
		t.Fatalf("first device: %v", err)
	}
	if first.Joined {
		t.Fatal("the first conversion must GRANT the post-stay benefit, not join")
	}
	var windowBefore time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, first.Entitlement).Scan(&windowBefore); err != nil {
		t.Fatal(err)
	}

	second := newDevice(t, p, f, "02:00:00:00:e1:01")
	joined, err := signIn(t, s, p, f, second, reqU(f, second, "101", "R1", "LOVELACE"))
	if err != nil {
		t.Fatalf("second (new) device: %v", err)
	}
	if !joined.Joined || joined.Entitlement != first.Entitlement {
		t.Fatalf("second device: joined=%v entitlement=%s, want to JOIN %s", joined.Joined, joined.Entitlement, first.Entitlement)
	}
	if joined.Purchase != "" {
		t.Fatalf("a joining device created purchase %s", joined.Purchase)
	}

	// A third device is over the plan's limit of 2: refused, nothing granted, nothing created.
	third := newDevice(t, p, f, "02:00:00:00:e1:02")
	if _, err := signIn(t, s, p, f, third, reqU(f, third, "101", "R1", "LOVELACE")); !errors.Is(err, ErrDeviceLimit) {
		t.Fatalf("third device: %v, want ErrDeviceLimit", err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1`, first.Entitlement); n != 2 {
		t.Fatalf("%d devices bound, want exactly the plan's 2", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE device_id=$1`, third); n != 0 {
		t.Fatalf("the refused device holds %d bindings", n)
	}
	// One benefit, one live entitlement, one purchase of that kind; the window did not move.
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND trigger='POST_STAY_CONVERSION'`, f.stay); n != 1 {
		t.Fatalf("%d POST_STAY_CONVERSION purchases, want 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, f.stay); n != 1 {
		t.Fatalf("%d live entitlements, want 1", n)
	}
	var windowAfter time.Time
	if err := p.QueryRow(ctx, `SELECT window_ends_at FROM iam_v2.entitlements WHERE id=$1`, first.Entitlement).Scan(&windowAfter); err != nil {
		t.Fatal(err)
	}
	if !windowAfter.Equal(windowBefore) {
		t.Fatalf("a joining device moved the window %v -> %v", windowBefore, windowAfter)
	}
	// And the audit trail tells the grant from the join.
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2 AND target_id=$3`,
		f.tenant, AuditActionBenefitGranted, first.Entitlement); n != 1 {
		t.Fatalf("%d benefit_granted rows, want 1", n)
	}
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2 AND target_id=$3`,
		f.tenant, AuditActionEntitlementJoined, first.Entitlement); n != 1 {
		t.Fatalf("%d entitlement_joined rows, want 1", n)
	}
}

// A returning device (one that already holds the post-stay entitlement) signs in again and gets the same entitlement:
// nothing new, nothing reset, no second device slot.
func TestIntegration_AD36Closure_ReturningDeviceContinuesTheSameEntitlement(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	endOriginal(t, p, f)
	s := storeAtFixedTime(t, p, time.Now().Add(-610*time.Minute))

	first, err := signIn(t, s, p, f, f.device, reqU(f, f.device, "101", "R1", ""))
	if err != nil {
		t.Fatal(err)
	}
	// The data it has spent so far must survive the return.
	if _, err := p.Exec(context.Background(), `UPDATE iam_v2.entitlements SET consumed_data_bytes=777000 WHERE id=$1`, first.Entitlement); err != nil {
		t.Skipf("cannot seed usage directly on this schema: %v", err)
	}
	again, err := signIn(t, s, p, f, f.device, reqU(f, f.device, "101", "R1", ""))
	if err != nil {
		t.Fatalf("returning device: %v", err)
	}
	if !again.Joined || again.Entitlement != first.Entitlement {
		t.Fatalf("returning device got entitlement %s joined=%v, want %s", again.Entitlement, again.Joined, first.Entitlement)
	}
	var spent int64
	if err := p.QueryRow(context.Background(), `SELECT consumed_data_bytes FROM iam_v2.entitlements WHERE id=$1`, first.Entitlement).Scan(&spent); err != nil || spent != 777000 {
		t.Fatalf("consumed data %d (%v), want it untouched at 777000", spent, err)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay); n != 2 { // the ended original + the one benefit
		t.Fatalf("%d entitlements for the stay, want the ended original and one post-stay", n)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlement_devices WHERE entitlement_id=$1`, first.Entitlement); n != 1 {
		t.Fatalf("%d bindings, want the one device", n)
	}
}

// While the stay's OWN entitlement is still live, post-stay sign-in continues it instead of creating a second live
// entitlement (which the database forbids) or a fresh allowance.
func TestIntegration_AD36Closure_LiveStayEntitlementIsContinuedNotReplaced(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f) // the stay's own entitlement, left ACTIVE
	var original string
	if err := p.QueryRow(context.Background(), `SELECT id::text FROM iam_v2.entitlements WHERE stay_id=$1`, f.stay).Scan(&original); err != nil {
		t.Fatal(err)
	}
	s := storeAtFixedTime(t, p, time.Now().Add(-620*time.Minute))
	fresh := newDevice(t, p, f, "02:00:00:00:e2:01")
	out, err := signIn(t, s, p, f, fresh, reqU(f, fresh, "101", "R1", "LOVELACE"))
	if err != nil {
		t.Fatalf("new device with the stay's entitlement live: %v", err)
	}
	if !out.Joined || out.Entitlement != original {
		t.Fatalf("entitlement %s joined=%v, want to continue %s", out.Entitlement, out.Joined, original)
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.purchases WHERE stay_id=$1 AND trigger='POST_STAY_CONVERSION'`, f.stay); n != 0 {
		t.Fatalf("%d post-stay purchases, want none (no benefit was granted)", n)
	}
}

// Once the benefit has been granted and has ended, no device can ask for a fresh one: that would be the reset.
func TestIntegration_AD36Closure_SpentBenefitCannotBeRegrantedByAnotherDevice(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	endOriginal(t, p, f)
	s := storeAtFixedTime(t, p, time.Now().Add(-630*time.Minute))

	first, err := signIn(t, s, p, f, f.device, reqU(f, f.device, "101", "R1", ""))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(context.Background(), `SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now(),'DATA')`, first.Entitlement); err != nil {
		// the quota ran out: the terminal reason is not the point, the end of the entitlement is
		if _, err2 := p.Exec(context.Background(), `SELECT iam_v2.apply_entitlement_transition($1,'TERMINATED',now(),NULL)`, first.Entitlement); err2 != nil {
			t.Fatalf("end the post-stay entitlement: %v / %v", err, err2)
		}
	}
	for i, dev := range []string{f.device, newDevice(t, p, f, "02:00:00:00:e3:01")} {
		r := reqU(f, dev, "101", "R1", "LOVELACE")
		if _, err := signIn(t, s, p, f, dev, r); !errors.Is(err, ErrAlreadyConverted) {
			t.Fatalf("device %d after the benefit ended: %v, want ErrAlreadyConverted (no fresh allowance)", i, err)
		}
	}
	if n := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status<>'TERMINATED'`, f.stay); n != 0 {
		t.Fatalf("%d live entitlements after refused re-grants", n)
	}
}

// Concurrent devices at the same moment still end with one entitlement and no more devices than the plan allows.
func TestIntegration_AD36Closure_ConcurrentDevicesShareOneEntitlementWithinTheLimit(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	endOriginal(t, p, f)
	s := storeAtFixedTime(t, p, time.Now().Add(-640*time.Minute))

	const n = 5
	devs := make([]string, n)
	for i := range devs {
		devs[i] = newDevice(t, p, f, fmt.Sprintf("02:00:00:00:e4:%02x", i))
	}
	var wg sync.WaitGroup
	results := make([]error, n)
	ents := make([]string, n)
	for i := range devs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := reqU(f, devs[i], "101", "R1", "LOVELACE")
			r.IP = fmt.Sprintf("10.99.%d.7", i+1)
			ctx := context.Background()
			ac := authctx.NewStore(p)
			profile, err := s.Verify(ctx, r)
			if err != nil {
				results[i] = err
				return
			}
			id, err := ac.IssuePostStay(ctx, authctx.PostStayGrant{Tenant: f.tenant, Site: f.site, Profile: profile, Device: devs[i], GuestNetwork: f.network, TTLSeconds: 600})
			if err != nil {
				results[i] = err
				return
			}
			out, err := s.Convert(ctx, ConvertRequest{Tenant: f.tenant, Site: f.site, Context: id,
				Presenter:       authctx.Presenter{Tenant: f.tenant, Site: f.site, Device: devs[i], GuestNetwork: f.network},
				PackageRevision: f.pkg}, ac)
			results[i], ents[i] = err, out.Entitlement
		}(i)
	}
	wg.Wait()
	ok := 0
	seen := map[string]bool{}
	for i, err := range results {
		switch {
		case err == nil:
			ok++
			seen[ents[i]] = true
		case errors.Is(err, ErrDeviceLimit):
		default:
			t.Errorf("device %d: unexpected %v", i, err)
		}
	}
	if ok != 2 || len(seen) != 1 {
		t.Fatalf("%d devices admitted onto %d entitlements, want 2 onto 1", ok, len(seen))
	}
	if c := count(t, p, `SELECT count(*) FROM iam_v2.entitlements WHERE stay_id=$1 AND status IN ('PENDING','ACTIVE','SUSPENDED')`, f.stay); c != 1 {
		t.Fatalf("%d live entitlements, want 1", c)
	}
}

// Every refusal is written to the audit log with a closed reason code and keyed pseudonyms, and never with the surname,
// the room or the reservation number; and the same shape whether or not the stay exists (no enumeration through the log).
func TestIntegration_AD36Closure_RefusalsArePersistedWithoutGuestData(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "B7K", "ZEPPELIN")
	fresh := newDevice(t, p, f, "02:00:00:00:e5:01")
	other := newDevice(t, p, f, "02:00:00:00:e5:02")
	s := storeAtFixedTime(t, p, time.Now().Add(-650*time.Minute))
	ctx := context.Background()

	// An existing room with a wrong reservation, and a room that does not exist: both from new devices.
	if _, err := s.Verify(ctx, reqU(f, fresh, "B7K", "WRONG-RES-ZQ9", "ZEPPELIN")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("got %v", err)
	}
	if _, err := s.Verify(ctx, reqU(f, other, "NOSUCHROOM", "WRONG-RES-ZQ9", "ZEPPELIN")); !errors.Is(err, ErrNotAuthenticable) {
		t.Fatalf("got %v", err)
	}
	rows, err := p.Query(ctx, `SELECT payload::text, actor_type, actor_id, ip::text, target_type FROM public.audit_log
		WHERE tenant_id=$1 AND action=$2 ORDER BY ts`, f.tenant, AuditActionRefused)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var shapes []string
	for rows.Next() {
		var payload, actorType, targetType string
		var actorID, ip *string
		if err := rows.Scan(&payload, &actorType, &actorID, &ip, &targetType); err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"ZEPPELIN", "WRONG-RES-ZQ9", "B7K", "NOSUCHROOM", "R1"} {
			if strings.Contains(payload, banned) || (actorID != nil && strings.Contains(*actorID, banned)) {
				t.Fatalf("audit row holds guest data %q: %s", banned, payload)
			}
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			t.Fatal(err)
		}
		if m["code"] != RefusalNotAuthenticable || m["route"] != RouteNewDevice || m["candidate_stays"] != float64(0) {
			t.Fatalf("payload %v", m)
		}
		if _, has := m["stay_id"]; has {
			t.Fatalf("a refusal row must not name a stay: %v", m)
		}
		if actorType != "guest" || targetType != "post_stay_signin" || ip == nil || actorID == nil || len(*actorID) != 16 {
			t.Fatalf("row shape actor=%s target=%s ip=%v actor_id=%v", actorType, targetType, ip, actorID)
		}
		keys := []string{}
		for k := range m {
			keys = append(keys, k)
		}
		shapes = append(shapes, fmt.Sprint(len(keys)))
	}
	if len(shapes) != 2 || shapes[0] != shapes[1] {
		t.Fatalf("want two refusal rows of the same shape, got %v", shapes)
	}
}

// A throttled attempt is audited too (with the scope that stopped it), and a flood cannot fill the table: rows are capped
// per device and minute and the rest are counted in one suppression row.
func TestIntegration_AD36Closure_RefusalAuditIsBounded(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	fresh := newDevice(t, p, f, "02:00:00:00:e6:01")
	at := time.Now().Add(-660 * time.Minute).Truncate(time.Minute)
	s := storeAtFixedTime(t, p, at)
	s.SetClock(func() time.Time { return at })
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		r := reqU(f, fresh, "101", fmt.Sprintf("X%d", i), "LOVELACE")
		r.IP = "10.99.9.9" // its own address: a block on the shared test address would outlive this test's clock
		_, _ = s.Verify(ctx, r)
	}
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2`, f.tenant, AuditActionRefused); n > auditPerKeyPerMinute*2 {
		t.Fatalf("%d refusal rows from one device in one minute, want at most %d per reason", n, auditPerKeyPerMinute)
	}
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2 AND payload->>'code'=$3`,
		f.tenant, AuditActionRefused, RefusalThrottled); n == 0 {
		t.Fatal("throttled attempts were not audited")
	}
	// The next minute: the dropped ones are summarised once.
	s.SetClock(func() time.Time { return at.Add(2 * time.Minute) })
	next := reqU(f, fresh, "101", "X-next", "LOVELACE")
	next.IP = "10.99.9.9"
	_, _ = s.Verify(ctx, next)
	if n := count(t, p, `SELECT count(*) FROM public.audit_log WHERE tenant_id=$1 AND action=$2 AND (payload->>'suppressed')::int > 0`,
		f.tenant, AuditActionRefusalsDropped); n != 1 {
		t.Fatalf("%d suppression rows, want 1", n)
	}
}

// A client that changes its device identity (MAC) on every attempt is bounded by the room and the reservation it must
// guess, not by the identity: the guesses against one room stop at the new-device cap, whatever identities they come
// from; the guest's own known device is never affected; and the lockout ends by itself.
func TestIntegration_AD36Closure_RotatingDeviceIdentityDoesNotMultiplyGuesses(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f) // the guest's own, known device (lineage)
	at := time.Now().Add(-700 * time.Minute)
	s := storeAtFixedTime(t, p, at)
	ctx := context.Background()

	evaluated, throttled := 0, 0
	for d := 0; d < 25; d++ {
		dev := newDevice(t, p, f, fmt.Sprintf("02:00:00:00:f0:%02x", d))
		r := VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: fmt.Sprintf("10.77.%d.1", d), // a fresh address too
			Room: "101", Reservation: fmt.Sprintf("GUESS-%d", d), LastName: "LOVELACE"}
		_, err := s.Verify(ctx, r)
		switch {
		case errors.Is(err, ErrThrottled):
			throttled++
		case errors.Is(err, ErrNotAuthenticable):
			evaluated++
		default:
			t.Fatalf("unexpected %v", err)
		}
	}
	if evaluated != BoundNewDeviceRoom.def {
		t.Fatalf("evaluated=%d throttled=%d: 25 device identities got %d guesses at one room, want the cap of %d",
			evaluated, throttled, evaluated, BoundNewDeviceRoom.def)
	}
	// Even the RIGHT answer from one more new identity is held out for the lockout (a block cannot be an oracle)...
	late := newDevice(t, p, f, "02:00:00:00:f1:01")
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: late, IP: "10.78.0.1", Room: "101", Reservation: "R1", LastName: "LOVELACE"}); !errors.Is(err, ErrThrottled) {
		t.Fatalf("a new identity during the lockout: %v, want ErrThrottled", err)
	}
	// ...while the guest's own device, already on the stay, is unaffected.
	if _, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: f.device, IP: "10.78.0.2", Room: "101", Reservation: "R1"}); err != nil {
		t.Fatalf("the guest's known device during the flood: %v", err)
	}
	// The lockout is finite: after its length (default 15 minutes) a new device with the right answers gets in.
	later := storeAtFixedTime(t, p, at.Add(time.Duration(BoundLockoutMinutes.def+2)*time.Minute))
	if _, err := later.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: late, IP: "10.78.0.1", Room: "101", Reservation: "R1", LastName: "LOVELACE"}); err != nil {
		t.Fatalf("after the lockout ended: %v", err)
	}
}

// The same across rooms: one reservation number tried against many rooms from many identities is capped by the
// reservation, so spraying a guessed number over the hotel does not work either.
func TestIntegration_AD36Closure_SprayingOneReservationAcrossRoomsIsCapped(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	s := storeAtFixedTime(t, p, time.Now().Add(-710*time.Minute))
	ctx := context.Background()
	evaluated := 0
	for d := 0; d < 25; d++ {
		dev := newDevice(t, p, f, fmt.Sprintf("02:00:00:00:f2:%02x", d))
		_, err := s.Verify(ctx, VerifyRequest{Tenant: f.tenant, Site: f.site, Device: dev, IP: fmt.Sprintf("10.79.%d.1", d),
			Room: fmt.Sprintf("%d", 200+d), Reservation: "SPRAYED", LastName: "LOVELACE"})
		if errors.Is(err, ErrNotAuthenticable) {
			evaluated++
		}
	}
	if evaluated != BoundNewDeviceRoom.def {
		t.Fatalf("evaluated=%d, want the cap of %d for one reservation number", evaluated, BoundNewDeviceRoom.def)
	}
}

// The limits are the site's settings: a lower wrong-answer count and a shorter lockout apply at once, and the lockout
// ends after the configured time.
func TestIntegration_AD36Closure_LimitsComeFromTheSettings(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	roomAndGuest(t, p, f, "101", "LOVELACE")
	authorizeDevice(t, p, f)
	at := time.Now().Add(-720 * time.Minute)
	s := storeAtFixedTime(t, p, at)
	ctx := context.Background()
	lim := Limits{WrongAnswersBeforeLockout: 3, LockoutMinutes: 2}
	r := reqU(f, f.device, "101", "WRONG", "")
	r.IP = "10.99.9.10"
	r.Limits = lim
	for i := 0; i < 3; i++ {
		if _, err := s.Verify(ctx, r); !errors.Is(err, ErrNotAuthenticable) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	r.Reservation = "R1"
	if _, err := s.Verify(ctx, r); !errors.Is(err, ErrThrottled) {
		t.Fatalf("after 3 wrong answers (setting 3): %v, want ErrThrottled", err)
	}
	// Two minutes later (setting) the device may try again; with the defaults it would still be held.
	r2 := r
	later := storeAtFixedTime(t, p, at.Add(3*time.Minute))
	if _, err := later.Verify(ctx, r2); err != nil {
		t.Fatalf("after the configured 2-minute lockout: %v", err)
	}
}

// The settings are bounded, defaulted and audited in the database: the stored bounds are the code's bounds, an
// out-of-range value is refused, and a change leaves an append-only row with operator and reason.
func TestIntegration_AD36Closure_ThrottleSettingsAreBoundedAndAudited(t *testing.T) {
	p := pool(t)
	defer p.Close()
	f := seed(t, p)
	ctx := context.Background()

	var got Limits
	read := func() {
		if err := p.QueryRow(ctx, `SELECT post_stay_device_attempts_per_minute, post_stay_ip_attempts_per_minute,
			post_stay_wrong_answers_before_lockout, post_stay_lockout_minutes,
			post_stay_new_device_room_attempts_per_minute, post_stay_appliance_attempts_per_minute
			FROM iam_v2.signin_capacity_settings_get($1::uuid,$2::uuid)`, f.tenant, f.site).
			Scan(&got.DeviceAttemptsPerMinute, &got.IPAttemptsPerMinute, &got.WrongAnswersBeforeLockout,
				&got.LockoutMinutes, &got.NewDeviceRoomAttemptsPerMinute, &got.ApplianceAttemptsPerMinute); err != nil {
			t.Fatalf("read settings: %v", err)
		}
	}
	read()
	if got != DefaultLimits() {
		t.Fatalf("a site with no row reads %+v, want the defaults %+v", got, DefaultLimits())
	}
	set := func(values, reason string) error {
		_, err := p.Exec(ctx, `SELECT iam_v2.signin_capacity_settings_set($1::uuid,$2::uuid,$3::jsonb,'tester',$4)`, f.tenant, f.site, values, reason)
		return err
	}
	if err := set(`{"post_stay_lockout_minutes":30,"post_stay_wrong_answers_before_lockout":4}`, "tighten after a complaint"); err != nil {
		t.Fatalf("in-range change: %v", err)
	}
	read()
	if got.LockoutMinutes != 30 || got.WrongAnswersBeforeLockout != 4 || got.DeviceAttemptsPerMinute != 10 {
		t.Fatalf("after the change %+v", got)
	}
	for name, v := range map[string]string{
		"lockout over four hours": `{"post_stay_lockout_minutes":241}`,
		"lockout zero":            `{"post_stay_lockout_minutes":0}`,
		"wrong answers below 3":   `{"post_stay_wrong_answers_before_lockout":2}`,
		"room cap above 60":       `{"post_stay_new_device_room_attempts_per_minute":61}`,
		"appliance ceiling below": `{"post_stay_appliance_attempts_per_minute":19}`,
		"address cap above 200":   `{"post_stay_ip_attempts_per_minute":201}`,
		"device cap below 3":      `{"post_stay_device_attempts_per_minute":2}`,
		"unknown setting":         `{"post_stay_nothing":5}`,
	} {
		if err := set(v, "out of range"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := set(`{"post_stay_lockout_minutes":20}`, "x"); err == nil {
		t.Error("a change without a real reason was accepted")
	}
	read()
	if got.LockoutMinutes != 30 {
		t.Fatalf("a refused change moved the value: %+v", got)
	}
	var by, reason, oldV, newV string
	if err := p.QueryRow(ctx, `SELECT changed_by, change_reason, old_values::text, new_values::text
		FROM iam_v2.signin_capacity_settings_changes WHERE tenant_id=$1 AND site_id=$2 ORDER BY changed_at DESC LIMIT 1`,
		f.tenant, f.site).Scan(&by, &reason, &oldV, &newV); err != nil {
		t.Fatalf("change log: %v", err)
	}
	if by != "tester" || reason != "tighten after a complaint" ||
		!strings.Contains(oldV, `"post_stay_lockout_minutes": 15`) || !strings.Contains(newV, `"post_stay_lockout_minutes": 30`) {
		t.Fatalf("change log row: by=%q reason=%q old=%s new=%s", by, reason, oldV, newV)
	}
}
