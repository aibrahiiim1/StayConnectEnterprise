package main

// How long guest personal data is kept before scd's hourly run anonymises it (sign-in attempts and system logs
// are deleted instead). Shortening a period anonymises history on the next run, so the setting sits under the
// "backups" key and asks for the password. Defaults come from iam_v2.data_retention_defaults; the bounds below
// mirror the table CHECKs, which are the authority.

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

type retentionBound struct {
	Key  string
	Min  int
	Max  int
	Unit string
}

// dataRetentionBounds is the order the periods are shown and validated in.
var dataRetentionBounds = []retentionBound{
	{"pms_stay_days", 7, 3650, "days after checkout"},
	{"client_activity_days", 30, 3650, "days after the device was last seen or the session ended"},
	{"inactive_identity_days", 30, 3650, "days without any sign-in or use"},
	{"expired_record_days", 1, 365, "days after the record expired"},
	{"audit_log_days", 90, 3650, "days"},
	{"sign_in_attempt_days", 7, 365, "days (attempts are deleted, not anonymised)"},
	// Up to two years where local law asks for connection logs to be kept longer; scd removes them.
	{"log_days", 1, 730, "days (system logs are deleted)"},
	// Free text on PMS case decisions, device-slot actions and post-stay revocations is replaced after this period.
	{"staff_note_days", 30, 3650, "days (the note is removed, the record stays)"},
}

func (s *server) dataRetentionRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getDataRetention)
	r.Put("/", s.setDataRetention)
	// What the proposed periods would remove, counted by the retention functions themselves and rolled back (audit AD-67).
	r.Post("/preview", s.previewDataRetention)
	return r
}

const dataRetentionCols = `pms_stay_days, client_activity_days, inactive_identity_days, expired_record_days,
	audit_log_days, sign_in_attempt_days, log_days, staff_note_days`

func scanRetention(row interface{ Scan(...any) error }, extra ...any) (map[string]int, error) {
	v := make([]int, len(dataRetentionBounds))
	dest := make([]any, 0, len(v)+len(extra))
	for i := range v {
		dest = append(dest, &v[i])
	}
	if err := row.Scan(append(dest, extra...)...); err != nil {
		return nil, err
	}
	out := make(map[string]int, len(v))
	for i, b := range dataRetentionBounds {
		out[b.Key] = v[i]
	}
	return out, nil
}

func (s *server) getDataRetention(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "data retention")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	var updatedAt *time.Time
	var updatedBy *string
	settings, err := scanRetention(s.db.QueryRow(ctx, `SELECT `+dataRetentionCols+`, config_version, updated_at, updated_by
	     FROM iam_v2.data_retention_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID), &version, &updatedAt, &updatedBy)
	if err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have data retention yet")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the data retention could not be read")
		return
	}
	defaults, err := scanRetention(s.db.QueryRow(ctx, `SELECT `+dataRetentionCols+` FROM iam_v2.data_retention_defaults()`))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the data retention defaults could not be read")
		return
	}
	bounds := map[string]any{}
	for _, b := range dataRetentionBounds {
		bounds[b.Key] = map[string]any{"min": b.Min, "max": b.Max, "default": defaults[b.Key], "unit": b.Unit}
	}

	type change struct {
		ChangedAt time.Time      `json:"changed_at"`
		ChangedBy string         `json:"changed_by"`
		Reason    string         `json:"reason"`
		Old       map[string]any `json:"old"`
		New       map[string]any `json:"new"`
	}
	changes := []change{}
	if rows, err := s.db.Query(ctx, `SELECT changed_at, changed_by, change_reason, old_settings, new_settings
	     FROM iam_v2.data_retention_changes WHERE tenant_id=$1 AND site_id=$2 ORDER BY changed_at DESC LIMIT 20`,
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
		Category       string    `json:"category"`
		RetentionDays  int       `json:"retention_days"`
		Cutoff         time.Time `json:"cutoff"`
		RowsAnonymised int       `json:"rows_anonymised"`
	}
	runs := []run{}
	if rows, err := s.db.Query(ctx, `SELECT ran_at, category, retention_days, cutoff, rows_anonymised
	     FROM iam_v2.data_retention_runs WHERE tenant_id=$1 AND site_id=$2 ORDER BY ran_at DESC LIMIT 20`,
		s.tenantID, s.siteID); err == nil {
		for rows.Next() {
			var x run
			if rows.Scan(&x.RanAt, &x.Category, &x.RetentionDays, &x.Cutoff, &x.RowsAnonymised) == nil {
				runs = append(runs, x)
			}
		}
		rows.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": settings, "config_version": version, "updated_at": updatedAt, "updated_by": updatedBy,
		"bounds": bounds, "changes": changes, "runs": runs,
	})
}

// retentionValues validates a full set of proposed periods against the bounds; msg is the refusal when it is not valid.
func retentionValues(settings map[string]*int) (values []any, code, msg string) {
	values = make([]any, 0, len(dataRetentionBounds))
	for _, b := range dataRetentionBounds {
		v := settings[b.Key]
		if v == nil || *v < b.Min || *v > b.Max {
			return nil, "invalid_setting", b.Key + " is required and must be " + itoa(b.Min) + "-" + itoa(b.Max) + " " + b.Unit
		}
		values = append(values, *v)
	}
	if len(settings) != len(dataRetentionBounds) {
		return nil, "invalid_setting", "unknown retention setting in the request"
	}
	return values, "", ""
}

// retentionImpact is what the retention functions would remove, by category, under the periods in force and under the proposed ones.
type retentionImpact struct {
	Current    map[string]int `json:"current"`
	Proposed   map[string]int `json:"proposed"`
	Complete   bool           `json:"complete"`
	NotCounted []string       `json:"not_counted"`
}

// additional is what the change adds to what the periods in force would already remove: per category, never below zero.
func (im retentionImpact) additional() (total int, byCategory map[string]int) {
	byCategory = map[string]int{}
	for c, p := range im.Proposed {
		if d := p - im.Current[c]; d > 0 {
			byCategory[c] = d
			total += d
		}
	}
	return total, byCategory
}

// retentionShortened reports whether any proposed period is shorter than the one in force (unknown old values count as shortened).
func retentionShortened(old map[string]int, values []any) bool {
	if old == nil {
		return true
	}
	for i, b := range dataRetentionBounds {
		if values[i].(int) < old[b.Key] {
			return true
		}
	}
	return false
}

func (s *server) retentionPreview(ctx context.Context, values []any) (retentionImpact, error) {
	var raw []byte
	args := append([]any{s.tenantID, s.siteID}, values...)
	if err := s.db.QueryRow(ctx, `SELECT public.data_retention_preview($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10)::text`, args...).Scan(&raw); err != nil {
		return retentionImpact{}, err
	}
	var im retentionImpact
	if err := json.Unmarshal(raw, &im); err != nil {
		return retentionImpact{}, err
	}
	return im, nil
}

// previewDataRetention answers "what would these periods remove?" without changing anything: no password (nothing is written), but
// the same permission as the change.
func (s *server) previewDataRetention(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Settings map[string]*int `json:"settings"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	values, code, msg := retentionValues(in.Settings)
	if values == nil {
		jsonErr(w, http.StatusBadRequest, code, msg)
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet, so there is nothing to preview")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	old, err := scanRetention(s.db.QueryRow(ctx, `SELECT `+dataRetentionCols+` FROM iam_v2.data_retention_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID))
	if err != nil && !isNoRows(err) {
		jsonErr(w, http.StatusServiceUnavailable, "read_failed", "the current retention periods could not be read")
		return
	}
	im, err := s.retentionPreview(ctx, values)
	if err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database cannot preview a retention change yet")
			return
		}
		jsonErr(w, http.StatusServiceUnavailable, "impact_unavailable", "the effect of these periods could not be worked out, so none was shown")
		return
	}
	total, by := im.additional()
	writeJSON(w, http.StatusOK, map[string]any{"impact": im, "shortens": retentionShortened(old, values),
		"additional_records": total, "additional_by_category": by})
}

func (s *server) setDataRetention(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Settings map[string]*int `json:"settings"`
		Reason   string          `json:"reason"`
		Password string          `json:"password"`
		// ConfirmRecords is the second confirmation of a SHORTENING (audit AD-67): the number of additional records the preview
		// showed. A shortening that would remove more is refused until the request repeats the number the appliance now counts.
		ConfirmRecords *int `json:"confirm_records"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	values, code, msg := retentionValues(in.Settings)
	if values == nil {
		jsonErr(w, http.StatusBadRequest, code, msg)
		return
	}
	reason := settingReason(in.Reason)
	if !validReason(reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet, so nothing can be saved")
		return
	}
	actor, ok := s.stepUp(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// The periods being replaced go into the audit; an unreadable "old" must not be recorded as null (audit P9 L14).
	old, err := scanRetention(s.db.QueryRow(ctx, `SELECT `+dataRetentionCols+` FROM iam_v2.data_retention_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID))
	if err != nil && !isNoRows(err) {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have data retention yet")
			return
		}
		jsonErr(w, http.StatusServiceUnavailable, "read_failed", "the current retention periods could not be read, so nothing was changed")
		return
	}
	// A shortening shows what it will remove first, and is made only on a second confirmation of that number (audit AD-67).
	impactConfirmed := -1
	if retentionShortened(old, values) {
		pctx, pcancel := context.WithTimeout(r.Context(), 100*time.Second) // two rolled-back runs, each bounded at 45 s in the database
		im, perr := s.retentionPreview(pctx, values)
		pcancel()
		if perr != nil {
			jsonErr(w, http.StatusServiceUnavailable, "impact_unavailable",
				"The effect of these shorter periods could not be worked out, so nothing was changed. Try again in a moment.")
			return
		}
		total, by := im.additional()
		if total > 0 {
			if in.ConfirmRecords == nil || *in.ConfirmRecords != total {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "impact_confirmation_required",
					"message": "These shorter periods will anonymise or delete " + itoa(total) + " more record" + map[bool]string{true: "", false: "s"}[total == 1] +
						" on the next hourly run. This cannot be undone. Confirm the number to go ahead.",
					"impact": im, "additional_records": total, "additional_by_category": by})
				return
			}
			impactConfirmed = total
		}
	}
	args := append([]any{s.tenantID, s.siteID}, values...)
	args = append(args, actor, reason)
	var version int64
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.data_retention_set($1::uuid,$2::uuid,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		args...).Scan(&version); err != nil {
		if isUndefinedObject(err) {
			jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have data retention yet")
			return
		}
		if isSettingRefusal(err) {
			jsonErr(w, http.StatusBadRequest, "invalid_setting", "the database refused these retention periods")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "update_failed", "the data retention could not be saved")
		return
	}
	saved := map[string]int{}
	for i, b := range dataRetentionBounds {
		saved[b.Key] = values[i].(int)
	}
	auditPayload := map[string]any{"old": old, "new": saved, "config_version": version, "reason": reason}
	if impactConfirmed >= 0 {
		auditPayload["impact_confirmed_records"] = impactConfirmed
	}
	s.audit(r, "data_retention.changed", "data_retention", s.siteID, auditPayload)
	writeJSON(w, http.StatusOK, map[string]any{"settings": saved, "config_version": version})
}
