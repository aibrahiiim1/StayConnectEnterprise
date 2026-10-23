package main

// Retention of per-sample accounting detail (iam_v2.accounting_records): 30-3650 days or unlimited (nothing
// purged). acctd's hourly purge removes only detail of ended sessions whose entitlements have all ended;
// entitlement and session totals are never touched. Shortening removes history, so the setting sits under the
// "backups" key and asks for the password.

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	accountingRetentionMinDays = 30
	accountingRetentionMaxDays = 3650
	// accountingRetentionDefaultDays mirrors iam_v2.accounting_retention_default(), which decides.
	accountingRetentionDefaultDays = 365
)

func (s *server) accountingRetentionRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getAccountingRetention)
	r.Put("/", s.setAccountingRetention)
	return r
}

func (s *server) getAccountingRetention(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "accounting retention")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var days *int
	var version int64
	var updatedAt *time.Time
	var updatedBy *string
	if err := s.db.QueryRow(ctx, `SELECT detail_retention_days, config_version, updated_at, updated_by
	     FROM iam_v2.accounting_retention_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).
		Scan(&days, &version, &updatedAt, &updatedBy); err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have accounting retention yet")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the accounting retention could not be read")
		return
	}
	type change struct {
		ChangedAt time.Time `json:"changed_at"`
		ChangedBy string    `json:"changed_by"`
		Reason    string    `json:"reason"`
		Old       *int      `json:"old"`
		New       *int      `json:"new"`
	}
	changes := []change{}
	if rows, err := s.db.Query(ctx, `SELECT changed_at, changed_by, change_reason, old_retention_days, new_retention_days
	     FROM iam_v2.accounting_retention_changes WHERE tenant_id=$1 AND site_id=$2 ORDER BY changed_at DESC LIMIT 20`,
		s.tenantID, s.siteID); err == nil {
		for rows.Next() {
			var c change
			if rows.Scan(&c.ChangedAt, &c.ChangedBy, &c.Reason, &c.Old, &c.New) == nil {
				changes = append(changes, c)
			}
		}
		rows.Close()
	}
	type run struct {
		RanAt          time.Time `json:"ran_at"`
		RetentionDays  int       `json:"retention_days"`
		Cutoff         time.Time `json:"cutoff"`
		RecordsDeleted int       `json:"records_deleted"`
	}
	runs := []run{}
	if rows, err := s.db.Query(ctx, `SELECT ran_at, retention_days, cutoff, records_deleted
	     FROM iam_v2.accounting_purge_runs WHERE tenant_id=$1 AND site_id=$2 ORDER BY ran_at DESC LIMIT 20`,
		s.tenantID, s.siteID); err == nil {
		for rows.Next() {
			var x run
			if rows.Scan(&x.RanAt, &x.RetentionDays, &x.Cutoff, &x.RecordsDeleted) == nil {
				runs = append(runs, x)
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": map[string]any{"detail_retention_days": days}, "config_version": version,
		"updated_at": updatedAt, "updated_by": updatedBy,
		"bounds": map[string]any{"detail_retention_days": map[string]any{
			"min": accountingRetentionMinDays, "max": accountingRetentionMaxDays, "default": accountingRetentionDefaultDays,
			"unit": "days; empty = unlimited (nothing is purged)"}},
		"changes": changes, "purge_runs": runs,
	})
}

func (s *server) setAccountingRetention(w http.ResponseWriter, r *http.Request) {
	var in struct {
		// nil means unlimited, which must be stated explicitly via Unlimited, never implied by a missing field.
		DetailRetentionDays *int   `json:"detail_retention_days"`
		Unlimited           bool   `json:"unlimited"`
		Reason              string `json:"reason"`
		Password            string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.Unlimited == (in.DetailRetentionDays != nil) {
		jsonErr(w, http.StatusBadRequest, "invalid_setting",
			"send either detail_retention_days (30-3650) or unlimited: true")
		return
	}
	if in.DetailRetentionDays != nil &&
		(*in.DetailRetentionDays < accountingRetentionMinDays || *in.DetailRetentionDays > accountingRetentionMaxDays) {
		jsonErr(w, http.StatusBadRequest, "invalid_setting", "detail_retention_days must be 30-3650, or unlimited")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet, so nothing can be saved")
		return
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var old *int
	_ = s.db.QueryRow(ctx, `SELECT detail_retention_days FROM iam_v2.accounting_retention_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&old)
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.accounting_retention_set($1::uuid,$2::uuid,$3,$4,$5)`,
		s.tenantID, s.siteID, in.DetailRetentionDays, actor, reason).Scan(&version); err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have accounting retention yet")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the accounting retention could not be saved")
		return
	}
	s.audit(r, "accounting_retention.changed", "accounting_retention", s.siteID, map[string]any{
		"old": old, "new": in.DetailRetentionDays, "config_version": version, "reason": reason})
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": map[string]any{"detail_retention_days": in.DetailRetentionDays}, "config_version": version})
}
