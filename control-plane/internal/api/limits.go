package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoSubscription means the tenant has no active/trialing subscription and
// limits can't be evaluated. Handlers translate this to 402 Payment Required.
var ErrNoSubscription = errors.New("no active subscription for tenant")

// GetIntLimit returns the effective int limit for a tenant and key.
//   - Returns (-1, nil) when the key exists and is unlimited (stored as -1).
//   - Returns (val, nil) on a numeric limit.
//   - Returns (0, ErrNoSubscription) when the tenant has no rows in
//     tenant_effective_limits at all (i.e. no subscription).
//   - Returns (0, nil) when the key is missing for this tenant — handler
//     decides whether that means "unlimited" or "fail".
func GetIntLimit(ctx context.Context, db *pgxpool.Pool, tenantID, key string) (int64, error) {
	// Determine whether the tenant has any effective limits (= has a sub).
	var any bool
	if err := db.QueryRow(ctx, `
        SELECT EXISTS (SELECT 1 FROM tenant_effective_limits WHERE tenant_id = $1)
    `, tenantID).Scan(&any); err != nil {
		return 0, err
	}
	if !any {
		return 0, ErrNoSubscription
	}

	var v *int64
	err := db.QueryRow(ctx, `
        SELECT int_value FROM tenant_effective_limits
         WHERE tenant_id = $1 AND key = $2 AND value_type = 'int'
         LIMIT 1
    `, tenantID, key).Scan(&v)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if v == nil {
		return 0, nil
	}
	return *v, nil
}

// EnforceCreateLimit checks that creating one more row would not exceed
// `limitKey`. countQuery must return a single bigint count of existing rows
// for the tenant. When over quota, writes 403 and returns false.
//
// Behaviour:
//   - limit = -1  (unlimited) → pass
//   - limit = 0 or missing key → pass (limit not modelled yet; don't break)
//   - ErrNoSubscription → 402
//   - count + 1 > limit → 403 "limit_exceeded"
func EnforceCreateLimit(
	ctx context.Context,
	db *pgxpool.Pool,
	w http.ResponseWriter,
	r *http.Request,
	tenantID, limitKey string,
	countQuery string,
	countArgs ...any,
) bool {
	lim, err := GetIntLimit(ctx, db, tenantID, limitKey)
	if err != nil {
		if errors.Is(err, ErrNoSubscription) {
			Fail(w, r, http.StatusPaymentRequired, CodePaymentRequired, "no active subscription")
			return false
		}
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "limits lookup failed")
		return false
	}
	if lim == -1 || lim == 0 {
		return true
	}
	var count int64
	if err := db.QueryRow(ctx, countQuery, countArgs...).Scan(&count); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "limits count failed")
		return false
	}
	if count+1 > lim {
		Fail(w, r, http.StatusForbidden, CodeLimitExceeded, "plan limit reached", map[string]any{
			"limit_key": limitKey,
			"limit":     lim,
			"current":   count,
		})
		return false
	}
	return true
}
