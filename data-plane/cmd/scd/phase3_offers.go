package main

// The offer set is computed by the eligibility engine against server-pinned Stay evidence, not a site-wide
// catalogue (which would grant the best package to whoever asks first). What is decided here is snapshotted
// into the Quote, which must not re-evaluate if the Stay later changes.

import (
	"context"
	"encoding/json"
	"strings"
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
	// MatchedTier is the first-match grant tier; it is part of what the guest was offered.
	MatchedTier    *int
	MatchedTierRaw []byte
	// EvidenceVersion is the occupancy-evidence version the decision was made under.
	EvidenceVersion int64
	// Price and the acquisition methods this stay may use now (phase3_paid.go); an included package has exactly NOT_REQUIRED.
	PriceMinor        int64
	Currency          string
	CurrencyExponent  int
	SettlementMethods []string
	Methods           []string
}

// stayEvidenceFor reads the Stay facts a rule may test from the Stay row, never from the guest's request.
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

// networkLineage reads the networks this one continues (iam_v2.guest_network_lineage). A read failure is an
// error, not an empty lineage, which would silently narrow the offer set.
func (p *phase3Auth) networkLineage(ctx context.Context, guestNetworkID string) ([]string, error) {
	if strings.TrimSpace(guestNetworkID) == "" {
		return nil, nil
	}
	rows, err := p.srv.db.Query(ctx, `
		SELECT ancestor_id::text FROM iam_v2.guest_network_lineage
		 WHERE tenant_id=$1 AND site_id=$2 AND network_id=$3 ORDER BY depth`,
		p.srv.tenID, p.srv.siteID, guestNetworkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// offersFor computes the set this verified Stay qualifies for.
//
// guestNetworkID is the network the appliance derived from the source IP (resolveNetwork), never from the
// guest. It must be passed: SITE_NETWORK rules test it, and an empty id would drop every network-restricted
// package from every room sign-in. deviceID is the device row the purchase-history limit counts against.
func (p *phase3Auth) offersFor(ctx context.Context, stayID, interfaceID, guestNetworkID, deviceID string, now time.Time) ([]offerDecision, error) {
	evidence, err := p.stayEvidenceFor(ctx, stayID)
	if err != nil {
		return nil, err
	}
	// A replaced network keeps its logical identity, so a rule written against the retired id matches its successor.
	lineage, err := p.networkLineage(ctx, guestNetworkID)
	if err != nil {
		return nil, err
	}
	subject := iamv2.EligibilitySubject{
		Now:                 now,
		AuthMethod:          iamv2.Method("PMS"),
		Kind:                iamv2.SubjectStay,
		GuestNetworkID:      guestNetworkID,
		GuestNetworkLineage: lineage,
		Stay:                evidence,
	}

	// Candidates are current revisions of active, non-system, visible packages. The price and settlement filter
	// mirrors staygrant's, so the portal never offers what the grant then refuses.
	rows, err := p.srv.db.Query(ctx, `
		SELECT ipr.id::text, ip.id::text, ip.code, ipr.service_plan_revision_id::text, ipr.package_type,
		       ipr.duration_policy, COALESCE(spr.down_kbps,0), COALESCE(spr.up_kbps,0),
		       ipr.price_minor, COALESCE(ipr.currency,''), COALESCE(ipr.currency_exponent,2), ipr.settlement_methods
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip
		    ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		  LEFT JOIN iam_v2.service_plan_revisions spr ON spr.id = ipr.service_plan_revision_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2
		   AND ip.current_revision_id = ipr.id
		   AND ip.is_system IS NOT TRUE
		   -- DEACTIVATING A PACKAGE MUST REMOVE IT FROM THE PORTAL. It did not: this predicate never read
		   -- ip.active, so a package an operator switched off in Hotel Admin carried on being offered to
		   -- guests. It was found with a deactivated 2 Gbps package still guest-selectable beside its 2 Mbps
		   -- replacement — two offers where the operator believed there was one, and the wrong one grantable.
		   AND ip.active IS TRUE
		   AND ipr.package_type <> 'CHECKOUT_GRACE'
		   AND (ipr.visible_from IS NULL OR ipr.visible_from <= now())
		   AND (ipr.visible_until IS NULL OR ipr.visible_until > now())
		   -- Included packages (price 0, acquirable as Free) and priced packages; which methods apply to THIS
		   -- stay is decided per package below (stayMethods), so nothing is offered that would then fail.
		   AND ((ipr.price_minor = 0 AND 'NOT_REQUIRED' = ANY (ipr.settlement_methods))
		        OR (ipr.price_minor > 0 AND ipr.settlement_methods && ARRAY['PMS_POSTING','ONLINE_PAYMENT']::text[]))
		   -- A STAY TAKES EACH PACKAGE REVISION ONCE (purchase_once_per_stay, contract). One it already holds
		   -- was still offered, and choosing it failed on that index -- after the guest's details were right,
		   -- as "we are unable to verify your stay". Same predicate as the index, so the offer set and the
		   -- grant can never disagree.
		   --
		   -- EXCEPT the package behind the stay's LIVE access. While the stay holds an ACTIVE, unexpired
		   -- entitlement, choosing its package does not buy it again: the grant JOINS that entitlement (a second
		   -- device of the room, or the same guest signing in again), and no purchase is written. Excluding it
		   -- left a property with one free package unable to admit anyone into a room that already had access
		   -- -- the guest proved who they were and was told the stay could not be verified.
		   AND NOT EXISTS (SELECT 1 FROM iam_v2.purchases pu
		                    WHERE pu.stay_id = $3::uuid AND pu.package_revision_id = ipr.id
		                      AND pu.trigger = 'GUEST_SELECTION'
		                      AND pu.state IN ('PENDING','AWAITING_SETTLEMENT','MANUAL_REVIEW','GRANTED')
		                      AND NOT EXISTS (SELECT 1 FROM iam_v2.entitlements e
		                                       WHERE e.purchase_id = pu.id AND e.stay_id = $3::uuid
		                                         AND e.status = 'ACTIVE'
		                                         AND (e.window_ends_at IS NULL OR e.window_ends_at > now())))
		 ORDER BY ip.code`, p.srv.tenID, p.srv.siteID, stayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var candidates []offerDecision
	for rows.Next() {
		var d offerDecision
		if err := rows.Scan(&d.PackageRevisionID, &d.PackageID, &d.Code, &d.ServicePlanRevID,
			&d.PackageType, &d.DurationPolicy, &d.DownKbps, &d.UpKbps,
			&d.PriceMinor, &d.Currency, &d.CurrencyExponent, &d.SettlementMethods); err != nil {
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
		// PRIOR_PURCHASE history is evaluated per stay and per device, like every other client.
		pkgSubject := subject
		evalRules := rules
		if pol := iamv2.PriorPurchasePolicyOf(rules, d.PriceMinor); pol.Present {
			prior, joins, herr := p.stayPriorPurchase(ctx, stayID, deviceID, d, pol)
			if herr != nil {
				return nil, herr
			}
			pkgSubject.HasPriorPurchaseOfPackage = prior
			if joins {
				// Choosing the package behind the stay's live access is a join, not a second acquisition, so the
				// free-access limit must not lock the room's other devices out.
				evalRules = withoutForbidsPrior(rules)
			}
		}
		if ok, _ := iamv2.EvaluatePackageEligible(evalRules, pkgSubject); !ok {
			continue
		}
		tiers, err := p.grantTiers(ctx, d.PackageRevisionID)
		if err != nil {
			return nil, err
		}
		if len(tiers) > 0 {
			tier, matched := iamv2.FirstMatchTier(tiers, pkgSubject)
			if !matched {
				// No tier matches, so the Quote could not be honoured.
				continue
			}
			order := tier.Order
			d.MatchedTier = &order
			d.MatchedTierRaw, _ = json.Marshal(tier.Value)
		}
		d.EvidenceVersion = evidence.EvidenceVersion
		d.Currency = strings.TrimSpace(d.Currency)
		d.Methods = p.stayMethods(ctx, stayID, interfaceID, d)
		if len(d.Methods) == 0 {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// stayPriorPurchaseSQL answers, for one package (any revision; history is keyed on the package), whether the
// stay has had it, whether the device has (when the policy counts devices), and whether the stay holds live
// access of the offered revision (the join case). The window runs from the grant, a pending grant counting as now.
const stayPriorPurchaseSQL = `
	SELECT EXISTS (SELECT 1 FROM iam_v2.entitlements e
	                 JOIN iam_v2.internet_package_revisions r ON r.id = e.package_revision_id
	                WHERE e.tenant_id=$1 AND e.site_id=$2 AND r.package_id=$3::uuid AND e.stay_id=$4::uuid
	                  AND ($6::timestamptz IS NULL OR COALESCE(e.activated_at, now()) >= $6::timestamptz))
	    OR ($7 AND $5::uuid IS NOT NULL AND EXISTS (
	           SELECT 1 FROM iam_v2.entitlements e
	             JOIN iam_v2.internet_package_revisions r ON r.id = e.package_revision_id
	             JOIN iam_v2.purchases pu     ON pu.id = e.purchase_id
	             JOIN iam_v2.auth_contexts ac ON ac.id = pu.auth_context_id
	            WHERE e.tenant_id=$1 AND e.site_id=$2 AND r.package_id=$3::uuid AND ac.device_id=$5::uuid
	              AND ($6::timestamptz IS NULL OR COALESCE(e.activated_at, now()) >= $6::timestamptz))),
	       EXISTS (SELECT 1 FROM iam_v2.entitlements e
	                WHERE e.tenant_id=$1 AND e.site_id=$2 AND e.stay_id=$4::uuid AND e.package_revision_id=$8::uuid
	                  AND e.status='ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now()))`

// stayPriorPurchase reports (prior, joinsLiveAccess). Any error is returned: an unreadable history must not
// become "no history", which would hand out a limited package again.
func (p *phase3Auth) stayPriorPurchase(ctx context.Context, stayID, deviceID string, d offerDecision,
	pol iamv2.PriorPurchasePolicy) (bool, bool, error) {
	var since *time.Time
	if pol.WithinHours > 0 {
		s := time.Now().Add(-time.Duration(pol.WithinHours) * time.Hour)
		since = &s
	}
	var dev *string
	if strings.TrimSpace(deviceID) != "" {
		dev = &deviceID
	}
	var prior, joins bool
	err := p.srv.db.QueryRow(ctx, stayPriorPurchaseSQL, p.srv.tenID, p.srv.siteID, d.PackageID, stayID, dev, since,
		pol.AlsoByDevice, d.PackageRevisionID).Scan(&prior, &joins)
	return prior, joins, err
}

// withoutForbidsPrior drops the free-access limit (PRIOR_PURCHASE forbids_prior) from a rule list.
func withoutForbidsPrior(rules []iamv2.EligibilityRule) []iamv2.EligibilityRule {
	out := make([]iamv2.EligibilityRule, 0, len(rules))
	for _, r := range rules {
		if strings.EqualFold(strings.TrimSpace(r.Type), iamv2.RulePriorPurchase) {
			if b, _ := r.Value["forbids_prior"].(bool); b {
				continue
			}
		}
		out = append(out, r)
	}
	return out
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

// recordOfferSet persists what was offered against the Auth Context, so a grant can check "was this offered
// to this Stay?" rather than only "is this generally grantable?".
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
