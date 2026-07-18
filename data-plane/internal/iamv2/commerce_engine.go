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
	LoadPlanRevision(ctx context.Context, tenantID, siteID, planRevisionID string) (PlanRevisionRow, error)
	LoadEligibilityRules(ctx context.Context, packageRevisionID string) ([]EligibilityRule, error)
	LoadGrantTiers(ctx context.Context, packageRevisionID string) ([]GrantTier, error)
	HasPriorPurchase(ctx context.Context, tenantID, siteID, packageRevisionID string, subj CommerceSubject) (bool, error)
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
	MarkPurchaseGranted(ctx context.Context, purchaseID string) error
}

// CommerceSubject is the non-PMS authenticated subject a free purchase is pinned to (exactly one id).
type CommerceSubject struct {
	Kind        SubjectKind
	VoucherID   string
	AccountID   string
	PrincipalID string
	Method      Method
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
	GrantSnapshot     map[string]any
	ExpiresAt         time.Time
	Now               time.Time
}

type OfferQuoteRow struct {
	ID                string
	TenantID, SiteID  string
	AuthContextID     string
	PackageRevisionID string
	PriceMinor        int64
	Currency          string
	GrantSnapshot     map[string]any
	ExpiresAt         time.Time
	Consumed          bool
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
	PolicySnapshot     map[string]any
	TimeAccountingMode string
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

// PurchaseResult is the result of ConfirmFreePurchase.
type PurchaseResult struct {
	Disabled      bool
	PurchaseID    string
	EntitlementID string
	Superseded    string
	Reason        string
}
