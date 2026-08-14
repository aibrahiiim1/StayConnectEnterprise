// Package posting is the Phase-4 financial execution core: the Posting domain, the durable per-interface
// P# allocator, the financial outbox and its per-interface worker lanes, FIAS PS construction, PA
// correlation, and the UNKNOWN safety state.
//
// It is DARK. Every flag below defaults OFF, and with them OFF this package cannot emit a byte to a PMS or
// a payment provider — see transport.go, which makes that a property of the code rather than a property of
// the deployment. Nothing here performs a real financial transmission in this milestone.
package posting

import (
	"strconv"
	"strings"
)

// Phase-4 financial feature flags. Deployment-controlled env only: no DB flag table, no admin UI, no
// runtime toggle. The shape deliberately mirrors the Phase-3 flags in internal/iamv2/pms_config.go — a
// master flag plus per-surface children, where a child ON while the master is OFF is a fail-closed startup
// error rather than a quietly-half-enabled system.
const (
	EnvPhase4Master   = "STAYCONNECT_PHASE4_MASTER"
	EnvPhase4Posting  = "STAYCONNECT_PHASE4_POSTING"          // Posting domain: creation + gate
	EnvPhase4Outbox   = "STAYCONNECT_PHASE4_OUTBOX_WORKER"    // outbox worker lanes may claim work
	EnvPhase4Transmit = "STAYCONNECT_PHASE4_PMS_TRANSMIT"     // the ONLY flag that can permit PS bytes
	EnvPhase4Review   = "STAYCONNECT_PHASE4_FINANCIAL_REVIEW" // manual-review write surface
)

// Config holds the Phase-4 flag state. The zero value is the delivered, deployed state: everything OFF.
type Config struct {
	MasterEnabled   bool
	PostingEnabled  bool
	OutboxEnabled   bool
	TransmitEnabled bool
	ReviewEnabled   bool
}

// DefaultConfig is all-OFF.
func DefaultConfig() Config { return Config{} }

// A surface is live only when BOTH the master flag and its own flag are ON.
func (c Config) PostingOn() bool { return c.MasterEnabled && c.PostingEnabled }
func (c Config) OutboxOn() bool  { return c.MasterEnabled && c.OutboxEnabled }
func (c Config) ReviewOn() bool  { return c.MasterEnabled && c.ReviewEnabled }

// TransmitOn is the single predicate that separates DARK from not-DARK. It is deliberately stricter than
// the others: transmission requires the master flag, the outbox lane flag AND its own flag, so no single
// mis-set variable can put financial bytes on a wire.
func (c Config) TransmitOn() bool {
	return c.MasterEnabled && c.OutboxEnabled && c.TransmitEnabled
}

// Dark reports whether the process is in DARK mode: no financial egress is permitted, at all, by anyone.
func (c Config) Dark() bool { return !c.TransmitOn() }

func (c Config) anyChildSet() bool {
	return c.PostingEnabled || c.OutboxEnabled || c.TransmitEnabled || c.ReviewEnabled
}

// Validate fails closed on an incoherent flag set.
func (c Config) Validate() error {
	if !c.MasterEnabled && c.anyChildSet() {
		return &Error{Code: ErrConfig, Msg: "phase4 surface flag enabled while " + EnvPhase4Master + " is OFF"}
	}
	if c.TransmitEnabled && !c.OutboxEnabled {
		return &Error{Code: ErrConfig, Msg: EnvPhase4Transmit + " enabled while " + EnvPhase4Outbox + " is OFF"}
	}
	return nil
}

// Getenv matches the Phase-3 loader signature.
type Getenv func(string) string

// LoadConfigFromEnv builds a Config from env. A malformed boolean is a startup failure, never a default.
func LoadConfigFromEnv(get Getenv) (Config, error) {
	var c Config
	for _, f := range []struct {
		name string
		dst  *bool
	}{
		{EnvPhase4Master, &c.MasterEnabled},
		{EnvPhase4Posting, &c.PostingEnabled},
		{EnvPhase4Outbox, &c.OutboxEnabled},
		{EnvPhase4Transmit, &c.TransmitEnabled},
		{EnvPhase4Review, &c.ReviewEnabled},
	} {
		v, err := parseBoolStrict(f.name, get(f.name))
		if err != nil {
			return Config{}, err
		}
		*f.dst = v
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// parseBoolStrict treats an empty value as OFF and anything unparseable as a startup error. "probably
// false" is not a state a financial flag is allowed to be in.
func parseBoolStrict(name, raw string) (bool, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(s)
	if err != nil {
		return false, &Error{Code: ErrConfig, Msg: name + " is not a boolean"}
	}
	return v, nil
}

// SafeFlagSummary is a log-safe description. It contains no secrets and no identifiers.
func (c Config) SafeFlagSummary() string {
	var b strings.Builder
	b.WriteString("phase4 master=")
	b.WriteString(strconv.FormatBool(c.MasterEnabled))
	b.WriteString(" posting=")
	b.WriteString(strconv.FormatBool(c.PostingEnabled))
	b.WriteString(" outbox=")
	b.WriteString(strconv.FormatBool(c.OutboxEnabled))
	b.WriteString(" transmit=")
	b.WriteString(strconv.FormatBool(c.TransmitEnabled))
	b.WriteString(" review=")
	b.WriteString(strconv.FormatBool(c.ReviewEnabled))
	b.WriteString(" dark=")
	b.WriteString(strconv.FormatBool(c.Dark()))
	return b.String()
}
