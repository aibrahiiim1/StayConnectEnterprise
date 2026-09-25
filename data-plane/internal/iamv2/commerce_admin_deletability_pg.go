package iamv2

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// WHAT STILL POINTS AT A PACKAGE OR A PLAN, AND THE DELETION ITSELF, are answered by the database
// (migration 0091): iam_v2.internet_package_deletion_blockers / service_plan_deletion_blockers count every
// reference -- vouchers, batches, portal offers and settlement mappings included, which svc_edged cannot read --
// and iam_v2.internet_package_delete_unused / service_plan_delete_unused refuse on the very same list under a
// row lock. svc_edged holds EXECUTE on those four and DELETE on none of the catalogue tables, so there is no
// second, weaker path to keep in step.

const packageBlockersSQL = `SELECT reason, n FROM iam_v2.internet_package_deletion_blockers($1::uuid, $2::uuid, $3::uuid)`
const planBlockersSQL = `SELECT reason, n FROM iam_v2.service_plan_deletion_blockers($1::uuid, $2::uuid, $3::uuid)`
const packageDeleteSQL = `SELECT iam_v2.internet_package_delete_unused($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5)`
const planDeleteSQL = `SELECT iam_v2.service_plan_delete_unused($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5)`

func (r *PgCommerceAdminRepository) PackageBlockers(ctx context.Context, tenantID, siteID, packageID string) ([]CatalogueBlocker, error) {
	return r.blockers(ctx, packageBlockersSQL, tenantID, siteID, packageID)
}

func (r *PgCommerceAdminRepository) PlanBlockers(ctx context.Context, tenantID, siteID, planID string) ([]CatalogueBlocker, error) {
	return r.blockers(ctx, planBlockersSQL, tenantID, siteID, planID)
}

func (r *PgCommerceAdminRepository) blockers(ctx context.Context, sql, tenantID, siteID, id string) ([]CatalogueBlocker, error) {
	rows, err := r.db.Query(ctx, sql, tenantID, siteID, id)
	if err != nil {
		return nil, mapCatalogueErr(err)
	}
	defer rows.Close()
	out := []CatalogueBlocker{}
	for rows.Next() {
		var b CatalogueBlocker
		if err := rows.Scan(&b.Code, &b.N); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, mapCatalogueErr(rows.Err())
}

func (r *PgCommerceAdminRepository) DeleteUnusedPackage(ctx context.Context, tenantID, siteID, packageID, operatorID, reason string) error {
	_, err := r.db.Exec(ctx, packageDeleteSQL, tenantID, siteID, packageID, operatorID, reason)
	return mapCatalogueErr(err)
}

func (r *PgCommerceAdminRepository) DeleteUnusedPlan(ctx context.Context, tenantID, siteID, planID, operatorID, reason string) error {
	_, err := r.db.Exec(ctx, planDeleteSQL, tenantID, siteID, planID, operatorID, reason)
	return mapCatalogueErr(err)
}

// mapCatalogueErr turns the functions' documented refusals into typed errors and leaves everything else as-is.
// A foreign-key violation is the schema's own backstop catching a reference no blocker counted: still "in use".
func mapCatalogueErr(err error) error {
	if err == nil {
		return nil
	}
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return err
	}
	switch {
	case pe.Code == "42883": // undefined_function: the database predates 0091
		return ErrCatalogueDeleteUnavailable
	case pe.Code == "23001", pe.Code == "23503": // restrict_violation (…_IN_USE), foreign_key_violation
		return ErrCatalogueInUse
	case pe.Code == "P0002": // no_data_found (…_NOT_FOUND)
		return ErrCommerceNotFound
	case pe.Code == "23514" && strings.HasPrefix(pe.Message, "CATALOGUE_DELETE_NEEDS_A_REASON"):
		return ErrCatalogueDeleteNeedsReason
	}
	return err
}
