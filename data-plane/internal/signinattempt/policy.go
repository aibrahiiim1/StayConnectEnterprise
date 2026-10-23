package signinattempt

// Wrong credentials are counted per device in a rolling window; at the threshold that device is refused
// for a fixed period. All three numbers are editable per site without a deployment.
//
// Scope is the device, not the room (a room restriction locks out the real occupant and lets an attacker
// deny service by guessing wrong) and not the IP (guest networks NAT; an IP restriction restricts a floor).
//
// "Device" is the MAC the appliance itself read from its neighbour table, never a client-sent value. A
// determined attacker on the guest VLAN can change MAC and get a fresh counter, so this only stops casual
// enumeration; it is not an unspoofable identity.

import "fmt"

// Policy is the site's configured protection. Zero values are never used: a caller that cannot read the
// settings uses Defaults() rather than "0 attempts allowed".
type Policy struct {
	// MaxFailedAttempts wrong submissions from one device inside the window trigger a restriction. The
	// submission that reaches the number is still answered as an ordinary wrong credential; the next is refused.
	MaxFailedAttempts int
	// WindowSeconds is a rolling window (not a fixed boundary), so two failures at 10:00:59 and three at
	// 10:01:01 are not two clean slates.
	WindowSeconds int
	// RestrictionSeconds is measured from the failure that triggered the restriction.
	RestrictionSeconds int
}

func Defaults() Policy {
	return Policy{MaxFailedAttempts: 5, WindowSeconds: 60, RestrictionSeconds: 60}
}

// Bounds: a threshold below three would restrict guests for ordinary typing; a window or restriction
// below thirty seconds is shorter than a guest takes to retype, so it would only confuse guests. The upper
// bounds keep a mistyped setting from locking guests out for a day.
const (
	MinFailedAttempts     = 3
	MaxFailedAttemptsCap  = 20
	MinWindowSeconds      = 30
	MaxWindowSeconds      = 3600
	MinRestrictionSeconds = 30
	MaxRestrictionSeconds = 3600
)

// Validate's messages name the unit and range because they are shown to an operator typing into a form.
func (p Policy) Validate() error {
	if p.MaxFailedAttempts < MinFailedAttempts || p.MaxFailedAttempts > MaxFailedAttemptsCap {
		return fmt.Errorf("maximum failed attempts must be between %d and %d (got %d)",
			MinFailedAttempts, MaxFailedAttemptsCap, p.MaxFailedAttempts)
	}
	if p.WindowSeconds < MinWindowSeconds || p.WindowSeconds > MaxWindowSeconds {
		return fmt.Errorf("the observation window must be between %d and %d seconds (got %d)",
			MinWindowSeconds, MaxWindowSeconds, p.WindowSeconds)
	}
	if p.RestrictionSeconds < MinRestrictionSeconds || p.RestrictionSeconds > MaxRestrictionSeconds {
		return fmt.Errorf("the restriction duration must be between %d and %d seconds (got %d)",
			MinRestrictionSeconds, MaxRestrictionSeconds, p.RestrictionSeconds)
	}
	return nil
}

type Restriction struct {
	ID           string
	DeviceMAC    string
	GuestNetwork string
	// LastSubmittedRoom is unverified input, labelled as such wherever shown; reading it as identity would
	// identify a person by a string an attacker chose.
	LastSubmittedRoom string
	FailureCount      int
	Reason            string
	StartedAt         string
	ExpiresAt         string
	RemainingSeconds  int
}

const ReasonThreshold = "FAILED_CREDENTIAL_THRESHOLD"
