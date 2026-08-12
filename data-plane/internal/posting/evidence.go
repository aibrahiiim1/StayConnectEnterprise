package posting

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Pinned is the exact financial evidence a Posting is created against. Every field is an identifier or a
// value that was decided ONCE, at creation, and is then never re-derived.
//
// This is the whole of C1 and C2 in one struct. A retry, a restart, a recovery or a manual-review-authorized
// second attempt all read these values back from the durable posting row; none of them re-resolves "the
// current stay", "the current revision" or "today's price". If the property changed its posting code or
// published a new interface revision after the charge was authorized, the charge that was authorized is
// still the charge that gets sent.
type Pinned struct {
	TenantID string
	SiteID   string

	PMSInterfaceID string
	// AuthInterfaceRevisionID is the revision that authenticated the guest. It is pinned separately from
	// the posting revision because they are genuinely allowed to differ: authentication happened earlier.
	AuthInterfaceRevisionID string
	// PostingInterfaceRevisionID is the revision whose financial configuration authorizes THIS posting —
	// folio identity strategy and financial base currency both come from it.
	PostingInterfaceRevisionID string
	// SecretGenerationID is pinned when the connector used a credential; empty when it did not.
	SecretGenerationID string

	StayID  string
	FolioID string

	PackageRevisionID   string
	SettlementMappingID string
	PurchaseID          string
	SettlementID        string

	AmountMinor      int64
	Currency         string
	CurrencyExponent int16

	// RN and G# are financial TARGETING evidence. RN is never the posting's identity — the identity is
	// IdempotencyKey, and the protocol reference is the P# allocated at transmission time.
	RN      string
	GNumber string

	// PostingCode is the property-configured charge code from the pinned settlement mapping. It becomes the
	// bounded CT field on the wire; it is configuration, not free text from a caller.
	PostingCode string

	IdempotencyKey string

	// Expectations the caller read when it decided to charge. If the database disagrees at creation time,
	// the evidence is stale and the posting is refused rather than silently created against newer facts.
	ExpectStayLifecycleVersion *int
	ExpectPurchaseState        string
}

// Snapshot is what the database says about the pinned objects, read in ONE consistent statement so no two
// halves of the gate can see different worlds.
type Snapshot struct {
	InterfaceLifecycleState  string
	InterfaceCurrentRevision string

	FolioIdentityStrategy string
	InterfaceCurrency     string
	InterfaceExponent     *int16

	PurchaseCurrency string
	PurchaseExponent *int16
	PurchaseState    string

	PackageCurrency string
	PackageExponent *int16

	StayStatus           string
	StayPostingAllowed   bool
	StayLifecycleVersion int
	// The PMS's own targeting values for the PINNED stay and folio. RN is the normalized room number and
	// G# is the folio's external identifier -- which is precisely why folio_identity_strategy gates posting
	// at all. The gate compares the caller's verified values against these, so a guest who moved room
	// between authorization and transmission produces a refusal rather than a charge to the wrong room.
	StayRoomNumber  string
	FolioExternalID string

	SettlementMappingRetired bool
}

// Querier is the read surface the gate needs. Both *pgxpool.Pool and pgx.Tx satisfy it.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrEvidenceNotResolvable is returned when the pinned set does not resolve as one coherent, in-scope whole.
var ErrEvidenceNotResolvable = errors.New("pinned financial evidence does not resolve within one tenant/site/interface")

// LoadSnapshot reads every pinned object in a single statement.
//
// Every join below is COMPOSITE-pinned on (tenant, site, interface). That is not decoration: it means a
// folio, stay or revision belonging to a different property cannot be read at all, so cross-scope evidence
// fails as "not resolvable" instead of quietly passing a field-by-field comparison. The database enforces
// the same thing again through composite foreign keys when the row is written; this is the check that
// happens BEFORE anything is written.
func LoadSnapshot(ctx context.Context, q Querier, p Pinned) (Snapshot, error) {
	var s Snapshot
	const sql = `
SELECT i.lifecycle_state,
       coalesce(i.current_revision_id::text, ''),
       r.folio_identity_strategy,
       coalesce(r.financial_base_currency, ''),
       r.financial_base_currency_exponent,
       coalesce(pu.currency, ''), pu.currency_exponent, pu.state,
       coalesce(pk.currency, ''), pk.currency_exponent,
       st.status, st.posting_allowed, st.lifecycle_version,
       coalesce(st.normalized_room_number, ''), fo.external_folio_id,
       (sm.retired_at IS NOT NULL)
  FROM iam_v2.pms_interfaces i
  JOIN iam_v2.pms_interface_revisions r
    ON r.tenant_id = i.tenant_id AND r.site_id = i.site_id
   AND r.pms_interface_id = i.id AND r.id = $4
  JOIN iam_v2.purchases pu
    ON pu.tenant_id = i.tenant_id AND pu.site_id = i.site_id AND pu.id = $5
   AND pu.pms_interface_id = i.id
  JOIN iam_v2.internet_package_revisions pk
    ON pk.tenant_id = i.tenant_id AND pk.site_id = i.site_id AND pk.id = pu.package_revision_id
   AND pk.id = $6
  JOIN iam_v2.stays st
    ON st.tenant_id = i.tenant_id AND st.site_id = i.site_id
   AND st.pms_interface_id = i.id AND st.id = $7
  JOIN iam_v2.folios fo
    ON fo.tenant_id = i.tenant_id AND fo.site_id = i.site_id
   AND fo.pms_interface_id = i.id AND fo.id = $8
  JOIN iam_v2.package_settlement_mappings sm
    ON sm.tenant_id = i.tenant_id AND sm.site_id = i.site_id
   AND sm.pms_interface_id = i.id AND sm.package_revision_id = pk.id AND sm.id = $9
  JOIN iam_v2.settlements se
    ON se.tenant_id = i.tenant_id AND se.site_id = i.site_id AND se.id = $10
   AND se.purchase_id = pu.id
 WHERE i.tenant_id = $1 AND i.site_id = $2 AND i.id = $3`
	err := q.QueryRow(ctx, sql,
		p.TenantID, p.SiteID, p.PMSInterfaceID, p.PostingInterfaceRevisionID,
		p.PurchaseID, p.PackageRevisionID, p.StayID, p.FolioID, p.SettlementMappingID, p.SettlementID,
	).Scan(
		&s.InterfaceLifecycleState, &s.InterfaceCurrentRevision,
		&s.FolioIdentityStrategy, &s.InterfaceCurrency, &s.InterfaceExponent,
		&s.PurchaseCurrency, &s.PurchaseExponent, &s.PurchaseState,
		&s.PackageCurrency, &s.PackageExponent,
		&s.StayStatus, &s.StayPostingAllowed, &s.StayLifecycleVersion,
		&s.StayRoomNumber, &s.FolioExternalID,
		&s.SettlementMappingRetired,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, fail(ErrEvidenceOutOfScope, ErrEvidenceNotResolvable.Error())
	}
	if err != nil {
		return Snapshot{}, fail(ErrRepo, "loading pinned financial evidence failed")
	}
	return s, nil
}

// wireUnsafe reports whether a value carries a byte that FIAS framing or field separation would eat.
// Rejecting here rather than escaping later is deliberate: an escaping bug in one sender is a money defect,
// and there is no legitimate room number containing a pipe or a control character.
func wireUnsafe(v string) bool {
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c == 0x7f || c == '|' {
			return true
		}
	}
	return false
}

func blank(v string) bool { return strings.TrimSpace(v) == "" }
