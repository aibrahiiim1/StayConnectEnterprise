package main

// Phase-3 Hotel-Admin surface: THE PMS INTERFACE ITSELF.
//
// resources_phase3.go covers what the interface produces — Stays, events, resolutions, alerts. This file
// covers the interface as a configured thing: which connector a property runs, which Revision of its
// configuration is published, which guest networks route to it, whether its secret has been rotated, whether
// it is actually connected, and how far behind it is.
//
// Four rules shape all of it, and each exists because the alternative is a specific bad afternoon:
//
//	REVISIONS ARE IMMUTABLE. A Revision is never edited. Changing configuration means creating the next
//	Revision and publishing it, so "what was this interface configured as when that Stay resolved?" always
//	has an answer. Every Stay, resolution and Auth Context pins the exact Revision it was decided under; if
//	Revisions could be edited, those pins would point at text that no longer says what it said.
//
//	PUBLISHING IS A SEPARATE, DELIBERATE ACT. Creating a Revision changes nothing. Publishing it changes
//	what every subsequent guest is resolved against, so it takes a step-up, an expected version, and a
//	reason — and it refuses rather than overwrite a publication somebody else made while this operator was
//	looking at the form.
//
//	SECRETS ARE WRITE-ONLY. The credential can be set and rotated; it can never be read back, not even by
//	the operator who typed it. There is no endpoint that returns it, no field that carries it, and the list
//	surfaces show only the generation number and when it was superseded.
//
//	HEALTH IS DERIVED, NEVER STORED AS A VERDICT. The runtime row carries facts — last heartbeat, last
//	valid event, resync state. The words "healthy" and "degraded" are computed from those facts at read
//	time. A stored verdict is a claim that keeps its value after it stops being true.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
)

// ---------- interfaces ----------

type pmsInterfaceRow struct {
	ID                string `json:"id"`
	ConnectorKind     string `json:"connector_kind"`
	DisplayLabel      string `json:"display_label"`
	LifecycleState    string `json:"lifecycle_state"`
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
	// CurrentRevisionNo is what an operator actually recognises; the id is for the machine.
	CurrentRevisionNo *int `json:"current_revision_no,omitempty"`
	RevisionCount     int  `json:"revision_count"`
	// Published says whether this interface has a published Revision at all. An interface without one
	// resolves nothing, and that is worth stating plainly rather than leaving as an empty field.
	Published bool `json:"published"`
	// SecretGeneration is the CURRENT credential generation number — never the credential.
	SecretGeneration *int       `json:"secret_generation,omitempty"`
	SecretRotatedAt  *time.Time `json:"secret_rotated_at,omitempty"`
}

func (s *server) pmsInterfacesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listPMSInterfaces)
	r.Get("/{id}", s.getPMSInterface)
	r.Get("/{id}/revisions", s.listPMSInterfaceRevisions)
	r.Get("/{id}/health", s.getPMSInterfaceHealth)
	// Creating an interface and authoring its configuration. Both were absent, which left the endpoint the
	// connector dials -- and every timeout, bound and mode it reads -- unreachable from the product.
	r.Post("/", s.createPMSInterface)
	r.Post("/{id}/revisions", s.authorPMSInterfaceRevision)
	r.Post("/{id}/publish", s.publishPMSInterfaceRevision)
	r.Post("/{id}/full-resync", s.requestFullResync)
	r.Post("/{id}/secret", s.rotatePMSInterfaceSecret)
	// THE LIFECYCLE TRANSITION. An interface is created AUTH_DISABLED and publishing a revision does not
	// change that — deliberately, because publishing decides WHAT the connector would dial and activating
	// decides WHETHER it dials at all, and collapsing the two means the moment an operator finishes typing
	// a configuration is the moment a socket opens to the property's PMS.
	//
	// What was missing is the second act. pmsd selects `WHERE lifecycle_state='ACTIVE'`, and nothing in the
	// product could produce that state, so a fully created and published interface was never picked up by
	// the connector and never would be. The symptom is the worst kind: every screen reports success and
	// nothing connects.
	r.Post("/{id}/lifecycle", s.setPMSInterfaceLifecycle)
	return r
}

type setLifecycleReq struct {
	State      string `json:"state"`
	ReasonCode string `json:"reason_code"`
	Password   string `json:"password"`
}

// pmsLifecycleTransitions is the allowed transition map. The schema's CHECK constraint lists the four legal
// values; it cannot say which moves between them make sense, and that is what this encodes.
//
// DECOMMISSIONED is terminal and is NOT reachable here. Retiring an interface has consequences this route
// does not handle — Stays and events keep referencing it, and the authoring path already refuses to
// configure one — so it needs its own deliberate operation rather than an option in a dropdown.
var pmsLifecycleTransitions = map[string]map[string]bool{
	"ACTIVE":        {"AUTH_DISABLED": true, "DRAINING": true},
	"AUTH_DISABLED": {"ACTIVE": true},
	"DRAINING":      {"AUTH_DISABLED": true, "ACTIVE": true},
}

// setPMSInterfaceLifecycle activates, disables or drains a PMS Interface.
//
// Step-up and a reason code are required in both directions. Activating opens a live connection to the
// property's PMS; disabling stops resolving every guest on every network mapped to it. Neither is a change
// anybody should be able to make by mis-clicking, and Phase-3 §25 asks for step-up on exactly this surface.
func (s *server) setPMSInterfaceLifecycle(w http.ResponseWriter, r *http.Request) {
	var in setLifecycleReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	want := strings.TrimSpace(in.State)
	if want != "ACTIVE" && want != "AUTH_DISABLED" && want != "DRAINING" {
		jsonErr(w, http.StatusBadRequest, "validation",
			"state must be ACTIVE, AUTH_DISABLED or DRAINING (DECOMMISSIONED is terminal and is not set here)")
		return
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason code is required: this decides whether a live PMS connection exists")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var cur, label, currentRev string
	if err := tx.QueryRow(ctx, `SELECT lifecycle_state, COALESCE(display_label,''),
	        COALESCE(current_revision_id::text,'')
	      FROM iam_v2.pms_interfaces WHERE id=$1 AND tenant_id=$2 AND site_id=$3 FOR UPDATE`,
		id, s.tenantID, s.siteID).Scan(&cur, &label, &currentRev); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "interface not found")
		return
	}
	if cur == want {
		// Idempotent, and reported as such rather than as a change that did not happen.
		writeJSON(w, http.StatusOK, map[string]any{"id": id, "lifecycle_state": cur, "changed": false})
		return
	}
	if cur == "DECOMMISSIONED" {
		jsonErr(w, http.StatusConflict, "conflict", "this interface is decommissioned; that state is terminal")
		return
	}
	if !pmsLifecycleTransitions[cur][want] {
		jsonErr(w, http.StatusConflict, "conflict", "cannot move a PMS interface from "+cur+" to "+want)
		return
	}
	// ACTIVATION REQUIRES A PUBLISHED REVISION. Without one there is no endpoint, no timeout and no
	// timezone, so pmsd would select the interface, find nothing to dial, and report an error the operator
	// has no way to connect back to a missing step.
	if want == "ACTIVE" && currentRev == "" {
		jsonErr(w, http.StatusConflict, "validation",
			"publish a revision before activating: an interface with no published revision has no endpoint to dial")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET lifecycle_state=$4
	      WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, id, s.tenantID, s.siteID, want); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "lifecycle update failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "pms_interface.lifecycle", "pms_interface", id,
		map[string]any{"from": cur, "to": want, "reason_code": in.ReasonCode, "display_label": label})
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "lifecycle_state": want, "previous_lifecycle_state": cur, "changed": true,
	})
}

const pmsInterfaceCols = `i.id::text, i.connector_kind, i.display_label, i.lifecycle_state,
       COALESCE(i.current_revision_id::text,''),
       (SELECT r.revision_no FROM iam_v2.pms_interface_revisions r WHERE r.id = i.current_revision_id),
       (SELECT count(*) FROM iam_v2.pms_interface_revisions r WHERE r.pms_interface_id = i.id)::int,
       (SELECT g.generation_no FROM iam_v2.pms_interface_secret_generations g
         WHERE g.pms_interface_id = i.id AND g.superseded_at IS NULL
         ORDER BY g.generation_no DESC LIMIT 1),
       (SELECT max(g.superseded_at) FROM iam_v2.pms_interface_secret_generations g
         WHERE g.pms_interface_id = i.id)`

func scanPMSInterface(row interface{ Scan(...any) error }, e *pmsInterfaceRow) error {
	if err := row.Scan(&e.ID, &e.ConnectorKind, &e.DisplayLabel, &e.LifecycleState,
		&e.CurrentRevisionID, &e.CurrentRevisionNo, &e.RevisionCount,
		&e.SecretGeneration, &e.SecretRotatedAt); err != nil {
		return err
	}
	e.Published = e.CurrentRevisionID != ""
	return nil
}

func (s *server) listPMSInterfaces(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT `+pmsInterfaceCols+`
		FROM iam_v2.pms_interfaces i
		WHERE i.tenant_id=$1 AND i.site_id=$2
		ORDER BY i.display_label, i.id`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []pmsInterfaceRow{}
	for rows.Next() {
		var e pmsInterfaceRow
		if err := scanPMSInterface(rows, &e); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		out = append(out, e)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interfaces": out})
}

func (s *server) getPMSInterface(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var e pmsInterfaceRow
	err := scanPMSInterface(s.db.QueryRow(ctx, `SELECT `+pmsInterfaceCols+`
		FROM iam_v2.pms_interfaces i
		WHERE i.tenant_id=$1 AND i.site_id=$2 AND i.id=$3::uuid`,
		s.tenantID, s.siteID, chi.URLParam(r, "id")), &e)
	if errors.Is(err, pgx.ErrNoRows) {
		// Scoped to this site, so an interface belonging to another site is indistinguishable from one that
		// does not exist. A different answer would confirm which sites a neighbouring property runs.
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	// The guest networks that route to this interface belong on its detail: "which guests does this reach?"
	// is the question an operator has when they are about to publish or rotate anything.
	routes, err := s.routesForInterface(r, e.ID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interface": e, "guest_networks": routes})
}

// ---------- revisions ----------

type pmsRevisionRow struct {
	ID                    string `json:"id"`
	RevisionNo            int    `json:"revision_no"`
	SourceTimezone        string `json:"source_timezone"`
	FolioIdentityStrategy string `json:"folio_identity_strategy"`
	NormalizationVersion  int    `json:"normalization_version"`
	SourceFingerprint     string `json:"source_fingerprint,omitempty"`
	// Config is the Revision's declarative configuration, REDACTED before it leaves the process — see
	// redactRevisionConfig. A Revision's config is operator-authored and can acquire anything over time.
	Config json.RawMessage `json:"config"`
	// Published marks the ONE Revision this interface currently resolves against. It is derived from the
	// interface's current_revision_id, never from "the highest revision number" — a property can publish an
	// older Revision to roll back, and then the highest number is exactly the wrong answer.
	Published bool `json:"published"`
}

// secretishKeys are config keys whose VALUE must never be rendered to an operator. The Revision config is
// operator-authored JSON, so it will eventually contain a credential somebody pasted into the wrong field —
// and an admin page is precisely where that becomes a screenshot in a support ticket.
var secretishKeys = []string{"password", "secret", "token", "key", "credential", "apikey", "api_key", "auth"}

func redactRevisionConfig(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// Unparseable config is not rendered at all. Passing it through would defeat the redaction below,
		// and an operator cannot act on malformed JSON anyway.
		return json.RawMessage(`{"unreadable":true}`)
	}
	redactMap(m)
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{"unreadable":true}`)
	}
	return out
}

func redactMap(m map[string]any) {
	for k, v := range m {
		lower := strings.ToLower(k)
		hit := false
		for _, s := range secretishKeys {
			if strings.Contains(lower, s) {
				hit = true
				break
			}
		}
		if hit {
			m[k] = "[redacted]"
			continue
		}
		if child, ok := v.(map[string]any); ok {
			redactMap(child)
		}
	}
}

func (s *server) listPMSInterfaceRevisions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	rows, err := s.db.Query(ctx, `
		SELECT rev.id::text, rev.revision_no, rev.source_timezone, rev.folio_identity_strategy,
		       rev.normalization_version, COALESCE(rev.source_fingerprint,''), rev.config,
		       (rev.id = i.current_revision_id) AS published
		  FROM iam_v2.pms_interface_revisions rev
		  JOIN iam_v2.pms_interfaces i ON i.id = rev.pms_interface_id
		 WHERE rev.tenant_id=$1 AND rev.site_id=$2 AND rev.pms_interface_id=$3::uuid
		 ORDER BY rev.revision_no DESC`, s.tenantID, s.siteID, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []pmsRevisionRow{}
	for rows.Next() {
		var e pmsRevisionRow
		var cfg []byte
		var published *bool
		if err := rows.Scan(&e.ID, &e.RevisionNo, &e.SourceTimezone, &e.FolioIdentityStrategy,
			&e.NormalizationVersion, &e.SourceFingerprint, &cfg, &published); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		e.Config = redactRevisionConfig(cfg)
		e.Published = published != nil && *published
		out = append(out, e)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

type publishRevisionReq struct {
	RevisionID string `json:"revision_id"`
	// ExpectedRevisionID is the Revision the operator BELIEVED was published when they opened the form. It is
	// the optimistic check: if somebody else published in the meantime, this refuses rather than silently
	// reverting their change. An empty string means "I believe nothing is published yet".
	ExpectedRevisionID string `json:"expected_revision_id"`
	ReasonCode         string `json:"reason_code"`
	Password           string `json:"password"`
}

func (s *server) publishPMSInterfaceRevision(w http.ResponseWriter, r *http.Request) {
	var in publishRevisionReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(in.RevisionID) == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "revision_id is required")
		return
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason code is required: publishing changes what every subsequent guest is resolved against")
		return
	}
	// Step-up, for the same reason the reason code is required.
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")

	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the interface before reading its published Revision, so the compare-and-set below cannot be
	// interleaved by a concurrent publication of a different Revision.
	var current string
	err = tx.QueryRow(ctx, `SELECT COALESCE(current_revision_id::text,'')
		FROM iam_v2.pms_interfaces WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid FOR UPDATE`,
		s.tenantID, s.siteID, id).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if current != strings.TrimSpace(in.ExpectedRevisionID) {
		// 409 with the current value, so the UI can show what actually changed rather than asking the
		// operator to guess why their publication was refused.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":               "revision_conflict",
			"message":             "another operator published a different revision while this form was open",
			"current_revision_id": current,
		})
		return
	}

	// The Revision must belong to THIS interface. Without this check an operator could publish another
	// interface's Revision, and every subsequent resolution would be decided against a configuration written
	// for a different PMS.
	var revNo int
	err = tx.QueryRow(ctx, `SELECT revision_no FROM iam_v2.pms_interface_revisions
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid AND id=$4::uuid`,
		s.tenantID, s.siteID, id, strings.TrimSpace(in.RevisionID)).Scan(&revNo)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusBadRequest, "revision_invalid", "that revision does not belong to this interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	if _, err := tx.Exec(ctx, `UPDATE iam_v2.pms_interfaces SET current_revision_id=$4::uuid
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`,
		s.tenantID, s.siteID, id, strings.TrimSpace(in.RevisionID)); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the publication was refused")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the publication was refused")
		return
	}

	s.audit(r, "pms_interface.revision_published", "pms_interface", id, map[string]any{
		"revision_id":          strings.TrimSpace(in.RevisionID),
		"revision_no":          revNo,
		"previous_revision_id": current,
		"reason_code":          in.ReasonCode,
	})
	writeJSON(w, http.StatusOK, map[string]any{"current_revision_id": strings.TrimSpace(in.RevisionID), "revision_no": revNo})
}

// ---------- secret rotation (write-only) ----------

type rotateSecretReq struct {
	Secret     string `json:"secret"`
	ReasonCode string `json:"reason_code"`
	Password   string `json:"password"`
}

// rotatePMSInterfaceSecret stores a NEW credential generation and supersedes the previous one.
//
// There is deliberately no corresponding GET. The credential exists to be presented to the PMS, not to be
// read by people; an endpoint that returned it would turn every operator session, browser cache and support
// screenshot into a place the property's PMS credential lives.
//
// The response says only which generation number now applies. That is enough to answer "did my rotation take
// effect?" without ever echoing what was typed.
func (s *server) rotatePMSInterfaceSecret(w http.ResponseWriter, r *http.Request) {
	var in rotateSecretReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if strings.TrimSpace(in.Secret) == "" {
		jsonErr(w, http.StatusBadRequest, "secret_required", "a credential is required")
		return
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason code is required to rotate a credential")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "an operator identity is required")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")

	// The keyring is the appliance's, not the request's. If it is not configured, rotation is refused rather
	// than stored in the clear — a credential written unencrypted "for now" is a credential written
	// unencrypted forever, and nothing downstream would ever notice.
	keyID, keyring := s.pmsSecretKeyring()
	if keyID == "" || keyring == nil {
		jsonErr(w, http.StatusServiceUnavailable, "encryption_unavailable",
			"credential encryption is not configured on this appliance")
		return
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the interface so two concurrent rotations cannot both compute the same next generation number and
	// leave two rows claiming to be current.
	var exists bool
	err = tx.QueryRow(ctx, `SELECT true FROM iam_v2.pms_interfaces
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid FOR UPDATE`,
		s.tenantID, s.siteID, id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	var generation int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(max(generation_no),0)+1
		FROM iam_v2.pms_interface_secret_generations
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid`,
		s.tenantID, s.siteID, id).Scan(&generation); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	// The row id is chosen HERE because it is part of the AEAD's additional authenticated data: the ciphertext
	// is bound to the exact (tenant, site, interface, generation) it belongs to, so a row copied to another
	// interface fails authentication instead of decrypting into the wrong PMS.
	var generationID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&generationID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	sealed, err := pmsd.SealSecret(keyring, keyID, pmsd.Interface{
		TenantID: s.tenantID, SiteID: s.siteID, ID: id,
	}, generationID, []byte(in.Secret))
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "encryption_unavailable", "the credential rotation was refused")
		return
	}

	// Supersede the previous generation and append the new one in ONE transaction. The order matters only in
	// that both must be true together: a moment with two live generations is a moment where which credential
	// the connector uses is decided by an ORDER BY.
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.pms_interface_secret_generations
		SET superseded_at = now()
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid AND superseded_at IS NULL`,
		s.tenantID, s.siteID, id); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the credential rotation was refused")
		return
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.pms_interface_secret_generations
		(id, tenant_id, site_id, pms_interface_id, generation_no, ciphertext, nonce, encryption_key_id, cipher_version)
		VALUES ($1::uuid,$2,$3,$4::uuid,$5,$6,$7,$8::uuid,$9)`,
		generationID, s.tenantID, s.siteID, id, generation,
		sealed.Ciphertext, sealed.Nonce, sealed.EncryptionKey, sealed.CipherVersion); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the credential rotation was refused")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the credential rotation was refused")
		return
	}

	// The audit records THAT a rotation happened and by whom. It does not record the credential, and it does
	// not record a hash of it either: a hash of a short operator-chosen string is not much of a secret.
	s.audit(r, "pms_interface.secret_rotated", "pms_interface", id, map[string]any{
		"generation_no": generation, "reason_code": strings.TrimSpace(in.ReasonCode),
	})
	writeJSON(w, http.StatusOK, map[string]any{"generation_no": generation})
}

// ---------- health ----------

// interfaceHealth is the DERIVED operational picture. Every field is computed from the runtime facts at read
// time; none of it is stored. The four dimensions are separate because they fail separately and an operator
// acts differently on each: transport is "is it connected", continuity is "did we miss anything", sync is
// "are we mid-resync", occupancy is "is what we hold about the property still current".
// roomAuth* are the CLOSED set of reasons Room authentication cannot currently be served by an interface.
//
// Bounded codes rather than sentences: this is a machine field, the wording belongs to whichever surface
// renders it, and a free-text reason built from runtime state is how PMS detail leaks into places nobody
// audited. Each maps to one clause of the server's own feed-health rule.
const (
	roomAuthNotActive      = "INTERFACE_NOT_ACTIVE"
	roomAuthNoRevision     = "NO_PUBLISHED_REVISION"
	roomAuthContinuityGap  = "CONTINUITY_GAP"
	roomAuthContinuityNone = "CONTINUITY_NOT_ESTABLISHED"
	roomAuthNotInSync      = "NOT_IN_SYNC"
	roomAuthFeedSilent     = "FEED_SILENT"
	roomAuthRevisionUnpin  = "REVISION_NOT_PINNED"

	// THE MIRROR HAS NEVER BEEN FILLED. Distinct from NOT_IN_SYNC, which describes a mirror that exists and is
	// momentarily behind. This one means no complete sync has ever finished, so there is nothing local to fall
	// back to while the transport is down — the tables would answer "no such Stay" for a hotel full of guests.
	roomAuthNeverSynced = "MIRROR_NEVER_SYNCHRONIZED"

	// A COMPLETE SYNC IS PARTWAY THROUGH. Some of the truth has been applied and the rest has not, which is the
	// one state where local Stay data is genuinely inconsistent rather than merely stale. Brief and
	// self-clearing; the operator needs to know it is temporary, not investigate it.
	roomAuthResyncInFlight = "RESYNC_IN_FLIGHT"

	// THE GUEST LIST IS ARRIVING, not absent. Distinct from NOT_IN_SYNC on purpose: the feed is healthy and a
	// generation is published, but the applier has not finished writing it into iam_v2.stays yet, so Room
	// sign-in is briefly and correctly closed. Seconds, not an outage, and the operator should be told which.
	roomAuthMaterializing = "MATERIALIZATION_BEHIND"
)

type interfaceHealth struct {
	InterfaceID string `json:"pms_interface_id"`

	// ROOM-AUTH FEED READINESS, decided here rather than by whoever is displaying it.
	//
	// Phase 3 mints a PMS Auth Context on EITHER a live feed or a trusted local mirror, so this answer must
	// not be derived from the socket alone. A client cannot evaluate the live branch's bound anyway: it lives
	// in the Revision's config, and Hotel Admin was hardcoding the 300-second DEFAULT as though it were the
	// rule. An interface configured with a different timeout would then be described to an operator using a
	// number that interface does not use.
	//
	// TRANSPORT DOWN IS NO LONGER A REASON ON ITS OWN. Telling staff "guests cannot sign in" because the
	// socket dropped was false whenever the mirror was intact, and false in the worst direction: it invites
	// the front desk to start handing out workarounds for a problem the guests do not have. When the
	// transport is down the readiness answer comes from the mirror instead — has a complete sync ever
	// finished, and is one in flight right now — which is exactly the offline branch of
	// iam_v2.p3_feed_authorizes. The transport's own state is still reported in full alongside this, in
	// Transport/DisconnectedSince/TransportError, so an operator sees both facts and they no longer contradict
	// each other.
	//
	// So the same runtime and the same active Revision that Phase 3 reads answer the question here, and the
	// client renders the answer. What is deliberately EXCLUDED is the Stay-specific half of
	// iam_v2.p3_feed_authorizes — occupancy evidence, its age and its revision pin — because that is a
	// property of one guest's Stay, not of the feed, and this endpoint is about the interface.
	RoomAuthReady  bool   `json:"room_auth_ready"`
	RoomAuthReason string `json:"room_auth_reason,omitempty"`

	Transport         string     `json:"transport_status"`
	LastConnectedAt   *time.Time `json:"last_connected_at,omitempty"`
	LastHeartbeatAt   *time.Time `json:"last_heartbeat_at,omitempty"`
	DisconnectedSince *time.Time `json:"disconnected_since,omitempty"`
	TransportError    string     `json:"transport_error_code,omitempty"`

	Continuity              string     `json:"continuity_status"`
	LastValidEventAt        *time.Time `json:"last_valid_event_at,omitempty"`
	DiscontinuityDetectedAt *time.Time `json:"discontinuity_detected_at,omitempty"`

	Sync                string     `json:"sync_status"`
	ResyncRequestedAt   *time.Time `json:"resync_requested_at,omitempty"`
	ResyncStartedAt     *time.Time `json:"resync_started_at,omitempty"`
	LastCompleteSyncAt  *time.Time `json:"last_complete_sync_at,omitempty"`
	LastSyncFailureCode string     `json:"last_sync_failure_code,omitempty"`

	// Occupancy is what the interface currently believes about the property, and is the dimension an
	// operator can sanity-check against reality by walking the corridor.
	InHouseStays  int        `json:"in_house_stays"`
	LastStayEvent *time.Time `json:"last_stay_event_at,omitempty"`

	// SYNCHRONIZATION, as an operator experiences it. Stage is a closed vocabulary the database enforces, and
	// the counts are real: RecordsReceived is what has actually been staged under the open generation, and
	// there is deliberately no total, percentage or remaining count beside it, because FIAS provides no total
	// before DE and every such number would be invented.
	MaterializationReady bool       `json:"materialization_ready"`
	SyncStage            string     `json:"sync_stage,omitempty"`
	SyncStageAt          *time.Time `json:"sync_stage_at,omitempty"`
	RecordsReceived      int64      `json:"sync_records_received"`
	RecordsSkipped       int64      `json:"sync_records_skipped"`
	SyncFailureCode      string     `json:"sync_failure_code,omitempty"`
	LastSyncInHouse      *int64     `json:"last_sync_in_house_count,omitempty"`
	ResyncRequestedBy    string     `json:"resync_command_reason,omitempty"`
	ResyncCommandAt      *time.Time `json:"resync_command_requested_at,omitempty"`

	// Backlog is the ingestion queue: events admitted but not yet applied, and how old the oldest is. A
	// backlog that is merely large is a busy morning; a backlog whose OLDEST item is hours old is a stuck
	// processor, and the two need different responses.
	PendingEvents   int        `json:"pending_events"`
	ReviewEvents    int        `json:"review_events"`
	OldestPendingAt *time.Time `json:"oldest_pending_at,omitempty"`
}

func (s *server) getPMSInterfaceHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	h, err := s.interfaceHealthRow(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"health": h})
}

func (s *server) interfaceHealthRow(ctx context.Context, id string) (interfaceHealth, error) {
	var h interfaceHealth
	h.InterfaceID = id
	err := s.db.QueryRow(ctx, `
		SELECT COALESCE(rt.transport_status,'UNKNOWN'), rt.last_connected_at, rt.last_heartbeat_at,
		       rt.disconnected_since, COALESCE(rt.transport_error_code,''),
		       COALESCE(rt.continuity_status,'UNKNOWN'), rt.last_valid_event_at, rt.discontinuity_detected_at,
		       COALESCE(rt.sync_status,'UNKNOWN'), rt.resync_requested_at, rt.resync_started_at,
		       rt.last_complete_sync_at, COALESCE(rt.last_sync_failure_code,''),
		       (SELECT count(*) FROM iam_v2.stays st
		         WHERE st.pms_interface_id=$3::uuid AND st.status='IN_HOUSE')::int,
		       (SELECT max(ev.received_at) FROM iam_v2.stay_events ev WHERE ev.pms_interface_id=$3::uuid),
		       (SELECT count(*) FROM iam_v2.stay_events ev
		         WHERE ev.pms_interface_id=$3::uuid AND ev.processing_status='PENDING')::int,
		       (SELECT count(*) FROM iam_v2.stay_events ev
		         WHERE ev.pms_interface_id=$3::uuid AND ev.processing_status='MANUAL_REVIEW')::int,
		       (SELECT min(ev.received_at) FROM iam_v2.stay_events ev
		         WHERE ev.pms_interface_id=$3::uuid AND ev.processing_status='PENDING'),
		       COALESCE(rt.sync_stage,''), rt.sync_stage_at,
		       COALESCE(rt.sync_records_received,0), COALESCE(rt.sync_records_skipped,0),
		       COALESCE(rt.sync_failure_code,''),
			       -- Materialization readiness, using the SAME claimable-pending term the auth predicate
			       -- uses. A display that disagreed with the authorisation rule would be worse than none.
			       NOT EXISTS (SELECT 1 FROM iam_v2.stay_events se
			                    WHERE se.tenant_id=rt.tenant_id AND se.site_id=rt.site_id
			                      AND se.pms_interface_id=rt.pms_interface_id
			                      AND se.processing_status='PENDING'
			                      AND (se.admission_kind='LIVE'
			                           OR se.resync_generation <= rt.published_resync_generation)),
		       COALESCE(rt.resync_command_reason,''), rt.resync_command_requested_at,
		       -- ROOM-AUTH FEED READINESS. The clauses and their order mirror the feed half of
		       -- iam_v2.p3_feed_authorizes; the Stay-specific half is deliberately absent. The heartbeat bound
		       -- comes from THIS interface's published Revision via the same iam_v2.p3_cfg_secs the
		       -- authentication path uses, so a Revision configured with a non-default heartbeat_timeout_ms is
		       -- judged by its own number instead of by the default.
		       CASE
		         WHEN i.lifecycle_state <> 'ACTIVE'            THEN '`+roomAuthNotActive+`'
		         WHEN pr.id IS NULL                            THEN '`+roomAuthNoRevision+`'
		         -- Shared by both branches, so these are asked first and a transport-down interface is
		         -- judged by them exactly as a connected one is.
		         WHEN rt.continuity_status = 'GAP_DETECTED'    THEN '`+roomAuthContinuityGap+`'
		         WHEN COALESCE(rt.continuity_status,'UNKNOWN') <> 'CONTINUOUS'
		                                                       THEN '`+roomAuthContinuityNone+`'
		         WHEN rt.pinned_revision_id IS DISTINCT FROM pr.id
		                                                       THEN '`+roomAuthRevisionUnpin+`'
		         -- TRANSPORT DOWN: the mirror answers instead. Sign-in survives if a complete sync has
		         -- actually happened and none is in flight; those two are the whole of the offline branch.
		         WHEN COALESCE(rt.transport_status,'UNKNOWN') <> 'CONNECTED' THEN
		           CASE
		             WHEN rt.last_complete_sync_at IS NULL     THEN '`+roomAuthNeverSynced+`'
		             WHEN rt.resync_started_at IS NOT NULL     THEN '`+roomAuthResyncInFlight+`'
		             ELSE ''
		           END
		         -- TRANSPORT UP: the live-feed branch, unchanged.
		         WHEN COALESCE(rt.sync_status,'UNKNOWN') <> 'IN_SYNC'
		                                                       THEN '`+roomAuthNotInSync+`'
		         WHEN COALESCE(rt.last_heartbeat_at, rt.last_connected_at) IS NULL
		           OR COALESCE(rt.last_heartbeat_at, rt.last_connected_at)
		              <= now() - make_interval(secs => iam_v2.p3_cfg_secs(pr.config,'heartbeat_timeout_ms',300))
		                                                       THEN '`+roomAuthFeedSilent+`'
		         ELSE ''
		       END
		  FROM iam_v2.pms_interfaces i
		  LEFT JOIN iam_v2.pms_interface_runtime rt
		         ON rt.tenant_id=i.tenant_id AND rt.site_id=i.site_id AND rt.pms_interface_id=i.id
		  LEFT JOIN iam_v2.pms_interface_revisions pr
		         ON pr.tenant_id=i.tenant_id AND pr.site_id=i.site_id
		        AND pr.pms_interface_id=i.id AND pr.id=i.current_revision_id
		 WHERE i.tenant_id=$1 AND i.site_id=$2 AND i.id=$3::uuid`,
		s.tenantID, s.siteID, id).Scan(
		&h.Transport, &h.LastConnectedAt, &h.LastHeartbeatAt, &h.DisconnectedSince, &h.TransportError,
		&h.Continuity, &h.LastValidEventAt, &h.DiscontinuityDetectedAt,
		&h.Sync, &h.ResyncRequestedAt, &h.ResyncStartedAt, &h.LastCompleteSyncAt, &h.LastSyncFailureCode,
		&h.InHouseStays, &h.LastStayEvent, &h.PendingEvents, &h.ReviewEvents, &h.OldestPendingAt,
		&h.SyncStage, &h.SyncStageAt, &h.RecordsReceived, &h.RecordsSkipped, &h.SyncFailureCode,
		&h.MaterializationReady, &h.ResyncRequestedBy, &h.ResyncCommandAt,
		&h.RoomAuthReason)
	h.RoomAuthReady = err == nil && h.RoomAuthReason == ""
	return h, err
}

// ---------- guest-network routing ----------

type guestNetworkRoute struct {
	GuestNetworkID   string `json:"guest_network_id"`
	GuestNetworkName string `json:"guest_network_name,omitempty"`
	InterfaceID      string `json:"pms_interface_id"`
	InterfaceLabel   string `json:"pms_interface_label,omitempty"`
	IsDefault        bool   `json:"is_default"`
	RoutingMode      string `json:"routing_mode"`
}

func (s *server) pmsRoutingRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listPMSRouting)
	// THE WRITE PATH. This surface used to be read-only, on the reasoning that which PMS a VLAN resolves
	// against is a network-topology decision and therefore belongs where the networks are configured. The
	// reasoning is sound; the problem was that the guest-network API has no PMS field, so the conclusion in
	// practice was that the mapping could not be set ANYWHERE. It existed only as a row inserted by
	// integration-test fixtures, which meant a real deployment reached "PMS connected, Stays ingested,
	// nothing authenticates" with no product action available to fix it.
	//
	// So the write lives here, next to the read that already explains the mapping, and it is deliberately
	// explicit rather than casual: one guest network at a time, named in the path, with the interface
	// validated against this site and required to be publishable. Wiring it into the guest-network revision
	// pipeline instead would couple a network apply/confirm/rollback cycle to PMS configuration — a much
	// larger change that makes a routing typo a network outage.
	r.Put("/{guest_network_id}", s.setPMSRoute)
	r.Delete("/{guest_network_id}", s.clearPMSRoute)
	return r
}

type setPMSRouteReq struct {
	InterfaceID string `json:"pms_interface_id"`
	// RoutingMode is MAPPED (this network resolves against exactly this interface) or
	// ALL_ACTIVE_INTERFACES (it fans out across every active interface at the site). Defaults to MAPPED:
	// a single named interface is the answer that cannot surprise anyone.
	RoutingMode string `json:"routing_mode"`
}

// setPMSRoute maps one guest network to one PMS Interface.
//
// It is an upsert on (guest_network_id, pms_interface_id) and it also REPLACES any other interface mapped to
// the same guest network. Accumulating mappings would be the more literal reading of an upsert, but a guest
// network resolving against two properties' PMS at once is not a configuration anyone wants and not one the
// resolver can make sense of — so setting a route means setting it, not adding to it.
func (s *server) setPMSRoute(w http.ResponseWriter, r *http.Request) {
	gnID := strings.TrimSpace(chi.URLParam(r, "guest_network_id"))
	var in setPMSRouteReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	ifaceID := strings.TrimSpace(in.InterfaceID)
	if ifaceID == "" {
		jsonErr(w, http.StatusBadRequest, "validation", "pms_interface_id is required")
		return
	}
	mode := strings.TrimSpace(in.RoutingMode)
	if mode == "" {
		mode = "MAPPED"
	}
	if mode != "MAPPED" && mode != "ALL_ACTIVE_INTERFACES" {
		jsonErr(w, http.StatusBadRequest, "validation", "routing_mode must be MAPPED or ALL_ACTIVE_INTERFACES")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Both sides are checked against THIS site rather than trusted from the path/body. The foreign keys would
	// catch a cross-site id too, but as a 23503 that reaches the operator as "internal" — and "which property
	// does this VLAN belong to" deserves an answer, not a 500.
	var gnName string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(name,'') FROM public.guest_networks
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, gnID, s.tenantID, s.siteID).Scan(&gnName); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "guest network not found at this site")
		return
	}
	var ifaceLabel, lifecycle string
	var published bool
	// PUBLICATION IS `current_revision_id`, not a per-revision timestamp. An earlier draft of this query
	// tested `pms_interface_revisions.published_at`, a column that does not exist — so the query errored,
	// the error was indistinguishable from "no such row", and a correctly published interface was rejected
	// as "not found at this site". A missing column reported as a missing interface sends the operator to
	// look at the wrong thing entirely.
	if err := tx.QueryRow(ctx, `
	    SELECT COALESCE(i.display_label,''), COALESCE(i.lifecycle_state,''),
	           i.current_revision_id IS NOT NULL
	      FROM iam_v2.pms_interfaces i
	     WHERE i.id=$1 AND i.tenant_id=$2 AND i.site_id=$3`,
		ifaceID, s.tenantID, s.siteID).Scan(&ifaceLabel, &lifecycle, &published); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "PMS interface not found at this site")
		return
	}
	// An interface with no published revision has no endpoint, so a network mapped to it resolves against
	// nothing. Refusing here turns a silent dead end into a message naming the missing step.
	if !published {
		jsonErr(w, http.StatusConflict, "validation",
			"PMS interface has no published revision — publish one before routing a guest network to it")
		return
	}

	// Replace, don't accumulate: drop any other interface currently mapped to this network.
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map
	     WHERE tenant_id=$1 AND site_id=$2 AND guest_network_id=$3 AND pms_interface_id <> $4`,
		s.tenantID, s.siteID, gnID, ifaceID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route replace failed")
		return
	}
	// is_default=true: with exactly one mapping per network it IS the default, and the partial unique index
	// gnpm_one_default permits one per network. The DELETE above runs first in the same transaction, so the
	// index cannot see two.
	if _, err := tx.Exec(ctx, `
	    INSERT INTO iam_v2.guest_network_pms_map
	      (tenant_id, site_id, guest_network_id, pms_interface_id, is_default, routing_mode)
	    VALUES ($1,$2,$3,$4,true,$5)
	    ON CONFLICT (guest_network_id, pms_interface_id)
	    DO UPDATE SET is_default=true, routing_mode=EXCLUDED.routing_mode`,
		s.tenantID, s.siteID, gnID, ifaceID, mode); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route write failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "pms_routing.set", "guest_network", gnID,
		map[string]any{"pms_interface_id": ifaceID, "routing_mode": mode, "guest_network_name": gnName})
	writeJSON(w, http.StatusOK, guestNetworkRoute{
		GuestNetworkID: gnID, GuestNetworkName: gnName,
		InterfaceID: ifaceID, InterfaceLabel: ifaceLabel, IsDefault: true, RoutingMode: mode,
	})
}

// clearPMSRoute unmaps a guest network. The network then resolves against nothing, which is a legitimate
// state (a staff VLAN has no business consulting the PMS) and is reported as such by listPMSRouting's
// unmapped_guest_networks — so removal is not a hole, it is an answer.
func (s *server) clearPMSRoute(w http.ResponseWriter, r *http.Request) {
	gnID := strings.TrimSpace(chi.URLParam(r, "guest_network_id"))
	ctx, cancel := dbCtx(r)
	defer cancel()
	tag, err := s.db.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map
	     WHERE tenant_id=$1 AND site_id=$2 AND guest_network_id=$3`, s.tenantID, s.siteID, gnID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route delete failed")
		return
	}
	s.audit(r, "pms_routing.cleared", "guest_network", gnID,
		map[string]any{"removed": tag.RowsAffected()})
	writeJSON(w, http.StatusOK, map[string]any{"guest_network_id": gnID, "removed": tag.RowsAffected()})
}

// listPMSRouting answers "which guest networks resolve against which PMS interface?".
//
// It is a read surface on purpose. The mapping decides which property's PMS a device on a given VLAN is
// checked against, so getting it wrong resolves a guest against a neighbouring property's occupancy — and
// changing it is a network-topology decision, made where the networks themselves are configured, not a PMS
// integration one.
func (s *server) listPMSRouting(w http.ResponseWriter, r *http.Request) {
	routes, err := s.routesForInterface(r, "")
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	// A guest network with no mapping at all is the interesting case: devices on it resolve against nothing,
	// and the absence is invisible in a list that only shows what IS mapped.
	unmapped, err := s.unmappedGuestNetworks(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"routes": routes, "unmapped_guest_networks": unmapped})
}

func (s *server) routesForInterface(r *http.Request, ifaceID string) ([]guestNetworkRoute, error) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var arg any
	if ifaceID != "" {
		arg = ifaceID
	}
	rows, err := s.db.Query(ctx, `
		SELECT m.guest_network_id::text, COALESCE(gn.name,''), m.pms_interface_id::text,
		       COALESCE(i.display_label,''), m.is_default, COALESCE(m.routing_mode,'')
		  FROM iam_v2.guest_network_pms_map m
		  LEFT JOIN public.guest_networks gn ON gn.id = m.guest_network_id
		  LEFT JOIN iam_v2.pms_interfaces i ON i.id = m.pms_interface_id
		 WHERE m.tenant_id=$1 AND m.site_id=$2
		   AND ($3::uuid IS NULL OR m.pms_interface_id = $3::uuid)
		 ORDER BY gn.name, m.guest_network_id`, s.tenantID, s.siteID, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []guestNetworkRoute{}
	for rows.Next() {
		var e guestNetworkRoute
		if err := rows.Scan(&e.GuestNetworkID, &e.GuestNetworkName, &e.InterfaceID,
			&e.InterfaceLabel, &e.IsDefault, &e.RoutingMode); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *server) unmappedGuestNetworks(r *http.Request) ([]map[string]string, error) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT gn.id::text, COALESCE(gn.name,'')
		  FROM public.guest_networks gn
		 WHERE gn.site_id=$2
		   AND NOT EXISTS (SELECT 1 FROM iam_v2.guest_network_pms_map m
		                    WHERE m.tenant_id=$1 AND m.site_id=$2 AND m.guest_network_id = gn.id)
		 ORDER BY gn.name, gn.id`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"guest_network_id": id, "guest_network_name": name})
	}
	return out, rows.Err()
}

// ---------- source conflicts ----------

type sourceConflictRow struct {
	ID              string `json:"id"`
	InterfaceA      string `json:"interface_a"`
	InterfaceALabel string `json:"interface_a_label,omitempty"`
	InterfaceB      string `json:"interface_b"`
	InterfaceBLabel string `json:"interface_b_label,omitempty"`
	Severity        string `json:"severity,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
}

func (s *server) pmsSourceConflictsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSourceConflicts)
	return r
}

// listSourceConflicts shows where two interfaces claim authority over the same source.
//
// This is the condition that makes a resolution AMBIGUOUS for reasons the guest cannot fix and the front desk
// cannot explain: two interfaces both say room 412 is occupied, by different people. The operator's job is to
// decide which one owns it; this surface exists so the question is visible before a guest asks it.
func (s *server) listSourceConflicts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT c.id::text, c.interface_a::text, COALESCE(ia.display_label,''),
		       c.interface_b::text, COALESCE(ib.display_label,''),
		       COALESCE(c.severity,''), COALESCE(c.resolution,'')
		  FROM iam_v2.pms_source_conflicts c
		  LEFT JOIN iam_v2.pms_interfaces ia ON ia.id = c.interface_a
		  LEFT JOIN iam_v2.pms_interfaces ib ON ib.id = c.interface_b
		 WHERE c.tenant_id=$1 AND c.site_id=$2
		 ORDER BY c.severity DESC NULLS LAST, c.id`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	out := []sourceConflictRow{}
	for rows.Next() {
		var e sourceConflictRow
		if err := rows.Scan(&e.ID, &e.InterfaceA, &e.InterfaceALabel, &e.InterfaceB, &e.InterfaceBLabel,
			&e.Severity, &e.Resolution); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		out = append(out, e)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": out})
}

// pmsSecretKeyring returns the appliance's credential-encryption key id and keyring, loaded from the same
// environment pmsd loads them from — one source, so a rotation performed here is decryptable by the connector
// that has to use it. An unset key means rotation is refused, never that it silently stores plaintext.
func (s *server) pmsSecretKeyring() (string, pmsd.Keyring) {
	keyID := strings.TrimSpace(os.Getenv("PMSD_SECRET_KEY_ID"))
	if keyID == "" {
		return "", nil
	}
	kb, err := hex.DecodeString(os.Getenv("PMSD_SECRET_KEY_HEX"))
	if err != nil || len(kb) != 32 {
		return "", nil
	}
	return keyID, pmsd.MapKeyring{keyID: kb}
}
