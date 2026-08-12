// Package payment is the Phase-4 online-payment runtime: durable intent, provider execution behind a
// DARK guard, callback application, and the handoff to the existing Phase-2 atomic entitlement grant.
//
// It is DARK. The delivered configuration enables nothing, there is no networking anywhere in this
// package, and the only providers in the tree are deterministic in-process doubles.
//
// WHAT THIS PACKAGE DOES NOT DECIDE. Every money-shaped rule lives in the database: the amount comes from
// the pinned Purchase, the status machine is a trigger, the cumulative refund bound is an advisory-locked
// sum, duplicate callbacks are a unique index, and the settlement moves in the same transaction as the
// payment. This package's job is to call those in the right ORDER and never to substitute its own answer.
package payment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

// Code is a deterministic, non-sensitive classification.
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

// Error is a deterministic typed error. Msg never carries card data, credentials or guest PII.
type Error struct {
	Code Code
	Msg  string
}

func (e *Error) Error() string { return "payment: " + string(e.Code) + ": " + e.Msg }

// CodeOf returns the classification of an error, or "" if it is not an *Error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func fail(c Code, msg string) error { return &Error{Code: c, Msg: msg} }

// ---------------------------------------------------------------- flags

const (
	EnvPhase4Master   = "STAYCONNECT_PHASE4_MASTER"
	EnvPhase4Payment  = "STAYCONNECT_PHASE4_PAYMENT"          // the payment domain may create intents
	EnvPhase4Provider = "STAYCONNECT_PHASE4_PAYMENT_PROVIDER" // the ONLY flag that can permit provider calls
)

// Config is the payment flag posture. The zero value is the delivered state: everything OFF.
type Config struct {
	MasterEnabled   bool
	PaymentEnabled  bool
	ProviderEnabled bool
}

// DomainOn reports whether payment intents may be created at all.
func (c Config) DomainOn() bool { return c.MasterEnabled && c.PaymentEnabled }

// ProviderOn is the single predicate separating DARK from not-DARK. It is deliberately stricter than the
// others: contacting a provider needs the master flag, the domain flag AND its own.
func (c Config) ProviderOn() bool { return c.MasterEnabled && c.PaymentEnabled && c.ProviderEnabled }

// Dark reports whether provider egress is forbidden.
func (c Config) Dark() bool { return !c.ProviderOn() }

// Getenv matches the loader signature used elsewhere in the tree.
type Getenv func(string) string

// LoadConfigFromEnv builds a Config. A malformed boolean is a startup failure, never a default, and a
// child flag set while the master is OFF is refused.
func LoadConfigFromEnv(get Getenv) (Config, error) {
	var c Config
	for _, f := range []struct {
		name string
		dst  *bool
	}{{EnvPhase4Master, &c.MasterEnabled}, {EnvPhase4Payment, &c.PaymentEnabled}, {EnvPhase4Provider, &c.ProviderEnabled}} {
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
	return c, nil
}

// SafeFlagSummary is log-safe: no secrets, no identifiers.
func (c Config) SafeFlagSummary() string {
	return "phase4 payment master=" + strconv.FormatBool(c.MasterEnabled) +
		" domain=" + strconv.FormatBool(c.PaymentEnabled) +
		" provider=" + strconv.FormatBool(c.ProviderEnabled) +
		" dark=" + strconv.FormatBool(c.Dark())
}

// ---------------------------------------------------------------- the provider capability contract

// Outcome is what a provider says happened. The three-way split is the whole safety model.
type Outcome string

const (
	// OutcomeNotSent means the request provably never reached the provider. Safe to abandon; no money moved.
	OutcomeNotSent Outcome = "NOT_SENT"
	// OutcomeCaptured means the provider conclusively took the money.
	OutcomeCaptured Outcome = "CAPTURED"
	// OutcomeDeclined means the provider conclusively did not take the money.
	OutcomeDeclined Outcome = "DECLINED"
	// OutcomeUnknown means the request MAY have moved money and the result cannot be determined. It is
	// never retried, and it routes the settlement to manual review.
	OutcomeUnknown Outcome = "UNKNOWN"
)

// Request is what a provider adapter is given. ClientRef is StayConnect's DURABLE LOCAL reference, created
// before this call and guaranteed to exist afterwards whatever happens.
type Request struct {
	ClientRef       string
	MerchantAccount string
	AmountMinor     int64
	Currency        string
	Exponent        int16
	Kind            string // CHARGE | REFUND | CHARGEBACK
	ParentRef       string // the parent's ClientRef, for a refund
}

// Result is a provider adapter's answer.
type Result struct {
	Outcome Outcome
	// ProviderTxnRef is the provider's own reference. Required on a conclusive outcome.
	ProviderTxnRef string
	// ReasonCode is a short non-sensitive provider code, recorded as bounded callback evidence.
	ReasonCode string
}

// Provider is the capability contract an adapter must satisfy.
//
// THIS IS A CAPABILITY CONTRACT, NOT A CLAIM ABOUT ANY REAL PROVIDER. The correlation model requires an
// adapter that can carry StayConnect's ClientRef to the provider AND return it on every subsequent
// notification about that transaction. No real provider has been integrated or verified here, and naming
// one that "supports" this would be asserting something this milestone has not tested. An adapter that
// cannot demonstrate the capability must fail closed at construction rather than degrade to guessing.
type Provider interface {
	// Name is a short stable identifier stored as payment_transactions.provider.
	Name() string
	// SupportsClientReference reports whether this adapter can carry and return the client reference. An
	// adapter answering false cannot be used: correlation would have to be guessed.
	SupportsClientReference() bool
	// Execute performs one money-moving request. It must classify its own failure honestly: only a failure
	// that PROVES nothing was sent may be OutcomeNotSent.
	Execute(ctx context.Context, r Request) (Result, error)
}

// newClientRef mints a durable local reference. It is created BEFORE the intent row is written and is the
// idempotency root the whole correlation model rests on.
func newClientRef() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fail(ErrRepo, "could not generate a durable client reference")
	}
	return "sc_" + hex.EncodeToString(b[:]), nil
}

// ---------------------------------------------------------------- the authenticated notification contract

// RawNotification is a provider delivery exactly as it arrived, before anybody has decided to believe it.
//
// It is bytes and transport headers, nothing more. It is deliberately not parsed into fields before the
// adapter sees it, because most provider signature schemes sign the RAW body: re-serialising it first is
// how signature verification quietly starts passing on modified payloads.
type RawNotification struct {
	Body    []byte
	Headers map[string]string
}

// ParsedNotification is what an adapter returns after it has AUTHENTICATED a delivery.
//
// Note what it cannot express: there is no "trusted" flag and no way to name an internal transaction id, a
// tenant, a site, a settlement or an amount. An adapter reports what the authenticated delivery said about
// ITS OWN transaction; every question of whose money that is gets answered from durable local state.
type ParsedNotification struct {
	// ClientRef is StayConnect's own reference, echoed back by the provider. It is the correlation handle.
	ClientRef string
	// ProviderEventID is the provider's identifier for this delivery, and the deduplication key.
	ProviderEventID string
	EventType       string
	Outcome         Outcome
	ProviderTxnRef  string
	ReasonCode      string
}

// NotificationAuthenticator is the capability an adapter must have before ANY out-of-band outcome from it
// can be believed.
//
// It is a separate interface from Provider on purpose. An adapter that can send a charge but cannot verify
// a webhook signature is not partially usable for notifications -- it is unusable for them, and the type
// system should say so rather than leaving it to a runtime check somebody may forget.
type NotificationAuthenticator interface {
	// AuthenticateNotification verifies the delivery against the provider's signing scheme and returns its
	// parsed contents. It MUST return an error unless the delivery is cryptographically attributable to the
	// provider; "looks well-formed" is not authentication.
	AuthenticateNotification(ctx context.Context, raw RawNotification) (ParsedNotification, error)
}
