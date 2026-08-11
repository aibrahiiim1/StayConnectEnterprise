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
	r.Post("/{id}/publish", s.publishPMSInterfaceRevision)
	r.Post("/{id}/secret", s.rotatePMSInterfaceSecret)
	return r
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
type interfaceHealth struct {
	InterfaceID string `json:"pms_interface_id"`

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
		         WHERE ev.pms_interface_id=$3::uuid AND ev.processing_status='PENDING')
		  FROM iam_v2.pms_interfaces i
		  LEFT JOIN iam_v2.pms_interface_runtime rt
		         ON rt.tenant_id=i.tenant_id AND rt.site_id=i.site_id AND rt.pms_interface_id=i.id
		 WHERE i.tenant_id=$1 AND i.site_id=$2 AND i.id=$3::uuid`,
		s.tenantID, s.siteID, id).Scan(
		&h.Transport, &h.LastConnectedAt, &h.LastHeartbeatAt, &h.DisconnectedSince, &h.TransportError,
		&h.Continuity, &h.LastValidEventAt, &h.DiscontinuityDetectedAt,
		&h.Sync, &h.ResyncRequestedAt, &h.ResyncStartedAt, &h.LastCompleteSyncAt, &h.LastSyncFailureCode,
		&h.InHouseStays, &h.LastStayEvent, &h.PendingEvents, &h.ReviewEvents, &h.OldestPendingAt)
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
	return r
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
