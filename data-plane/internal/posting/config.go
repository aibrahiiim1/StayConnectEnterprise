// Package posting is the financial execution core: postings, the P# allocator, the outbox lanes, PS
// construction, PA correlation and the UNKNOWN state. Every flag defaults OFF (DARK), and DARK cannot emit a
// byte to a PMS or payment provider (see transport.go).
package posting

import (
	"strconv"
	"strings"
)

// Financial flags are deployment env only (no DB table, UI or runtime toggle). A child ON while the master is
// OFF is a fail-closed startup error, as in internal/iamv2/pms_config.go.
const (
	EnvPhase4Master   = "STAYCONNECT_PHASE4_MASTER"
	EnvPhase4Posting  = "STAYCONNECT_PHASE4_POSTING"          // Posting domain: creation + gate
	EnvPhase4Outbox   = "STAYCONNECT_PHASE4_OUTBOX_WORKER"    // outbox worker lanes may claim work
	EnvPhase4Transmit = "STAYCONNECT_PHASE4_PMS_TRANSMIT"     // the ONLY flag that can permit PS bytes
	EnvPhase4Review   = "STAYCONNECT_PHASE4_FINANCIAL_REVIEW" // manual-review write surface
)

// Config's zero value is everything OFF.
type Config struct {
	MasterEnabled   bool
	PostingEnabled  bool
	OutboxEnabled   bool
	TransmitEnabled bool
	ReviewEnabled   bool
}

func DefaultConfig() Config { return Config{} }

// A surface is live only when BOTH the master flag and its own flag are ON.
func (c Config) PostingOn() bool { return c.MasterEnabled && c.PostingEnabled }
func (c Config) OutboxOn() bool  { return c.MasterEnabled && c.OutboxEnabled }
func (c Config) ReviewOn() bool  { return c.MasterEnabled && c.ReviewEnabled }

// TransmitOn separates DARK from not-DARK and needs master, outbox and its own flag, so no single mis-set variable can put financial bytes on a wire.
func (c Config) TransmitOn() bool {
	return c.MasterEnabled && c.OutboxEnabled && c.TransmitEnabled
}

func (c Config) Dark() bool { return !c.TransmitOn() }

func (c Config) anyChildSet() bool {
	return c.PostingEnabled || c.OutboxEnabled || c.TransmitEnabled || c.ReviewEnabled
}

func (c Config) Validate() error {
	if !c.MasterEnabled && c.anyChildSet() {
		return &Error{Code: ErrConfig, Msg: "phase4 surface flag enabled while " + EnvPhase4Master + " is OFF"}
	}
	if c.TransmitEnabled && !c.OutboxEnabled {
		return &Error{Code: ErrConfig, Msg: EnvPhase4Transmit + " enabled while " + EnvPhase4Outbox + " is OFF"}
	}
	return nil
}

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

// parseBoolStrict treats empty as OFF and anything unparseable as a startup error.
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
