package main

// Phase-3 Hotel-Admin surface: Stays, Stay Events (including the review queue), Folios, PMS resolutions,
// Checkout-Grace configuration and OPERATIONAL ALERTS.
//
// Two rules shape everything here:
//
//   - DARK BY DEFAULT. These routes are mounted only when the Phase-3 master flag AND its admin flag are both
//     ON. While dark the appliance issues zero Phase-3 SQL and the paths simply do not exist (404), rather
//     than existing and returning "disabled" — an unmounted route cannot leak a schema that is not live yet.
//   - NO GUEST PII IN RESOLUTION EVIDENCE. Resolution rows expose outcome codes and identifiers only; a guest's
//     name or room never appears in an operator's resolution list. Stay detail is a different, role-gated
//     surface where the operator is legitimately looking at one guest's stay.

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ---------- Stays ----------

type stayRow struct {
	ID                  string     `json:"id"`
	Interface           string     `json:"pms_interface_id"`
	Reservation         string     `json:"external_reservation_id"`
	Room                *string    `json:"room,omitempty"`
	Status              string     `json:"status"`
	LifecycleVersion    int        `json:"lifecycle_version"`
	Arrival             *time.Time `json:"arrival,omitempty"`
	Departure           *time.Time `json:"departure,omitempty"`
	EffectiveCheckoutAt *time.Time `json:"effective_checkout_at,omitempty"`
	PostingAllowed      bool       `json:"posting_allowed"`
	Occupants           int        `json:"occupants"`
}

const stayCols = `s.id::text, s.pms_interface_id::text, s.external_reservation_id,
       s.normalized_room_number, s.status, s.lifecycle_version, s.arrival, s.departure,
       s.effective_checkout_at, s.posting_allowed,
       (SELECT count(*) FROM iam_v2.stay_guests g WHERE g.stay_id = s.id)::int`

func scanStay(row interface{ Scan(...any) error }, e *stayRow) error {
	return row.Scan(&e.ID, &e.Interface, &e.Reservation, &e.Room, &e.Status, &e.LifecycleVersion,
		&e.Arrival, &e.Departure, &e.EffectiveCheckoutAt, &e.PostingAllowed, &e.Occupants)
}

func (s *server) pmsStaysRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listStays)
	r.Get("/{id}", s.getStay)
	return r
}

func (s *server) listStays(w http.ResponseWriter, r *http.Request) {
	var statusArg any
	if v := r.URL.Query().Get("status"); v != "" {
		switch v {
		case "RESERVED", "IN_HOUSE", "CHECKED_OUT", "POST_STAY_ACTIVE", "CANCELLED", "NO_SHOW":
			statusArg = v
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request", "unknown stay status")
			return
		}
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT `+stayCols+` FROM iam_v2.stays s
		WHERE s.tenant_id=$1 AND ($2::text IS NULL OR s.status=$2)
		ORDER BY s.updated_at DESC NULLS LAST, s.id LIMIT 200`, s.tenantID, statusArg)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []stayRow{}
	for rows.Next() {
		var e stayRow
		if err := scanStay(rows, &e); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	writeList(w, out)
}

type stayOccupant struct {
	Display   *string `json:"display_name,omitempty"`
	IsPrimary bool    `json:"is_primary"`
}

type stayFolio struct {
	ExternalID string `json:"external_folio_id"`
	Kind       string `json:"folio_kind"`
	Status     string `json:"status"`
	IsDefault  bool   `json:"is_default_posting_target"`
}

type stayDetail struct {
	stayRow
	Occupants []stayOccupant `json:"occupant_list"`
	Folios    []stayFolio    `json:"folios"`
}

func (s *server) getStay(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()
	var d stayDetail
	err := scanStay(s.db.QueryRow(ctx, `SELECT `+stayCols+` FROM iam_v2.stays s
		WHERE s.id=$1 AND s.tenant_id=$2`, id, s.tenantID), &d.stayRow)
	if isNoRows(err) {
		jsonErr(w, http.StatusNotFound, "not_found", "stay not found")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	d.Occupants = []stayOccupant{}
	rows, err := s.db.Query(ctx, `SELECT display_name, is_primary FROM iam_v2.stay_guests
		WHERE stay_id=$1 ORDER BY is_primary DESC, display_name NULLS LAST`, id)
	if err == nil {
		for rows.Next() {
			var o stayOccupant
			if rows.Scan(&o.Display, &o.IsPrimary) == nil {
				d.Occupants = append(d.Occupants, o)
			}
		}
		rows.Close()
	}
	d.Folios = []stayFolio{}
	frows, err := s.db.Query(ctx, `SELECT f.external_folio_id, f.folio_kind, f.status, sf.is_default_posting_target
		FROM iam_v2.stay_folios sf JOIN iam_v2.folios f ON f.id=sf.folio_id
		WHERE sf.stay_id=$1 ORDER BY sf.is_default_posting_target DESC, f.external_folio_id`, id)
	if err == nil {
		for frows.Next() {
			var f stayFolio
			if frows.Scan(&f.ExternalID, &f.Kind, &f.Status, &f.IsDefault) == nil {
				d.Folios = append(d.Folios, f)
			}
		}
		frows.Close()
	}
	writeJSON(w, http.StatusOK, d)
}

// ---------- Stay events (including the MANUAL_REVIEW queue) ----------

type stayEventRow struct {
	ID         string     `json:"id"`
	Interface  string     `json:"pms_interface_id"`
	Identity   string     `json:"external_event_identity"`
	Type       string     `json:"event_type"`
	Status     string     `json:"processing_status"`
	ReviewCode *string    `json:"review_code,omitempty"`
	StayID     *string    `json:"stay_id,omitempty"`
	PMSAt      *time.Time `json:"pms_timestamp_utc,omitempty"`
	ReceivedAt time.Time  `json:"received_at"`
}

func (s *server) pmsEventsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listStayEvents)
	return r
}

func (s *server) listStayEvents(w http.ResponseWriter, r *http.Request) {
	var statusArg any
	if v := r.URL.Query().Get("processing_status"); v != "" {
		switch v {
		case "PENDING", "APPLIED", "SKIPPED_DUPLICATE", "MANUAL_REVIEW", "REJECTED":
			statusArg = v
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request", "unknown processing status")
			return
		}
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT id::text, pms_interface_id::text, external_event_identity, event_type,
			processing_status, review_code, stay_id::text, pms_timestamp_utc, received_at
		FROM iam_v2.stay_events
		WHERE tenant_id=$1 AND ($2::text IS NULL OR processing_status=$2)
		ORDER BY received_at DESC LIMIT 200`, s.tenantID, statusArg)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []stayEventRow{}
	for rows.Next() {
		var e stayEventRow
		if err := rows.Scan(&e.ID, &e.Interface, &e.Identity, &e.Type, &e.Status, &e.ReviewCode,
			&e.StayID, &e.PMSAt, &e.ReceivedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	writeList(w, out)
}

// ---------- PMS resolutions (evidence only, never guest PII) ----------

type resolutionRow struct {
	ID           string    `json:"id"`
	GuestNetwork string    `json:"guest_network_id"`
	Outcome      string    `json:"outcome_code"`
	Resolved     bool      `json:"resolved"`
	ResolvedAt   time.Time `json:"resolved_at"`
}

func (s *server) pmsResolutionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listResolutions)
	return r
}

func (s *server) listResolutions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	// deliberately NO stay identity, guest name, room or reservation: a resolution list is operational
	// evidence, and a failed resolution must not become a way to enumerate who is staying at the property.
	rows, err := s.db.Query(ctx, `SELECT id::text, guest_network_id::text, outcome_code,
			(resolved_stay_id IS NOT NULL), resolved_at
		FROM iam_v2.auth_resolutions WHERE tenant_id=$1
		ORDER BY resolved_at DESC LIMIT 200`, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []resolutionRow{}
	for rows.Next() {
		var e resolutionRow
		if err := rows.Scan(&e.ID, &e.GuestNetwork, &e.Outcome, &e.Resolved, &e.ResolvedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, e)
	}
	writeList(w, out)
}

// ---------- Checkout-Grace configuration ----------

type checkoutGraceConfig struct {
	PackageRevisionID    *string `json:"grace_package_revision_id,omitempty"`
	DurationSeconds      int     `json:"grace_duration_seconds"`
	DownKbps             int     `json:"grace_down_kbps"`
	UpKbps               int     `json:"grace_up_kbps"`
	DataQuotaBytes       int64   `json:"grace_data_quota_bytes"`
	DeviceLimit          int     `json:"grace_device_limit"`
	DeviceLimitPolicy    string  `json:"grace_device_limit_policy"`
	EligibilityWindowSec int     `json:"eligibility_window_seconds"`
	ConfigVersion        int     `json:"config_version"`
}

// checkoutGracePublishReq is what publishing REQUIRES beyond the policy itself: the version the operator was
// looking at, a password step-up, and a bounded reason. Publishing changes what every departing guest gets,
// so it is treated like the other privileged, destructive operations on this appliance.
type checkoutGracePublishReq struct {
	checkoutGraceConfig
	ExpectedConfigVersion *int   `json:"expected_config_version"`
	Password              string `json:"password"`
	ReasonCode            string `json:"reason_code"`
}

// supportedDeviceLimitPolicies lists what the enforcement path can actually honour. DISCONNECT_OLDEST and
// ADMIN_APPROVAL stay capability-disabled: offering them would let an operator publish a policy that silently
// degrades to something else at the boundary.
var supportedDeviceLimitPolicies = []string{"REJECT_NEW_DEVICE"}

func (s *server) checkoutGraceConfigRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getCheckoutGraceConfig)
	r.Put("/", s.putCheckoutGraceConfig)
	return r
}

func (s *server) getCheckoutGraceConfig(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var c checkoutGraceConfig
	err := s.db.QueryRow(ctx, `SELECT grace_package_revision_id::text, grace_duration_seconds, grace_down_kbps,
			grace_up_kbps, grace_data_quota_bytes, grace_device_limit, grace_device_limit_policy,
			eligibility_window_seconds, config_version
		FROM iam_v2.site_checkout_grace_config WHERE tenant_id=$1 AND site_id=$2`, s.tenantID, s.siteID).
		Scan(&c.PackageRevisionID, &c.DurationSeconds, &c.DownKbps, &c.UpKbps, &c.DataQuotaBytes,
			&c.DeviceLimit, &c.DeviceLimitPolicy, &c.EligibilityWindowSec, &c.ConfigVersion)
	if isNoRows(err) {
		// A site with nothing published yet is a starting point, not a failure: version 0 says exactly that,
		// and the operator can send it back as their expected version.
		writeJSON(w, http.StatusOK, map[string]any{
			"published":                 false,
			"config_version":            0,
			"supported_device_policies": supportedDeviceLimitPolicies,
		})
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"published":                 true,
		"config_version":            c.ConfigVersion,
		"supported_device_policies": supportedDeviceLimitPolicies,
		"policy":                    c,
	})
}

func (s *server) putCheckoutGraceConfig(w http.ResponseWriter, r *http.Request) {
	var in checkoutGracePublishReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if in.ExpectedConfigVersion == nil {
		jsonErr(w, http.StatusBadRequest, "expected_version_required",
			"expected_config_version is required so a concurrent publication cannot be silently overwritten")
		return
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason code is required to publish")
		return
	}
	// Step-up: publishing changes what every departing guest receives.
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}
	// Shape checks only — the database re-validates everything and owns the authoritative decision.
	if in.DurationSeconds <= 0 || in.DownKbps <= 0 || in.UpKbps <= 0 || in.DeviceLimit <= 0 || in.EligibilityWindowSec <= 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "duration, rates, device limit and eligibility window must be positive")
		return
	}
	supported := false
	for _, p := range supportedDeviceLimitPolicies {
		if in.DeviceLimitPolicy == p {
			supported = true
		}
	}
	if !supported {
		jsonErr(w, http.StatusBadRequest, "policy_unsupported",
			"only REJECT_NEW_DEVICE is implemented; the others are capability-disabled")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int
	err := s.db.QueryRow(ctx, `SELECT iam_v2.publish_checkout_grace_policy(
			$1,$2,$3::uuid,$4,$5,$6,$7,$8,$9,$10,$11,$12::uuid,$13)`,
		s.tenantID, s.siteID, in.PackageRevisionID, in.DurationSeconds, in.DownKbps, in.UpKbps,
		in.DataQuotaBytes, in.DeviceLimit, in.DeviceLimitPolicy, in.EligibilityWindowSec,
		*in.ExpectedConfigVersion, sess.OperatorID, in.ReasonCode).Scan(&version)
	if err != nil {
		status, code := graceFailureStatus(err.Error())
		jsonErr(w, status, code, "the checkout-grace policy was refused")
		return
	}
	s.audit(r, "checkout_grace.published", "site_checkout_grace_config", s.siteID,
		map[string]any{"config_version": version, "reason_code": in.ReasonCode})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}

// graceFailureStatus maps the controlled operation's bounded failure prefixes to HTTP. A version conflict is
// a 409 so the UI can reload and show the operator what actually changed instead of overwriting it.
func graceFailureStatus(msg string) (int, string) {
	switch {
	case strings.Contains(msg, "GRACE_VERSION_CONFLICT"):
		return http.StatusConflict, "version_conflict"
	case strings.Contains(msg, "GRACE_ACTOR_INVALID"):
		return http.StatusForbidden, "actor_invalid"
	case strings.Contains(msg, "GRACE_PACKAGE_INVALID"):
		return http.StatusBadRequest, "package_invalid"
	case strings.Contains(msg, "GRACE_POLICY_UNSUPPORTED"):
		return http.StatusBadRequest, "policy_unsupported"
	default:
		return http.StatusBadRequest, "bad_request"
	}
}

// ---------- Operational alerts ----------

type alertRow struct {
	AuditID          string    `json:"audit_id"`
	StayID           string    `json:"stay_id"`
	LifecycleVersion int       `json:"lifecycle_version"`
	AlertCode        string    `json:"alert_code"`
	Trigger          string    `json:"trigger"`
	ReasonCode       *string   `json:"reason_code,omitempty"`
	BoundaryAt       time.Time `json:"boundary_at"`
	ClockSuspect     bool      `json:"boundary_clock_suspect"`
	CreatedAt        time.Time `json:"created_at"`
	// State + Seq are what an operator must send back on the next action. Without them two operators can
	// each act on a stale view and one silently overwrites the other.
	State          string     `json:"state"`
	Seq            int64      `json:"seq"`
	StateChangedAt *time.Time `json:"state_changed_at,omitempty"`
}

func (s *server) operationalAlertsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listAlerts)
	r.Post("/{auditID}/acknowledge", s.alertAction("ACKNOWLEDGED"))
	r.Post("/{auditID}/resolve", s.alertAction("RESOLVED"))
	return r
}

func (s *server) listAlerts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	// the view returns only alerts whose lifecycle head is not RESOLVED — an operator's queue is what still
	// needs attention — and carries that head state so the next action can be optimistic.
	rows, err := s.db.Query(ctx, `SELECT audit_id::text, stay_id::text, lifecycle_version, alert_code, trigger,
			reason_code, boundary_at, boundary_clock_suspect, created_at, alert_state, alert_seq, state_changed_at
		FROM iam_v2.active_operational_alerts WHERE tenant_id=$1 AND site_id=$2
		ORDER BY created_at DESC LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []alertRow{}
	for rows.Next() {
		var a alertRow
		if err := rows.Scan(&a.AuditID, &a.StayID, &a.LifecycleVersion, &a.AlertCode, &a.Trigger,
			&a.ReasonCode, &a.BoundaryAt, &a.ClockSuspect, &a.CreatedAt,
			&a.State, &a.Seq, &a.StateChangedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		out = append(out, a)
	}
	writeList(w, out)
}

type alertActionReq struct {
	// ExpectedState is what the operator was looking at. It is REQUIRED: an action sent without it is an
	// action taken on an unknown world, and this API refuses to guess on the operator's behalf.
	ExpectedState string `json:"expected_state"`
	ReasonCode    string `json:"reason_code"`
}

// alertAction records an ACKNOWLEDGED or RESOLVED action through the ONE controlled database operation, which
// owns the lock, the contiguous sequence, the legal edges, the actor check and the optimistic state match.
// This handler's only jobs are to identify the operator and to map the operation's bounded failure prefixes
// onto the right HTTP status.
func (s *server) alertAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auditID := chi.URLParam(r, "auditID")
		var in alertActionReq
		if err := decodeJSON(r, &in); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
			return
		}
		switch in.ExpectedState {
		case "OPEN", "ACKNOWLEDGED":
		default:
			jsonErr(w, http.StatusBadRequest, "bad_request", "expected_state must be OPEN or ACKNOWLEDGED")
			return
		}
		sess := sessFrom(r.Context())
		if sess == nil || sess.OperatorID == "" {
			jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
			return
		}
		var reasonArg any
		if in.ReasonCode != "" {
			reasonArg = in.ReasonCode
		}
		ctx, cancel := dbCtx(r)
		defer cancel()
		var seq int64
		err := s.db.QueryRow(ctx, `SELECT iam_v2.record_alert_action($1,$2,$3::uuid,$4,$5::uuid,$6,$7)`,
			s.tenantID, s.siteID, auditID, action, sess.OperatorID, reasonArg, in.ExpectedState).Scan(&seq)
		if err != nil {
			status, code := alertFailureStatus(err.Error())
			jsonErr(w, status, code, "the alert action was refused")
			return
		}
		s.audit(r, "operational_alert."+strings.ToLower(action), "checkout_grace_audit", auditID,
			map[string]any{"seq": seq, "expected_state": in.ExpectedState})
		writeJSON(w, http.StatusOK, map[string]any{"audit_id": auditID, "action": action, "seq": seq})
	}
}

// alertFailureStatus maps the controlled operation's bounded failure prefixes to HTTP. The database is the
// authority on WHY something was refused; this only decides how to say it over HTTP.
func alertFailureStatus(msg string) (int, string) {
	switch {
	case strings.Contains(msg, "ALERT_NOT_FOUND"):
		return http.StatusNotFound, "not_found"
	case strings.Contains(msg, "ALERT_STATE_CONFLICT"):
		return http.StatusConflict, "state_conflict"
	case strings.Contains(msg, "ALERT_ACTOR_INVALID"):
		return http.StatusForbidden, "actor_invalid"
	case strings.Contains(msg, "ALERT_ACTION_INVALID"):
		return http.StatusBadRequest, "bad_request"
	default:
		return http.StatusConflict, "conflict"
	}
}
