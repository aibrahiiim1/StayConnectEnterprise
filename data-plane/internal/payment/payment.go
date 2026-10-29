// Package payment is the online-payment runtime: durable intent, provider execution behind a DARK guard, callback
// application and handoff to the atomic entitlement grant.
// Money-shaped rules live in the database (pinned amount, status-machine trigger, advisory-locked refund bound,
// unique-index dedupe); this package only calls them in the right order.
package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

type Code string

const (
	ErrConfig          Code = "config"
	ErrRepo            Code = "repository"
	ErrDarkNoEgress    Code = "dark_no_egress"
	ErrNotExecutable   Code = "not_executable"
	ErrUncorrelated    Code = "callback_uncorrelated"
	ErrRefConflict     Code = "provider_reference_conflict"
	ErrProviderUnknown Code = "provider_outcome_unknown"
	ErrGrant           Code = "entitlement_grant"
	ErrUntrustedInput  Code = "untrusted_input"
	ErrNoAccount       Code = "no_configured_account"
	ErrUntrusted       Code = "untrusted_notification"
	ErrRecoveryHeld    Code = "financial_recovery_mode"
)

type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string { return "payment: " + string(e.Code) + ": " + e.Msg }

func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func fail(c Code, msg string) error { return &Error{Code: c, Msg: msg} }

const (
	EnvPhase4Master   = "STAYCONNECT_PHASE4_MASTER"
	EnvPhase4Payment  = "STAYCONNECT_PHASE4_PAYMENT"
	EnvPhase4Provider = "STAYCONNECT_PHASE4_PAYMENT_PROVIDER" // the ONLY flag that can permit provider calls
	// EnvPhase4Recheck switches on the slow read-only re-query of failed and unknown card charges for a late capture (AD-43).
	// Off by default; it permits no provider call unless the provider flag is also on.
	EnvPhase4Recheck = "STAYCONNECT_PHASE4_PAYMENT_RECHECK"

	// EnvOutcomeDSN is a separate credential from execution: with one, a single theft could start a payment and declare
	// it captured.
	EnvOutcomeDSN = "STAYCONNECT_PHASE4_PAYMENT_OUTCOME_DSN"
)

type Config struct {
	MasterEnabled   bool
	PaymentEnabled  bool
	ProviderEnabled bool
	// RecheckEnabled: the late-capture re-query may run. Read-only toward the provider, but a new traffic pattern, so it has its own switch.
	RecheckEnabled bool
}

func (c Config) DomainOn() bool { return c.MasterEnabled && c.PaymentEnabled }

// ProviderOn is stricter than the others: provider calls need the master flag, the domain flag and its own.
func (c Config) ProviderOn() bool { return c.MasterEnabled && c.PaymentEnabled && c.ProviderEnabled }

func (c Config) Dark() bool { return !c.ProviderOn() }

// RecheckOn: the late-capture re-query needs the provider to be on AND its own switch.
func (c Config) RecheckOn() bool { return c.ProviderOn() && c.RecheckEnabled }

type Getenv func(string) string

// A malformed boolean is a startup failure, never a default; a child flag set while the master is OFF is refused.
func LoadConfigFromEnv(get Getenv) (Config, error) {
	var c Config
	for _, f := range []struct {
		name string
		dst  *bool
	}{{EnvPhase4Master, &c.MasterEnabled}, {EnvPhase4Payment, &c.PaymentEnabled}, {EnvPhase4Provider, &c.ProviderEnabled}, {EnvPhase4Recheck, &c.RecheckEnabled}} {
		raw := strings.TrimSpace(get(f.name))
		if raw == "" {
			continue
		}
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fail(ErrConfig, f.name+" is not a boolean")
		}
		*f.dst = v
	}
	if !c.MasterEnabled && (c.PaymentEnabled || c.ProviderEnabled) {
		return Config{}, fail(ErrConfig, "a phase-4 payment flag is enabled while "+EnvPhase4Master+" is OFF")
	}
	if c.ProviderEnabled && !c.PaymentEnabled {
		return Config{}, fail(ErrConfig, EnvPhase4Provider+" is enabled while "+EnvPhase4Payment+" is OFF")
	}
	if c.RecheckEnabled && !c.ProviderEnabled {
		return Config{}, fail(ErrConfig, EnvPhase4Recheck+" is enabled while "+EnvPhase4Provider+" is OFF")
	}
	return c, nil
}

func (c Config) SafeFlagSummary() string {
	return "phase4 payment master=" + strconv.FormatBool(c.MasterEnabled) +
		" domain=" + strconv.FormatBool(c.PaymentEnabled) +
		" provider=" + strconv.FormatBool(c.ProviderEnabled) +
		" recheck=" + strconv.FormatBool(c.RecheckEnabled) +
		" dark=" + strconv.FormatBool(c.Dark())
}

type Outcome string

const (
	// OutcomeNotSent: the request provably never reached the provider; no money moved.
	OutcomeNotSent  Outcome = "NOT_SENT"
	OutcomeCaptured Outcome = "CAPTURED"
	OutcomeDeclined Outcome = "DECLINED"
	// OutcomeUnknown: the request MAY have moved money; never retried, routed to manual review.
	OutcomeUnknown Outcome = "UNKNOWN"
)

// ClientRef is the durable local reference, created before the call.
type Request struct {
	ClientRef       string
	MerchantAccount string
	AmountMinor     int64
	Currency        string
	Exponent        int16
	Kind            string
	ParentRef       string
}

type Result struct {
	Outcome        Outcome
	ProviderTxnRef string
	ReasonCode     string
}

// Provider is a capability contract: an adapter must carry ClientRef to the provider and get it back on every
// notification, and must fail closed at construction if it cannot rather than guess.
type Provider interface {
	Name() string
	// SupportsClientReference false makes the adapter unusable: correlation would be guessed.
	SupportsClientReference() bool
	// Execute must classify its own failure honestly: only a failure that PROVES nothing was sent may be
	// OutcomeNotSent.
	Execute(ctx context.Context, r Request) (Result, error)
}

// newClientRef is created before the intent row and is the idempotency root of the correlation model.
func newClientRef() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fail(ErrRepo, "could not generate a durable client reference")
	}
	return "sc_" + hex.EncodeToString(b[:]), nil
}

// RawNotification is bytes and headers, not parsed: providers sign the raw body, and re-serialising first lets
// verification pass on modified payloads.
type RawNotification struct {
	Body    []byte
	Headers map[string]string
}

// ParsedNotification has no "trusted" flag and cannot name an internal transaction, tenant, site, settlement or amount;
// those come from durable local state.
type ParsedNotification struct {
	ClientRef       string
	ProviderEventID string
	EventType       string
	Outcome         Outcome
	ProviderTxnRef  string
	ReasonCode      string
}

// NotificationAuthenticator is separate from Provider on purpose: an adapter that cannot verify a webhook signature is
// unusable for notifications, and the type system says so.
type NotificationAuthenticator interface {
	// AuthenticateNotification MUST return an error unless the delivery is cryptographically attributable to the
	// provider.
	AuthenticateNotification(ctx context.Context, raw RawNotification) (ParsedNotification, error)
}
