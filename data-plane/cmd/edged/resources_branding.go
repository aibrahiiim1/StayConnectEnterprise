package main

// PORTAL BRANDING — a design an operator publishes, not a JSON blob they edit.
//
// WHAT WAS THERE
// --------------
// One endpoint that read tenants.branding and one that overwrote it, behind a textarea containing raw JSON.
// It had no schema, no validation, no history and no rollback — and, most tellingly, NOTHING READ IT. The
// captive portal never fetched branding at all, so whatever an operator typed had no effect on what a guest
// saw. It was write-only configuration.
//
// THE SHAPE OF THE DOCUMENT
// -------------------------
// Still one jsonb column — the internal representation is an implementation detail and the operator never
// sees it — but now structured:
//
//	{
//	  "design":    { ...the PUBLISHED design the portal serves... },
//	  "draft":     { ...work in progress, never served to a guest... },
//	  "revisions": [ { version, published_at, published_by, design } ... ]
//	}
//
// Revisions are append-only and bounded. Rollback re-publishes an earlier revision as a NEW revision rather
// than rewinding the list, so the history says what actually happened: "on Tuesday they went back to
// version 3", not "version 4 never existed".
//
// SECURITY OF THE ADVANCED MODE
// -----------------------------
// Operators may supply custom CSS and a custom HTML fragment. This is a CAPTIVE PORTAL: the page collects a
// guest's room number, surname and voucher codes, so anything injected into it can steal credentials. Script
// is therefore not merely discouraged, it is refused — see validateAdvanced, which rejects the document
// rather than sanitising it. Silently stripping a tag teaches an operator that their template "worked".

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// maxRevisions bounds the history kept in the document. Branding is presentation, not audit evidence: a
// hotel that has re-themed two hundred times does not need its first attempt, and an unbounded array inside
// a row read on every portal load is a performance problem waiting to happen.
const maxRevisions = 20

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
	r.Get("/", s.getBranding)
	r.Put("/draft", s.saveBrandingDraft)
	r.Post("/publish", s.publishBranding)
	r.Post("/rollback/{version}", s.rollbackBranding)
	return r
}

func (s *server) loadBranding(req *http.Request) (*brandingDoc, error) {
	ctx, cancel := dbCtx(req)
	defer cancel()
	var raw []byte
	if err := s.db.QueryRow(ctx,
		`SELECT COALESCE(branding, '{}'::jsonb) FROM tenants WHERE id = $1`, s.tenantID).Scan(&raw); err != nil {
		return nil, err
	}
	var doc brandingDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		// A pre-existing document written before designs were versioned IS the published design. Adopting it
		// rather than discarding it means the first save under the new shape does not silently wipe whatever
		// the hotel had.
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

// revisionSummaries omits each revision's full design from the listing. The history panel needs to say what
// happened and when; the designs themselves are fetched only when an operator actually rolls one back.
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

// ------------------------------------------------------------------------------------------- validation

var (
	// A CSS colour an operator may set. Deliberately narrow: this value is interpolated into a style
	// property, so "anything the browser accepts" would include url() and expressions.
	reColor  = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$|^rgb\(\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*\)$`)
	reLength = regexp.MustCompile(`^\d{1,3}(px|rem|em|%)$`)
	// Script in any of its spellings. Matched case-insensitively against the raw text rather than a parsed
	// tree, because the parse that matters happens in the guest's browser, not here.
	reScript = regexp.MustCompile(`(?is)<\s*script|javascript\s*:|\son[a-z]+\s*=|<\s*iframe|<\s*object|<\s*embed|<\s*form|expression\s*\(|@import|behaviour\s*:|behavior\s*:`)
)

// validateDesign refuses a design rather than repairing one.
//
// Every field here ends up inside the guest's page. A URL that is not a URL, a colour that is really a
// url(javascript:...), or a "logo" pointing at an attacker's host are all ways to turn the sign-in page into
// something that collects credentials for somebody else.
func validateDesign(d map[string]any) error {
	str := func(k string) (string, bool) {
		v, ok := d[k]
		if !ok || v == nil {
			return "", false
		}
		s, ok := v.(string)
		return s, ok
	}
	for _, k := range []string{"brand_color", "brand_color_dark", "text_color"} {
		if v, ok := str(k); ok && v != "" && !reColor.MatchString(v) {
			return fmt.Errorf("%s must be a hex or rgb() colour", k)
		}
	}
	if v, ok := str("corner_radius"); ok && v != "" && !reLength.MatchString(v) {
		return fmt.Errorf("corner_radius must be a length such as 18px")
	}
	for _, k := range []string{"logo_url", "background_url"} {
		if v, ok := str(k); ok && v != "" {
			// "//host/path" is PROTOCOL-RELATIVE: it starts with a slash and resolves to an external host
			// over whatever scheme the page was served with. A leading-slash check alone accepts it, which is
			// how an "appliance path" becomes somebody else's server.
			if strings.HasPrefix(v, "//") {
				return fmt.Errorf("%s must not be protocol-relative; use an appliance path or a full https URL", k)
			}
			if !strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "https://") && !strings.HasPrefix(v, "data:image/") {
				return fmt.Errorf("%s must be an appliance path, an https URL or an inline image", k)
			}
			if reScript.MatchString(v) {
				return fmt.Errorf("%s contains script", k)
			}
		}
	}
	if v, ok := str("hotel_name"); ok && len(v) > 120 {
		return fmt.Errorf("hotel_name is too long")
	}
	if v, ok := str("font_family"); ok && strings.ContainsAny(v, "{};<>") {
		return fmt.Errorf("font_family contains characters that are not part of a font stack")
	}
	return validateAdvanced(d)
}

// validateAdvanced polices the custom HTML/CSS escape hatch.
//
// REFUSES, NEVER SANITISES. Stripping a <script> would leave the operator believing their template works and
// leave the next reader unsure what the stored document really contains. A refusal names the problem while
// they are still looking at it.
func validateAdvanced(d map[string]any) error {
	for _, k := range []string{"custom_css", "custom_html"} {
		v, ok := d[k]
		if !ok || v == nil {
			continue
		}
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be text", k)
		}
		if len(s) > 64*1024 {
			return fmt.Errorf("%s is larger than 64 KB", k)
		}
		if reScript.MatchString(s) {
			return fmt.Errorf(
				"%s contains script, an inline event handler, a frame or an @import. This page collects room "+
					"numbers and voucher codes, so executable content in it can steal a guest's credentials — "+
					"styling and markup only", k)
		}
	}
	return nil
}

// ------------------------------------------------------------------------------------------- mutations

type brandingDraftReq struct {
	Design map[string]any `json:"design"`
}

func (s *server) saveBrandingDraft(w http.ResponseWriter, req *http.Request) {
	var in brandingDraftReq
	if err := decodeJSON(req, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "body must be a JSON object with a design")
		return
	}
	if in.Design == nil {
		jsonErr(w, http.StatusBadRequest, "validation", "a design is required")
		return
	}
	// The draft is validated too. A draft that cannot be published is a trap: the operator designs against it
	// for an hour and learns at the last step that it was never acceptable.
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
	s.audit(req, "branding.draft_saved", "tenant", s.tenantID, nil)
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

type brandingPublishReq struct {
	Design   map[string]any `json:"design"`
	Note     string         `json:"note"`
	Password string         `json:"password"`
}

// publishBranding makes a design the one guests see.
//
// STEP-UP REQUIRED. Publishing changes the page every arriving guest types their surname and room number
// into; it is the surface an attacker would most like to control, so the change is attributed to a
// re-authenticated operator.
func (s *server) publishBranding(w http.ResponseWriter, req *http.Request) {
	var in brandingPublishReq
	if err := decodeJSON(req, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !s.reauth(req, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	doc, err := s.loadBranding(req)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "branding load failed")
		return
	}
	design := in.Design
	if design == nil {
		design = doc.Draft // publishing "what I have been working on"
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
	doc.Draft = nil // published; there is no longer anything in progress
	if err := s.storeBranding(req, doc); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the design could not be published")
		return
	}
	s.audit(req, "branding.published", "tenant", s.tenantID, map[string]any{"version": next, "note": in.Note})
	writeJSON(w, http.StatusOK, map[string]any{"version": next})
}

type brandingRollbackReq struct {
	Password string `json:"password"`
}

// rollbackBranding re-publishes an earlier revision AS A NEW REVISION.
//
// It does not rewind the list. History should say what happened -- "they went back to version 3 on Tuesday"
// -- and a rollback that deleted version 4 would leave a record implying it never existed, which is the same
// class of lie as deleting an audit row.
func (s *server) rollbackBranding(w http.ResponseWriter, req *http.Request) {
	var in brandingRollbackReq
	if err := decodeJSON(req, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !s.reauth(req, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
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
	s.audit(req, "branding.rolled_back", "tenant", s.tenantID,
		map[string]any{"to_version": want, "new_version": next})
	writeJSON(w, http.StatusOK, map[string]any{"version": next, "restored_from": want})
}
