package transfer

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/jackc/pgx/v5"
)

// maxReasonRunes bounds the operator's text as stored (migration 0202 holds 4..500).
const maxReasonRunes = 500

// boundedReason is what is stored with the transfer: the operator's text, trimmed and cut to the stored bound. Execute has
// already required at least four characters.
func boundedReason(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxReasonRunes {
		s = string(r[:maxReasonRunes])
	}
	return s
}

type landingCandidate struct {
	pkgRev, planRev, pkg string
	system               bool
}

// landingPackage chooses the zero-price, no-posting package the transferred guest lands on, inside the transaction. Among
// equals the order is deterministic (audit P6 L11). A package whose current revision is restricted by a PMS_INTERFACE rule
// to other interfaces is not offered at the destination (audit AD-46): the guest lands only on a package the destination
// interface may grant. System packages (the checkout-grace catalogue) carry no such rule and are never filtered.
func (s *Store) landingPackage(ctx context.Context, tx pgx.Tx, tenant, site, toIface string) (pkgRev, planRev string, err error) {
	rows, err := tx.Query(ctx, `
		SELECT ipr.id::text, ipr.service_plan_revision_id::text, ip.id::text, ip.is_system
		  FROM iam_v2.internet_package_revisions ipr
		  JOIN iam_v2.internet_packages ip
		    ON ip.tenant_id=ipr.tenant_id AND ip.site_id=ipr.site_id AND ip.id=ipr.package_id
		 WHERE ipr.tenant_id=$1 AND ipr.site_id=$2 AND ip.active
		   AND ip.current_revision_id = ipr.id
		   AND ipr.price_minor = 0
		   AND ipr.settlement_methods = ARRAY['NOT_REQUIRED']::text[]
		   AND ipr.package_type IN ('CHECKOUT_GRACE','FREE_STAY','GENERAL')
		 ORDER BY CASE ipr.package_type WHEN 'CHECKOUT_GRACE' THEN 0 WHEN 'FREE_STAY' THEN 1 ELSE 2 END,
		          ip.code, ipr.id
		 LIMIT 50`, tenant, site)
	if err != nil {
		return "", "", err
	}
	var cands []landingCandidate
	for rows.Next() {
		var c landingCandidate
		if err := rows.Scan(&c.pkgRev, &c.planRev, &c.pkg, &c.system); err != nil {
			rows.Close()
			return "", "", err
		}
		cands = append(cands, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	for _, c := range cands {
		if !c.system {
			ok, err := allowsInterface(ctx, tx, tenant, site, c.pkg, toIface)
			if err != nil {
				return "", "", err
			}
			if !ok {
				continue
			}
		}
		return c.pkgRev, c.planRev, nil
	}
	return "", "", ErrNoGracePackage
}

// allowsInterface reads the package's current eligibility rules through the scoped reader (the Hotel Admin login has no
// SELECT on the rules table) and answers whether every PMS_INTERFACE rule names the interface. No such rule means the
// package is not restricted by interface.
func allowsInterface(ctx context.Context, tx pgx.Tx, tenant, site, pkg, iface string) (bool, error) {
	rows, err := tx.Query(ctx, `SELECT value FROM iam_v2.p2_package_current_conditions($1,$2,$3) WHERE kind='RULE' AND rule_type='PMS_INTERFACE'`,
		tenant, site, pkg)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var v struct {
			IDs []string `json:"pms_interface_ids"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			// An unreadable restriction is a restriction we cannot honour: refuse the package, never guess.
			return false, nil
		}
		found := false
		for _, id := range v.IDs {
			if strings.EqualFold(id, iface) {
				found = true
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, rows.Err()
}
