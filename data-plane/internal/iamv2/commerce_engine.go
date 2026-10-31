package iamv2

import (
	"context"
	"time"
)

// CommerceEngine is the commerce entry point (offer quotes, purchases). With the master flag off it holds a
// nil repository, issues no SQL and returns disabled results.
type CommerceEngine struct {
	cfg  CommerceConfig
	repo CommerceRepository
	obs  Observer
	now  func() time.Time
	ttl  time.Duration // offer-quote TTL (5 min unless overridden)
	// Gates NEW acquisition of the AGGREGATE_ONLINE_TIME mode; TimeModeAcquirable is the single rule every
	// acquisition path shares.
	aggregateOnlineTime bool
	methodGate          MethodGate
}

// repo must be nil while the master flag is off and non-nil when enabled (fail closed); an incoherent flag set
// is rejected.
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

type CommerceOption func(*CommerceEngine)

func WithCommerceClock(f func() time.Time) CommerceOption {
	return func(e *CommerceEngine) { e.now = f }
}

func WithQuoteTTL(d time.Duration) CommerceOption { return func(e *CommerceEngine) { e.ttl = d } }

// Must derive from the same Phase-6 flag that turns on acctd's accrual tick: a runtime that can create the
// entitlement but not account for it is what this gate prevents.
func WithAggregateOnlineTime(on bool) CommerceOption {
	return func(e *CommerceEngine) { e.aggregateOnlineTime = on }
}

// Auth-context consumption happens inside the commerce transaction, never via the standalone
// SessionEngine.ConsumeAuthContext, so a consumed context cannot be left without a Purchase.

type CommerceRepository interface {
	WithTx(ctx context.Context, fn func(CommerceTx) error) error
}

type CommerceTx interface {
	LoadAuthContext(ctx context.Context, tenantID, siteID, authContextID string) (AuthContextRow, error)
	ResolveActivePackageRevision(ctx context.Context, tenantID, siteID, packageID string) (PackageRevisionRow, error)
	ListActivePackageRevisions(ctx context.Context, tenantID, siteID string) ([]PackageRevisionRow, error)
	LoadPlanRevision(ctx context.Context, tenantID, siteID, planRevisionID string) (PlanRevisionRow, error)
	LoadEligibilityRules(ctx context.Context, packageRevisionID string) ([]EligibilityRule, error)
	// The package revision a voucher was printed against; republishing must not change what a printed card is
	// worth, and the offer path narrows to it so a guest is never offered a choice the grant kernel refuses.
	VoucherPinnedPackageRevision(ctx context.Context, tenantID, siteID, voucherID string) (string, error)
	LoadGrantTiers(ctx context.Context, packageRevisionID string) ([]GrantTier, error)
	// Client networks the device's network continues (iam_v2.guest_network_lineage). A replacement gives a
	// network a new id, so SITE_NETWORK rules in pinned revisions would stop matching and printed vouchers
	// become unredeemable. Empty is normal; a read failure is an error, never an empty slice, since losing
	// lineage silently narrows eligibility.
	GuestNetworkAncestors(ctx context.Context, tenantID, siteID, guestNetworkID string) ([]string, error)
	// Keyed on the package (any revision) so a republish never resets a free allowance.
	HasPriorPurchase(ctx context.Context, tenantID, siteID string, q PriorPurchaseQuery) (bool, error)
	// A free package of this subject (an anonymous one: this device) that staff ended with End Package (0202) and that
	// has not been reset since. It is not acquired again automatically; a priced purchase is unaffected.
	EndedByStaff(ctx context.Context, tenantID, siteID, packageRevisionID string, subj CommerceSubject) (bool, error)
	InsertOfferQuote(ctx context.Context, q OfferQuoteSpec) (string, error)

	LockOfferQuoteForUpdate(ctx context.Context, tenantID, siteID, quoteID string) (OfferQuoteRow, error)
	LockAuthContextForUpdate(ctx context.Context, tenantID, siteID, authContextID string) (AuthContextRow, error)
	AcquireSubjectLock(ctx context.Context, tenantID, siteID string, subj CommerceSubject) error
	ConsumeOfferQuote(ctx context.Context, quoteID string, now time.Time) (bool, error)
	ConsumeAuthContextByID(ctx context.Context, authContextID string, now time.Time) (bool, error)
	InsertPurchase(ctx context.Context, p PurchaseSpec) (string, error)
	InsertSettlement(ctx context.Context, tenantID, siteID, purchaseID string) error
	TerminateLiveEntitlementForSubject(ctx context.Context, tenantID, siteID string, subj CommerceSubject) (supersededID string, err error)
	InsertEntitlement(ctx context.Context, e EntitlementSpec) (string, error)

	// The FREE grant entry point: one call into the kernel shared with the paid path.
	GrantQuotedEntitlement(ctx context.Context, tenantID, siteID, purchaseID string) (string, string, error)
	MarkPurchaseGranted(ctx context.Context, purchaseID string) error

	// Resolves one revision by id, current or not: a voucher is honoured against the immutable revision it was
	// printed for.
	LoadPackageRevisionByID(ctx context.Context, tenantID, siteID, packageRevisionID string) (PackageRevisionRow, error)
	InsertPurchaseAs(ctx context.Context, p PurchaseSpec, trigger, state string) (string, error)
	InsertSettlementAs(ctx context.Context, tenantID, siteID, purchaseID, method, status string) (string, error)
	GrantVoucherEntitlement(ctx context.Context, tenantID, siteID, purchaseID string) (string, string, error)

	// Resolves everything the grant needs from rows the purchase already points at, so a caller supplies
	// nothing.
	LoadSettledPurchaseGrant(ctx context.Context, tenantID, siteID, purchaseID string) (SettledPurchaseGrant, error)
}

type CommerceSubject struct {
	Kind        SubjectKind
	VoucherID   string
	AccountID   string
	PrincipalID string
	// Opaque anonymous subject of open package selection; never a MAC.
	AnonymousID string
	Method      Method
	// Not a subject: free-package limits for an anonymous subject are evaluated against the device, so a new
	// anonymous subject never resets them.
	DeviceID string
}

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
	ClientGroupID  string
}

type PriorPurchaseQuery struct {
	PackageID string
	Subject   CommerceSubject
	Policy    PriorPurchasePolicy
}

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
	// FIXED when absent. A PER_STAY_NIGHT package needs a stay to compute its allowance, so non-room paths
	// never offer it (applicableMethods).
	DataAllocationMode string
}

type PlanRevisionRow struct {
	ID                   string
	DownKbps             int
	UpKbps               int
	MaxConcurrentDevices int
	TimeQuotaSeconds     int64
	DataQuotaBytes       int64
	TimeAccountingMode   string
}

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

// Carries every money/settlement/tax pin so ConfirmFreePurchase can re-validate the quote before consuming
// anything.
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

// Guest-safe: opaque id and display only, no pins or price internals the client could tamper with.
type QuoteResult struct {
	Disabled  bool
	QuoteID   string
	ExpiresAt time.Time
	Display   map[string]any // guest-appropriate grant/plan display
	Reason    string
}

// A Free or Voucher confirm grants immediately (EntitlementID set). A Card confirm creates the purchase and
// its required settlement and grants nothing until a provider-verified capture (AwaitingSettlement,
// SettlementID set).
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
