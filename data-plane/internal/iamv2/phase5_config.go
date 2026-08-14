package iamv2

import (
	"fmt"
	"strconv"
	"strings"
)

// PHASE-5 DARK FLAGS.
//
// Same shape as Phase 2, 3 and 4, and for the same reason: a child flag set while the master is off is a
// STARTUP FAILURE rather than a quiet no-op. A deployment mistake that silently leaves a surface off looks
// identical to a correct dark deployment, and the difference only surfaces when somebody needs the feature.
//
// While the master is off, the Phase-5 routes are not mounted at all, so the guest and operator surfaces are
// ABSENT (404) rather than present-and-refusing — the same posture the earlier phases ship in.
const (
	EnvPhase5Master        = "STAYCONNECT_PHASE5_MASTER"
	EnvPhase5PostStayGuest = "STAYCONNECT_PHASE5_POSTSTAY_GUEST"
	EnvPhase5PostStayAdmin = "STAYCONNECT_PHASE5_POSTSTAY_ADMIN"
	EnvPhase5Transfer      = "STAYCONNECT_PHASE5_TRANSFER"
)

// Phase5Config is the Post-Stay / Cross-PMS-Transfer flag set.
type Phase5Config struct {
	MasterEnabled        bool
	PostStayGuestEnabled bool
	PostStayAdminEnabled bool
	TransferEnabled      bool
}

// GuestOn reports whether the guest-facing post-stay surface should be mounted.
func (c Phase5Config) GuestOn() bool { return c.MasterEnabled && c.PostStayGuestEnabled }

// AdminOn reports whether the operator post-stay surface should be mounted.
func (c Phase5Config) AdminOn() bool { return c.MasterEnabled && c.PostStayAdminEnabled }

// TransferOn reports whether the cross-PMS transfer surface should be mounted.
func (c Phase5Config) TransferOn() bool { return c.MasterEnabled && c.TransferEnabled }

// Enabled reports whether ANY Phase-5 surface is on.
func (c Phase5Config) Enabled() bool { return c.GuestOn() || c.AdminOn() || c.TransferOn() }

// Validate refuses an incoherent flag set.
func (c Phase5Config) Validate() error {
	if c.MasterEnabled {
		return nil
	}
	for _, f := range []struct {
		on   bool
		name string
	}{
		{c.PostStayGuestEnabled, EnvPhase5PostStayGuest},
		{c.PostStayAdminEnabled, EnvPhase5PostStayAdmin},
		{c.TransferEnabled, EnvPhase5Transfer},
	} {
		if f.on {
			return fmt.Errorf("%s is set while %s is off: a child flag without its master is a deployment "+
				"mistake, and it must be loud rather than silently off anyway", f.name, EnvPhase5Master)
		}
	}
	return nil
}

// LoadPhase5ConfigFromEnv reads the flag set. Every flag defaults OFF and an unparseable value is an error,
// never a false: "true-ish" spelling mistakes must not decide whether a surface exists.
func LoadPhase5ConfigFromEnv(get Getenv) (Phase5Config, error) {
	master, err := parseBoolStrict(EnvPhase5Master, get(EnvPhase5Master))
	if err != nil {
		return Phase5Config{}, err
	}
	guest, err := parseBoolStrict(EnvPhase5PostStayGuest, get(EnvPhase5PostStayGuest))
	if err != nil {
		return Phase5Config{}, err
	}
	admin, err := parseBoolStrict(EnvPhase5PostStayAdmin, get(EnvPhase5PostStayAdmin))
	if err != nil {
		return Phase5Config{}, err
	}
	transfer, err := parseBoolStrict(EnvPhase5Transfer, get(EnvPhase5Transfer))
	if err != nil {
		return Phase5Config{}, err
	}
	c := Phase5Config{
		MasterEnabled:        master,
		PostStayGuestEnabled: guest,
		PostStayAdminEnabled: admin,
		TransferEnabled:      transfer,
	}
	if err := c.Validate(); err != nil {
		return Phase5Config{}, err
	}
	return c, nil
}

// SafeFlagSummary is a log-safe description (no secrets).
func (c Phase5Config) SafeFlagSummary() string {
	var b strings.Builder
	b.WriteString("phase5 master=")
	b.WriteString(strconv.FormatBool(c.MasterEnabled))
	b.WriteString(" poststay_guest=")
	b.WriteString(strconv.FormatBool(c.PostStayGuestEnabled))
	b.WriteString(" poststay_admin=")
	b.WriteString(strconv.FormatBool(c.PostStayAdminEnabled))
	b.WriteString(" transfer=")
	b.WriteString(strconv.FormatBool(c.TransferEnabled))
	return b.String()
}
