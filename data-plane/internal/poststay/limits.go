package poststay

import "time"

// Limits are the post-stay sign-in's brute-force bounds. They are the site's persisted, bounded, audited settings
// (migration 0190, docs/OPERATIONAL_SETTINGS_POLICY.md), read by scd with the other sign-in limits. The zero value, and
// any single field that is zero or outside its bounds, means the DEFAULT -- never "unlimited", never "off": a database
// that cannot answer must not loosen a limit. The bounds below equal the database CHECKs and the Hotel Admin console's.
type Limits struct {
	// DeviceAttemptsPerMinute: attempts, right or wrong, one device may make a minute.
	DeviceAttemptsPerMinute int
	// IPAttemptsPerMinute: attempts one network address may make a minute, across every device identity it uses.
	IPAttemptsPerMinute int
	// WrongAnswersBeforeLockout: attempts a minute per stay and device (a device with no stay of its own: per device)
	// before that device is locked out.
	WrongAnswersBeforeLockout int
	// LockoutMinutes: how long a lockout lasts. Bounded above, so no lockout is permanent.
	LockoutMinutes int
	// NewDeviceRoomAttemptsPerMinute: attempts a minute, from devices with no stay of their own, that name one room
	// (and, separately, one reservation number). It is what stops a client that changes its device identity (MAC)
	// from multiplying its guesses: the identity is free to change, the room and reservation it must guess are not.
	NewDeviceRoomAttemptsPerMinute int
	// ApplianceAttemptsPerMinute: attempts the whole appliance accepts a minute. Attempts a client was already refused
	// for do not use it up (throttle.Rule.Global), so one noisy client cannot take post-stay sign-in from every guest.
	ApplianceAttemptsPerMinute int
}

type limitBound struct{ min, max, def int }

// The bounds, one place. Mirrored by migration 0190's CHECKs and cmd/edged's signinCapacityFields (a test in each
// package pins them to this table).
var (
	BoundDeviceAttempts    = limitBound{3, 60, 10}
	BoundIPAttempts        = limitBound{5, 200, 20}
	BoundWrongAnswers      = limitBound{3, 20, 5}
	BoundLockoutMinutes    = limitBound{1, 240, 15}
	BoundNewDeviceRoom     = limitBound{3, 60, 10}
	BoundApplianceAttempts = limitBound{20, 5000, 200}
)

// Min, Max and Default expose a bound to other packages' tests.
func (b limitBound) Min() int     { return b.min }
func (b limitBound) Max() int     { return b.max }
func (b limitBound) Default() int { return b.def }

func (b limitBound) pick(v int) int {
	if v < b.min || v > b.max {
		return b.def
	}
	return v
}

// DefaultLimits are the values the code used as constants before they became settings.
func DefaultLimits() Limits { return Limits{}.Bounded() }

// Bounded returns the limits with every missing or out-of-range field replaced by its default.
func (l Limits) Bounded() Limits {
	return Limits{
		DeviceAttemptsPerMinute:        BoundDeviceAttempts.pick(l.DeviceAttemptsPerMinute),
		IPAttemptsPerMinute:            BoundIPAttempts.pick(l.IPAttemptsPerMinute),
		WrongAnswersBeforeLockout:      BoundWrongAnswers.pick(l.WrongAnswersBeforeLockout),
		LockoutMinutes:                 BoundLockoutMinutes.pick(l.LockoutMinutes),
		NewDeviceRoomAttemptsPerMinute: BoundNewDeviceRoom.pick(l.NewDeviceRoomAttemptsPerMinute),
		ApplianceAttemptsPerMinute:     BoundApplianceAttempts.pick(l.ApplianceAttemptsPerMinute),
	}
}

func (l Limits) lockout() time.Duration { return time.Duration(l.LockoutMinutes) * time.Minute }
