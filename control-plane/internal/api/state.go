package api

import "time"

// THE ONE PLACE APPLIANCE STATE IS DECIDED (docs/CENTRAL_CONTROL_PLANE.md §3).
//
// Every list, every detail page and the overview call DeriveState on the same facts. The console never
// derives a state itself, and nothing stores one: the appliance lifecycle column holds identity only
// (pending_approval, assigned, revoked, decommissioned).

// Activation states.
const (
	ActivationWaiting    = "waiting"
	ActivationActivating = "activating"
	ActivationActivated  = "activated"
	ActivationRetiring   = "retiring"
	ActivationRetired    = "retired"
)

// Connection states.
const (
	ConnectionConnected    = "connected"
	ConnectionRecentlySeen = "recently_seen"
	ConnectionOffline      = "offline"
	ConnectionNever        = "never"
)

// Licence states.
const (
	LicenseNone      = "none"
	LicenseActive    = "active"
	LicenseExpiring  = "expiring"
	LicenseGrace     = "grace"
	LicenseExpired   = "expired"
	LicenseSuspended = "suspended"
	LicenseRevoked   = "revoked"
)

// Thresholds. A healthy appliance calls Central every 30 s.
const (
	ConnectedWithin    = 5 * time.Minute
	RecentlySeenWithin = 24 * time.Hour
	ExpiringWithin     = 30 * 24 * time.Hour
)

// LicenseFacts is the appliance's licence as stored: the CURRENT row (status active or suspended) when one
// exists, otherwise the newest revoked row. Superseded rows are never facts.
type LicenseFacts struct {
	Status           string // active | suspended | revoked
	ValidUntil       time.Time
	GracePeriodDays  int
	OfflineGraceDays int
}

// EffectiveGraceDays mirrors the appliance's own rule (license.Document.EffectiveGraceDays): the explicit
// grace period when set, otherwise the offline allowance.
func (l LicenseFacts) EffectiveGraceDays() int {
	if l.GracePeriodDays > 0 {
		return l.GracePeriodDays
	}
	return l.OfflineGraceDays
}

// GraceEndsAt is valid_until plus the effective grace period.
func (l LicenseFacts) GraceEndsAt() time.Time {
	return l.ValidUntil.Add(time.Duration(l.EffectiveGraceDays()) * 24 * time.Hour)
}

// ApplianceFacts is everything DeriveState needs, read in one query.
type ApplianceFacts struct {
	Lifecycle string // pending_approval | assigned | revoked | decommissioned
	// TerminalDelivery is appliance_terminal_delivery.delivery_state, "" when retirement never started.
	TerminalDelivery string
	HasActiveCert    bool
	LastSeenAt       *time.Time
	License          *LicenseFacts // nil = no licence was ever issued (or only superseded ones)
}

// DerivedState is what the API returns for every appliance.
type DerivedState struct {
	Activation string
	Connection string
	License    string
}

// DeriveState computes the three §3 fields.
//
//	activation
//	  waiting     lifecycle pending_approval
//	  retired     lifecycle revoked or decommissioned
//	  retiring    lifecycle assigned and a retirement (terminal delivery) was started and not yet confirmed
//	  activated   lifecycle assigned, an ACTIVE client certificate exists, and a licence exists (licence
//	              state is not "none") — i.e. the appliance holds everything activation hands it
//	  activating  lifecycle assigned otherwise (certificate or licence not yet in place)
//
//	connection   from last_seen_at: <= 5 min connected, <= 24 h recently_seen, older offline, never
//
//	license      from LicenseFacts: suspended; revoked (no current licence, newest is revoked); otherwise by
//	             time — active, expiring (<= 30 days left), grace (past valid_until, inside the effective grace
//	             period), expired. none when there is no licence.
func DeriveState(f ApplianceFacts, now time.Time) DerivedState {
	d := DerivedState{
		Connection: deriveConnection(f.LastSeenAt, now),
		License:    DeriveLicense(f.License, now),
	}
	switch f.Lifecycle {
	case "pending_approval", "":
		d.Activation = ActivationWaiting
	case "revoked", "decommissioned":
		d.Activation = ActivationRetired
	default: // assigned
		switch {
		case f.TerminalDelivery != "":
			d.Activation = ActivationRetiring
		case f.HasActiveCert && d.License != LicenseNone:
			d.Activation = ActivationActivated
		default:
			d.Activation = ActivationActivating
		}
	}
	return d
}

func deriveConnection(lastSeen *time.Time, now time.Time) string {
	if lastSeen == nil || lastSeen.IsZero() {
		return ConnectionNever
	}
	age := now.Sub(*lastSeen)
	switch {
	case age <= ConnectedWithin:
		return ConnectionConnected
	case age <= RecentlySeenWithin:
		return ConnectionRecentlySeen
	default:
		return ConnectionOffline
	}
}

// DeriveLicense is the licence half of DeriveState, also used for a single licence row in the licence list.
func DeriveLicense(l *LicenseFacts, now time.Time) string {
	if l == nil {
		return LicenseNone
	}
	switch l.Status {
	case "suspended":
		return LicenseSuspended
	case "revoked":
		return LicenseRevoked
	case "active":
	default:
		return LicenseNone
	}
	switch {
	case !now.After(l.ValidUntil):
		if l.ValidUntil.Sub(now) <= ExpiringWithin {
			return LicenseExpiring
		}
		return LicenseActive
	case !now.After(l.GraceEndsAt()):
		return LicenseGrace
	default:
		return LicenseExpired
	}
}
