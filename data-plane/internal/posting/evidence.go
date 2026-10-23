package posting

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Pinned is the financial evidence a Posting is created against, decided once at creation and never re-derived.
// Retries, recovery and reviewed second attempts read it back from the durable row, so a later posting-code or
// revision change cannot alter the charge that was authorized.
type Pinned struct {
	TenantID string
	SiteID   string

	PMSInterfaceID string
	// AuthInterfaceRevisionID may differ from the posting revision: authentication happened earlier.
	AuthInterfaceRevisionID string
	// PostingInterfaceRevisionID supplies the posting target model and financial base currency.
	PostingInterfaceRevisionID string
	// SecretGenerationID is empty when the connector used no credential.
	SecretGenerationID string

	StayID string

	PackageRevisionID   string
	SettlementMappingID string
	PurchaseID          string
	SettlementID        string

	AmountMinor      int64
	Currency         string
	CurrencyExponent int16

	// G# is pinned at creation and never re-resolved. RN is mutable: the reservation's current room, re-read
	// from the pinned stay just before each attempt is built. Neither is the business identity (IdempotencyKey).
	RN      string
	GNumber string

	// PostingCode comes from the pinned settlement mapping and becomes the bounded CT field.
	PostingCode string

	IdempotencyKey string

	// PostingID is set once the posting exists; permission is then evaluated for this charge, so its own
	// POSTING_UNRESOLVED block (an UNKNOWN a reviewer authorised to retry) does not stop it.
	PostingID string

	// If the database disagrees with these expectations at creation, the evidence is stale and the posting is refused.
	ExpectStayLifecycleVersion *int
	ExpectPurchaseState        string
}

// Snapshot is read in one statement so the halves of the gate cannot see different worlds.
type Snapshot struct {
	InterfaceLifecycleState  string
	InterfaceCurrentRevision string

	PostingTargetModel string
	InterfaceCurrency  string
	InterfaceExponent  *int16

	PurchaseCurrency string
	PurchaseExponent *int16
	PurchaseState    string

	PackageCurrency string
	PackageExponent *int16

	StayStatus           string
	StayPostingAllowed   bool
	StayLifecycleVersion int
	// The stay's current reservation number and normalized room. The posting's G# must equal the reservation;
	// the attempt's RN is this room, so a room move re-targets the room, never the reservation.
	StayRoomNumber  string
	StayReservation string

	SettlementMappingRetired bool

	// ConnectorKind decides the wire rules; protel-fias is exponent-2 only.
	ConnectorKind string
	// FreshnessBlock is the first failed PMS runtime axis, or "". It comes from the same SQL function the
	// database enforces with, so the gate and the database cannot disagree about "fresh".
	FreshnessBlock string
}

type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var ErrEvidenceNotResolvable = errors.New("pinned financial evidence does not resolve within one tenant/site/interface")

// LoadSnapshot joins every object on (tenant, site, interface), so evidence from another property cannot be read
// and fails as "not resolvable" before anything is written (composite foreign keys enforce it again on write).
func LoadSnapshot(ctx context.Context, q Querier, p Pinned) (Snapshot, error) {
	var s Snapshot
	const sql = `
SELECT i.lifecycle_state,
       coalesce(i.current_revision_id::text, ''),
       i.connector_kind,
       coalesce(iam_v2.p4_interface_freshness_block(i.tenant_id, i.site_id, i.id, r.id, now()), ''),
       r.posting_target_model,
       coalesce(r.financial_base_currency, ''),
       r.financial_base_currency_exponent,
       coalesce(pu.currency, ''), pu.currency_exponent, pu.state,
       coalesce(pk.currency, ''), pk.currency_exponent,
       st.status, coalesce(iam_v2.p4_stay_postable_for(st.id, NULLIF($10,'')::uuid), false), st.lifecycle_version,
       coalesce(st.normalized_room_number, ''), st.external_reservation_id,
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
  JOIN iam_v2.package_settlement_mappings sm
    ON sm.tenant_id = i.tenant_id AND sm.site_id = i.site_id
   AND sm.pms_interface_id = i.id AND sm.package_revision_id = pk.id AND sm.id = $8
  JOIN iam_v2.settlements se
    ON se.tenant_id = i.tenant_id AND se.site_id = i.site_id AND se.id = $9
   AND se.purchase_id = pu.id
 WHERE i.tenant_id = $1 AND i.site_id = $2 AND i.id = $3`
	err := q.QueryRow(ctx, sql,
		p.TenantID, p.SiteID, p.PMSInterfaceID, p.PostingInterfaceRevisionID,
		p.PurchaseID, p.PackageRevisionID, p.StayID, p.SettlementMappingID, p.SettlementID, p.PostingID,
	).Scan(
		&s.InterfaceLifecycleState, &s.InterfaceCurrentRevision, &s.ConnectorKind, &s.FreshnessBlock,
		&s.PostingTargetModel, &s.InterfaceCurrency, &s.InterfaceExponent,
		&s.PurchaseCurrency, &s.PurchaseExponent, &s.PurchaseState,
		&s.PackageCurrency, &s.PackageExponent,
		&s.StayStatus, &s.StayPostingAllowed, &s.StayLifecycleVersion,
		&s.StayRoomNumber, &s.StayReservation,
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

// wireUnsafe rejects bytes FIAS framing would eat; rejecting beats escaping, since an escaping bug is a money defect.
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
