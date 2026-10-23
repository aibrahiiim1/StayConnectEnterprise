package iamv2

import (
	"strconv"
	"strings"
)

// PMS Stay-domain feature flags, from env only. Everything defaults OFF; with the master OFF nothing
// constructs a repository, opens a PMS connection or serves a route, and a child ON while master is OFF is a
// fail-closed startup error. NEXT_PUBLIC_PHASE3_ADMIN is a Hotel-Admin frontend gate, never parsed here.
const (
	EnvPhase3Master        = "STAYCONNECT_PHASE3_MASTER"
	EnvPhase3PMSConnector  = "STAYCONNECT_PHASE3_PMS_CONNECTOR"
	EnvPhase3PMSIngest     = "STAYCONNECT_PHASE3_PMS_INGEST"
	EnvPhase3PMSAuth       = "STAYCONNECT_PHASE3_PMS_AUTH"
	EnvPhase3CheckoutGrace = "STAYCONNECT_PHASE3_CHECKOUT_GRACE"
	EnvPhase3Admin         = "STAYCONNECT_PHASE3_ADMIN"
)

type PMSConfig struct {
	MasterEnabled        bool
	PMSConnectorEnabled  bool
	PMSIngestEnabled     bool
	PMSAuthEnabled       bool
	CheckoutGraceEnabled bool
	AdminEnabled         bool
}

func DefaultPMSConfig() PMSConfig { return PMSConfig{} }

// A surface is live only when both the master flag and its own flag are ON.
func (c PMSConfig) ConnectorOn() bool     { return c.MasterEnabled && c.PMSConnectorEnabled }
func (c PMSConfig) IngestOn() bool        { return c.MasterEnabled && c.PMSIngestEnabled }
func (c PMSConfig) AuthOn() bool          { return c.MasterEnabled && c.PMSAuthEnabled }
func (c PMSConfig) CheckoutGraceOn() bool { return c.MasterEnabled && c.CheckoutGraceEnabled }
func (c PMSConfig) AdminOn() bool         { return c.MasterEnabled && c.AdminEnabled }

// EnforcementOn (acctd's shaping producer, netd's network writer) follows the MASTER flag, deliberately not
// any surface flag: enforcement is the substrate every guest-session surface stands on (a Session commits as
// PENDING_ENFORCEMENT and only this plane makes it real). Tying it to a surface let Room Login go live with
// nothing enforcing, and a surface flag set in one service's environment but not another's would half-enable
// it. It is data-driven and idle at no cost when there are no sessions.
func (c PMSConfig) EnforcementOn() bool { return c.MasterEnabled }

func (c PMSConfig) Enabled() bool {
	return c.MasterEnabled &&
		(c.PMSConnectorEnabled || c.PMSIngestEnabled || c.PMSAuthEnabled || c.CheckoutGraceEnabled || c.AdminEnabled)
}

func (c PMSConfig) anyChildSet() bool {
	return c.PMSConnectorEnabled || c.PMSIngestEnabled || c.PMSAuthEnabled || c.CheckoutGraceEnabled || c.AdminEnabled
}

func (c PMSConfig) Validate() error {
	if !c.MasterEnabled && c.anyChildSet() {
		return &Error{Code: ErrConfig, Msg: "phase3 surface flag enabled while STAYCONNECT_PHASE3_MASTER is OFF"}
	}
	return nil
}

// LoadPMSConfigFromEnv fails closed on a malformed boolean or any child flag set while master is OFF.
func LoadPMSConfigFromEnv(get Getenv) (PMSConfig, error) {
	master, err := parseBoolStrict(EnvPhase3Master, get(EnvPhase3Master))
	if err != nil {
		return PMSConfig{}, err
	}
	connector, err := parseBoolStrict(EnvPhase3PMSConnector, get(EnvPhase3PMSConnector))
	if err != nil {
		return PMSConfig{}, err
	}
	ingest, err := parseBoolStrict(EnvPhase3PMSIngest, get(EnvPhase3PMSIngest))
	if err != nil {
		return PMSConfig{}, err
	}
	auth, err := parseBoolStrict(EnvPhase3PMSAuth, get(EnvPhase3PMSAuth))
	if err != nil {
		return PMSConfig{}, err
	}
	grace, err := parseBoolStrict(EnvPhase3CheckoutGrace, get(EnvPhase3CheckoutGrace))
	if err != nil {
		return PMSConfig{}, err
	}
	admin, err := parseBoolStrict(EnvPhase3Admin, get(EnvPhase3Admin))
	if err != nil {
		return PMSConfig{}, err
	}
	c := PMSConfig{
		MasterEnabled:        master,
		PMSConnectorEnabled:  connector,
		PMSIngestEnabled:     ingest,
		PMSAuthEnabled:       auth,
		CheckoutGraceEnabled: grace,
		AdminEnabled:         admin,
	}
	if err := c.Validate(); err != nil {
		return PMSConfig{}, err
	}
	return c, nil
}

func (c PMSConfig) SafeFlagSummary() string {
	var b strings.Builder
	b.WriteString("phase3 master=")
	b.WriteString(strconv.FormatBool(c.MasterEnabled))
	b.WriteString(" connector=")
	b.WriteString(strconv.FormatBool(c.PMSConnectorEnabled))
	b.WriteString(" ingest=")
	b.WriteString(strconv.FormatBool(c.PMSIngestEnabled))
	b.WriteString(" auth=")
	b.WriteString(strconv.FormatBool(c.PMSAuthEnabled))
	b.WriteString(" grace=")
	b.WriteString(strconv.FormatBool(c.CheckoutGraceEnabled))
	b.WriteString(" admin=")
	b.WriteString(strconv.FormatBool(c.AdminEnabled))
	return b.String()
}
