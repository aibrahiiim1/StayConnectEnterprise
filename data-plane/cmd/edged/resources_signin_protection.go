package main

// GUEST SIGN-IN PROTECTION — the operator surface over the policy and over the restrictions it creates.
//
// TWO RESOURCE KEYS, AND THEY ARE DELIBERATELY NOT ONE:
//
//	guest-signin-protection    — read and CHANGE the three numbers (threshold, window, restriction duration)
//	guest-signin-restrictions  — read the active restrictions, and RELEASE one
//
// Releasing a restriction and rewriting the policy are different powers with different blast radii. A release
// affects one device for the rest of one minute; a policy change affects every guest on the property until
// somebody changes it back. The desk needs the first and has no reason to hold the second, so holding the
// release permission must not carry the policy permission with it. Neither of them carries
// guest-signin-credentials either: an operator who can end a restriction still cannot read what anybody typed.
//
// A RELEASE PERMITS ANOTHER ATTEMPT. IT DOES NOT GRANT ACCESS. There is no code path from this file to an
// entitlement, a session or a grant — release clears the restriction and the device's counter, and the guest
// then signs in, or does not, on their own evidence. That is the whole of it, and it is worth being explicit
// because "unblock this guest" is exactly the request a desk would expect to be answered with access.
//
// SCOPE IS NEVER TAKEN FROM THE CLIENT. No route here accepts a tenant or a site; both are bound from the
// appliance's own signed assignment into every call, so a request cannot name another property.

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

// ---- the policy ------------------------------------------------------------------

// protectionPolicyOut is what the settings screen renders. It carries the BOUNDS as well as the values so the
// UI's validation and the server's cannot drift: there is one place that knows a threshold may not go below
// three, and the browser is told rather than being trusted to have been written correctly.
type protectionPolicyOut struct {
	MaxFailedAttempts        int `json:"max_failed_attempts"`
	ObservationWindowSeconds int `json:"observation_window_seconds"`
	RestrictionSeconds       int `json:"restriction_seconds"`
	// IsDefault says the site has never saved a policy and is running on the approved defaults. It is NOT
	// "protection is off" — there is no off — and the screen says so.
	IsDefault bool `json:"is_default"`

	Limits protectionLimitsOut `json:"limits"`
	// LastChange is the most recent entry from the policy's own append-only change log: who moved these
	// numbers, when, from what, and why. It is on the settings screen rather than only in an audit page
	// because the question "who made it five" is asked while looking at the five.
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
	// Old values are absent on a site's FIRST saved policy: there was no stored row, the defaults were in
	// force, and reporting "it was 5" would invent a record that never existed.
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

// effectiveProtection reads THE SAME function the enforcement path reads. That is what makes "one
// authoritative configuration source" a fact rather than an intention: if this screen and scd could disagree,
// the screen would be showing a policy the property is not running.
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

// putSignInProtection saves the three numbers.
//
// TAKES EFFECT WITHOUT A RESTART, A BUILD OR A DEPLOYMENT. scd reads the policy from this same table on every
// decision, so the next submission is judged by whatever was saved here a moment ago. Nothing is cached in a
// process, which is what makes that true rather than usually-true.
//
// EXISTING RESTRICTIONS KEEP THE EXPIRY THEY WERE GIVEN. Shortening the duration does not shorten a
// restriction already running: its expiry was recorded when it was created, and rewriting other people's
// recorded expiries from a settings screen would make the audit trail describe something that did not happen.
// An operator who needs one to end now releases it, which is a named action by a named person. The settings
// screen says this in so many words.
func (s *server) putSignInProtection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		MaxFailedAttempts        int    `json:"max_failed_attempts"`
		ObservationWindowSeconds int    `json:"observation_window_seconds"`
		RestrictionSeconds       int    `json:"restriction_seconds"`
		Reason                   string `json:"reason"`
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
	// Validated HERE with the operator-readable messages, and again by the table's CHECK constraint. The
	// second is not redundant: it is what makes an impossible policy unstorable even by a caller that never
	// came through this handler.
	if err := p.Validate(); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}

	sess := sessFrom(r.Context())
	actor := protectionActor(sess)
	if actor == "" {
		// The definer function refuses an empty actor, and refusing here as well turns a database error into
		// an answer an operator can act on.
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

	// The operator audit records the same change from the admin UI's side. The policy's own append-only log
	// answers "how did this site reach these numbers"; this answers "what did this person do today". Both
	// carry the previous and the new values, because a change record without the previous value cannot tell
	// anyone whether the control was weakened.
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

// ---- the restrictions -------------------------------------------------------------

// restrictionOut is one currently-restricted device.
//
// LastSubmittedRoom IS NAMED FOR WHAT IT IS. It is the room somebody TYPED, not the room they are in, and the
// field name, the JSON key and the screen label all say "submitted" so that an operator reading the list
// cannot mistake an attacker's chosen string for an identity. It is shown because it is the only human handle
// on the event — "the device that kept trying 412" — and withholding it would make the list unusable at a
// desk without making it safer.
type restrictionOut struct {
	ID                string    `json:"id"`
	DeviceMAC         string    `json:"device_mac"`
	GuestNetwork      string    `json:"guest_network,omitempty"`
	LastSubmittedRoom string    `json:"last_submitted_room,omitempty"`
	FailureCount      int       `json:"failure_count"`
	Reason            string    `json:"reason"`
	RestrictedAt      time.Time `json:"restricted_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	// RemainingSeconds is computed by the SERVER at the moment of the read, so a screen left open overnight
	// cannot show a countdown that drifted away from the appliance's own clock.
	RemainingSeconds int `json:"remaining_seconds"`
}

func (s *server) signInRestrictionsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSignInRestrictions)
	r.Post("/{id}/release", s.releaseSignInRestriction)
	return r
}

// listSignInRestrictions returns the ACTIVE restrictions, soonest to expire last.
//
// Expired rows are not returned and are not deleted: the row stays as the device's counter carrier, and the
// operator list asks about restrictions rather than about devices. A released or expired restriction that
// kept appearing would make the screen a history, and the sign-in attempts page is already the history.
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

// releaseSignInRestriction ends one restriction early.
//
// IT PERMITS ANOTHER ATTEMPT AND NOTHING MORE. The device's counter is reset so the guest is not restricted
// again by the failures that caused this one, and then they authenticate — or fail to — exactly as any other
// guest would. Nothing here creates an entitlement or a session.
//
// THE REASON IS MANDATORY, and it is enforced by the definer function as well as here. "Release" is the one
// action on this screen that weakens a control, so the record of who weakened it, for which device and why is
// the point of the action rather than paperwork attached to it.
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
	reason := strings.TrimSpace(in.Reason)
	if len([]rune(reason)) < 3 {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a short reason is required, so the record says why this device was allowed to try again")
		return
	}
	if len([]rune(reason)) > 200 {
		jsonErr(w, http.StatusBadRequest, "reason_too_long", "keep the reason under 200 characters")
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

	// Read the row FIRST so the audit record can name the device and the wait that was cut short. After the
	// release those fields are cleared, and an audit entry that could only say "something was released" would
	// be the least useful line in the log.
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
		// Either it never existed here, or it expired or was released a moment ago. All three are the same
		// answer: there is nothing to release, and saying which would tell a caller about another site's row.
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
		// Recorded as an unverified submission, with a key that says so. It is evidence about what was typed,
		// never a claim about who was typing it.
		"last_submitted_room_unverified": room,
		"failure_count":                  failures,
		"expires_at_before_release":      expires,
		"seconds_cut_short":              int(time.Until(expires).Seconds()),
		"reason":                         reason,
		// Stated in the record itself, because "released" reads like "let in" to anyone auditing later.
		"grants_access": false,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"released": true,
		"note":     "The device may attempt to sign in again. It has not been granted access.",
	})
}

// protectionActor is the label written into the policy change log and the release record.
//
// The email is preferred over the opaque operator id because these two records are read by humans after an
// incident, and an id requires a second lookup against a table that may itself have changed. The id is the
// fallback, and an unattributable action is refused rather than recorded as nobody.
func protectionActor(sess *session) string {
	if sess == nil {
		return ""
	}
	if e := strings.TrimSpace(sess.Email); e != "" {
		return e
	}
	return strings.TrimSpace(sess.OperatorID)
}
