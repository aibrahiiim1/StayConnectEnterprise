package api

import "time"

// DeriveState is the one place appliance state is decided: the console never derives one and nothing stores one;
// the lifecycle column holds identity only.

const (
	ActivationWaiting    = "waiting"
	ActivationActivating = "activating"
	ActivationActivated  = "activated"
	ActivationRetiring   = "retiring"
	ActivationRetired    = "retired"
)

const (
	ConnectionConnected    = "connected"
	ConnectionRecentlySeen = "recently_seen"
	ConnectionOffline      = "offline"
	ConnectionNever        = "never"
)

const (
	LicenseNone      = "none"
	LicenseActive    = "active"
	LicenseExpiring  = "expiring"
	LicenseGrace     = "grace"
	LicenseExpired   = "expired"
	LicenseSuspended = "suspended"
	LicenseRevoked   = "revoked"
)

// A healthy appliance calls Central every 30 s.
const (
	ConnectedWithin    = 5 * time.Minute
	RecentlySeenWithin = 24 * time.Hour
	ExpiringWithin     = 30 * 24 * time.Hour
)

// LicenseFacts is the current (active or suspended) row, else the newest revoked row; superseded rows are never facts.
type LicenseFacts struct {
	Status           string // active | suspended | revoked
	ValidUntil       time.Time
	GracePeriodDays  int
	OfflineGraceDays int
}

// EffectiveGraceDays mirrors the appliance's own rule (license.Document.EffectiveGraceDays): the stored grace is
// explicit and 0 means no grace. OfflineGraceDays is the cloud-staleness allowance, never a renewal grace.
func (l LicenseFacts) EffectiveGraceDays() int {
	if l.GracePeriodDays < 0 {
		return 0
	}
	return l.GracePeriodDays
}

func (l LicenseFacts) GraceEndsAt() time.Time {
	return l.ValidUntil.Add(time.Duration(l.EffectiveGraceDays()) * 24 * time.Hour)
}

type ApplianceFacts struct {
	Lifecycle string // pending_approval | assigned | revoked | decommissioned
	// TerminalDelivery is appliance_terminal_delivery.delivery_state, "" when retirement never started.
	TerminalDelivery string
	HasActiveCert    bool
	LastSeenAt       *time.Time
	License          *LicenseFacts // nil = no licence was ever issued (or only superseded ones)
}

type DerivedState struct {
	Activation string
	Connection string
	License    string
}

// DeriveState computes activation, connection and licence state.
//
//	activation
//	  waiting     lifecycle pending_approval
//	  retired     lifecycle revoked or decommissioned
//	  retiring    lifecycle assigned and a retirement was started and not yet confirmed
//	  activated   lifecycle assigned, an active client certificate and a licence exist
//	  activating  lifecycle assigned otherwise
//
//	connection   from last_seen_at: <= 5 min connected, <= 24 h recently_seen, older offline, never
//
//	license      suspended; revoked (newest is revoked, none current); otherwise by time: active, expiring
//	             (<= 30 days left), grace (past valid_until, inside the grace period), expired; none without a licence.
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
