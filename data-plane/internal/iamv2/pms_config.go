package iamv2

import (
	"strconv"
	"strings"
)

// Phase-3 PMS Stay-domain feature flags. Deployment-controlled env only (no DB flag table, no admin UI).
// Everything defaults OFF; when the master flag is OFF nothing Phase-3 constructs a repository, opens a
// PMS connection, issues Phase-3 SQL, serves a Phase-3 route, or exposes any UI. Child flags are only
// effective when the master flag is also ON (a child ON while master OFF is a fail-closed startup error).
//
// NEXT_PUBLIC_PHASE3_ADMIN is intentionally NOT listed here: it is a Hotel-Admin (Next.js) frontend
// build/runtime gate, not a Go STAYCONNECT_* flag, and is never parsed by this package.
const (
	EnvPhase3Master        = "STAYCONNECT_PHASE3_MASTER"
	EnvPhase3PMSConnector  = "STAYCONNECT_PHASE3_PMS_CONNECTOR"  // pmsd owns the PMS Interface socket(s)
	EnvPhase3PMSIngest     = "STAYCONNECT_PHASE3_PMS_INGEST"     // Stay/Event ingestion pipeline
	EnvPhase3PMSAuth       = "STAYCONNECT_PHASE3_PMS_AUTH"       // STRICT resolver + PMS Auth Context routes
	EnvPhase3CheckoutGrace = "STAYCONNECT_PHASE3_CHECKOUT_GRACE" // checkout-grace execution
	EnvPhase3Admin         = "STAYCONNECT_PHASE3_ADMIN"          // Hotel-Admin PMS/Stays/Routing API surface
)

// PMSConfig holds the Phase-3 flag state.
type PMSConfig struct {
	MasterEnabled        bool
	PMSConnectorEnabled  bool
	PMSIngestEnabled     bool
	PMSAuthEnabled       bool
	CheckoutGraceEnabled bool
	AdminEnabled         bool
}

// DefaultPMSConfig is all-OFF (the delivered/deployed state).
func DefaultPMSConfig() PMSConfig { return PMSConfig{} }

// Per-surface effective gates: a surface is live only when BOTH the master flag and its own flag are ON.
func (c PMSConfig) ConnectorOn() bool     { return c.MasterEnabled && c.PMSConnectorEnabled }
func (c PMSConfig) IngestOn() bool        { return c.MasterEnabled && c.PMSIngestEnabled }
func (c PMSConfig) AuthOn() bool          { return c.MasterEnabled && c.PMSAuthEnabled }
func (c PMSConfig) CheckoutGraceOn() bool { return c.MasterEnabled && c.CheckoutGraceEnabled }
func (c PMSConfig) AdminOn() bool         { return c.MasterEnabled && c.AdminEnabled }

// Enabled reports whether any Phase-3 surface is live (i.e. a repository/engine may be constructed).
func (c PMSConfig) Enabled() bool {
	return c.MasterEnabled &&
		(c.PMSConnectorEnabled || c.PMSIngestEnabled || c.PMSAuthEnabled || c.CheckoutGraceEnabled || c.AdminEnabled)
}

// childOn is true if any child flag is set (regardless of master).
func (c PMSConfig) anyChildSet() bool {
	return c.PMSConnectorEnabled || c.PMSIngestEnabled || c.PMSAuthEnabled || c.CheckoutGraceEnabled || c.AdminEnabled
}

// Validate fails closed on an incoherent flag set (any child ON while the master flag is OFF).
func (c PMSConfig) Validate() error {
	if !c.MasterEnabled && c.anyChildSet() {
		return &Error{Code: ErrConfig, Msg: "phase3 surface flag enabled while STAYCONNECT_PHASE3_MASTER is OFF"}
	}
	return nil
}

// LoadPMSConfigFromEnv builds a PMSConfig from env. A malformed boolean is a startup failure (fail
// closed), and any child flag set while master is OFF is rejected.
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

// SafeFlagSummary is a log-safe description (no secrets).
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
