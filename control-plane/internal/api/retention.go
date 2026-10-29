package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/audit"
)

// Central's retention periods are bounded settings. central_retention_apply() in the database holds the rules in
// one place; ApplyRetention runs it hourly from the housekeeping loop.

type retentionSettingsView struct {
	AuditLogMonths        int           `json:"audit_log_months"`
	AuditLog              settingBounds `json:"audit_log_months_bounds"`
	HistoryPersonalMonths int           `json:"history_personal_months"`
	HistoryPersonal       settingBounds `json:"history_personal_months_bounds"`
	HistoryYears          int           `json:"history_years"`
	History               settingBounds `json:"history_years_bounds"`
	DisabledAccountMonths int           `json:"disabled_account_months"`
	DisabledAccount       settingBounds `json:"disabled_account_months_bounds"`
	UpdatedAt             *time.Time    `json:"updated_at"`
	UpdatedBy             *string       `json:"updated_by"`
}

// The bounds match the CHECK constraints in the database.
var (
	retentionAuditLog        = settingBounds{Default: 24, Min: 6, Max: 120, Unit: "months"}
	retentionHistoryPersonal = settingBounds{Default: 24, Min: 6, Max: 120, Unit: "months"}
	retentionHistory         = settingBounds{Default: 6, Min: 1, Max: 10, Unit: "years"}
	retentionDisabledAccount = settingBounds{Default: 24, Min: 1, Max: 120, Unit: "months"}
)

// ForgetQueuedAuditEmails scrubs the emails central_forget_actor queued from the audit log payloads (migration 0063,
// audit P3 L2). It runs outside the transaction that removed the user; a failure is only logged, the queue keeps the
// email and the next housekeeping pass tries again.
func ForgetQueuedAuditEmails(ctx context.Context, b *Base) {
	if _, err := b.DB.Exec(ctx, `SELECT central_forget_audit_apply()`); err != nil {
		slog.Warn("audit scrub of removed users failed; it is retried by the next housekeeping pass", "err", err)
	}
}

func ApplyRetention(ctx context.Context, b *Base) (map[string]int64, error) {
	var raw []byte
	if err := b.DB.QueryRow(ctx, `SELECT central_retention_apply()`).Scan(&raw); err != nil {
		return nil, err
	}
	// Removed users' emails queued by accounts this run anonymised (and any a failed request left behind).
	ForgetQueuedAuditEmails(ctx, b)
	out := map[string]int64{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	for k, n := range out {
		if n == 0 {
			delete(out, k)
		}
	}
	return out, nil
}

func (b *Base) loadRetentionSettings(r *http.Request) (retentionSettingsView, error) {
	ctx, cancel := DBCtx(r)
	defer cancel()
	v := retentionSettingsView{AuditLog: retentionAuditLog, HistoryPersonal: retentionHistoryPersonal,
		History: retentionHistory, DisabledAccount: retentionDisabledAccount}
	var by string
	var at time.Time
	err := b.DB.QueryRow(ctx, `SELECT audit_log_months, history_personal_months, history_years, disabled_account_months,
                                      updated_at, COALESCE(updated_by,'')
                                 FROM central_retention_settings WHERE id`).Scan(&v.AuditLogMonths,
		&v.HistoryPersonalMonths, &v.HistoryYears, &v.DisabledAccountMonths, &at, &by)
	if err != nil {
		return v, err
	}
	v.UpdatedAt, v.UpdatedBy = &at, strPtr(by)
	return v, nil
}

// getRetentionSettings: GET /cloud/v1/settings/retention
func (b *Base) getRetentionSettings(w http.ResponseWriter, r *http.Request) {
	v, err := b.loadRetentionSettings(r)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its retention settings.")
		return
	}
	WriteJSON(w, http.StatusOK, v)
}

// patchRetentionSettings: PATCH /cloud/v1/settings/retention; omitted values keep their setting, and new periods
// apply at the next hourly run.
func (b *Base) patchRetentionSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AuditLogMonths        *int   `json:"audit_log_months"`
		HistoryPersonalMonths *int   `json:"history_personal_months"`
		HistoryYears          *int   `json:"history_years"`
		DisabledAccountMonths *int   `json:"disabled_account_months"`
		Reason                string `json:"reason"`
	}
	if err := DecodeJSON(r, &in); err != nil || (in.AuditLogMonths == nil && in.HistoryPersonalMonths == nil &&
		in.HistoryYears == nil && in.DisabledAccountMonths == nil) {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "at least one setting is required")
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		Fail(w, r, http.StatusBadRequest, CodeBadRequest, "reason is required")
		return
	}
	before, err := b.loadRetentionSettings(r)
	if err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "Central could not read its retention settings.")
		return
	}
	after := before
	payload := map[string]any{"reason": in.Reason}
	for _, f := range []struct {
		key   string
		in    *int
		cur   *int
		was   int
		b     settingBounds
		label string
	}{
		{"audit_log_months", in.AuditLogMonths, &after.AuditLogMonths, before.AuditLogMonths, retentionAuditLog, "The audit log period"},
		{"history_personal_months", in.HistoryPersonalMonths, &after.HistoryPersonalMonths, before.HistoryPersonalMonths, retentionHistoryPersonal, "The period before people are removed from the history"},
		{"history_years", in.HistoryYears, &after.HistoryYears, before.HistoryYears, retentionHistory, "The history period"},
		{"disabled_account_months", in.DisabledAccountMonths, &after.DisabledAccountMonths, before.DisabledAccountMonths, retentionDisabledAccount, "The disabled account period"},
	} {
		if f.in == nil {
			continue
		}
		if *f.in < f.b.Min || *f.in > f.b.Max {
			Fail(w, r, http.StatusBadRequest, CodeBadRequest,
				f.label+" must be between "+strconv.Itoa(f.b.Min)+" and "+strconv.Itoa(f.b.Max)+" "+f.b.Unit+".")
			return
		}
		*f.cur = *f.in
		if *f.in != f.was {
			payload[f.key] = map[string]int{"from": f.was, "to": *f.in}
		}
	}
	_, email := actorOf(r)
	ctx, cancel := DBCtx(r)
	defer cancel()
	if _, err := b.DB.Exec(ctx, `UPDATE central_retention_settings
                                    SET audit_log_months=$1, history_personal_months=$2, history_years=$3,
                                        disabled_account_months=$4, updated_at=now(), updated_by=$5
                                  WHERE id`, after.AuditLogMonths, after.HistoryPersonalMonths, after.HistoryYears,
		after.DisabledAccountMonths, email); err != nil {
		Fail(w, r, http.StatusInternalServerError, CodeInternal, "The setting could not be saved. Try again.")
		return
	}
	audit.Op(r.Context(), b.DB, r, "settings.retention_updated", "settings", "retention", payload)
	b.getRetentionSettings(w, r)
}
