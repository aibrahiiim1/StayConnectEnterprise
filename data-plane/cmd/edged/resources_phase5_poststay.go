package main

// THE OPERATOR POST-STAY SURFACE.
//
// Reads are ordinary. The two mutations are not, and both carry the full weight the contract puts on a
// destructive guest-facing action: RBAC on the resource key, password step-up on top of the session, a
// mandatory bounded reason, and an immutable audit row naming the operator from the SESSION rather than from
// the request body.
//
// RESET AND REVOKE ARE DIFFERENT ACTS, and the difference is the whole model:
//
//	RESET/REISSUE  rotates the credential of an ACTIVE profile. The episode, the profile and the guest's
//	               eligibility are untouched; only the secret changes, and the previous one stops working the
//	               instant it commits. This is the answer to "the guest lost the PIN" — including the case
//	               where the guest never received it, because a lost response is unrecoverable by design.
//	REVOKE         is TERMINAL for this Stay episode. It is not a stronger reset and it cannot be undone: a
//	               revoked profile is never reactivated, and the episode gets no second profile because the
//	               unique index permits one. The next post-stay identity for that guest exists only after a
//	               new episode.
//
// The PIN returned by a reset is shown ONCE, in that response. pin_revealed_at records that this server
// RETURNED it; it is never evidence that the operator or the guest actually received it. If the response is
// lost, the answer is another reset, not a second look at the same secret -- nothing can produce it again.

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/poststay"
)

const postStayResetValidity = 24 * time.Hour

func (s *server) postStayProfilesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listPostStayProfiles)
	r.Get("/{id}", s.getPostStayProfile)
	r.Post("/{id}/reset", s.resetPostStayPIN)
	r.Post("/{id}/revoke", s.revokePostStayProfile)
	return r
}

type postStayProfileRow struct {
	ID            string  `json:"id"`
	StayID        string  `json:"stay_id"`
	Episode       int     `json:"origin_lifecycle_version"`
	Reservation   string  `json:"external_reservation_id"`
	Room          *string `json:"normalized_room_number"`
	StayStatus    string  `json:"stay_status"`
	Status        string  `json:"status"`
	Generation    int     `json:"pin_generation"`
	IssuedVia     string  `json:"issued_via"`
	IssuedAt      string  `json:"issued_at"`
	ValidUntil    string  `json:"valid_until"`
	RevokedAt     *string `json:"revoked_at"`
	RevokeReason  *string `json:"revoke_reason"`
	Authenticable bool    `json:"authenticable"`
}

// listPostStayProfiles shows the site's post-stay identities. There is no PIN column and no way to ask for
// one: the operator screen shows WHETHER a profile can currently authenticate, never the secret.
func (s *server) listPostStayProfiles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT psp.id::text, psp.origin_stay_id::text, psp.origin_lifecycle_version,
		       st.external_reservation_id, st.normalized_room_number, st.status,
		       psp.status, psp.pin_generation, psp.issued_via,
		       to_char(psp.issued_at   AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(psp.valid_until AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(psp.revoked_at  AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       psp.revoke_reason,
		       iam_v2.p5_post_stay_authenticable(psp.tenant_id, psp.site_id, psp.id)
		  FROM iam_v2.post_stay_profiles psp
		  JOIN iam_v2.stays st
		    ON st.tenant_id=psp.tenant_id AND st.site_id=psp.site_id AND st.id=psp.origin_stay_id
		 WHERE psp.tenant_id=$1 AND psp.site_id=$2
		 ORDER BY psp.created_at DESC
		 LIMIT 500`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", err.Error())
		return
	}
	defer rows.Close()
	out := []postStayProfileRow{}
	for rows.Next() {
		var v postStayProfileRow
		if err := rows.Scan(&v.ID, &v.StayID, &v.Episode, &v.Reservation, &v.Room, &v.StayStatus,
			&v.Status, &v.Generation, &v.IssuedVia, &v.IssuedAt, &v.ValidUntil,
			&v.RevokedAt, &v.RevokeReason, &v.Authenticable); err != nil {
			jsonErr(w, http.StatusInternalServerError, "scan_failed", err.Error())
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

func (s *server) getPostStayProfile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	var v postStayProfileRow
	err := s.db.QueryRow(ctx, `
		SELECT psp.id::text, psp.origin_stay_id::text, psp.origin_lifecycle_version,
		       st.external_reservation_id, st.normalized_room_number, st.status,
		       psp.status, psp.pin_generation, psp.issued_via,
		       to_char(psp.issued_at   AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(psp.valid_until AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(psp.revoked_at  AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       psp.revoke_reason,
		       iam_v2.p5_post_stay_authenticable(psp.tenant_id, psp.site_id, psp.id)
		  FROM iam_v2.post_stay_profiles psp
		  JOIN iam_v2.stays st
		    ON st.tenant_id=psp.tenant_id AND st.site_id=psp.site_id AND st.id=psp.origin_stay_id
		 WHERE psp.tenant_id=$1 AND psp.site_id=$2 AND psp.id=$3`,
		s.tenantID, s.siteID, id).
		Scan(&v.ID, &v.StayID, &v.Episode, &v.Reservation, &v.Room, &v.StayStatus,
			&v.Status, &v.Generation, &v.IssuedVia, &v.IssuedAt, &v.ValidUntil,
			&v.RevokedAt, &v.RevokeReason, &v.Authenticable)
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such post-stay profile")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

type postStayActionReq struct {
	// Password is the step-up. There is deliberately no actor field: an audit record whose author comes from
	// the request body is not an audit record.
	Password string `json:"password"`
	Reason   string `json:"reason"`
}

// validReason bounds the mandatory justification. Empty is refused because an unexplained credential rotation
// is indistinguishable afterwards from an unauthorized one; a cap keeps a free-text field from becoming a
// place to store something else.
func validReason(reason string) bool {
	n := len(reason)
	return n >= 4 && n <= 500
}

// resetPostStayPIN rotates the credential of an ACTIVE profile and returns the new PIN once.
func (s *server) resetPostStayPIN(w http.ResponseWriter, r *http.Request) {
	var in postStayActionReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason (4-500 characters) is required")
		return
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	store := poststay.New(s.db, nil)
	out, err := store.Reset(ctx, poststay.ResetRequest{
		Tenant: s.tenantID, Site: s.siteID, Profile: id,
		Operator: actor, Reason: in.Reason, ValidFor: postStayResetValidity})
	if err != nil {
		switch {
		case errors.Is(err, poststay.ErrNotAuthenticable):
			// A revoked profile is terminal, and a missing one is not there. Both are "there is no ACTIVE
			// profile here to rotate", which is a conflict rather than a server fault.
			jsonErr(w, http.StatusConflict, "not_active",
				"no ACTIVE post-stay profile with that id; a revoked profile is terminal for its stay episode")
		case errors.Is(err, poststay.ErrNotEligible):
			jsonErr(w, http.StatusBadRequest, "invalid", "reset requires an operator and a reason")
		default:
			jsonErr(w, http.StatusInternalServerError, "reset_failed", err.Error())
		}
		return
	}
	// The audit records the ROTATION, never the secret. A PIN in an audit payload would outlive the one-time
	// reveal it exists to bound.
	s.audit(r, "poststay.pin_reset", "post_stay_profile", id, map[string]any{
		"reason": in.Reason, "valid_until": out.Expires.UTC().Format(time.RFC3339),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"profile_id":  id,
		"pin":         out.PIN,
		"valid_until": out.Expires.UTC().Format(time.RFC3339),
		// Said in the response because the screen must say it too: this is the only time this value exists.
		"notice": "This PIN is shown once. It is not stored and cannot be shown again — if it is lost, reset again.",
	})
}

// revokePostStayProfile ends post-stay access for this Stay episode. Terminal: not reversible, and the
// episode gets no replacement profile.
func (s *server) revokePostStayProfile(w http.ResponseWriter, r *http.Request) {
	var in postStayActionReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason (4-500 characters) is required")
		return
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	store := poststay.New(s.db, nil)
	if err := store.Revoke(ctx, poststay.RevokeRequest{
		Tenant: s.tenantID, Site: s.siteID, Profile: id,
		Operator: actor, Reason: in.Reason}); err != nil {
		switch {
		case errors.Is(err, poststay.ErrNotAuthenticable):
			jsonErr(w, http.StatusConflict, "not_active",
				"no ACTIVE post-stay profile with that id; revocation is terminal and is not repeated")
		default:
			jsonErr(w, http.StatusInternalServerError, "revoke_failed", err.Error())
		}
		return
	}
	s.audit(r, "poststay.revoke", "post_stay_profile", id, map[string]any{"reason": in.Reason})
	writeJSON(w, http.StatusOK, map[string]any{
		"profile_id": id, "status": "REVOKED",
		"notice": "Post-stay access is ended for this stay episode. This cannot be undone, and the episode " +
			"gets no replacement profile.",
	})
}
