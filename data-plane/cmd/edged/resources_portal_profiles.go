package main

// A portal profile is a named copy of the portal design (the same document the site's default design is: design, draft, revisions) kept in iam_v2.portal_profiles
// and assigned to any number of client networks. The site's own design (tenants.branding) is the default profile and is what every network without an assignment
// shows. A profile is appearance and wording only: it never changes which sign-in methods, PMS interface or packages a network has.

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

type profileCtxKey struct{}

const portalPreviewProfileParam = "profile"

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func profileFrom(r *http.Request) string {
	v, _ := r.Context().Value(profileCtxKey{}).(string)
	return v
}

func brandingTarget(r *http.Request) string {
	if profileFrom(r) != "" {
		return "portal_profile"
	}
	return "tenant"
}

func brandingTargetID(r *http.Request, tenant string) string {
	if p := profileFrom(r); p != "" {
		return p
	}
	return tenant
}

func (s *server) mountPortalProfiles(r chi.Router) {
	r.Route("/profiles", func(r chi.Router) {
		r.Get("/", s.listPortalProfiles)
		r.Post("/", s.createPortalProfile)
		r.Put("/assignments/{network}", s.assignPortalProfile)
		r.Route("/{profile}", func(r chi.Router) {
			r.Use(s.profileScope)
			r.Patch("/", s.renamePortalProfile)
			r.Delete("/", s.deletePortalProfile)
			r.Route("/branding", func(r chi.Router) { s.mountBranding(r) })
		})
	})
}

// profileScope admits only a profile of this site; anything else is "not found", so an id from another site or a malformed one reveals nothing.
func (s *server) profileScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "profile")
		if s.tenantID == "" || !uuidPattern.MatchString(id) {
			jsonErr(w, http.StatusNotFound, "not_found", "no such portal profile")
			return
		}
		ctx, cancel := dbCtx(r)
		defer cancel()
		var one int
		err := s.db.QueryRow(ctx, `SELECT 1 FROM iam_v2.portal_profiles WHERE id=$1::uuid AND tenant_id=$2::uuid AND site_id=$3::uuid`, id, s.tenantID, s.siteID).Scan(&one)
		if err == pgx.ErrNoRows {
			jsonErr(w, http.StatusNotFound, "not_found", "no such portal profile")
			return
		}
		if err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "portal profile lookup failed")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), profileCtxKey{}, id)))
	})
}

type portalProfileOut struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Networks []string `json:"network_ids"`
	Default  bool     `json:"is_default"`
}

func (s *server) listPortalProfiles(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" {
		writeAwaitingAssignment(w, "portal profiles")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, p.name, COALESCE(array_agg(a.network_id::text) FILTER (WHERE a.network_id IS NOT NULL), '{}')
		  FROM iam_v2.portal_profiles p LEFT JOIN iam_v2.portal_profile_assignments a ON a.profile_id = p.id
		 WHERE p.tenant_id=$1::uuid AND p.site_id=$2::uuid GROUP BY p.id, p.name ORDER BY lower(p.name)`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "portal profiles could not be read")
		return
	}
	defer rows.Close()
	out := []portalProfileOut{}
	assigned := map[string]bool{}
	for rows.Next() {
		var p portalProfileOut
		if err := rows.Scan(&p.ID, &p.Name, &p.Networks); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "portal profiles could not be read")
			return
		}
		for _, n := range p.Networks {
			assigned[n] = true
		}
		out = append(out, p)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "portal profiles could not be read")
		return
	}
	rest := []string{}
	nrows, err := s.db.Query(ctx, `SELECT id::text FROM guest_networks WHERE tenant_id=$1::uuid ORDER BY name`, s.tenantID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "portal profiles could not be read")
		return
	}
	defer nrows.Close()
	for nrows.Next() {
		var id string
		if nrows.Scan(&id) == nil && !assigned[id] {
			rest = append(rest, id)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"default": portalProfileOut{ID: "default", Name: "Site default", Networks: rest, Default: true}, "profiles": out})
}

func cleanProfileName(n string) (string, bool) {
	n = strings.TrimSpace(n)
	return n, n != "" && len([]rune(n)) <= 80
}

func (s *server) createPortalProfile(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" {
		writeAwaitingAssignment(w, "portal profiles")
		return
	}
	var in struct {
		Name     string `json:"name"`
		CopyFrom string `json:"copy_from"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	name, ok := cleanProfileName(in.Name)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "validation", "a profile needs a name of 1 to 80 characters")
		return
	}
	// A new profile starts as a published copy of another design (the site default unless one is named), so it shows something sensible at once.
	src := r
	if in.CopyFrom != "" && in.CopyFrom != "default" {
		if !uuidPattern.MatchString(in.CopyFrom) {
			jsonErr(w, http.StatusBadRequest, "validation", "copy_from must be \"default\" or a profile id")
			return
		}
		src = r.WithContext(context.WithValue(r.Context(), profileCtxKey{}, in.CopyFrom))
	}
	doc, err := s.loadBranding(src)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "validation", "the design to copy could not be read")
		return
	}
	start := brandingDoc{Design: doc.Design}
	if len(doc.Design) > 0 {
		actor := ""
		if sess := sessFrom(r.Context()); sess != nil {
			actor = sess.Email
		}
		start.Revisions = []brandingRevision{{Version: 1, PublishedAt: time.Now().UTC(), PublishedBy: actor, Note: "Copied when the profile was created", Design: doc.Design}}
	}
	raw, _ := json.Marshal(start)
	ctx, cancel := dbCtx(r)
	defer cancel()
	var id string
	err = s.db.QueryRow(ctx, `INSERT INTO iam_v2.portal_profiles (tenant_id, site_id, name, doc) VALUES ($1::uuid,$2::uuid,$3,$4::jsonb) RETURNING id::text`,
		s.tenantID, s.siteID, name, string(raw)).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "a portal profile with this name already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal profile could not be created")
		return
	}
	s.audit(r, "portal_profile.created", "portal_profile", id, map[string]any{"name": name})
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": name})
}

func (s *server) renamePortalProfile(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	name, ok := cleanProfileName(in.Name)
	if !ok {
		jsonErr(w, http.StatusBadRequest, "validation", "a profile needs a name of 1 to 80 characters")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	if _, err := s.db.Exec(ctx, `UPDATE iam_v2.portal_profiles SET name=$1, updated_at=now() WHERE id=$2::uuid`, name, profileFrom(r)); err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "a portal profile with this name already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal profile could not be renamed")
		return
	}
	s.audit(r, "portal_profile.renamed", "portal_profile", profileFrom(r), map[string]any{"name": name})
	writeJSON(w, http.StatusOK, map[string]any{"id": profileFrom(r), "name": name})
}

// deletePortalProfile returns its networks to the site default (their assignments go with it) and reports how many.
func (s *server) deletePortalProfile(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal profile could not be deleted")
		return
	}
	defer tx.Rollback(ctx)
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM iam_v2.portal_profile_assignments WHERE profile_id=$1::uuid`, profileFrom(r)).Scan(&n); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal profile could not be deleted")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.portal_profiles WHERE id=$1::uuid`, profileFrom(r)); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal profile could not be deleted")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the portal profile could not be deleted")
		return
	}
	s.audit(r, "portal_profile.deleted", "portal_profile", profileFrom(r), map[string]any{"networks_returned_to_default": n})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "networks_returned_to_default": n})
}

// assignPortalProfile points a network at a profile, or back at the site default with profile_id null. The network must be one of this site's.
func (s *server) assignPortalProfile(w http.ResponseWriter, r *http.Request) {
	network := chi.URLParam(r, "network")
	var in struct {
		ProfileID *string `json:"profile_id"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidPattern.MatchString(network) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "a network id and a profile id (or null for the site default) are required")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var one int
	if err := s.db.QueryRow(ctx, `SELECT 1 FROM guest_networks WHERE id=$1::uuid AND tenant_id=$2::uuid`, network, s.tenantID).Scan(&one); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such client network")
		return
	}
	if in.ProfileID == nil || *in.ProfileID == "default" {
		if _, err := s.db.Exec(ctx, `DELETE FROM iam_v2.portal_profile_assignments WHERE network_id=$1::uuid`, network); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the assignment could not be changed")
			return
		}
		s.audit(r, "portal_profile.assigned", "guest_network", network, map[string]any{"profile": "default"})
		writeJSON(w, http.StatusOK, map[string]any{"network_id": network, "profile_id": nil})
		return
	}
	if !uuidPattern.MatchString(*in.ProfileID) {
		jsonErr(w, http.StatusBadRequest, "validation", "profile_id must be a profile id or null")
		return
	}
	tag, err := s.db.Exec(ctx, `
		INSERT INTO iam_v2.portal_profile_assignments (network_id, profile_id, tenant_id, site_id)
		SELECT $1::uuid, p.id, p.tenant_id, p.site_id FROM iam_v2.portal_profiles p WHERE p.id=$2::uuid AND p.tenant_id=$3::uuid AND p.site_id=$4::uuid
		ON CONFLICT (network_id) DO UPDATE SET profile_id = EXCLUDED.profile_id, assigned_at = now()`, network, *in.ProfileID, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the assignment could not be changed")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "no such portal profile")
		return
	}
	s.audit(r, "portal_profile.assigned", "guest_network", network, map[string]any{"profile": *in.ProfileID})
	writeJSON(w, http.StatusOK, map[string]any{"network_id": network, "profile_id": *in.ProfileID})
}

// assetsInUse names every uploaded image that the default design or any profile's design or draft points at.
func (s *server) assetsInUse(r *http.Request) (map[string]bool, error) {
	used := map[string]bool{}
	note := func(docs ...map[string]any) {
		for _, d := range docs {
			for _, k := range portaldesign.ImageFields() {
				if v, ok := d[k].(string); ok {
					used[strings.TrimPrefix(v, "/assets/")] = true
				}
			}
		}
	}
	doc, err := s.loadBranding(r.WithContext(context.WithValue(r.Context(), profileCtxKey{}, "")))
	if err != nil {
		return nil, err
	}
	note(doc.Design, doc.Draft)
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT doc FROM iam_v2.portal_profiles WHERE tenant_id=$1::uuid AND site_id=$2::uuid`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var pd brandingDoc
		if json.Unmarshal(raw, &pd) == nil {
			note(pd.Design, pd.Draft)
		}
	}
	return used, rows.Err()
}
