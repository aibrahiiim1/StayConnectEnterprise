package poststay

// Persistent evidence of post-stay sign-in. Every refusal, and each grant or join of the post-stay entitlement, is a row
// in public.audit_log (the appliance's existing append-only audit table; svc_scd holds INSERT on it), so an operator can
// see guessing against a room and tell the one-time Post-Stay benefit from a device simply joining it.
//
// What a row may hold: ids the appliance already keeps (tenant, stay, entitlement), the source address in the table's own
// ip column (existing audit rows hold it), a reason CODE, and keyed pseudonyms (throttle.Store.Fingerprint) of the device,
// the room and the reservation number as typed. NEVER the surname, the room or reservation in clear, or any
// free text from the request or an error.
//
// Best effort, like internal/signinattempt: a guest is refused or admitted exactly as otherwise when the row cannot be
// written (it is logged). Bounded: under a flood the rows per device and per minute are capped and the rest are counted
// in one "suppressed" row a minute, so an attacker cannot fill the audit table through this endpoint.

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/throttle"
)

const (
	AuditActionRefused           = "post_stay.signin_refused"
	AuditActionRefusalsDropped   = "post_stay.signin_refusals_suppressed"
	AuditActionBenefitGranted    = "post_stay.benefit_granted"
	AuditActionEntitlementJoined = "post_stay.entitlement_joined"
)

// Refusal reason codes: a closed list, so a row never carries text taken from a request or an error.
const (
	RefusalNotAuthenticable = "NOT_AUTHENTICABLE" // wrong or unknown answer, revoked, expired, moved-on episode
	RefusalThrottled        = "THROTTLED"
	RefusalMalformed        = "MALFORMED"
	RefusalLicense          = "LICENSE_REFUSED"
	RefusalDeviceUnresolved = "DEVICE_UNRESOLVED"
	RefusalNoPackage        = "NO_GRANTABLE_PACKAGE"
	RefusalContext          = "CONTEXT_REFUSED"
	RefusalBenefitSpent     = "BENEFIT_ALREADY_GRANTED" // the once-per-stay benefit was used; a device cannot get it again
	RefusalNotJoinable      = "ENTITLEMENT_NOT_JOINABLE"
	RefusalDeviceLimit      = "DEVICE_LIMIT_REACHED"
	RefusalCapacity         = "LICENSED_CAPACITY"
	RefusalEnforcement      = "ENFORCEMENT_NOT_CONFIRMED"
	RefusalInternal         = "INTERNAL"
)

// Route says which proof the attempt was judged by.
const (
	RouteKnownDevice = "known_device" // the device was on a stay: room + reservation against that stay
	RouteNewDevice   = "new_device"   // no stay of its own: room + reservation + last name
)

// RefusalEvent is what the caller knows; Device/IP/Room/Reservation are fingerprinted or reduced before they are stored.
type RefusalEvent struct {
	Tenant, Code, Route, ThrottleScope string
	Candidates                         int
	Device, IP, Room, Reservation      string
}

const (
	auditPerKeyPerMinute = 3
	auditTotalPerMinute  = 120
)

// auditor is the bounded writer. One per Store.
type auditor struct {
	mu         sync.Mutex
	window     time.Time
	perKey     map[string]int
	total      int
	suppressed int
	tenant     string // a tenant to attribute the suppression row to
}

func newAuditor() *auditor { return &auditor{perKey: map[string]int{}} }

// admit reports whether a refusal row may be written now, and, when a new minute starts after rows were dropped, how many
// were dropped in the previous one.
func (a *auditor) admit(now time.Time, key, tenant string) (ok bool, dropped int, droppedTenant string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w := now.Truncate(time.Minute)
	if !w.Equal(a.window) {
		dropped, droppedTenant = a.suppressed, a.tenant
		a.window, a.perKey, a.total, a.suppressed = w, map[string]int{}, 0, 0
	}
	if a.total >= auditTotalPerMinute || a.perKey[key] >= auditPerKeyPerMinute {
		a.suppressed++
		a.tenant = tenant
		return false, dropped, droppedTenant
	}
	a.perKey[key]++
	a.total++
	return true, dropped, droppedTenant
}

func (s *Store) fp(kind throttle.ScopeKind, v string) string {
	if s.thr == nil {
		return ""
	}
	return s.thr.Fingerprint(kind, v)
}

// AuditRefusal records one refusal. Never returns an error and never blocks the caller past a short timeout.
func (s *Store) AuditRefusal(ctx context.Context, ev RefusalEvent) {
	if s == nil || s.pool == nil || ev.Tenant == "" {
		return
	}
	dev := s.fp(throttle.ScopeDevice, ev.Device)
	ok, dropped, dTenant := s.aud.admit(s.now(), ev.Code+"|"+dev, ev.Tenant)
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if dropped > 0 && dTenant != "" {
		s.writeAudit(actx, dTenant, "system", "", AuditActionRefusalsDropped, "", map[string]any{"suppressed": dropped})
	}
	if !ok {
		return
	}
	payload := map[string]any{"code": ev.Code}
	if ev.Route != "" {
		payload["route"] = ev.Route
		payload["candidate_stays"] = ev.Candidates
	}
	if ev.ThrottleScope != "" {
		payload["throttle_scope"] = ev.ThrottleScope
	}
	if dev != "" {
		payload["device_fp"] = dev
	}
	if r := s.fp(throttle.ScopeIdentity, "post-stay-room|"+ev.Room); ev.Room != "" && r != "" {
		payload["room_fp"] = r
	}
	if r := s.fp(throttle.ScopeIdentity, "post-stay-reservation|"+ev.Reservation); ev.Reservation != "" && r != "" {
		payload["reservation_fp"] = r
	}
	s.writeAudit(actx, ev.Tenant, "guest", dev, AuditActionRefused, ev.IP, payload)
}

func (s *Store) writeAudit(ctx context.Context, tenant, actorType, actorID, action, ip string, payload map[string]any) {
	var ipArg any
	if p := net.ParseIP(ip); p != nil {
		ipArg = p.String()
	}
	var actor any
	if actorID != "" {
		actor = actorID
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO public.audit_log
		(tenant_id, actor_type, actor_id, action, target_type, target_id, ip, payload)
		VALUES ($1::uuid, $2, $3, $4, 'post_stay_signin', NULL, $5::inet, $6)`,
		tenant, actorType, actor, action, ipArg, payload); err != nil {
		slog.Warn("post-stay audit row could not be written", "action", action, "err", err)
	}
}
