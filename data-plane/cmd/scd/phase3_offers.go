package main

// WHAT A VERIFIED STAY IS ACTUALLY OFFERED.
//
// The first version of this listed every free, visible package on the site. That is not an offer set — it is
// a catalogue. A property that publishes "suites get the premium tier" or "corporate rate plans get the
// business package" wrote those rules expecting them to decide something, and a site-wide list silently
// grants the best of them to whoever asks first.
//
// So the offer set is computed by the real Phase-2 eligibility engine, against SERVER-PINNED Stay evidence
// read from the resolved Stay under the pinned Interface Revision. Two Stays at the same property, verified a
// second apart, can legitimately get different offers — that is the point.
//
// Everything decided here is snapshotted into the Quote, because an offer is a statement about a moment: the
// Stay can change afterwards (a room move, a rate change, a checkout), and a Quote that silently re-evaluated
// would mean the guest agreed to one thing and received another.

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// offerDecision is one package the verified Stay qualifies for, with the evidence that decided it.
type offerDecision struct {
	PackageRevisionID string
	PackageID         string
	Code              string
	ServicePlanRevID  string
	PackageType       string
	DurationPolicy    []byte
	DownKbps          int
	UpKbps            int
	// MatchedTier is the first-match grant tier, when the package has tiers. Recorded because the tier is
	// part of what the guest was offered, not an implementation detail of how it was computed.
	MatchedTier    *int
	MatchedTierRaw []byte
	// EvidenceVersion is the occupancy-evidence version the decision was made under, carried so the offer
	// record and the Quote can both be re-justified against the exact evidence that produced them.
	EvidenceVersion int64
}

// stayEvidenceFor reads the authoritative Stay facts an eligibility rule may test. It reads them from the
// Stay row under the pinned Interface, never from anything the guest sent, and carries the occupancy-evidence
// version so the decision can be re-justified later against the exact evidence that produced it.
func (p *phase3Auth) stayEvidenceFor(ctx context.Context, stayID string) (*iamv2.StayEvidence, error) {
	var e iamv2.StayEvidence
	var vip *bool
	var arrival, departure *time.Time
	var roomType, ratePlan, travelAgent *string
	err := p.srv.db.QueryRow(ctx, `
		SELECT s.id::text, s.pms_interface_id::text, s.status,
		       s.room_type, s.travel_agent, s.vip, s.arrival, s.departure,
		       s.occupancy_evidence_version, s.rate_plan
		  FROM iam_v2.stays s
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.id=$3`,
		p.srv.tenID, p.srv.siteID, stayID).
		Scan(&e.StayID, &e.InterfaceID, &e.Status, &roomType, &travelAgent, &vip,
			&arrival, &departure, &e.EvidenceVersion, &ratePlan)
	if err != nil {
		return nil, err
	}
	if roomType != nil {
		e.RoomType = *roomType
	}
	if travelAgent != nil {
		e.TravelAgent = *travelAgent
	}
	if ratePlan != nil {
		e.RatePlan = *ratePlan
	}
	e.VIP, e.Arrival, e.Departure = vip, arrival, departure
	return &e, nil
}

// offersFor computes the exact set this verified Stay qualifies for.
func (p *phase3Auth) offersFor(ctx context.Context, stayID, interfaceID string, now time.Time) ([]offerDecision, error) {
	evidence, err := p.stayEvidenceFor(ctx, stayID)
	if err != nil {
		return nil, err
	}
	subject := iamv2.EligibilitySubject{
		Now:            now,
		AuthMethod:     iamv2.Method("PMS"),
		Kind:           iamv2.SubjectKind("PRINCIPAL"),
		GuestNetworkID: "",
		Stay:           evidence,
	}

	// Only CURRENT revisions of non-system, visible, zero-price packages are even candidates. The price and
	// settlement filter is the same one staygrant enforces at grant time — two predicates that "should"
	// agree are how a portal ends up offering something the grant then refuses.
	rows, err := p.srv.db.Query(ctx, `
		SELECT ipr.id::text, ip.id::text, ip.code, ipr.service_plan_revision_id::text, ipr.package_type,
		       ipr.duration_policy, COALESCE(spr.down_kbps,0), COALESCE(spr.up_kbps,0)
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip
		    ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = ipr.service_plan_revision_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2
		   AND ip.current_revision_id = ipr.id
		   AND ip.is_system IS NOT TRUE
		   AND ipr.package_type <> 'CHECKOUT_GRACE'
		   AND (ipr.visible_from IS NULL OR ipr.visible_from <= now())
		   AND (ipr.visible_until IS NULL OR ipr.visible_until > now())
		   AND ipr.price_minor = 0
		   AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
		 ORDER BY ip.code`, p.srv.tenID, p.srv.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []offerDecision
	for rows.Next() {
		var d offerDecision
		if err := rows.Scan(&d.PackageRevisionID, &d.PackageID, &d.Code, &d.ServicePlanRevID,
			&d.PackageType, &d.DurationPolicy, &d.DownKbps, &d.UpKbps); err != nil {
			return nil, err
		}
		candidates = append(candidates, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]offerDecision, 0, len(candidates))
	for _, d := range candidates {
		rules, err := p.eligibilityRules(ctx, d.PackageRevisionID)
		if err != nil {
			return nil, err
		}
		if ok, _ := iamv2.EvaluatePackageEligible(rules, subject); !ok {
			continue
		}
		tiers, err := p.grantTiers(ctx, d.PackageRevisionID)
		if err != nil {
			return nil, err
		}
		if len(tiers) > 0 {
			tier, matched := iamv2.FirstMatchTier(tiers, subject)
			if !matched {
				// The package is eligible but no tier matches this Stay, so there is nothing to grant. That
				// is not an offer; offering it would produce a Quote the grant could not honour.
				continue
			}
			order := tier.Order
			d.MatchedTier = &order
			d.MatchedTierRaw, _ = json.Marshal(tier.Value)
		}
		d.EvidenceVersion = evidence.EvidenceVersion
		out = append(out, d)
	}
	return out, nil
}

func (p *phase3Auth) eligibilityRules(ctx context.Context, packageRevID string) ([]iamv2.EligibilityRule, error) {
	rows, err := p.srv.db.Query(ctx, `
		SELECT rule_type, rule_value FROM iam_v2.package_eligibility_rules
		 WHERE package_revision_id=$1 ORDER BY rule_type`, packageRevID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []iamv2.EligibilityRule
	for rows.Next() {
		var r iamv2.EligibilityRule
		var raw []byte
		if err := rows.Scan(&r.Type, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &r.Value)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *phase3Auth) grantTiers(ctx context.Context, packageRevID string) ([]iamv2.GrantTier, error) {
	rows, err := p.srv.db.Query(ctx, `
		SELECT tier_order, grant_value FROM iam_v2.package_grant_tiers
		 WHERE package_revision_id=$1 ORDER BY tier_order`, packageRevID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []iamv2.GrantTier
	for rows.Next() {
		var t iamv2.GrantTier
		var raw []byte
		if err := rows.Scan(&t.Order, &raw); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &t.Value)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// recordOfferSet persists what was offered, against the Auth Context that will redeem it.
//
// This is what makes a grant checkable. Without it the server can only ask "is this package generally
// grantable?", which is a different question from "was this package offered to THIS verified Stay?" — and a
// guest naming any other free package on the site would pass the first while failing the second.
func (p *phase3Auth) recordOfferSet(ctx context.Context, tx pgx.Tx, authContextID string,
	evidenceVersion int64, offers []offerDecision, expiresAt time.Time) error {
	for _, o := range offers {
		var tier *int
		if o.MatchedTier != nil {
			tier = o.MatchedTier
		}
		if _, err := tx.Exec(ctx, `
			SELECT iam_v2.record_auth_context_offer($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7)`,
			p.srv.tenID, p.srv.siteID, authContextID, o.PackageRevisionID, tier,
			evidenceVersion, expiresAt); err != nil {
			return err
		}
	}
	return nil
}
