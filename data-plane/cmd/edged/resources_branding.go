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
// is therefore not merely discouraged, it is refused — see validateDesign, which rejects the document rather
// than sanitising it (silently stripping a tag teaches an operator that their template "worked"), and
// internal/portaldesign, whose allowlist both this service and the portal apply.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/portaldesign"
)

// maxRevisions bounds the history kept in the document. Branding is presentation, not audit evidence: a
// hotel that has re-themed two hundred times does not need its first attempt, and an unbounded array inside
// a row read on every portal load is a performance problem waiting to happen.
const maxRevisions = 20

// portalPreviewURL is portald's own sign-in page, on the loopback of this appliance. It is the source the
// preview renders, so the preview cannot drift from what a guest receives.
const portalPreviewURL = "http://127.0.0.1:8380/"

// portalLanguagesURL is the portal's own shipped wording -- the map it renders into every guest's page.
// Hotel Admin reads it through here so the Languages screen can show an operator the ACTUAL Italian a guest
// receives, rather than an empty field with the English behind it as a placeholder.
const portalLanguagesURL = "http://127.0.0.1:8380/api/languages"

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
	r.Get("/", s.getBranding)
	r.Put("/draft", s.saveBrandingDraft)
	// SETTINGS IS WHAT AN OPERATOR PRESSES. It is /publish's behaviour under the only vocabulary a hotel has:
	// there is one current configuration, you change it, you save it, guests see it. The revision history it
	// appends is kept for audit and recovery and is not something the screen talks about.
	r.Post("/settings", s.saveBrandingSettings)
	// The preview reads the REAL portal page rather than a second implementation of it in the admin. See
	// previewPortal.
	r.Get("/preview", s.previewPortal)
	// The shipped wording, read from the portal for the same reason. See portalLanguages.
	r.Get("/languages", s.portalLanguages)
	// What the portal would make of a design, without saving it. See validateBranding.
	r.Post("/validate", s.validateBranding)
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

// validateDesign refuses a design rather than repairing one.
//
// Every field here ends up inside the guest's page. A URL that is not a URL, a colour that is really a
// url(javascript:...), a "logo" pointing at an attacker's host, or a fragment carrying <base href> are all ways
// to turn the sign-in page into something that collects credentials for somebody else.
//
// THE RULES LIVE IN internal/portaldesign, not here, because portald applies exactly the same rules again
// before it serves a design to a guest. This used to be a regular-expression denylist over the raw text, which
// lost to every spelling it had not thought of (an <img/onerror> with no space before the handler, a <base>
// with no script in it at all, an entity-encoded javascript&#58;). It is now an allowlist over a parsed tree:
// the design is refused if its sanitised form would differ from what the operator wrote, and the refusal lists
// precisely what would have been removed.
func validateDesign(d map[string]any) error {
	errs := portaldesign.Errors(portaldesign.Validate(d))
	if len(errs) == 0 {
		return nil
	}
	msg := portaldesign.Summary(errs)
	for _, e := range errs {
		if e.Field == "custom_css" || e.Field == "custom_html" {
			msg += ". This page collects room numbers and voucher codes, so executable or re-targeting content " +
				"in it could steal a guest's credentials — styling and markup only"
			break
		}
	}
	return errors.New(msg)
}

// brandingBodyLimit caps every request on this surface. The design is read on each guest page load, and the
// field limits in portaldesign already bound what a valid one can weigh; a body larger than their sum is not a
// design, it is a way to make edged buffer megabytes before refusing it.
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

// validateBranding answers "what would the portal do with this design?" without saving anything.
//
// The designer calls it while an operator types in the Advanced editors, so a problem is named where it is
// being made -- "removed the onerror attribute on <img>" -- rather than as a refusal at the moment of saving.
// sanitized carries what a guest would actually receive for the two advanced fields, so the preview can
// render the SAFE version and an operator can adopt it with one click. Nothing here is stored, and nothing
// here needs the step-up: it changes nothing.
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

// refusedTooLarge answers 413 when a body hit brandingBodyLimit, rather than calling a design that is merely
// too big "malformed".
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

// ------------------------------------------------------------------------------------------- mutations

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
		if refusedTooLarge(w, err) {
			return
		}
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

// ------------------------------------------------------------------- the operator-facing settings surface

// saveBrandingSettings is what "Save changes" calls.
//
// WHY A SECOND DOOR ONTO THE SAME ROOM. publishBranding is correct and stays: it appends an immutable
// revision, records who and when, and is what rollback re-publishes. What was wrong was the VOCABULARY it
// forced on the screen -- draft, publish, version, live version, rollback. A hotel has one guest portal and
// one current configuration for it; "which version is live" is a question the product invented and then asked
// the operator to answer. So the history stays, in the document, for audit and recovery, and the screen says
// Save changes.
//
// THE STEP-UP NOW GUARDS THE SURFACE IT WAS WRITTEN FOR, AND ONLY THAT.
//
// Requiring a password to change a hotel's name is ceremony; requiring one to inject CSS and markup into the
// page that collects room numbers and voucher codes is the control. The two were the same check because they
// were the same endpoint. They are separated here on a measurable line rather than a judgement: a save that
// leaves custom_css and custom_html byte-identical to what is already stored cannot introduce executable
// content, because every other field is constrained by validateDesign to a colour, a length, a font stack, a
// bounded string or an appliance/https image path. A save that CHANGES either of them re-authenticates.
//
// Everything else that made publishing accountable is unchanged: the operator holds a permission, the action
// is audited with the actor, and the design is validated and refused rather than repaired.
type brandingSettingsReq struct {
	Design   map[string]any `json:"design"`
	Password string         `json:"password"`
}

// advancedChanged reports whether this save alters the executable-content escape hatch.
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
	// The list is portaldesign.AdvancedFields -- the SAME list the portal treats as markup, and the one
	// ADVANCED_KEYS in the Hotel Admin designer mirrors. A new field that can carry markup or CSS is added
	// there once, and this rule, the portal and the screen all follow.
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
		jsonErr(w, http.StatusUnauthorized, "reauth_required",
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
	doc.Draft = nil // there is nothing in progress once it is saved
	if err := s.storeBranding(req, doc); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the settings could not be saved")
		return
	}
	s.audit(req, "branding.published", "tenant", s.tenantID, map[string]any{"version": next, "via": "settings"})
	writeJSON(w, http.StatusOK, map[string]any{"saved": true})
}

// previewPortal hands the admin the REAL guest portal page.
//
// The alternative was to draw an approximation of the portal inside Hotel Admin, which is a second
// implementation of a page whose whole purpose is to be exactly what the guest sees. It would agree with the
// portal on the day it was written and drift from it forever after -- and a preview that is subtly wrong is
// worse than none, because it is believed.
//
// So the admin asks for portald's own landing HTML and renders it in a sandboxed frame with the settings
// being edited supplied to it. What the operator is looking at is the real template, the real stylesheet and
// the real script.
//
// THE HTML IS RETURNED INSIDE JSON, never served as a document from this origin. The admin puts it in an
// iframe with `sandbox="allow-scripts"` -- an opaque origin with no form submission and no access to the
// admin page around it -- and the frame needs no network at all, because the design is injected rather than
// fetched.
func (s *server) previewPortal(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, portalPreviewURL, nil)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "preview request could not be built")
		return
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		// Said plainly: the preview is a convenience and its absence must not read as "your settings are
		// broken". It is the PORTAL that is not answering.
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the guest portal service is not answering on this appliance, so a live preview cannot be shown. "+
				"Your settings are unaffected.")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the guest portal service did not return its sign-in page, so a live preview cannot be shown.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": string(body)})
}

// portalLanguages hands the admin the wording the portal actually ships.
//
// WHY THIS IS A PROXY AND NOT A COPY. The obvious implementation is six dictionaries in the admin's own
// source. That is 306 strings maintained in two places, and the failure mode is silent and expensive: the
// operator edits Italian against wording no guest receives, is satisfied, and the mismatch surfaces as a
// guest complaint months later. The portal owns the words; this fetches them.
//
// It is fetched fresh rather than cached in edged. The wording changes when portald is redeployed, which is
// exactly when a cached copy would be wrong, and one loopback request per visit to a settings screen is not
// a cost worth being clever about.
func (s *server) portalLanguages(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithTimeout(req.Context(), 5*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, portalLanguagesURL, nil)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the request could not be built")
		return
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		// Named for what it is. "Failed to load languages" would send an operator looking at their own
		// settings; it is the PORTAL service that is not answering.
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the guest portal service is not answering on this appliance, so its built-in wording cannot be "+
				"shown. Your settings are unaffected.")
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil || resp.StatusCode != http.StatusOK {
		jsonErr(w, http.StatusServiceUnavailable, "portal_unreachable",
			"the guest portal service did not return its built-in wording.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
