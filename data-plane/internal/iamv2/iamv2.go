// Package iamv2 is the application layer over the iam_v2 schema: credential validation, auth contexts,
// commerce and grace.
package iamv2

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Method string

const (
	MethodVoucher Method = "VOUCHER"
	MethodAccount Method = "ACCOUNT"
	MethodOTP     Method = "OTP"
	MethodSocial  Method = "SOCIAL"
)

type Config struct {
	MasterEnabled   bool            // master kill switch; when false nothing in iam_v2 runs
	Methods         map[Method]bool // per-method enable flags (absent/false = OFF)
	AllowSocialStub bool            // production MUST leave this false (refuse the Stub provider)
}

func DefaultConfig() Config {
	return Config{MasterEnabled: false, Methods: map[Method]bool{}, AllowSocialStub: false}
}

func (c Config) Enabled(m Method) bool {
	return c.MasterEnabled && c.Methods[m]
}

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
				return &Error{Code: ErrConfig, Msg: "method " + string(m) + " enabled but master switch is OFF"}
			}
		}
	}
	return nil
}

type Decision string

const (
	DecisionAllow    Decision = "allow"
	DecisionDeny     Decision = "deny"
	DecisionDisabled Decision = "disabled" // method flag OFF; no repository was invoked
)

// Request is a credential-validation request; Secret is never logged.
type Request struct {
	Method       Method
	TenantID     string
	SiteID       string
	Username     string // ACCOUNT
	Secret       string // ACCOUNT password / VOUCHER code / OTP code (never logged)
	FactorType   string // OTP: EMAIL|PHONE ; SOCIAL: SOCIAL_SUBJECT
	FactorIssuer string // SOCIAL issuer
	FactorValue  string // OTP verified email/phone ; SOCIAL subject (the provider's sub)
	Provider     string // SOCIAL provider name (Stub is refused in production)
	// Verified claims about the primary factor (hd, tid, email, source); never populated from anything the
	// browser sent.
	FactorAttrs      map[string]string
	SecondaryFactors []FactorClaim
	// Set only by the remembered-device path, where scd has already verified a device-bound resume credential;
	// no factor is re-proven.
	ResumedPrincipalID string
	// Credential names how the Client proved themselves (EMAIL_OTP, SOCIAL_GOOGLE, VOUCHER, ...) for the
	// operator; Method says which authority decided.
	Credential string
	// Set by scd when staff read the OTP to the client. Such a sign-in may create a new Client
	// (staff_assisted) but never signs in to an existing one.
	StaffAssisted bool
	Device        DeviceContext
}

// DeviceContext is the device/network the request arrived on; a MAC identifies a device, never a person.
type DeviceContext struct {
	MAC            string
	ApplianceID    string
	GuestNetworkID string
	IP             string
}

type Subject struct {
	VoucherID      string
	GuestAccountID string
	PrincipalID    string
}

type Result struct {
	Decision        Decision
	Method          Method
	Subject         Subject
	DeviceID        string
	AuthContextID   string // set when an auth_context was created (scratch/enabled path)
	Reason          string // deterministic, non-sensitive reason code
	ClientGroupID   string
	ClientGroupName string
	// The masked primary factor for guest pages; never the full value.
	IdentityLabel     string
	IdentityLinked    []string
	IdentityConflicts []string
}

type Repository interface {
	WithTx(ctx context.Context, fn func(Tx) error) error
}

type Tx interface {
	ResolveVoucherByHMAC(ctx context.Context, tenantID, siteID string, codeHMAC []byte, now time.Time) (id string, redeemable bool, err error)
	// An account on a different site returns "" and is treated as nonexistent.
	LookupAccount(ctx context.Context, tenantID, siteID, username string) (id, passwordHash string, enabled bool, validFrom, validUntil *time.Time, lockedUntil *time.Time, err error)
	// Deterministic; never merges two Clients.
	ResolvePrincipalByFactors(ctx context.Context, tenantID string, primary FactorClaim, secondary []FactorClaim, now time.Time) (PrincipalResolution, error)
	LoadPrincipalFactors(ctx context.Context, tenantID, principalID string) ([]FactorClaim, error)
	LoadClientGroups(ctx context.Context, tenantID, siteID string) ([]ClientGroup, error)
	UpsertDevice(ctx context.Context, tenantID, siteID, applianceID, mac, guestNetworkID, ip string, now time.Time) (deviceID string, err error)
	CreateAuthContext(ctx context.Context, spec AuthContextSpec) (id string, err error)
	// Atomically consumes a one-time auth_context pinned to tenant/site/method/device/guest-network; exactly
	// one concurrent winner.
	ConsumeAuthContext(ctx context.Context, req ConsumeAuthContextRequest) (ConsumedContext, error)
}

type AuthContextSpec struct {
	TenantID       string
	SiteID         string
	Method         Method
	Subject        Subject
	DeviceID       string
	GuestNetworkID string
	TTL            time.Duration
	Now            time.Time
	// Pinned so eligibility reads a recorded decision rather than re-deriving one.
	ClientGroupID       string
	ClientGroupEvidence map[string]any
	Credential          string
}

func (s Subject) subjectFor(m Method) (string, bool) {
	switch m {
	case MethodVoucher:
		return s.VoucherID, s.VoucherID != "" && s.GuestAccountID == "" && s.PrincipalID == ""
	case MethodAccount:
		return s.GuestAccountID, s.GuestAccountID != "" && s.VoucherID == "" && s.PrincipalID == ""
	case MethodOTP, MethodSocial:
		return s.PrincipalID, s.PrincipalID != "" && s.VoucherID == "" && s.GuestAccountID == ""
	}
	return "", false
}

// Validates every pin before any SQL so a malformed spec yields ErrInvalidInput rather than a database
// constraint error.
func (s AuthContextSpec) validate() error {
	if s.TenantID == "" {
		return &Error{Code: ErrInvalidInput, Msg: "auth_context: missing tenant"}
	}
	if s.SiteID == "" {
		return &Error{Code: ErrInvalidInput, Msg: "auth_context: missing site"}
	}
	if s.DeviceID == "" {
		return &Error{Code: ErrInvalidInput, Msg: "auth_context: missing device"}
	}
	if s.GuestNetworkID == "" {
		return &Error{Code: ErrInvalidInput, Msg: "auth_context: missing guest network"}
	}
	if _, ok := s.Subject.subjectFor(s.Method); !ok {
		return &Error{Code: ErrInvalidInput, Msg: "auth_context: subject not compatible with method"}
	}
	if s.TTL <= 0 {
		return &Error{Code: ErrInvalidInput, Msg: "auth_context: non-positive TTL"}
	}
	return nil
}

type Clock func() time.Time

// When a method is disabled it returns DecisionDisabled without touching the repository.
type Authenticator struct {
	cfg      Config
	repo     Repository
	obs      Observer
	now      Clock
	ttl      time.Duration
	vhmac    VoucherHMAC           // computes the voucher code HMAC (scratch/enabled only)
	vhmacAll VoucherHMACCandidates // one index per key generation; preferred when set (rotation)
}

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

type Option func(*Authenticator)

func WithClock(f Clock) Option { return func(a *Authenticator) { a.now = f } }

func WithTTL(d time.Duration) Option { return func(a *Authenticator) { a.ttl = d } }

func WithVoucherHMAC(v VoucherHMAC) Option { return func(a *Authenticator) { a.vhmac = v } }

// Supplies every blind index a submitted code could have, newest key generation first. A voucher's code_hmac
// is computed under the generation it pins, so after a rotation an index under the active key alone would
// orphan unredeemed vouchers. Takes precedence over WithVoucherHMAC.
func WithVoucherHMACCandidates(v VoucherHMACCandidates) Option {
	return func(a *Authenticator) { a.vhmacAll = v }
}

type VoucherHMACCandidates func(ctx context.Context, tenantID, siteID, code string) ([][]byte, error)

type VoucherHMAC func(ctx context.Context, tenantID, siteID, code string) ([]byte, error)

func (a *Authenticator) Authenticate(ctx context.Context, req Request) (Result, error) {
	// Disabled method means no repository invocation at all.
	if !a.cfg.Enabled(req.Method) {
		a.obs.Event("iamv2.disabled", map[string]string{"method": string(req.Method)})
		return Result{Decision: DecisionDisabled, Method: req.Method, Reason: "method_disabled"}, nil
	}
	if a.repo == nil {
		return Result{}, &Error{Code: ErrConfig, Msg: "no repository"}
	}
	if req.Method == MethodSocial && !a.cfg.AllowSocialStub && strings.EqualFold(strings.TrimSpace(req.Provider), "stub") {
		a.obs.Event("iamv2.social.stub_refused", nil)
		return Result{Decision: DecisionDeny, Method: MethodSocial, Reason: "social_stub_refused"}, ErrSocialStubRefusedErr
	}
	switch req.Method {
	case MethodVoucher:
		return a.authVoucher(ctx, req)
	case MethodAccount:
		return a.authAccount(ctx, req)
	case MethodOTP, MethodSocial:
		if req.ResumedPrincipalID != "" {
			return a.authResumedIdentity(ctx, req)
		}
		if req.Method == MethodOTP {
			return a.authOTPIdentity(ctx, req)
		}
		return a.authSocialIdentity(ctx, req)
	default:
		return Result{}, &Error{Code: ErrConfig, Msg: "unknown method " + string(req.Method)}
	}
}

var ErrSocialStubRefusedErr = errors.New("iamv2: social Stub provider refused in production")
