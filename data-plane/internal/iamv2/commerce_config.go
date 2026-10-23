package iamv2

import (
	"strconv"
	"strings"
)

// Commercial-package feature flags, from env only. All default OFF; with the master flag OFF the commerce
// engine never touches a repository.
const (
	EnvPhase2Master = "STAYCONNECT_PHASE2_MASTER"
	EnvPhase2Portal = "STAYCONNECT_PHASE2_PORTAL"
	EnvPhase2Admin  = "STAYCONNECT_PHASE2_ADMIN"
)

type CommerceConfig struct {
	MasterEnabled bool
	PortalEnabled bool
	AdminEnabled  bool
}

func DefaultCommerceConfig() CommerceConfig { return CommerceConfig{} }

// A surface is live only when both the master flag and its own flag are ON.
func (c CommerceConfig) PortalOn() bool { return c.MasterEnabled && c.PortalEnabled }
func (c CommerceConfig) AdminOn() bool  { return c.MasterEnabled && c.AdminEnabled }

func (c CommerceConfig) Enabled() bool { return c.MasterEnabled && (c.PortalEnabled || c.AdminEnabled) }

func (c CommerceConfig) Validate() error {
	if !c.MasterEnabled && (c.PortalEnabled || c.AdminEnabled) {
		return &Error{Code: ErrConfig, Msg: "phase2 surface flag enabled while STAYCONNECT_PHASE2_MASTER is OFF"}
	}
	return nil
}

// LoadCommerceConfigFromEnv fails closed: a malformed boolean, or a surface flag set while master is OFF, is a startup failure.
func LoadCommerceConfigFromEnv(get Getenv) (CommerceConfig, error) {
	master, err := parseBoolStrict(EnvPhase2Master, get(EnvPhase2Master))
	if err != nil {
		return CommerceConfig{}, err
	}
	portal, err := parseBoolStrict(EnvPhase2Portal, get(EnvPhase2Portal))
	if err != nil {
		return CommerceConfig{}, err
	}
	admin, err := parseBoolStrict(EnvPhase2Admin, get(EnvPhase2Admin))
	if err != nil {
		return CommerceConfig{}, err
	}
	c := CommerceConfig{MasterEnabled: master, PortalEnabled: portal, AdminEnabled: admin}
	if err := c.Validate(); err != nil {
		return CommerceConfig{}, err
	}
	return c, nil
}

func (c CommerceConfig) SafeFlagSummary() string {
	var b strings.Builder
	b.WriteString("phase2 master=")
	b.WriteString(strconv.FormatBool(c.MasterEnabled))
	b.WriteString(" portal=")
	b.WriteString(strconv.FormatBool(c.PortalEnabled))
	b.WriteString(" admin=")
	b.WriteString(strconv.FormatBool(c.AdminEnabled))
	return b.String()
}
