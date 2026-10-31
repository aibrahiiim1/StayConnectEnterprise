package poststay

// The guest-facing After Check-out sign-in never accepts a Stay, profile or PMS interface from the client. It derives the
// candidate stays from the server-resolved device: the stays whose live After Check-out entitlement this device holds a
// place on, i.e. that the checkout carried the device over to because it was authorised on the stay before check-out
// (online or not at the time). The typed room and reservation are compared with those stays' own records. A device
// with no such stay finds nothing, whatever it types, and a surname does not change that: room and reservation numbers are
// printed on paper, and a device that was never authorised on the stay is not entitled to it (Product Owner, 2026-10-30).

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strconv"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

// ErrNoLineage must never be distinguishable from a wrong answer at the API boundary.
var ErrNoLineage = errors.New("poststay: no authenticated lineage for this device")

// maxTypedLen bounds every typed value; a longer one cannot be a room, a reservation or a name.
const maxTypedLen = 64

// VerifyRequest is a guest's attempt. Nothing in it names a stay, a profile or an interface. Room and Reservation are the
// only things a guest types; no surname, first name or code is asked for or accepted.
type VerifyRequest struct {
	Tenant, Site string
	// Device is the device id scd resolved; IP is the source address. Both are throttle dimensions (hashed by the store).
	Device, IP        string
	Room, Reservation string
	// Limits are the site's brute-force settings (migration 0190); the zero value means the defaults.
	Limits Limits
}

type candidate struct {
	stay, room, reservation string
	version                 int
}

// candidateStays lists the stays whose LIVE After Check-out entitlement this device holds an open place on. That place exists
// only if the checkout conversion carried the device over (it was authorised on the same Stay, with the same interface and
// episode, at the verified checkout boundary -- online or offline) and no one removed it since; the database refuses any
// other way onto that entitlement (iam_v2.after_checkout_device_guard). It is scoped by tenant, site and device, and the
// stay row it leads to carries the interface and the episode, so a room number reused by the next occupant, or the same
// number on another PMS interface, is a different stay with a different lineage. A stay checked out before the unification
// whose earlier post-stay entitlement is still running is honoured the same way until it ends. Bounded to cap the work of
// one attempt. The room and reservation come from the stay's own record.
func (s *Store) candidateStays(ctx context.Context, tenant, site, device string) ([]candidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT st.id::text, st.lifecycle_version, COALESCE(st.normalized_room_number,''), COALESCE(st.external_reservation_id,'')
		  FROM (SELECT DISTINCT ON (e.stay_id) e.stay_id, ed.last_authorized
		          FROM iam_v2.entitlement_devices ed
		          JOIN iam_v2.entitlement_device_authorizations a
		            ON a.entitlement_id=ed.entitlement_id AND a.device_id=ed.device_id AND a.deauthorized_at IS NULL
		          JOIN iam_v2.entitlements e
		            ON e.tenant_id=ed.tenant_id AND e.site_id=ed.site_id AND e.id=ed.entitlement_id
		          LEFT JOIN iam_v2.purchases pu
		            ON pu.tenant_id=e.tenant_id AND pu.site_id=e.site_id AND pu.id=e.purchase_id
		         WHERE ed.tenant_id=$1 AND ed.site_id=$2 AND ed.device_id=$3 AND ed.status='AUTHORIZED'
		           AND e.stay_id IS NOT NULL
		           AND e.status='ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now())
		           AND (e.end_mode='GRACE_AFTER_CHECKOUT' OR pu.trigger='POST_STAY_CONVERSION')
		         ORDER BY e.stay_id, ed.last_authorized DESC) lineage
		  JOIN iam_v2.stays st ON st.tenant_id=$1 AND st.site_id=$2 AND st.id=lineage.stay_id
		 WHERE st.status IN ('CHECKED_OUT','POST_STAY_ACTIVE')
		 ORDER BY lineage.last_authorized DESC
		 LIMIT 5`, tenant, site, device)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.stay, &c.version, &c.room, &c.reservation); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// sameText compares two values in constant time regardless of their lengths (both are reduced to fixed-length digests
// first) and never matches an empty value, so a stay with no room on record cannot be "matched" by typing nothing.
func sameText(stored, typed string) bool {
	if stored == "" || typed == "" {
		// Still do the comparison's work so the empty case costs the same.
		a, b := sha256.Sum256([]byte(stored)), sha256.Sum256([]byte(typed))
		_ = subtle.ConstantTimeCompare(a[:], b[:])
		return false
	}
	a, b := sha256.Sum256([]byte(stored)), sha256.Sum256([]byte(typed))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// chargeThrottle charges one attempt against every dimension before any comparison is made, so a locked-out device
// learns nothing and a refusal costs the same as a wrong answer. An unreachable store denies the attempt. The identity
// rule is keyed by the stay identity (episode) AND the device; with no candidate it is keyed by the device alone, so a
// device with no stay is limited as tightly as one with several.
//
// A device identity (the MAC the kernel reports) costs nothing to change, so the limits keyed by it only bound an honest
// guest's mistakes. What bounds a guesser who rotates it is what they must guess: devices with no stay of their own are
// also charged against the room and against the reservation number they typed, shared by every identity. Devices that
// have a stay are not charged there -- they are compared with their own stays only and cannot enumerate anything -- so
// someone hammering a room number cannot lock out the guests who already have a device on that stay. The most a
// flood can do is hold NEW devices of that room out for the lockout (at most four hours, a setting), and only while it
// keeps sending; nothing is permanent and nothing needs an operator.
//
// The appliance-wide rule is Global: it is charged only for attempts every other rule allowed, so a client that is
// already refused cannot exhaust it for everybody.
func (s *Store) chargeThrottle(ctx context.Context, req VerifyRequest, cands []candidate, room, reservation string) (throttle.Decision, error) {
	lim := req.Limits.Bounded()
	block := lim.lockout()
	rules := []throttle.Rule{
		{Kind: throttle.ScopeDevice, Value: req.Device, Method: ThrottleMethod, Limit: lim.DeviceAttemptsPerMinute, Block: block},
		{Kind: throttle.ScopeEndpoint, Value: "post-stay-pin", Method: ThrottleMethod, Limit: lim.ApplianceAttemptsPerMinute, Global: true},
	}
	if strings.TrimSpace(req.IP) != "" {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIP, Value: req.IP, Method: ThrottleMethod, Limit: lim.IPAttemptsPerMinute, Block: block})
	}
	if len(cands) == 0 {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity, Value: "new-device|" + req.Device,
			Method: ThrottleMethod, Limit: lim.WrongAnswersBeforeLockout, Block: block})
		// An empty value is skipped by the store; an oversized one is refused as malformed below and is bounded by the
		// rules above.
		if room != "" && len(room) <= maxTypedLen {
			rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity, Value: "new-device-room|" + req.Tenant + "|" + req.Site + "|" + room,
				Method: ThrottleMethod, Limit: lim.NewDeviceRoomAttemptsPerMinute, Block: block})
		}
		if reservation != "" && len(reservation) <= maxTypedLen {
			rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity, Value: "new-device-reservation|" + req.Tenant + "|" + req.Site + "|" + reservation,
				Method: ThrottleMethod, Limit: lim.NewDeviceRoomAttemptsPerMinute, Block: block})
		}
	}
	for _, c := range cands {
		rules = append(rules, throttle.Rule{Kind: throttle.ScopeIdentity,
			Value:  "stay:" + c.stay + ":" + strconv.Itoa(c.version) + "|device:" + req.Device,
			Method: ThrottleMethod, Limit: lim.WrongAnswersBeforeLockout, Block: block})
	}
	d, err := s.thr.Allow(ctx, rules...)
	if err != nil {
		return throttle.Decision{Reason: throttle.ScopeEndpoint}, ErrThrottled
	}
	if !d.Allowed {
		return d, ErrThrottled
	}
	return d, nil
}

// Refusal is the error Verify returns for a refused attempt. It IS ErrNotAuthenticable or ErrThrottled to errors.Is and to
// Error() -- a caller that distinguishes more than that has built an oracle -- and carries only the facts the audit row
// records.
type Refusal struct {
	Err        error
	Route      string
	Candidates int
	Scope      string
}

func (r *Refusal) Error() string { return r.Err.Error() }
func (r *Refusal) Unwrap() error { return r.Err }

// Verify checks the room and reservation (and, from a new device, the last name) against the original stay and returns
// the profile id of that stay's episode. Every failure is ErrNotAuthenticable (or ErrThrottled) and the work done is the
// same on every path: the candidates are always read, every candidate is always compared, and the new-device lookup runs
// whenever there is no candidate. Every refusal is also written to the audit log (audit.go).
func (s *Store) Verify(ctx context.Context, req VerifyRequest) (string, error) {
	profile, rf, err := s.verify(ctx, req)
	if err != nil {
		return "", err
	}
	if rf != nil {
		code := RefusalNotAuthenticable
		if errors.Is(rf.Err, ErrThrottled) {
			code = RefusalThrottled
		}
		s.AuditRefusal(ctx, RefusalEvent{Tenant: req.Tenant, Code: code, Route: rf.Route, Candidates: rf.Candidates,
			ThrottleScope: rf.Scope, Device: req.Device, IP: req.IP,
			Room: namenorm.Room(req.Room), Reservation: namenorm.Reservation(req.Reservation)})
		return "", rf
	}
	return profile, nil
}

// verify returns a profile, or a Refusal, or (for a fault of the appliance rather than an answer) an error.
func (s *Store) verify(ctx context.Context, req VerifyRequest) (string, *Refusal, error) {
	cands, err := s.candidateStays(ctx, req.Tenant, req.Site, req.Device)
	if err != nil {
		return "", nil, err
	}
	route := RouteKnownDevice
	if len(cands) == 0 {
		route = RouteNewDevice
	}
	room, reservation := namenorm.Room(req.Room), namenorm.Reservation(req.Reservation)
	if s.thr != nil {
		if d, err := s.chargeThrottle(ctx, req, cands, room, reservation); err != nil {
			return "", &Refusal{Err: ErrThrottled, Route: route, Candidates: len(cands), Scope: string(d.Reason)}, nil
		}
	}
	if len(room) > maxTypedLen || len(reservation) > maxTypedLen {
		return "", &Refusal{Err: ErrNotAuthenticable, Route: route, Candidates: len(cands)}, nil
	}

	matchedStay := ""
	// Every candidate is compared; none short-circuits, so the position of a match is not visible in the time taken.
	for _, c := range cands {
		roomOK := sameText(namenorm.Room(c.room), room)
		resOK := sameText(namenorm.Reservation(c.reservation), reservation)
		if roomOK && resOK && matchedStay == "" {
			matchedStay = c.stay
		}
	}
	if len(cands) == 0 {
		// A device with no lineage: there is nothing to find, whatever it types. The same query's cost is spent anyway, so a
		// refusal here takes as long as a wrong answer from a device that has a stay.
		rows, err := s.pool.Query(ctx, `SELECT st.id::text FROM iam_v2.stays st
			WHERE st.tenant_id=$1 AND st.site_id=$2 AND st.normalized_room_number=$3 AND false LIMIT 2`, req.Tenant, req.Site, room)
		if err == nil {
			rows.Close()
		}
	}
	if matchedStay == "" {
		return "", &Refusal{Err: ErrNotAuthenticable, Route: route, Candidates: len(cands)}, nil
	}
	profile, err := s.ensureProfile(ctx, req.Tenant, req.Site, matchedStay)
	if err != nil {
		if errors.Is(err, ErrNotAuthenticable) {
			return "", &Refusal{Err: ErrNotAuthenticable, Route: route, Candidates: len(cands)}, nil
		}
		return "", nil, err
	}
	return profile, nil, nil
}
