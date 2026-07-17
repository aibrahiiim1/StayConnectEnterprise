package iamv2

import (
	"strconv"
	"strings"
)

// Phase-2 commercial-packages feature flags. Deployment-controlled env only (no DB flag table, no
// admin UI). Everything defaults OFF; when the master flag is OFF the commerce engine never touches a
// repository, issues zero Phase-2 SQL, and exposes no package UI — the appliance keeps legacy behavior.
const (
	EnvPhase2Master = "STAYCONNECT_PHASE2_MASTER"
	EnvPhase2Portal = "STAYCONNECT_PHASE2_PORTAL" // guest portal package discovery/selection/purchase
	EnvPhase2Admin  = "STAYCONNECT_PHASE2_ADMIN"  // Hotel Admin revisioned CRUD
)

// CommerceConfig holds the Phase-2 flag state.
type CommerceConfig struct {
	MasterEnabled bool
	PortalEnabled bool
	AdminEnabled  bool
}

// DefaultCommerceConfig is all-OFF.
func DefaultCommerceConfig() CommerceConfig { return CommerceConfig{} }

// PortalOn / AdminOn are the effective per-surface gates (a surface is live only when BOTH the master
// flag and its own flag are ON).
func (c CommerceConfig) PortalOn() bool { return c.MasterEnabled && c.PortalEnabled }
func (c CommerceConfig) AdminOn() bool  { return c.MasterEnabled && c.AdminEnabled }

// Enabled reports whether any Phase-2 surface is live (i.e. the engine may touch its repository).
func (c CommerceConfig) Enabled() bool { return c.MasterEnabled && (c.PortalEnabled || c.AdminEnabled) }

// Validate fails closed on an incoherent flag set (a surface enabled while the master flag is OFF).
func (c CommerceConfig) Validate() error {
	if !c.MasterEnabled && (c.PortalEnabled || c.AdminEnabled) {
		return &Error{Code: ErrConfig, Msg: "phase2 surface flag enabled while STAYCONNECT_PHASE2_MASTER is OFF"}
	}
	return nil
}

// LoadCommerceConfigFromEnv builds a CommerceConfig from env. A malformed boolean is a startup failure
// (fail closed), and a per-surface flag set while master is OFF is rejected.
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

// SafeFlagSummary is a log-safe description (no secrets).
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
