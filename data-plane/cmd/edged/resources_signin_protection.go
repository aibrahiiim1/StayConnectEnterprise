package main

// Guest sign-in protection: the operator surface over the policy and the restrictions it creates.
//
//	guest-signin-protection    — read and CHANGE the three numbers (threshold, window, restriction duration)
//	guest-signin-restrictions  — read the active restrictions, and RELEASE one
//
// Separate keys: a release affects one device briefly, a policy change affects every guest, and neither carries guest-signin-credentials.
// A release only permits another attempt; no path here reaches an entitlement, session or grant.
// Scope is never taken from the client: tenant and site come from the signed assignment.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

// protectionPolicyOut carries the BOUNDS with the values so UI and server validation cannot drift.
type protectionPolicyOut struct {
	MaxFailedAttempts        int `json:"max_failed_attempts"`
	ObservationWindowSeconds int `json:"observation_window_seconds"`
	RestrictionSeconds       int `json:"restriction_seconds"`
	// IsDefault: the site never saved a policy and runs on the approved defaults. It does NOT mean protection is off.
	IsDefault bool `json:"is_default"`

	Limits protectionLimitsOut `json:"limits"`
	// LastChange is the latest entry of the policy's append-only change log.
	LastChange *protectionChangeOut `json:"last_change,omitempty"`
}

type protectionLimitsOut struct {
	MinFailedAttempts  int `json:"min_failed_attempts"`
	MaxFailedAttempts  int `json:"max_failed_attempts"`
	MinWindowSeconds   int `json:"min_observation_window_seconds"`
	MaxWindowSeconds   int `json:"max_observation_window_seconds"`
	MinRestrictSeconds int `json:"min_restriction_seconds"`
	MaxRestrictSeconds int `json:"max_restriction_seconds"`
}

type protectionChangeOut struct {
	ChangedAt time.Time `json:"changed_at"`
	ChangedBy string    `json:"changed_by"`
	Reason    string    `json:"reason,omitempty"`
	// Old values are absent on a site's first saved policy: reporting a prior value would invent a record.
	OldMaxFailedAttempts        *int `json:"old_max_failed_attempts,omitempty"`
	OldObservationWindowSeconds *int `json:"old_observation_window_seconds,omitempty"`
	OldRestrictionSeconds       *int `json:"old_restriction_seconds,omitempty"`
	NewMaxFailedAttempts        int  `json:"new_max_failed_attempts"`
	NewObservationWindowSeconds int  `json:"new_observation_window_seconds"`
	NewRestrictionSeconds       int  `json:"new_restriction_seconds"`
}

func (s *server) signInProtectionRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getSignInProtection)
	r.Put("/", s.putSignInProtection)
	r.Get("/changes", s.listSignInProtectionChanges)
	r.Get("/network", s.getNetworkPolicy)
	r.Put("/network", s.putNetworkPolicy)
	return r
}

func (s *server) getSignInProtection(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	out, err := s.effectiveProtection(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_unreadable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// effectiveProtection reads the same function the enforcement path reads, so the screen cannot show a policy the property is not running.
func (s *server) effectiveProtection(ctx context.Context) (protectionPolicyOut, error) {
	var out protectionPolicyOut
	err := s.db.QueryRow(ctx, `
		SELECT max_failed_attempts, observation_window_seconds, restriction_seconds, is_default
		  FROM iam_v2.guest_signin_protection_get($1::uuid,$2::uuid)`,
		s.tenantID, s.siteID).Scan(&out.MaxFailedAttempts, &out.ObservationWindowSeconds,
		&out.RestrictionSeconds, &out.IsDefault)
	if err != nil {
		return out, err
	}
	out.Limits = protectionLimitsOut{
		MinFailedAttempts:  signinattempt.MinFailedAttempts,
		MaxFailedAttempts:  signinattempt.MaxFailedAttemptsCap,
		MinWindowSeconds:   signinattempt.MinWindowSeconds,
		MaxWindowSeconds:   signinattempt.MaxWindowSeconds,
		MinRestrictSeconds: signinattempt.MinRestrictionSeconds,
		MaxRestrictSeconds: signinattempt.MaxRestrictionSeconds,
	}
	last, err := s.lastProtectionChange(ctx)
	if err != nil {
		return out, err
	}
	out.LastChange = last
	return out, nil
}

func (s *server) lastProtectionChange(ctx context.Context) (*protectionChangeOut, error) {
	var c protectionChangeOut
	var reason *string
	err := s.db.QueryRow(ctx, `
		SELECT changed_at, changed_by, change_reason,
		       old_max_failed_attempts, old_observation_window_seconds, old_restriction_seconds,
		       new_max_failed_attempts, new_observation_window_seconds, new_restriction_seconds
		  FROM iam_v2.guest_signin_protection_changes
		 WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY changed_at DESC
		 LIMIT 1`, s.tenantID, s.siteID).Scan(&c.ChangedAt, &c.ChangedBy, &reason,
		&c.OldMaxFailedAttempts, &c.OldObservationWindowSeconds, &c.OldRestrictionSeconds,
		&c.NewMaxFailedAttempts, &c.NewObservationWindowSeconds, &c.NewRestrictionSeconds)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if reason != nil {
		c.Reason = *reason
	}
	return &c, nil
}

// putSignInProtection saves the three numbers. scd reads the policy from this table on every decision, so it takes effect immediately.
// Existing restrictions keep the expiry they were given; shortening the duration does not rewrite recorded expiries.
func (s *server) putSignInProtection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MaxFailedAttempts        int    `json:"max_failed_attempts"`
		ObservationWindowSeconds int    `json:"observation_window_seconds"`
		RestrictionSeconds       int    `json:"restriction_seconds"`
		Reason                   string `json:"reason"`
		// Password step-up and a bounded reason, checked before anything is read: this changes a control for every guest.
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	p := signinattempt.Policy{
		MaxFailedAttempts:  in.MaxFailedAttempts,
		WindowSeconds:      in.ObservationWindowSeconds,
		RestrictionSeconds: in.RestrictionSeconds,
	}
	// Validated here with operator-readable messages and again by the table's CHECK, so an impossible policy is unstorable.
	if err := p.Validate(); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}
	reason, ok := reasonText(in.Reason)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "reason_required", reasonBoundsMessage)
		return
	}
	in.Reason = reason

	if _, ok := s.stepUpActor(w, r, in.Password); !ok {
		return
	}
	sess := sessFrom(r.Context())
	actor := protectionActor(sess)
	if actor == "" {
		// The definer function refuses an empty actor; refusing here turns a database error into an actionable answer.
		jsonErr(w, http.StatusForbidden, "forbidden", "the change could not be attributed to an operator")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	before, err := s.effectiveProtection(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_unreadable", err.Error())
		return
	}

	var version int64
	err = s.db.QueryRow(ctx, `
		SELECT iam_v2.guest_signin_protection_set($1::uuid,$2::uuid,$3,$4,$5,$6,NULLIF($7,''))`,
		s.tenantID, s.siteID, p.MaxFailedAttempts, p.WindowSeconds, p.RestrictionSeconds,
		actor, strings.TrimSpace(in.Reason)).Scan(&version)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_not_saved", err.Error())
		return
	}

	// The operator audit records the change with previous and new values; the policy's own log records how the site reached these numbers.
	s.audit(r, "guest_signin_protection.update", "site", s.siteID, map[string]any{
		"previous": map[string]any{
			"max_failed_attempts":        before.MaxFailedAttempts,
			"observation_window_seconds": before.ObservationWindowSeconds,
			"restriction_seconds":        before.RestrictionSeconds,
			"was_default":                before.IsDefault,
		},
		"new": map[string]any{
			"max_failed_attempts":        p.MaxFailedAttempts,
			"observation_window_seconds": p.WindowSeconds,
			"restriction_seconds":        p.RestrictionSeconds,
		},
		"config_version": version,
		"reason":         strings.TrimSpace(in.Reason),
	})

	out, err := s.effectiveProtection(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "policy_unreadable", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) listSignInProtectionChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT changed_at, changed_by, change_reason,
		       old_max_failed_attempts, old_observation_window_seconds, old_restriction_seconds,
		       new_max_failed_attempts, new_observation_window_seconds, new_restriction_seconds
		  FROM iam_v2.guest_signin_protection_changes
		 WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY changed_at DESC
		 LIMIT 100`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "changes_unreadable", err.Error())
		return
	}
	defer rows.Close()

	out := []protectionChangeOut{}
	for rows.Next() {
		var c protectionChangeOut
		var reason *string
		if err := rows.Scan(&c.ChangedAt, &c.ChangedBy, &reason,
			&c.OldMaxFailedAttempts, &c.OldObservationWindowSeconds, &c.OldRestrictionSeconds,
			&c.NewMaxFailedAttempts, &c.NewObservationWindowSeconds, &c.NewRestrictionSeconds); err != nil {
			jsonErr(w, http.StatusInternalServerError, "changes_unreadable", err.Error())
			return
		}
		if reason != nil {
			c.Reason = *reason
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

// restrictionOut is one currently-restricted device. LastSubmittedRoom is the room somebody TYPED, not an identity, and is named "submitted" throughout for that reason.
type restrictionOut struct {
	ID                string    `json:"id"`
	DeviceMAC         string    `json:"device_mac"`
	GuestNetwork      string    `json:"guest_network,omitempty"`
	LastSubmittedRoom string    `json:"last_submitted_room,omitempty"`
	FailureCount      int       `json:"failure_count"`
	Reason            string    `json:"reason"`
	RestrictedAt      time.Time `json:"restricted_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	// RemainingSeconds is computed by the server at read time so an open screen cannot drift from the appliance clock.
	RemainingSeconds int `json:"remaining_seconds"`
}

func (s *server) signInRestrictionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSignInRestrictions)
	r.Post("/{id}/release", s.releaseSignInRestriction)
	r.Get("/network-pauses", s.listNetworkPauses)
	r.Get("/network-pauses/{id}/devices", s.listNetworkPauseDevices)
	r.Post("/network-pauses/{id}/end", s.endNetworkPause)
	r.Post("/network-pauses/{id}/confirm", s.confirmPausedDevice)
	return r
}

// listSignInRestrictions returns the ACTIVE restrictions. Expired rows are neither returned nor deleted: the row carries the device's counter, and history is the sign-in attempts page.
func (s *server) listSignInRestrictions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	rows, err := s.db.Query(ctx, `
		SELECT id::text, device_mac::text, COALESCE(guest_network_name,''), COALESCE(last_submitted_room,''),
		       failure_count, reason, restricted_at, expires_at,
		       GREATEST(0, CEIL(EXTRACT(EPOCH FROM (expires_at - now()))))::int
		  FROM iam_v2.guest_signin_restrictions
		 WHERE tenant_id=$1 AND site_id=$2
		   AND expires_at IS NOT NULL AND expires_at > now()
		 ORDER BY expires_at ASC
		 LIMIT 500`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "restrictions_unreadable", err.Error())
		return
	}
	defer rows.Close()

	out := []restrictionOut{}
	for rows.Next() {
		var x restrictionOut
		if err := rows.Scan(&x.ID, &x.DeviceMAC, &x.GuestNetwork, &x.LastSubmittedRoom,
			&x.FailureCount, &x.Reason, &x.RestrictedAt, &x.ExpiresAt, &x.RemainingSeconds); err != nil {
			jsonErr(w, http.StatusInternalServerError, "restrictions_unreadable", err.Error())
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{"restrictions": out})
}

// releaseSignInRestriction ends one restriction early. It permits another attempt and nothing more; the device counter is reset.
// The reason is mandatory (also enforced by the definer function): release weakens a control, so the record is the point.
func (s *server) releaseSignInRestriction(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "restriction id required")
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	reason, ok := reasonText(in.Reason)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			reasonBoundsMessage+", so the record says why this device was allowed to try again")
		return
	}
	sess := sessFrom(r.Context())
	actor := protectionActor(sess)
	if actor == "" {
		jsonErr(w, http.StatusForbidden, "forbidden", "the release could not be attributed to an operator")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()

	// Read the row first: the release clears the device and wait fields, which the audit record needs.
	var mac, room, network string
	var expires time.Time
	var failures int
	err := s.db.QueryRow(ctx, `
		SELECT device_mac::text, COALESCE(last_submitted_room,''), COALESCE(guest_network_name,''),
		       expires_at, failure_count
		  FROM iam_v2.guest_signin_restrictions
		 WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid
		   AND expires_at IS NOT NULL AND expires_at > now()`,
		s.tenantID, s.siteID, id).Scan(&mac, &room, &network, &expires, &failures)
	if errors.Is(err, pgx.ErrNoRows) {
		// Missing, expired and just-released answer identically, so a caller learns nothing about another site's row.
		jsonErr(w, http.StatusNotFound, "not_found", "no active restriction with that id")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "restriction_unreadable", err.Error())
		return
	}

	var affected int
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.guest_signin_release($1::uuid,$2::uuid,$3::uuid,$4,$5)`,
		s.tenantID, s.siteID, id, actor, reason).Scan(&affected); err != nil {
		jsonErr(w, http.StatusInternalServerError, "release_failed", err.Error())
		return
	}
	if affected == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "no active restriction with that id")
		return
	}

	s.audit(r, "guest_signin_restriction.release", "guest_device", mac, map[string]any{
		"restriction_id": id,
		"device_mac":     mac,
		"guest_network":  network,
		// Recorded as an unverified submission: evidence of what was typed, not who typed it.
		"last_submitted_room_unverified": room,
		"failure_count":                  failures,
		"expires_at_before_release":      expires,
		"seconds_cut_short":              int(time.Until(expires).Seconds()),
		"reason":                         reason,
		// Stated in the record because "released" reads like "let in" to a later auditor.
		"grants_access": false,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"released": true,
		"note":     "The device may attempt to sign in again. It has not been granted access.",
	})
}

// protectionActor is the label written into the policy change log and release record: the email, else the operator id; an unattributable action is refused.
func protectionActor(sess *session) string {
	if sess == nil {
		return ""
	}
	if e := strings.TrimSpace(sess.Email); e != "" {
		return e
	}
	return strings.TrimSpace(sess.OperatorID)
}
