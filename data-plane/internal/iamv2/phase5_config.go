package iamv2

import (
	"fmt"
	"strconv"
	"strings"
)

// A child flag set while the master is off is a startup failure, not a quiet no-op (a silently-off surface
// looks like a correct dark deployment). While the master is off the routes are not mounted (404).
const (
	EnvPhase5Master        = "STAYCONNECT_PHASE5_MASTER"
	EnvPhase5PostStayGuest = "STAYCONNECT_PHASE5_POSTSTAY_GUEST"
	EnvPhase5PostStayAdmin = "STAYCONNECT_PHASE5_POSTSTAY_ADMIN"
	EnvPhase5Transfer      = "STAYCONNECT_PHASE5_TRANSFER"
)

type Phase5Config struct {
	MasterEnabled        bool
	PostStayGuestEnabled bool
	PostStayAdminEnabled bool
	TransferEnabled      bool
}

func (c Phase5Config) GuestOn() bool { return c.MasterEnabled && c.PostStayGuestEnabled }

func (c Phase5Config) AdminOn() bool { return c.MasterEnabled && c.PostStayAdminEnabled }

func (c Phase5Config) TransferOn() bool { return c.MasterEnabled && c.TransferEnabled }

func (c Phase5Config) Enabled() bool { return c.GuestOn() || c.AdminOn() || c.TransferOn() }

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

// LoadPhase5ConfigFromEnv defaults every flag OFF; an unparseable value is an error, never false.
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
