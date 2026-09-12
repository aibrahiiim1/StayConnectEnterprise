package signinattempt

// GUEST SIGN-IN PROTECTION — the policy, its bounds, and what it is scoped to.
//
// The shape of the control is: count wrong credentials from ONE DEVICE within a rolling window, and when the
// count reaches the threshold, refuse that device for a fixed period. Three numbers, all editable per site by
// an authorised operator without a deployment.
//
// WHAT IT IS SCOPED TO, AND WHY NOT THE OBVIOUS ALTERNATIVES.
//
//	the ROOM   — no. A restriction on a room number locks out the guest who actually lives there, and it is
//	             the attacker who chose that room number, not the victim. It also makes a denial of service
//	             trivial: guess wrong at room 412 five times and its occupant cannot sign in.
//	the IP     — no. A hotel guest network NATs, and a floor can share one address. Restricting an IP
//	             restricts a floor.
//	the DEVICE — yes. It is the narrowest thing the appliance can attribute an attempt to, and it is the
//	             thing that is actually doing the guessing.
//
// WHAT "DEVICE" HONESTLY MEANS HERE. It is the MAC address the APPLIANCE ITSELF read from its own neighbour
// table for the source address of the request — never a value the client sent, so no cookie, request id or
// header can move it. That is a real boundary and it is not an unbreakable one: a determined attacker on the
// guest VLAN can change their MAC, and each new MAC is a new device with a fresh counter. What this policy
// buys is that CASUAL enumeration stops being cheap, that a guest who mistypes is not locked out of a hotel
// by someone else's behaviour, and that every restriction is attributable and releasable by a human. It is
// not, and is not claimed to be, an identity that cannot be spoofed.

import "fmt"

// Policy is the site's configured protection. Zero values are never used: a caller that cannot read the
// settings uses Defaults() rather than an accidental "0 attempts allowed".
type Policy struct {
	// MaxFailedAttempts is how many wrong-credential submissions from one device, inside the window, trigger
	// a restriction. The submission that REACHES this number is still answered as an ordinary wrong
	// credential; the next one is refused.
	MaxFailedAttempts int
	// WindowSeconds is the rolling observation window. Failures older than this stop counting, continuously
	// rather than at a fixed boundary: a guest who mistypes twice at 10:00:59 and three times at 10:01:01
	// must not be treated as two separate clean slates.
	WindowSeconds int
	// RestrictionSeconds is how long the refusal lasts, measured from the failure that triggered it.
	RestrictionSeconds int
}

// Defaults are the Product Owner's approved values: five wrong credentials in sixty seconds, refused for
// sixty seconds.
func Defaults() Policy {
	return Policy{MaxFailedAttempts: 5, WindowSeconds: 60, RestrictionSeconds: 60}
}

// The accepted bounds. They are not arbitrary, and the lower ones matter more than the upper ones.
//
// A threshold below three would restrict guests for ordinary typing — a surname entered with a trailing
// space and then corrected is two attempts before anyone has done anything wrong. A window or a restriction
// below thirty seconds is shorter than the time a guest takes to read the message and retype, so the control
// would be invisible to an attacker and merely confusing to a guest. The upper bounds exist so that a
// mistyped setting cannot lock a property's guests out for a day.
const (
	MinFailedAttempts     = 3
	MaxFailedAttemptsCap  = 20
	MinWindowSeconds      = 30
	MaxWindowSeconds      = 3600
	MinRestrictionSeconds = 30
	MaxRestrictionSeconds = 3600
)

// Validate reports why a policy is unacceptable, or nil.
//
// The messages name the unit and the accepted range, because this error is shown to an operator who is
// typing into a settings form and "invalid value" tells them nothing about what to type instead.
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

// Restriction is an active refusal for one device, as an operator sees it and as the guest experiences it.
type Restriction struct {
	ID           string
	DeviceMAC    string
	GuestNetwork string
	// LastSubmittedRoom is the room the device last typed. It is UNVERIFIED INPUT and is labelled as such
	// everywhere it is shown: it is what somebody entered, never a statement about who they are or where they
	// are staying. An operator who reads it as an identity would be identifying a person by a string an
	// attacker chose.
	LastSubmittedRoom string
	FailureCount      int
	Reason            string
	StartedAt         string
	ExpiresAt         string
	RemainingSeconds  int
}

// ReasonThreshold is the only reason a restriction is created automatically today. It is a named constant
// rather than a literal so the operator screen, the record and any future second reason cannot drift.
const ReasonThreshold = "FAILED_CREDENTIAL_THRESHOLD"
