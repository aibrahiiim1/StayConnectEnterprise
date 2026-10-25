package main

// Portal branding is one jsonb column (tenants.branding):
//
//	{
//	  "design":    { ...the published design the portal serves... },
//	  "draft":     { ...work in progress, never served to a guest... },
//	  "revisions": [ { version, published_at, published_by, design } ... ]
//	}
//
// Revisions are append-only and bounded; rollback re-publishes an earlier revision as a new one.
// Custom CSS/HTML is refused rather than sanitised (see validateDesign and internal/portaldesign): the portal
// page collects room numbers, surnames and voucher codes, so injected script could steal credentials.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// maxRevisions bounds the history in a row that is read on every portal load.
const maxRevisions = 20

// portalPreviewURL is portald's own sign-in page on loopback, so the preview cannot drift from what a guest receives.
const (
	portalPreviewPath = "/"
	portalPreviewURL  = portalHTTPBase + portalPreviewPath
)

// portalLanguagesURL is the portal's shipped wording, shown so the Languages screen reflects what guests receive.
const (
	portalLanguagesPath = "/api/languages"
	portalLanguagesURL  = portalHTTPBase + portalLanguagesPath
)

type brandingDoc struct {
	Design    map[string]any     `json:"design,omitempty"`
	Draft     map[string]any     `json:"draft,omitempty"`
	Revisions []brandingRevision `json:"revisions,omitempty"`
}

type brandingRevision struct {
	Version     int            `json:"version"`
	PublishedAt time.Time      `json:"published_at"`
	PublishedBy string         `json:"published_by,omitempty"`
	Note        string         `json:"note,omitempty"`
	Design      map[string]any `json:"design"`
}

func (s *server) brandingRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(brandingBodyLimit)
	s.mountBranding(r)
	s.mountPortalProfiles(r)
	return r
}

func (s *server) mountBranding(r chi.Router) {
	r.Get("/", s.getBranding)
	r.Put("/draft", s.saveBrandingDraft)
	r.Post("/settings", s.saveBrandingSettings)
	r.Get("/preview", s.previewPortal)
	r.Get("/languages", s.portalLanguages)
	r.Post("/validate", s.validateBranding)
	r.Post("/publish", s.publishBranding)
	r.Post("/rollback/{version}", s.rollbackBranding)
}

func (s *server) loadBranding(req *http.Request) (*brandingDoc, error) {
	ctx, cancel := dbCtx(req)
	defer cancel()
	var raw []byte
	if profile := profileFrom(req); profile != "" {
		if err := s.db.QueryRow(ctx, `SELECT doc FROM iam_v2.portal_profiles WHERE id = $1::uuid AND tenant_id = $2::uuid AND site_id = $3::uuid`,
			profile, s.tenantID, s.siteID).Scan(&raw); err != nil {
			return nil, err
		}
	} else if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(branding, '{}'::jsonb) FROM tenants WHERE id = $1`, s.tenantID).Scan(&raw); err != nil {
		return nil, err
	}
	var doc brandingDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		// A pre-versioning flat document is the published design; adopt it so the first save does not wipe it.
		var flat map[string]any
		if err2 := json.Unmarshal(raw, &flat); err2 != nil {
			return nil, err
		}
		doc = brandingDoc{Design: flat}
	}
	if doc.Design == nil && doc.Draft == nil && len(doc.Revisions) == 0 {
		var flat map[string]any
		if json.Unmarshal(raw, &flat) == nil && len(flat) > 0 {
			doc.Design = flat
		}
	}
	return &doc, nil
}

func (s *server) storeBranding(req *http.Request, doc *brandingDoc) error {
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	ctx, cancel := dbCtx(req)
	defer cancel()
	if profile := profileFrom(req); profile != "" {
		tag, err := s.db.Exec(ctx, `UPDATE iam_v2.portal_profiles SET doc = $1::jsonb, updated_at = now() WHERE id = $2::uuid AND tenant_id = $3::uuid AND site_id = $4::uuid`,
			string(raw), profile, s.tenantID, s.siteID)
		if err == nil && tag.RowsAffected() == 0 {
			err = errors.New("the portal profile no longer exists")
		}
		return err
	}
	_, err = s.db.Exec(ctx,
		`UPDATE tenants SET branding = $1::jsonb, updated_at = now() WHERE id = $2`, string(raw), s.tenantID)
	return err
}

func (s *server) getBranding(w http.ResponseWriter, req *http.Request) {
	if s.tenantID == "" {
		writeAwaitingAssignment(w, "portal branding")
		return
	}
	doc, err := s.loadBranding(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "branding load failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"design":    orEmpty(doc.Design),
		"draft":     orEmpty(doc.Draft),
		"revisions": revisionSummaries(doc.Revisions),
		"published": len(doc.Revisions) > 0 || len(doc.Design) > 0,
	})
}

// revisionSummaries omits each revision's full design from the listing.
func revisionSummaries(revs []brandingRevision) []map[string]any {
	out := make([]map[string]any, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- { // newest first
		out = append(out, map[string]any{
			"version":      revs[i].Version,
			"published_at": revs[i].PublishedAt,
			"published_by": revs[i].PublishedBy,
			"note":         revs[i].Note,
		})
	}
	return out
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// validateDesign refuses a design rather than repairing one: every field ends up inside the guest's page.
// The rules live in internal/portaldesign (an allowlist over a parsed tree, not a text denylist), which portald
// applies again before serving; a design is refused if its sanitised form would differ from what was written.
func validateDesign(d map[string]any) error {
	errs := portaldesign.Errors(portaldesign.Validate(d))
	if len(errs) == 0 {
		return nil
	}
	msg := portaldesign.Summary(errs)
	for _, e := range errs {
		if e.Field == "custom_css" || e.Field == "custom_html" {
			msg += ". This page collects room numbers and voucher codes, so executable or re-targeting content " +
				"in it could steal a client's credentials — styling and markup only"
			break
		}
	}
	return errors.New(msg)
}

// brandingBodyLimit caps every request; a body larger than the portaldesign field limits' sum is not a design.
func brandingBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, portaldesign.MaxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

type brandingValidateReq struct {
	Design map[string]any `json:"design"`
}

// validateBranding reports what the portal would do with a design; sanitized is what a guest would receive for
// the advanced fields. Nothing is stored, so no step-up.
func (s *server) validateBranding(w http.ResponseWriter, req *http.Request) {
	var in brandingValidateReq
	if err := decodeJSON(req, &in); err != nil {
		if refusedTooLarge(w, err) {
			return
		}
		jsonErr(w, http.StatusBadRequest, "bad_request", "body must be a JSON object with a design")
		return
	}
	if in.Design == nil {
		in.Design = map[string]any{}
	}
	issues := portaldesign.Validate(in.Design)
	if issues == nil {
		issues = []portaldesign.Issue{}
	}
	guest := portaldesign.ForGuests(in.Design)
	sanitized := map[string]any{
		"custom_css":  orString(guest["custom_css"]),
		"custom_html": orString(guest["custom_html"]),
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        len(portaldesign.Errors(issues)) == 0,
		"issues":    issues,
		"sanitized": sanitized,
		"templates": portaldesign.Templates,
	})
}

// refusedTooLarge answers 413 when a body hit brandingBodyLimit.
func refusedTooLarge(w http.ResponseWriter, err error) bool {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		jsonErr(w, http.StatusRequestEntityTooLarge, "too_large",
			fmt.Sprintf("the design is larger than the portal accepts (%d KB)", portaldesign.MaxBodyBytes/1024))
		return true
	}
	return false
}

func orString(v any) string {
	s, _ := v.(string)
	return s
}

type brandingDraftReq struct {
	Design map[string]any `json:"design"`
}

func (s *server) saveBrandingDraft(w http.ResponseWriter, req *http.Request) {
	var in brandingDraftReq
	if err := decodeJSON(req, &in); err != nil {
		if refusedTooLarge(w, err) {
			return
		}
		jsonErr(w, http.StatusBadRequest, "bad_request", "body must be a JSON object with a design")
		return
	}
	if in.Design == nil {
		jsonErr(w, http.StatusBadRequest, "validation", "a design is required")
		return
	}
	// Validate the draft too so it cannot be unpublishable.
	if err := validateDesign(in.Design); err != nil {
		jsonErr(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	doc, err := s.loadBranding(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "branding load failed")
		return
	}
	doc.Draft = in.Design
	if err := s.storeBranding(req, doc); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "draft could not be saved")
		return
	}
	s.audit(req, "branding.draft_saved", brandingTarget(req), brandingTargetID(req, s.tenantID), nil)
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

type brandingPublishReq struct {
	Design   map[string]any `json:"design"`
	Note     string         `json:"note"`
	Password string         `json:"password"`
}

// publishBranding requires step-up: it changes the page where guests enter surname and room number.
func (s *server) publishBranding(w http.ResponseWriter, req *http.Request) {
	var in brandingPublishReq
	if err := decodeJSON(req, &in); err != nil {
		if refusedTooLarge(w, err) {
			return
		}
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !s.reauth(req, in.Password) {
		s.reauthRefused(w, req, "password confirmation required")
		return
	}
	doc, err := s.loadBranding(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "branding load failed")
		return
	}
	design := in.Design
	if design == nil {
		design = doc.Draft
	}
	if len(design) == 0 {
		jsonErr(w, http.StatusBadRequest, "validation", "there is no design to publish")
		return
	}
	if err := validateDesign(design); err != nil {
		jsonErr(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	sess := sessFrom(req.Context())
	actor := ""
	if sess != nil {
		actor = sess.Email
	}
	next := 1
	if n := len(doc.Revisions); n > 0 {
		next = doc.Revisions[n-1].Version + 1
	}
	doc.Revisions = append(doc.Revisions, brandingRevision{
		Version: next, PublishedAt: time.Now().UTC(), PublishedBy: actor, Note: in.Note, Design: design,
	})
	if len(doc.Revisions) > maxRevisions {
		doc.Revisions = doc.Revisions[len(doc.Revisions)-maxRevisions:]
	}
	doc.Design = design
	doc.Draft = nil
	if err := s.storeBranding(req, doc); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the design could not be published")
		return
	}
	s.audit(req, "branding.published", brandingTarget(req), brandingTargetID(req, s.tenantID), map[string]any{"version": next, "note": in.Note})
	writeJSON(w, http.StatusOK, map[string]any{"version": next})
}

type brandingRollbackReq struct {
	Password string `json:"password"`
}

// rollbackBranding re-publishes an earlier revision as a new one; it never rewinds the list, which would erase history.
func (s *server) rollbackBranding(w http.ResponseWriter, req *http.Request) {
	var in brandingRollbackReq
	if err := decodeJSON(req, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !s.reauth(req, in.Password) {
		s.reauthRefused(w, req, "password confirmation required")
		return
	}
	var want int
	if _, err := fmt.Sscanf(chi.URLParam(req, "version"), "%d", &want); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "version must be a number")
		return
	}
	doc, err := s.loadBranding(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "branding load failed")
		return
	}
	var target *brandingRevision
	for i := range doc.Revisions {
		if doc.Revisions[i].Version == want {
			target = &doc.Revisions[i]
			break
		}
	}
	if target == nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such design version")
		return
	}
	sess := sessFrom(req.Context())
	actor := ""
	if sess != nil {
		actor = sess.Email
	}
	next := doc.Revisions[len(doc.Revisions)-1].Version + 1
	doc.Revisions = append(doc.Revisions, brandingRevision{
		Version: next, PublishedAt: time.Now().UTC(), PublishedBy: actor,
		Note:   fmt.Sprintf("rolled back to version %d", want),
		Design: target.Design,
	})
	if len(doc.Revisions) > maxRevisions {
		doc.Revisions = doc.Revisions[len(doc.Revisions)-maxRevisions:]
	}
	doc.Design = target.Design
	doc.Draft = nil
	if err := s.storeBranding(req, doc); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the rollback could not be published")
		return
	}
	s.audit(req, "branding.rolled_back", brandingTarget(req), brandingTargetID(req, s.tenantID),
		map[string]any{"to_version": want, "new_version": next})
	writeJSON(w, http.StatusOK, map[string]any{"version": next, "restored_from": want})
}

type brandingSettingsReq struct {
	Design   map[string]any `json:"design"`
	Password string         `json:"password"`
}

// advancedChanged reports whether this save alters the executable-content escape hatch. Only a change to
// those fields needs step-up: every other field is constrained by validateDesign and cannot carry executable content.
func advancedChanged(next, current map[string]any) bool {
	get := func(m map[string]any, k string) string {
		if m == nil {
			return ""
		}
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	// portaldesign.AdvancedFields is the list the portal treats as markup; Hotel Admin's ADVANCED_KEYS mirrors it.
	for _, k := range portaldesign.AdvancedFields {
		if get(next, k) != get(current, k) {
			return true
		}
	}
	return false
}

func (s *server) saveBrandingSettings(w http.ResponseWriter, req *http.Request) {
	if s.tenantID == "" {
		writeAwaitingAssignment(w, "portal branding")
		return
	}
	var in brandingSettingsReq
	if err := decodeJSON(req, &in); err != nil {
		if refusedTooLarge(w, err) {
			return
		}
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if in.Design == nil {
		jsonErr(w, http.StatusBadRequest, "validation", "there are no settings to save")
		return
	}
	if err := validateDesign(in.Design); err != nil {
		jsonErr(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	doc, err := s.loadBranding(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "branding load failed")
		return
	}
	if advancedChanged(in.Design, doc.Design) && !s.reauth(req, in.Password) {
		s.reauthRefused(w, req,
			"confirm your password to change the portal's custom CSS or HTML. This page collects room numbers "+
				"and voucher codes, so the styling and markup injected into it are confirmed separately.")
		return
	}

	sess := sessFrom(req.Context())
	actor := ""
	if sess != nil {
		actor = sess.Email
	}
	next := 1
	if n := len(doc.Revisions); n > 0 {
		next = doc.Revisions[n-1].Version + 1
	}
	doc.Revisions = append(doc.Revisions, brandingRevision{
		Version: next, PublishedAt: time.Now().UTC(), PublishedBy: actor, Design: in.Design,
	})
	if len(doc.Revisions) > maxRevisions {
		doc.Revisions = doc.Revisions[len(doc.Revisions)-maxRevisions:]
	}
	doc.Design = in.Design
	doc.Draft = nil
	if err := s.storeBranding(req, doc); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the settings could not be saved")
		return
	}
	s.audit(req, "branding.published", brandingTarget(req), brandingTargetID(req, s.tenantID), map[string]any{"version": next, "via": "settings"})
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

// previewPortal returns portald's real landing HTML inside JSON, never as a document from this origin; the admin
// renders it in a sandboxed iframe (sandbox="allow-scripts", opaque origin).
func (s *server) previewPortal(w http.ResponseWriter, req *http.Request) {
	// Fetched on loopback without following redirects (see portal_loopback.go).
	path := portalPreviewPath
	if profile := profileFrom(req); profile != "" {
		path += "?" + portalPreviewProfileParam + "=" + profile
	}
	status, body, err := s.portalGet(req.Context(), path)
	if err != nil {
		// The portal is not answering; the operator's settings are fine.
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the Client Portal service is not answering on this appliance, so a live preview cannot be shown. "+
				"Your settings are unaffected.")
		return
	}
	if status != http.StatusOK {
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the Client Portal service did not return its sign-in page, so a live preview cannot be shown.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": string(body)})
}

// portalLanguages proxies the portal's shipped wording (the portal owns the words; no copy in the admin) and is
// not cached because the wording changes when portald is redeployed.
func (s *server) portalLanguages(w http.ResponseWriter, req *http.Request) {
	status, body, err := s.portalGet(req.Context(), portalLanguagesPath)
	if err != nil {
		// The portal is not answering; the operator's settings are fine.
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the Client Portal service is not answering on this appliance, so its built-in wording cannot be "+
				"shown. Your settings are unaffected.")
		return
	}
	if status != http.StatusOK {
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the Client Portal service did not return its built-in wording.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
