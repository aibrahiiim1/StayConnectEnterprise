package main

// CLOUD SYNC — how long delivered records are kept, and the one action that rescues records the appliance
// gave up on.
//
// TWO KEYS, NOT ONE. Changing a retention period and releasing nine thousand abandoned records back onto the
// wire are different powers with different blast radii, so `cloud-sync-settings` and `cloud-sync-recovery`
// are separate permissions and neither implies the other. The same reasoning that split the guest sign-in
// policy from the release of one device's restriction.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Bounds live here as well as in the CHECK constraint and the definer function, and are sent to the browser
// so the form validates against the server's numbers instead of a copy that can drift.
const (
	minRetentionDays = 1
	maxRetentionDays = 365
	// defaultRetentionDays is the approved default. It is repeated from the migration deliberately: this is
	// what the screen SAYS the standard is, and a test asserts the two agree.
	defaultRetentionDays = 30

	// maxRecoveryBatch is the largest number of records one operator action may release. The database
	// function refuses more; this is the same number said earlier, in a message an operator can read.
	maxRecoveryBatch     = 20000
	defaultRecoveryBatch = 1000
)

type cloudSyncSettingsOut struct {
	DeliveredRetentionDays int  `json:"delivered_retention_days"`
	IsDefault              bool `json:"is_default"`
	Limits                 struct {
		MinDays int `json:"min_days"`
		MaxDays int `json:"max_days"`
	} `json:"limits"`
	LastChange *cloudSyncChangeOut `json:"last_change,omitempty"`
}

type cloudSyncChangeOut struct {
	ChangedAt time.Time `json:"changed_at"`
	ChangedBy string    `json:"changed_by"`
	Reason    string    `json:"reason,omitempty"`
	OldDays   *int      `json:"old_delivered_retention_days,omitempty"`
	NewDays   int       `json:"new_delivered_retention_days"`
	Version   int64     `json:"new_config_version"`
}

func (s *server) cloudSyncSettingsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getCloudSyncSettings)
	r.Put("/", s.putCloudSyncSettings)
	return r
}

func (s *server) cloudSyncRecoveryRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getCloudSyncRecovery)
	r.Post("/", s.postCloudSyncRecovery)
	return r
}

func (s *server) effectiveCloudSyncSettings(r *http.Request) (cloudSyncSettingsOut, error) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	var out cloudSyncSettingsOut
	// The SAME function scd's retention pass reads. A screen that could disagree with the thing it describes
	// is worse than no screen.
	if err := s.db.QueryRow(ctx,
		`SELECT delivered_retention_days, is_default
		   FROM iam_v2.cloud_sync_settings_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&out.DeliveredRetentionDays, &out.IsDefault); err != nil {
		return out, err
	}
	out.Limits.MinDays = minRetentionDays
	out.Limits.MaxDays = maxRetentionDays

	var c cloudSyncChangeOut
	var reason *string
	err := s.db.QueryRow(ctx, `
		SELECT changed_at, changed_by, reason,
		       old_delivered_retention_days, new_delivered_retention_days, new_config_version
		  FROM iam_v2.cloud_sync_settings_changes
		 WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY changed_at DESC LIMIT 1`, s.tenantID, s.siteID).
		Scan(&c.ChangedAt, &c.ChangedBy, &reason, &c.OldDays, &c.NewDays, &c.Version)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return out, nil
	case err != nil:
		return out, err
	}
	if reason != nil {
		c.Reason = *reason
	}
	out.LastChange = &c
	return out, nil
}

func (s *server) getCloudSyncSettings(w http.ResponseWriter, r *http.Request) {
	out, err := s.effectiveCloudSyncSettings(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// putCloudSyncSettings saves the retention period.
//
// IT REACHES DELIVERED RECORDS ONLY, and the screen says so in a sentence rather than a footnote. A hotel
// administrator reading "keep records for 30 days" beside a queue of 77 000 undelivered ones would
// reasonably conclude that the queue is about to be tidied up; it is not, and the difference between
// "delivered records are removed after 30 days" and "the backlog gets deleted" is a fleet's worth of
// telemetry.
func (s *server) putCloudSyncSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DeliveredRetentionDays int    `json:"delivered_retention_days"`
		Reason                 string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if in.DeliveredRetentionDays < minRetentionDays || in.DeliveredRetentionDays > maxRetentionDays {
		jsonErr(w, http.StatusBadRequest, "invalid_retention",
			"Keep delivered records for between "+strconv.Itoa(minRetentionDays)+" and "+
				strconv.Itoa(maxRetentionDays)+" days.")
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return
	}

	before, err := s.effectiveCloudSyncSettings(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", err.Error())
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.cloud_sync_settings_set($1::uuid,$2::uuid,$3,$4,NULLIF($5,''))`,
		s.tenantID, s.siteID, in.DeliveredRetentionDays, actor, strings.TrimSpace(in.Reason)).
		Scan(&version); err != nil {
		jsonErr(w, http.StatusInternalServerError, "settings_not_saved", err.Error())
		return
	}

	s.audit(r, "cloud_sync_settings.update", "site", s.siteID, map[string]any{
		"previous": map[string]any{
			"delivered_retention_days": before.DeliveredRetentionDays,
			"was_default":              before.IsDefault,
		},
		"new":    map[string]any{"delivered_retention_days": in.DeliveredRetentionDays},
		"reason": strings.TrimSpace(in.Reason),
	})

	out, err := s.effectiveCloudSyncSettings(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "settings_unreadable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// getCloudSyncRecovery lists what has already been recovered. A recovery that left records behind is the
// normal case — the batches are bounded — so the history is how an operator knows whether to run it again.
func (s *server) getCloudSyncRecovery(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT requested_at, requested_by, reason, rows_recovered,
		       seq_from, seq_to, oldest_created_at, exhausted_remaining
		  FROM public.sync_outbox_recovery_log
		 ORDER BY requested_at DESC LIMIT 50`)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "recovery_log_unreadable", err.Error())
		return
	}
	defer rows.Close()

	type entry struct {
		RequestedAt     time.Time  `json:"requested_at"`
		RequestedBy     string     `json:"requested_by"`
		Reason          string     `json:"reason"`
		Recovered       int        `json:"recovered"`
		SeqFrom         *int64     `json:"seq_from,omitempty"`
		SeqTo           *int64     `json:"seq_to,omitempty"`
		OldestCreatedAt *time.Time `json:"oldest_created_at,omitempty"`
		Remaining       int64      `json:"exhausted_remaining"`
	}
	out := []entry{}
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.RequestedAt, &e.RequestedBy, &e.Reason, &e.Recovered,
			&e.SeqFrom, &e.SeqTo, &e.OldestCreatedAt, &e.Remaining); err != nil {
			jsonErr(w, http.StatusInternalServerError, "recovery_log_unreadable", err.Error())
			return
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveries": out})
}

// postCloudSyncRecovery releases a bounded batch of abandoned records back onto the queue.
//
// IT DELETES NOTHING AND SENDS NOTHING. It clears the flag that made the drain loop skip these records and
// resets their retry budget; the ordinary drain then publishes them in sequence order, which for this
// appliance's abandoned block means oldest first and ahead of everything still waiting. The far end keys on
// (appliance, sequence) inside one transaction, so a record that arrives twice is recorded once.
func (s *server) postCloudSyncRecovery(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
		Limit  int    `json:"limit"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if len(reason) < 3 {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"Say why these records are being recovered — it is recorded with your name.")
		return
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultRecoveryBatch
	}
	if limit < 1 || limit > maxRecoveryBatch {
		jsonErr(w, http.StatusBadRequest, "invalid_limit",
			"Recover between 1 and "+strconv.Itoa(maxRecoveryBatch)+" records at a time.")
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the recovery could not be attributed to an operator")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	var recovered int
	var from, to *int64
	var remaining int64
	if err := s.db.QueryRow(ctx,
		`SELECT rows_recovered, seq_from, seq_to, exhausted_remaining
		   FROM public.sync_outbox_recover_exhausted($1,$2,$3)`,
		actor, reason, limit).Scan(&recovered, &from, &to, &remaining); err != nil {
		jsonErr(w, http.StatusInternalServerError, "recovery_failed", err.Error())
		return
	}

	s.audit(r, "cloud_sync_recovery.run", "site", s.siteID, map[string]any{
		"recovered": recovered, "seq_from": from, "seq_to": to,
		"exhausted_remaining": remaining, "batch_limit": limit, "reason": reason,
	})

	note := "Nothing needed recovering."
	if recovered > 0 {
		note = "Returned to the queue. They are sent in the order they were recorded, oldest first, " +
			"and the cloud records each one once however many times it arrives."
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"recovered": recovered, "seq_from": from, "seq_to": to,
		"exhausted_remaining": remaining, "note": note,
	})
}
