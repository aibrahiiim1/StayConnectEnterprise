package main

// Reads are ordinary; both mutations are destructive guest-facing actions: RBAC on the resource key, password
// step-up, a mandatory bounded reason, and an immutable audit row naming the operator from the SESSION.
// Reset rotates the secret of an ACTIVE profile (the old PIN stops working at commit); revoke is TERMINAL for the
// Stay episode and the episode never gets a second profile. The reset PIN is shown once; pin_revealed_at only
// records that the server returned it, so a lost response means another reset.

import (
	"errors"
	"log/slog"
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
	// The reservation number and revoke reason are withheld from roles without pms-stay-identity.
	IdentityHidden bool `json:"identity_hidden,omitempty"`
}

func hidePostStayIdentity(v *postStayProfileRow) {
	v.Reservation, v.RevokeReason, v.IdentityHidden = "", nil, true
}

// There is no PIN column: the operator sees whether a profile can authenticate, never the secret.
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
	identity := canSeeStayIdentity(r)
	out := []postStayProfileRow{}
	for rows.Next() {
		var v postStayProfileRow
		if err := rows.Scan(&v.ID, &v.StayID, &v.Episode, &v.Reservation, &v.Room, &v.StayStatus,
			&v.Status, &v.Generation, &v.IssuedVia, &v.IssuedAt, &v.ValidUntil,
			&v.RevokedAt, &v.RevokeReason, &v.Authenticable); err != nil {
			jsonErr(w, http.StatusInternalServerError, "scan_failed", err.Error())
			return
		}
		if !identity {
			hidePostStayIdentity(&v)
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
	if !canSeeStayIdentity(r) {
		hidePostStayIdentity(&v)
	}
	writeJSON(w, http.StatusOK, v)
}

type postStayActionReq struct {
	// Password is the step-up. There is deliberately no actor field: the audit author must come from the session.
	Password string `json:"password"`
	Reason   string `json:"reason"`
	// ExtendValidity (reset only) also restarts the validity window (24 h from now); otherwise a reset rotates the PIN only.
	ExtendValidity bool `json:"extend_validity"`
}

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
	validFor := time.Duration(0)
	if in.ExtendValidity {
		validFor = postStayResetValidity
	}
	out, err := store.Reset(ctx, poststay.ResetRequest{
		Tenant: s.tenantID, Site: s.siteID, Profile: id,
		Operator: actor, Reason: in.Reason, ValidFor: validFor})
	if err != nil {
		switch {
		case errors.Is(err, poststay.ErrNotAuthenticable):
			// A revoked or missing profile is a conflict, not a server fault.
			jsonErr(w, http.StatusConflict, "not_active",
				"no ACTIVE post-stay profile with that id; a revoked profile is terminal for its stay episode")
		case errors.Is(err, poststay.ErrNotEligible):
			jsonErr(w, http.StatusBadRequest, "invalid", "reset requires an operator and a reason")
		default:
			slog.Error("post-stay reset failed", "err", err)
			jsonErr(w, http.StatusInternalServerError, "reset_failed", "the PIN could not be reset")
		}
		return
	}
	// The audit records the rotation, never the secret, so a PIN cannot outlive its one-time reveal.
	s.audit(r, "poststay.pin_reset", "post_stay_profile", id, map[string]any{
		"reason": in.Reason, "valid_until": out.Expires.UTC().Format(time.RFC3339),
		"validity_extended": in.ExtendValidity,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"profile_id":  id,
		"pin":         out.PIN,
		"valid_until": out.Expires.UTC().Format(time.RFC3339),
		"notice":      "This PIN is shown once. It is not stored and cannot be shown again — if it is lost, reset again.",
	})
}

// Terminal: not reversible, and the episode gets no replacement profile.
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
