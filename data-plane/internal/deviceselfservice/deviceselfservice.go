// Package deviceselfservice is the Phase-6 guest device-management surface: an authenticated guest may see
// the devices on their OWN entitlement and release one that is offline.
//
// THREE THINGS THIS PACKAGE DOES NOT DO, and each is deliberate.
//
//  1. It accepts no subject identifier of any kind. There is no MAC, room, stay, PMS-interface, profile or
//     entitlement parameter anywhere in its API — absent, not validated. The caller's entitlement is resolved
//     from the authenticated context by the layer above and passed in; a parameter that does not exist cannot
//     be validated wrongly, which is the same conclusion Phase 5 reached for post-stay.
//
//  2. It does not decide whether a device may be released. That decision is inseparable from the write —
//     an offline check followed by a separate removal is a race, not a rule — so both happen inside
//     iam_v2.p6_guest_release_device under one lock order. This package's job is to ask the right question
//     and to say nothing useful about the answer.
//
//  3. It never explains a refusal to the guest. Every refusal returns the same opaque outcome to the caller
//     precisely so that "that device is not yours" and "that device does not exist" are indistinguishable
//     from outside. The reason is recorded in the durable audit, where an operator can see it.
//
// TWO CONTROLS GATE THE SURFACE, and both must permit it: the Phase-6 deployment gate (is this build allowed
// to serve the route at all) and the per-appliance product setting (does this hotel offer the feature). They
// are different questions with different owners and are never collapsed into one check.
package deviceselfservice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcome is the result of a release attempt, as the database decided it.
type Outcome string

const (
	OutcomeOK               Outcome = "OK"
	OutcomeRefusedOnline    Outcome = "REFUSED_ONLINE"
	OutcomeRefusedNotFound  Outcome = "REFUSED_NOT_FOUND"
	OutcomeRefusedAlready   Outcome = "REFUSED_ALREADY_RELEASED"
	OutcomeRefusedDisabled  Outcome = "REFUSED_DISABLED"
	OutcomeRefusedThrottled Outcome = "REFUSED_THROTTLED"
)

// Released reports whether the attempt actually freed a slot. Everything else is a refusal, and callers are
// expected to treat all refusals identically on the guest surface.
func (o Outcome) Released() bool { return o == OutcomeOK }

// ErrDisabled means the feature is not available on this appliance. It is returned rather than a refusal
// outcome so a caller cannot accidentally render "you may not remove that device" when the truth is that the
// hotel does not offer device management at all.
var ErrDisabled = errors.New("guest device self-service is not enabled on this appliance")

// Device is one device on the caller's own entitlement, as the guest may see it.
//
// It carries NO MAC address. A guest does not need one to recognise their own phone, and printing it would
// hand every guest a stable network identifier for a device on a shared network — including, if the listing
// were ever mis-scoped, somebody else's. LastSeen and the online flag are what a person actually uses to tell
// their devices apart.
type Device struct {
	ID       string
	LastSeen *time.Time
	Online   bool
	// Removable is the same predicate the database will re-check under lock. It is advisory only: it says
	// what was true when the list was built, and the release path re-decides. Rendering it saves a guest from
	// an obviously futile attempt; trusting it would be a race.
	Removable bool
}

// Service reads and mutates guest device state.
type Service struct{ pool *pgxpool.Pool }

// New builds a Service over an existing pool.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// EnabledForAppliance reports the per-appliance product setting.
//
// LOCAL-FIRST BY CONSTRUCTION: it is one read of the site database on the appliance itself. There is no
// Central Control Plane call on this path, so an appliance with no uplink answers exactly as one with an
// uplink — which is the property the feature is required to have, and the reason the setting does not live
// in a remotely-fetched configuration bundle.
//
// A MISSING ROW IS OFF. The default is expressed in the schema and again here: an appliance nobody has
// configured has not opted in, and inventing a default of true at read time would quietly enable a
// guest-facing capability on every unconfigured appliance in the fleet.
func (s *Service) EnabledForAppliance(ctx context.Context, tenant, site, appliance string) (bool, error) {
	var on bool
	err := s.pool.QueryRow(ctx, `SELECT guest_device_self_service
		FROM iam_v2.appliance_product_settings
		WHERE tenant_id=$1 AND site_id=$2 AND appliance_id=$3`, tenant, site, appliance).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read guest_device_self_service: %w", err)
	}
	return on, nil
}

// SetForAppliance changes the setting and records who changed it.
//
// BOTH THE SCOPE AND THE ACTOR ARE THE SERVER'S. tenant/site/appliance describe the appliance this process is
// running on, resolved from its own local identity, and operatorID is the authenticated operator resolved
// from the session — neither is ever taken from a request body. The database enforces both: the settings row
// is foreign-keyed to (id, tenant_id, site_id) of a real enrolled appliance, so a real appliance under
// somebody else's scope is refused, and the audit's actor is foreign-keyed to a real operator record.
//
// The audit row is written in the SAME transaction as the change. A setting that moved without a trace, or a
// trace of a change that did not happen, are both worse than no audit at all.
func (s *Service) SetForAppliance(ctx context.Context, tenant, site, appliance string,
	on bool, operatorID, operatorLabel, reason string) error {
	// ONE CONTROLLED OPERATION, not two writes in a transaction. The Go layer used to write the setting and
	// its audit itself, which was atomic only because this function chose to be: the ROLE could write either
	// alone, so the audit was mandatory by convention and optional by privilege. svc_edged now holds no
	// direct write on either table and only EXECUTE on this operation, so a setting cannot move without its
	// audit -- there is no privilege that would do it.
	//
	// Every argument is still the SERVER'S. tenant/site/appliance come from the appliance's own trusted local
	// assignment and the operator from authenticated session context; the foreign keys behind them refuse
	// anything invented.
	_, err := s.pool.Exec(ctx,
		`SELECT iam_v2.p6_set_guest_device_self_service($1,$2,$3,$4,$5,$6,$7)`,
		tenant, site, appliance, on, operatorID, operatorLabel, nullIfEmpty(reason))
	if err != nil {
		return fmt.Errorf("set guest_device_self_service: %w", err)
	}
	return nil
}

// ListOwnDevices returns the devices bound to the caller's OWN entitlement.
//
// LISTING IS NOT AUDITED, and the schema no longer claims it is. An earlier version of guest_device_actions
// admitted a LIST action that no path ever wrote; migration 0035 narrowed the action set to RELEASE, which is
// the only guest action that changes durable state. Auditing reads would also have meant granting this
// surface a write on its own audit table -- exactly the privilege the Phase-6 audit removed on purpose.
//
// The entitlement is the one the server resolved from the authenticated context. Every row returned is
// scoped by it in the WHERE clause, so "only your own devices" is a property of the query rather than of a
// filter somebody has to remember to apply.
//
// Released bindings are included with Online=false and Removable=false: a guest who released a device should
// see that it is no longer using a slot, rather than watch it vanish and wonder whether the release worked.
func (s *Service) ListOwnDevices(ctx context.Context, entitlementID string) ([]Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT d.id::text,
		       d.last_seen,
		       (live.n > 0) AS online,
		       (ed.status = 'AUTHORIZED' AND live.n = 0) AS removable
		  FROM iam_v2.entitlement_devices ed
		  JOIN iam_v2.devices d ON d.id = ed.device_id
		  CROSS JOIN LATERAL (
		      SELECT count(*) AS n FROM iam_v2.sessions se
		       WHERE se.entitlement_id = ed.entitlement_id AND se.device_id = ed.device_id
		         AND se.state IN ('active','PENDING_ENFORCEMENT')) live
		 WHERE ed.entitlement_id = $1
		 ORDER BY d.last_seen DESC NULLS LAST, d.id`, entitlementID)
	if err != nil {
		return nil, fmt.Errorf("list own devices: %w", err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		var d Device
		if err := rows.Scan(&d.ID, &d.LastSeen, &d.Online, &d.Removable); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Release attempts to free the slot held by one device on the caller's own entitlement.
//
// It delegates the entire decision to iam_v2.p6_guest_release_device, which is the only place the offline
// state, the binding status and the throttle can be read and acted on atomically. This function deliberately
// contains no logic that could disagree with it.
func (s *Service) Release(ctx context.Context, entitlementID, deviceID string) (Outcome, error) {
	// THE POLICY ENTRY POINT, which takes no throttle parameter. The three-argument primitive exists for
	// tests and is not granted to any runtime role: a caller that can pass its own hourly limit can pass
	// 2147483647 and use the approved function to bypass the approved policy, so the runtime is never in a
	// position to name one.
	var out string
	if err := s.pool.QueryRow(ctx,
		`SELECT iam_v2.p6_guest_release_device_policy($1,$2)`, entitlementID, deviceID).Scan(&out); err != nil {
		return "", fmt.Errorf("release device: %w", err)
	}
	return Outcome(out), nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// EntitlementForDevice resolves the entitlement this device is currently authorized under.
//
// THIS IS THE WHOLE SUBJECT DERIVATION, and it takes nothing from any request. The caller supplies a device
// identity that the server itself resolved from the source address and hardware address against its own
// tables; from there the entitlement is whichever LIVE entitlement currently holds an AUTHORIZED binding for
// that device. A guest cannot name an entitlement, a Stay, a room, a PMS interface or a profile, because no
// parameter here would carry one.
//
// A device with no live authorized binding gets ErrNoEntitlement, which callers must render as the same
// uniform non-success as everything else: "you have no access here" and "that device is not yours" must be
// indistinguishable from outside.
func (s *Service) EntitlementForDevice(ctx context.Context, tenant, site, device string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT e.id::text
		  FROM iam_v2.entitlement_devices ed
		  JOIN iam_v2.entitlements e ON e.id = ed.entitlement_id
		 WHERE ed.tenant_id = $1 AND ed.site_id = $2 AND ed.device_id = $3
		   AND ed.status = 'AUTHORIZED'
		   AND e.status IN ('ACTIVE','PENDING','SUSPENDED')
		 ORDER BY ed.last_authorized DESC NULLS LAST
		 LIMIT 1`, tenant, site, device).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoEntitlement
	}
	if err != nil {
		return "", fmt.Errorf("resolve entitlement for device: %w", err)
	}
	return id, nil
}

// ErrNoEntitlement means this device holds no live authorized binding. It is a distinct error from a refusal
// so the caller can log which happened, and it must NOT be distinguishable in anything the guest sees.
var ErrNoEntitlement = errors.New("no live authorized entitlement for this device")
