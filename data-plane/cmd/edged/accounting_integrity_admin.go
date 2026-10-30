package main

// Read-only operator view of iam_v2.accounting_integrity_summary(tenant, site) (migration 0181).
//
// The accounting chooses the CONSERVATIVE direction instead of an exact figure in a few defined cases (a retroactive session
// binding older than the raw retention, a checkpoint lifted to the exact figure, an estimated 0180 backfill bucket). Each case is
// recorded as an integrity event; this endpoint reports them per kind so an operator can see whether any guest's data usage at
// this site is an upper bound rather than sample-exact. Empty means every figure is exact. It returns counts and byte totals only:
// no guest, device, session or entitlement identifier.

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

func (s *server) accountingIntegrityRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getAccountingIntegrity)
	return r
}

type accountingIntegrityKind struct {
	Kind         string     `json:"kind"`
	Events       int64      `json:"events"`
	Entitlements int64      `json:"entitlements"`
	Bytes        int64      `json:"bytes"`
	LastAt       *time.Time `json:"last_at"`
}

func (s *server) getAccountingIntegrity(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "accounting integrity")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT kind, events, entitlements, bytes, last_at
	     FROM iam_v2.accounting_integrity_summary($1::uuid,$2::uuid) ORDER BY kind`, s.tenantID, s.siteID)
	if err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have the accounting integrity summary yet")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the accounting integrity summary could not be read")
		return
	}
	defer rows.Close()
	kinds := []accountingIntegrityKind{}
	for rows.Next() {
		var k accountingIntegrityKind
		if err := rows.Scan(&k.Kind, &k.Events, &k.Entitlements, &k.Bytes, &k.LastAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the accounting integrity summary could not be read")
			return
		}
		kinds = append(kinds, k)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the accounting integrity summary could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		// exact: no estimate and no conservative lift has been recorded for this site; every usage figure is sample-exact.
		"exact": len(kinds) == 0,
		"kinds": kinds,
		"note":  "Each kind is a case where the accounting counted an upper bound instead of an exact figure, so a guest was never given more allowance than they used. Empty means every figure is exact.",
	})
}
