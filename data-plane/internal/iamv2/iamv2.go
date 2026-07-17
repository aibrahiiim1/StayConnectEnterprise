// Package iamv2 is the DARK Phase 1B application layer for the clean-slate IAM schema (iam_v2).
//
// It defines the credential-validation contracts, the normalized subject/principal, device and
// authentication-context models, deterministic typed errors, repository/transaction interfaces, a
// configuration model (master kill switch + per-method flags, all default OFF), redacted
// observability, and method adapters (voucher/account/OTP/social).
//
// DARK / production invariants (D1, D3, decision D10 scope):
//   - Every flag defaults OFF; nothing self-activates.
//   - When a method is not enabled, the Authenticator returns DecisionDisabled WITHOUT invoking any
//     repository method — so in production (flags OFF) there is NO iam_v2 read, write, or shadow
//     execution, and no rolled-back write attempt.
//   - All functional iam_v2 repository execution therefore happens ONLY in scratch/test, where the
//     flags are explicitly enabled and a scratch Repository is provided.
//   - Production must refuse the social "Stub" provider.
package iamv2

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Method is the Phase 1B credential method (subset of the auth_contexts CHECK domain).
type Method string

const (
	MethodVoucher Method = "VOUCHER"
	MethodAccount Method = "ACCOUNT"
	MethodOTP     Method = "OTP"
	MethodSocial  Method = "SOCIAL"
)

// Config is deployment-controlled (env/config only; no DB flag table/UI in Phase 1B). Every flag
// defaults OFF.
type Config struct {
	MasterEnabled   bool            // master kill switch; when false nothing in iam_v2 runs
	Methods         map[Method]bool // per-method enable flags (absent/false = OFF)
	AllowSocialStub bool            // production MUST leave this false (refuse the Stub provider)
}

// DefaultConfig returns the safe production default: everything OFF.
func DefaultConfig() Config {
	return Config{MasterEnabled: false, Methods: map[Method]bool{}, AllowSocialStub: false}
}

// Enabled reports whether a method may run (master on AND that method on).
func (c Config) Enabled(m Method) bool {
	return c.MasterEnabled && c.Methods[m]
}

// Validate checks the configuration at startup. It never enables anything; it only rejects an
// incoherent configuration (fail closed).
func (c Config) Validate() error {
	for m := range c.Methods {
		switch m {
		case MethodVoucher, MethodAccount, MethodOTP, MethodSocial:
		default:
			return &Error{Code: ErrConfig, Msg: "unknown method flag: " + string(m)}
		}
	}
	if !c.MasterEnabled {
		for m, on := range c.Methods {
			if on {
				// A per-method flag on while the master switch is off is a misconfiguration.
				return &Error{Code: ErrConfig, Msg: "method " + string(m) + " enabled but master switch is OFF"}
			}
		}
	}
	return nil
}

// Decision is the outcome classification.
type Decision string

const (
	DecisionAllow    Decision = "allow"
	DecisionDeny     Decision = "deny"
	DecisionDisabled Decision = "disabled" // method flag OFF; no repository was invoked
)

// Request is a credential-validation request. Secrets (Secret) are never logged.
type Request struct {
	Method   Method
	TenantID string
	SiteID   string
	// Method-specific inputs:
	Username     string // ACCOUNT
	Secret       string // ACCOUNT password / VOUCHER code / OTP code (never logged)
	FactorType   string // OTP: EMAIL|PHONE ; SOCIAL: SOCIAL_SUBJECT
	FactorIssuer string // SOCIAL issuer
	FactorValue  string // OTP verified email/phone ; SOCIAL subject
	Provider     string // SOCIAL provider name (Stub is refused in production)
	Device       DeviceContext
}

// DeviceContext is the device/network the request arrived on. MAC identifies a device, never a person.
type DeviceContext struct {
	MAC            string
	ApplianceID    string
	GuestNetworkID string
	IP             string
}

// Subject is the normalized resolved subject of a successful validation (exactly one is set).
type Subject struct {
	VoucherID      string
	GuestAccountID string
	PrincipalID    string
}

// Result is the outcome of Authenticate.
type Result struct {
	Decision      Decision
	Method        Method
	Subject       Subject
	DeviceID      string
	AuthContextID string // set when an auth_context was created (scratch/enabled path)
	Reason        string // deterministic, non-sensitive reason code
}

// Repository is the iam_v2 data-access boundary. In production it is NEVER invoked (flags OFF); a
// scratch implementation is supplied only in tests / enabled scratch runs.
type Repository interface {
	WithTx(ctx context.Context, fn func(Tx) error) error
}

// Tx is the transactional surface the adapters use.
type Tx interface {
	// ResolveVoucherByHMAC returns the voucher id + whether it is currently redeemable.
	ResolveVoucherByHMAC(ctx context.Context, tenantID, siteID string, codeHMAC []byte, now time.Time) (id string, redeemable bool, err error)
	// LookupAccount returns the account row needed for validation.
	LookupAccount(ctx context.Context, tenantID, username string) (id, passwordHash string, enabled bool, validFrom, validUntil *time.Time, lockedUntil *time.Time, err error)
	// ResolvePrincipalByIdentity finds or creates a principal for a verified factor identity.
	ResolvePrincipalByIdentity(ctx context.Context, tenantID, factorType, issuer, valueNorm string, now time.Time) (principalID string, err error)
	// UpsertDevice resolves a device by (tenant, mac) and records the network appearance.
	UpsertDevice(ctx context.Context, tenantID, siteID, applianceID, mac, guestNetworkID, ip string, now time.Time) (deviceID string, err error)
	// CreateAuthContext writes a one-time auth_context (method<->subject enforced by DB CHECKs).
	CreateAuthContext(ctx context.Context, spec AuthContextSpec) (id string, err error)
}

// AuthContextSpec is the one-time authentication context to create.
type AuthContextSpec struct {
	TenantID       string
	SiteID         string
	Method         Method
	Subject        Subject
	DeviceID       string
	GuestNetworkID string
	TTL            time.Duration
	Now            time.Time
}

// Clock is injectable.
type Clock func() time.Time

// Authenticator is the dark entry point. When a method is disabled it returns DecisionDisabled
// WITHOUT touching the repository — the core production-safety property.
type Authenticator struct {
	cfg   Config
	repo  Repository
	obs   Observer
	now   Clock
	ttl   time.Duration
	vhmac VoucherHMAC // computes the voucher code HMAC (scratch/enabled only)
}

// New builds an Authenticator. repo may be nil in production (it is never used while flags are OFF).
func New(cfg Config, repo Repository, obs Observer, opts ...Option) (*Authenticator, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	a := &Authenticator{cfg: cfg, repo: repo, obs: obs, now: time.Now, ttl: 10 * time.Minute}
	for _, o := range opts {
		o(a)
	}
	if obs == nil {
		a.obs = NopObserver{}
	}
	if cfg.MasterEnabled && repo == nil {
		return nil, &Error{Code: ErrConfig, Msg: "master enabled but no repository provided"}
	}
	return a, nil
}

// Option configures the Authenticator.
type Option func(*Authenticator)

// WithClock overrides the time source.
func WithClock(f Clock) Option { return func(a *Authenticator) { a.now = f } }

// WithTTL sets the auth_context TTL.
func WithTTL(d time.Duration) Option { return func(a *Authenticator) { a.ttl = d } }

// WithVoucherHMAC sets the voucher HMAC computer.
func WithVoucherHMAC(v VoucherHMAC) Option { return func(a *Authenticator) { a.vhmac = v } }

// VoucherHMAC computes the blind-index HMAC of a voucher code for a tenant/site.
type VoucherHMAC func(ctx context.Context, tenantID, siteID, code string) ([]byte, error)

// Authenticate validates a credential in the DARK model. With the method disabled it returns
// DecisionDisabled and NEVER calls the repository.
func (a *Authenticator) Authenticate(ctx context.Context, req Request) (Result, error) {
	// Production-safety gate: disabled method => no repository invocation at all.
	if !a.cfg.Enabled(req.Method) {
		a.obs.Event("iamv2.disabled", map[string]string{"method": string(req.Method)})
		return Result{Decision: DecisionDisabled, Method: req.Method, Reason: "method_disabled"}, nil
	}
	if a.repo == nil {
		return Result{}, &Error{Code: ErrConfig, Msg: "no repository"}
	}
	// Production must refuse the social Stub provider.
	if req.Method == MethodSocial && !a.cfg.AllowSocialStub && strings.EqualFold(strings.TrimSpace(req.Provider), "stub") {
		a.obs.Event("iamv2.social.stub_refused", nil)
		return Result{Decision: DecisionDeny, Method: MethodSocial, Reason: "social_stub_refused"}, ErrSocialStubRefusedErr
	}
	switch req.Method {
	case MethodVoucher:
		return a.authVoucher(ctx, req)
	case MethodAccount:
		return a.authAccount(ctx, req)
	case MethodOTP:
		return a.authOTPIdentity(ctx, req)
	case MethodSocial:
		return a.authSocialIdentity(ctx, req)
	default:
		return Result{}, &Error{Code: ErrConfig, Msg: "unknown method " + string(req.Method)}
	}
}

// ErrSocialStubRefusedErr is returned (alongside a deny Result) when production refuses the Stub.
var ErrSocialStubRefusedErr = errors.New("iamv2: social Stub provider refused in production")
