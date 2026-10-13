package iamv2

import (
	"context"
	"time"
)

// CommerceEngine is the DARK Phase-2 commercial-packages entry point (offer quotes + free purchases).
// When the Phase-2 master flag is OFF it holds a nil repository, issues zero SQL, and every method
// returns a disabled result WITHOUT touching a repository — the appliance keeps legacy behavior.
type CommerceEngine struct {
	cfg  CommerceConfig
	repo CommerceRepository
	obs  Observer
	now  func() time.Time
	ttl  time.Duration // offer-quote TTL (5 min unless overridden)
	// aggregateOnlineTime is the Phase-6 capability, OFF by default. It gates NEW acquisition of that time
	// mode through this engine; see TimeModeAcquirable, which is the single rule every acquisition path
	// shares.
	aggregateOnlineTime bool
	// methodGate reports which optional acquisition modules are effective at this site (acquisition.go).
	methodGate MethodGate
}

// NewCommerceEngine builds the engine. repo MUST be nil while the master flag is OFF (dark) and MUST be
// non-nil when enabled (fail closed). An incoherent flag set is rejected.
func NewCommerceEngine(cfg CommerceConfig, repo CommerceRepository, obs Observer, opts ...CommerceOption) (*CommerceEngine, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Enabled() && repo == nil {
		return nil, &Error{Code: ErrConfig, Msg: "phase2 enabled but no commerce repository provided"}
	}
	if obs == nil {
		obs = NopObserver{}
	}
	e := &CommerceEngine{cfg: cfg, repo: repo, obs: obs, now: time.Now, ttl: 5 * time.Minute}
	for _, o := range opts {
		o(e)
	}
	return e, nil
}

// CommerceOption configures the engine (tests).
type CommerceOption func(*CommerceEngine)

// WithCommerceClock overrides the time source.
func WithCommerceClock(f func() time.Time) CommerceOption {
	return func(e *CommerceEngine) { e.now = f }
}

// WithQuoteTTL overrides the offer-quote TTL.
func WithQuoteTTL(d time.Duration) CommerceOption { return func(e *CommerceEngine) { e.ttl = d } }

// WithAggregateOnlineTime declares whether this process may create NEW acquisitions whose effective time
// mode is AGGREGATE_ONLINE_TIME. Default OFF. It must be derived from the SAME Phase-6 flag that turns on
// acctd's accrual tick: a runtime that can create the entitlement but not account for it is exactly the
// state this gate exists to prevent.
func WithAggregateOnlineTime(on bool) CommerceOption {
	return func(e *CommerceEngine) { e.aggregateOnlineTime = on }
}

// ---- transaction-scoped commerce contract (the WHOLE grant runs on one pgx.Tx) ----
//
// Auth-context consumption happens INSIDE the commerce transaction (never via the standalone
// SessionEngine.ConsumeAuthContext), so a consumed context can never be left without a Purchase.

// CommerceRepository is the Phase-2 data boundary. In the DARK deployment it is nil and never invoked.
type CommerceRepository interface {
	WithTx(ctx context.Context, fn func(CommerceTx) error) error
}

// CommerceTx is the transactional surface. Reads for CreateQuote and the full grant for
// ConfirmFreePurchase run on the same transaction.
type CommerceTx interface {
	// --- CreateQuote reads (no consumption) ---
	LoadAuthContext(ctx context.Context, tenantID, siteID, authContextID string) (AuthContextRow, error)
	ResolveActivePackageRevision(ctx context.Context, tenantID, siteID, packageID string) (PackageRevisionRow, error)
	ListActivePackageRevisions(ctx context.Context, tenantID, siteID string) ([]PackageRevisionRow, error)
	LoadPlanRevision(ctx context.Context, tenantID, siteID, planRevisionID string) (PlanRevisionRow, error)
	LoadEligibilityRules(ctx context.Context, packageRevisionID string) ([]EligibilityRule, error)
	// VoucherPinnedPackageRevision returns the package revision a voucher was PRINTED against.
	//
	// iam_v2.vouchers.package_revision_id has been NOT NULL and pinned at issuance since mg3, and the
	// issuance path states why: republishing a package must not retroactively change what an
	// already-printed card is worth. Nothing read it back. So the offer path had nothing to narrow itself
	// with, and a card printed for one free tier could be redeemed against another. Migration 0088 makes
	// that refusal an invariant in the grant kernel; this read is what stops a guest ever being offered
	// the choice that would be refused.
	VoucherPinnedPackageRevision(ctx context.Context, tenantID, siteID, voucherID string) (string, error)
	LoadGrantTiers(ctx context.Context, packageRevisionID string) ([]GrantTier, error)
	// GuestNetworkAncestors returns the client networks the device's network is the continuation of, from
	// iam_v2.guest_network_lineage (0106).
	//
	// WHY IT EXISTS. A package revision is immutable and names client networks by id. Replacing a client
	// network — a VLAN id, a port, a subnet — gives it a NEW id, so every SITE_NETWORK rule in every EXISTING
	// revision stopped matching the guests on that very network the moment the cable moved. For the CURRENT
	// revision a forward republish fixes it; for a PINNED one it cannot. An unused printed voucher redeems its
	// pinned revision directly and deliberately, so a cabling change made valid cards unredeemable with no
	// explanation anyone could see. Lineage is what makes the successor the same LOGICAL network.
	//
	// Empty is the normal answer. A read failure is an error, never an empty slice: silently losing the lineage
	// would silently narrow eligibility, which is the defect this closes.
	GuestNetworkAncestors(ctx context.Context, tenantID, siteID, guestNetworkID string) ([]string, error)
	// HasPriorPurchase answers whether the subject (and, when the policy says so, the device) already acquired
	// the PACKAGE -- any revision of it -- within the policy window. Keyed on the package so a republish never
	// resets a free allowance (contract §5).
	HasPriorPurchase(ctx context.Context, tenantID, siteID string, q PriorPurchaseQuery) (bool, error)
	InsertOfferQuote(ctx context.Context, q OfferQuoteSpec) (string, error)

	// --- ConfirmFreePurchase (deterministic lock order) ---
	LockOfferQuoteForUpdate(ctx context.Context, tenantID, siteID, quoteID string) (OfferQuoteRow, error)
	LockAuthContextForUpdate(ctx context.Context, tenantID, siteID, authContextID string) (AuthContextRow, error)
	AcquireSubjectLock(ctx context.Context, tenantID, siteID string, subj CommerceSubject) error
	ConsumeOfferQuote(ctx context.Context, quoteID string, now time.Time) (bool, error)
	ConsumeAuthContextByID(ctx context.Context, authContextID string, now time.Time) (bool, error)
	InsertPurchase(ctx context.Context, p PurchaseSpec) (string, error)
	InsertSettlement(ctx context.Context, tenantID, siteID, purchaseID string) error
	TerminateLiveEntitlementForSubject(ctx context.Context, tenantID, siteID string, subj CommerceSubject) (supersededID string, err error)
	InsertEntitlement(ctx context.Context, e EntitlementSpec) (string, error)

	// GrantQuotedEntitlement is the FREE grant entry point: one call into the shared kernel that both the
	// free and the paid path use (migration 0024). It returns the entitlement and whatever it superseded.
	GrantQuotedEntitlement(ctx context.Context, tenantID, siteID, purchaseID string) (string, string, error)
	MarkPurchaseGranted(ctx context.Context, purchaseID string) error

	// --- Acquisition (acquisition.go / acquisition_flow.go) ---
	// LoadPackageRevisionByID resolves one revision by id, current or not: a voucher is honoured against the
	// immutable revision it was printed for, whatever the package's current revision or active flag.
	LoadPackageRevisionByID(ctx context.Context, tenantID, siteID, packageRevisionID string) (PackageRevisionRow, error)
	// InsertPurchaseAs writes a purchase with an explicit trigger and initial state.
	InsertPurchaseAs(ctx context.Context, p PurchaseSpec, trigger, state string) (string, error)
	// InsertSettlementAs writes a settlement with an explicit method and first status (the DB birth rule
	// refuses incoherent pairs).
	InsertSettlementAs(ctx context.Context, tenantID, siteID, purchaseID, method, status string) (string, error)
	// GrantVoucherEntitlement is the VOUCHER grant entry point (migration 0095).
	GrantVoucherEntitlement(ctx context.Context, tenantID, siteID, purchaseID string) (string, string, error)

	// --- GrantSettledPurchase (money already moved; see commerce_settled_grant.go) ---
	// A READ that resolves everything the grant needs from rows the purchase already points at, so the
	// paid entry point pins exactly what the free one pins and a caller supplies nothing.
	LoadSettledPurchaseGrant(ctx context.Context, tenantID, siteID, purchaseID string) (SettledPurchaseGrant, error)
}

// CommerceSubject is the non-PMS authenticated subject a free purchase is pinned to (exactly one id).
type CommerceSubject struct {
	Kind        SubjectKind
	VoucherID   string
	AccountID   string
	PrincipalID string
	// AnonymousID is the opaque anonymous access subject of open package selection. It is never a MAC.
	AnonymousID string
	Method      Method
	// DeviceID is the device the auth context was issued to. It is NOT a subject: it is what free-package
	// limits for an anonymous subject are evaluated against, so a new anonymous subject never resets them.
	DeviceID string
}

// AuthContextRow is the loaded auth_context (never mutated by CreateQuote).
type AuthContextRow struct {
	ID             string
	TenantID       string
	SiteID         string
	Method         Method
	Subject        CommerceSubject
	DeviceID       string
	GuestNetworkID string
	ExpiresAt      time.Time
	Consumed       bool
	StayID         string // non-empty only for PMS (unused in Phase 2)
	// ClientGroupID is the effective Client Group pinned at sign-in ("" = Public).
	ClientGroupID string
}

// PriorPurchaseQuery is one history question: did this subject (or this device) get this package before?
type PriorPurchaseQuery struct {
	PackageID string
	Subject   CommerceSubject
	Policy    PriorPurchasePolicy
}

// PackageRevisionRow is the resolved active/published immutable package revision.
type PackageRevisionRow struct {
	ID                 string
	PackageID          string
	PlanRevisionID     string
	PackageType        string
	PriceMinor         int64
	Currency           string
	CurrencyExponent   int
	SettlementMethods  []string
	VisibleFrom        *time.Time
	VisibleUntil       *time.Time
	PackageActive      bool
	IsCurrent          bool // this revision is the package's current_revision
	TimeAccountingMode string
	Display            map[string]any
	DurationPolicy     map[string]any
}

// PlanRevisionRow carries the grant parameters snapshotted into a quote.
type PlanRevisionRow struct {
	ID                   string
	DownKbps             int
	UpKbps               int
	MaxConcurrentDevices int
	TimeQuotaSeconds     int64
	DataQuotaBytes       int64
	TimeAccountingMode   string
}

// OfferQuoteSpec / OfferQuoteRow / PurchaseSpec / EntitlementSpec are the write shapes.
type OfferQuoteSpec struct {
	TenantID, SiteID  string
	AuthContextID     string
	PackageRevisionID string
	PriceMinor        int64
	Currency          string
	CurrencyExponent  int
	GrantSnapshot     GrantSnapshot
	ExpiresAt         time.Time
	Now               time.Time
}

// OfferQuoteRow carries EVERY money/settlement/tax pin so ConfirmFreePurchase can re-validate the quote
// as a Phase-2 free quote before consuming anything.
type OfferQuoteRow struct {
	ID                  string
	TenantID, SiteID    string
	AuthContextID       string
	PackageRevisionID   string
	PriceMinor          int64
	Currency            string
	CurrencyExponent    int
	PMSInterfaceID      *string
	SettlementMappingID *string
	TaxCode             *string
	TaxRateBP           *int
	TaxAmountMinor      *int64
	GrantSnapshot       GrantSnapshot
	ExpiresAt           time.Time
	Consumed            bool
}

type PurchaseSpec struct {
	TenantID, SiteID  string
	PackageRevisionID string
	OfferQuoteID      string
	AuthContextID     string
	Subject           CommerceSubject
	AmountMinor       int64
	Currency          string
	CurrencyExponent  int
}

type EntitlementSpec struct {
	TenantID, SiteID   string
	PurchaseID         string
	Subject            CommerceSubject
	ServicePlanRevID   string
	PackageRevID       string
	PolicySnapshot     GrantSnapshot
	TimeAccountingMode string
	EndMode            string
	SupersedesID       string // "" if none
	WindowEndsAt       *time.Time
}

// QuoteResult is the guest-safe result of CreateQuote (opaque id + display only; never pins/price
// internals the client could tamper with beyond the opaque id).
type QuoteResult struct {
	Disabled  bool
	QuoteID   string
	ExpiresAt time.Time
	Display   map[string]any // guest-appropriate grant/plan display
	Reason    string
}

// PurchaseResult is the result of a confirm. A Free or Voucher confirm grants immediately (EntitlementID set).
// A Card payment confirm creates the purchase and its REQUIRED settlement and grants NOTHING: access follows
// only a provider-verified capture (AwaitingSettlement, SettlementID set).
type PurchaseResult struct {
	Disabled           bool
	PurchaseID         string
	EntitlementID      string
	Superseded         string
	SettlementID       string
	Method             string
	AwaitingSettlement bool
	Reason             string
}
