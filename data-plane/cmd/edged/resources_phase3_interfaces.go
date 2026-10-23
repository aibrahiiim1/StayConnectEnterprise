package main

// The PMS interface as a configured thing: connector, published Revision, guest-network routing, secret rotation, health.
// Revisions are immutable (stays, resolutions and auth contexts pin the exact Revision they were decided under);
// publishing is separate and needs step-up, expected version and reason, and refuses to overwrite a concurrent
// publication; secrets are write-only and never read back; health is derived from runtime facts at read time, never stored as a verdict.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

type pmsInterfaceRow struct {
	ID                string `json:"id"`
	ConnectorKind     string `json:"connector_kind"`
	DisplayLabel      string `json:"display_label"`
	LifecycleState    string `json:"lifecycle_state"`
	CurrentRevisionID string `json:"current_revision_id,omitempty"`
	CurrentRevisionNo *int   `json:"current_revision_no,omitempty"`
	RevisionCount     int    `json:"revision_count"`
	// An interface without a published Revision resolves nothing.
	Published        bool       `json:"published"`
	SecretGeneration *int       `json:"secret_generation,omitempty"`
	SecretRotatedAt  *time.Time `json:"secret_rotated_at,omitempty"`

	// What the interface is pointed at, projected read-only from its published Revision.
	Endpoint       *string `json:"endpoint,omitempty"`
	SourceTimezone *string `json:"source_timezone,omitempty"`
	BaseCurrency   *string `json:"financial_base_currency,omitempty"`

	ProviderLabel string `json:"provider_label"`
	Transport     string `json:"transport"`
	Verification  string `json:"verification"`
}

func (s *server) pmsInterfacesRoutes() http.Handler {
	r := chi.NewRouter()
	r.Use(rejectMalformedRouteID)
	r.Get("/", s.listPMSInterfaces)
	r.Get("/{id}", s.getPMSInterface)
	r.Get("/{id}/revisions", s.listPMSInterfaceRevisions)
	r.Get("/{id}/health", s.getPMSInterfaceHealth)
	// Recovery bounds belong to the connection they govern, so they are addressed as part of it.
	r.Get("/{id}/connection-settings", s.getInterfaceConnectionSettings)
	r.Put("/{id}/connection-settings", s.updateInterfaceConnectionSettings)
	r.Post("/", s.createPMSInterface)
	r.Post("/{id}/revisions", s.authorPMSInterfaceRevision)
	r.Post("/{id}/publish", s.publishPMSInterfaceRevision)
	r.Post("/{id}/full-resync", s.requestFullResync)
	r.Post("/{id}/secret", s.rotatePMSInterfaceSecret)
	r.Post("/{id}/test-connection", s.testPMSConnection)
	// The lifecycle transition: an interface is created AUTH_DISABLED and publishing does not change that, because
	// publishing decides what the connector would dial and activating decides whether it dials at all. pmsd selects
	// `WHERE lifecycle_state='ACTIVE'`.
	r.Post("/{id}/lifecycle", s.setPMSInterfaceLifecycle)
	return r
}

func rejectMalformedRouteID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rctx := chi.RouteContext(r.Context())
		rest := ""
		if rctx != nil {
			rest = strings.Trim(rctx.RoutePath, "/")
		}
		seg := rest
		if i := strings.Index(rest, "/"); i >= 0 {
			seg = rest[:i]
		}
		if seg != "" && !uuidRe.MatchString(seg) {
			jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type setLifecycleReq struct {
	State      string `json:"state"`
	ReasonCode string `json:"reason_code"`
	Password   string `json:"password"`
}

// The allowed transitions. DECOMMISSIONED is terminal and not reachable here: Stays and events keep referencing
// it and authoring refuses it, so retiring needs its own deliberate operation.
var pmsLifecycleTransitions = map[string]map[string]bool{
	"ACTIVE":        {"AUTH_DISABLED": true, "DRAINING": true},
	"AUTH_DISABLED": {"ACTIVE": true},
	"DRAINING":      {"AUTH_DISABLED": true, "ACTIVE": true},
}

// Step-up and a reason code are required in both directions: activating opens a live connection to the PMS and
// disabling stops resolving every guest on every network mapped to it.
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
		s.reauthRefused(w, r, "password confirmation required")
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
	// Activation requires a published Revision: otherwise pmsd finds nothing to dial and the operator cannot connect the error to the missing step.
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
         WHERE g.pms_interface_id = i.id),
       (SELECT r.config->>'endpoint' FROM iam_v2.pms_interface_revisions r WHERE r.id = i.current_revision_id),
       (SELECT r.source_timezone FROM iam_v2.pms_interface_revisions r WHERE r.id = i.current_revision_id),
       (SELECT r.financial_base_currency::text FROM iam_v2.pms_interface_revisions r WHERE r.id = i.current_revision_id)`

func scanPMSInterface(row interface{ Scan(...any) error }, e *pmsInterfaceRow) error {
	if err := row.Scan(&e.ID, &e.ConnectorKind, &e.DisplayLabel, &e.LifecycleState,
		&e.CurrentRevisionID, &e.CurrentRevisionNo, &e.RevisionCount,
		&e.SecretGeneration, &e.SecretRotatedAt,
		&e.Endpoint, &e.SourceTimezone, &e.BaseCurrency); err != nil {
		return err
	}
	e.Published = e.CurrentRevisionID != ""
	e.ProviderLabel, e.Transport, e.Verification = providerMeta(e.ConnectorKind)
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
		// Scoped to this site, so another site's interface is indistinguishable from a missing one and does not reveal which sites a neighbour runs.
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	routes, err := s.routesForInterface(r, e.ID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"interface": e, "guest_networks": routes})
}

type pmsRevisionRow struct {
	ID                   string `json:"id"`
	RevisionNo           int    `json:"revision_no"`
	SourceTimezone       string `json:"source_timezone"`
	PostingTargetModel   string `json:"posting_target_model"`
	NormalizationVersion int    `json:"normalization_version"`
	SourceFingerprint    string `json:"source_fingerprint,omitempty"`
	// A revision column, not a config key; the console carries it through an edit because a draft without it drops it.
	FinancialBaseCurrency *string `json:"financial_base_currency,omitempty"`
	FinancialCurrencyExp  *int    `json:"financial_base_currency_exponent,omitempty"`
	// Config is REDACTED before it leaves the process (redactRevisionConfig); operator-authored JSON can acquire anything over time.
	Config json.RawMessage `json:"config"`
	// Provenance comes from the audit log only; pms_interface_revisions stores no timestamp, author or reason. Where
	// the log has no entry these stay empty and the UI says so, so an unrecorded origin reads as unknown.
	AuthoredAt  *time.Time `json:"authored_at,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	ActorID     string     `json:"actor_id,omitempty"`
	ActorLabel  string     `json:"actor_label,omitempty"`
	ReasonCode  string     `json:"reason_code,omitempty"`
	// Marks the one Revision currently resolved against, derived from current_revision_id, never the highest number (a rollback publishes an older one).
	Published bool `json:"published"`
}

// Config keys whose value must never be rendered: operator-authored JSON can contain a credential pasted into the wrong field.
var secretishKeys = []string{"password", "secret", "token", "key", "credential", "apikey", "api_key", "auth"}

func redactRevisionConfig(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		// Unparseable config is not rendered at all: passing it through would defeat the redaction below.
		return json.RawMessage(`{"unreadable":true}`)
	}
	redactMap(m)
	out, err := json.Marshal(m)
	if err != nil {
		return json.RawMessage(`{"unreadable":true}`)
	}
	return out
}

// Settings whose name contains a secret-ish word but whose value is an operational number (max_auth_cache_age_seconds
// contains "auth"); redacting them made the console drop them from edited drafts.
var notSecretConfigKeys = map[string]bool{"max_auth_cache_age_seconds": true}

func redactMap(m map[string]any) {
	for k, v := range m {
		if notSecretConfigKeys[k] {
			continue
		}
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

// Read from the audit log, the only place provenance is written. Named so a test can execute this exact text;
// the `public.` qualifiers are load-bearing for that test, which retargets the statement at a schema with production column types.
const pmsRevisionProvenanceSQL = `
	SELECT COALESCE(a.payload->>'revision_id', p.payload->>'revision_id') AS rev_id,
	       min(a.ts) AS authored_at, min(p.ts) AS published_at,
	       COALESCE(max(p.actor_id::text), max(a.actor_id::text), '') AS actor_id,
	       COALESCE(max(op.display_name), max(op.email), '') AS actor_label,
	       COALESCE(max(p.payload->>'reason_code'), '') AS reason_code
	  FROM public.audit_log a
	  FULL JOIN public.audit_log p
	         ON p.action='pms_interface.revision_published'
	        AND p.payload->>'revision_id' = a.payload->>'revision_id'
	  -- COMPARED AS TEXT, DELIBERATELY. audit_log.actor_id is text and operators.id is uuid, so the direct
	  -- comparison is not a style question: PostgreSQL has no uuid = text operator and rejects the whole
	  -- statement. Casting the other way (actor_id::uuid) would parse every actor string in the audit log
	  -- and throw on the first that is not a uuid -- and actor_id is text precisely because it is not
	  -- guaranteed to be one. Widening uuid to text always succeeds.
	  LEFT JOIN public.operators op ON op.id::text = COALESCE(p.actor_id, a.actor_id)
	 WHERE a.action='pms_interface_revision.authored' OR p.action='pms_interface.revision_published'
	 GROUP BY 1`

func (s *server) listPMSInterfaceRevisions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	// The configuration comes from the configuration tables only; joining the audit log would take the configuration
	// down where audit tables are absent. Provenance is fetched separately below.
	rows, err := s.db.Query(ctx, `
		SELECT rev.id::text, rev.revision_no, rev.source_timezone, rev.posting_target_model,
		       rev.normalization_version, COALESCE(rev.source_fingerprint,''), rev.config,
		       (rev.id = i.current_revision_id) AS published,
		       rev.financial_base_currency::text, rev.financial_base_currency_exponent::int
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
		if err := rows.Scan(&e.ID, &e.RevisionNo, &e.SourceTimezone, &e.PostingTargetModel,
			&e.NormalizationVersion, &e.SourceFingerprint, &cfg, &published,
			&e.FinancialBaseCurrency, &e.FinancialCurrencyExp); err != nil {
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
	// Provenance is layered on and allowed to be missing: a failure leaves empty provenance rather than costing the
	// operator the configuration. Because the error is tolerated it is the query's one hazard (a uuid/text join once
	// failed silently), so the query lives in a named constant that
	// TestIntegration_API_PmsRevisionProvenanceSQLRunsAgainstProductionColumnTypes executes against production column
	// types. The Integration_API_ prefix is load-bearing: the gate selects with `-run Integration`. The failure is
	// logged and the response carries provenance_status, so "says nothing" and "could not be read" differ.
	provenanceStatus := "AVAILABLE"
	if len(out) > 0 {
		prov, err := s.db.Query(ctx, pmsRevisionProvenanceSQL)
		switch {
		case err != nil:
			provenanceStatus = "UNAVAILABLE"
			slog.Error("pms revisions: provenance unreadable; configuration served without it",
				"err", err, "interface", id)
		default:
			byID := map[string]pmsRevisionRow{}
			var scanErr error
			for prov.Next() {
				var revID string
				var e pmsRevisionRow
				if err := prov.Scan(&revID, &e.AuthoredAt, &e.PublishedAt,
					&e.ActorID, &e.ActorLabel, &e.ReasonCode); err != nil {
					scanErr = err
					continue
				}
				if revID != "" {
					byID[revID] = e
				}
			}
			// rows.Err() catches a connection dropped mid-iteration, which would otherwise serve a partial map as the whole truth.
			if e := prov.Err(); e != nil {
				scanErr = e
			}
			prov.Close()
			if scanErr != nil {
				provenanceStatus = "UNAVAILABLE"
				slog.Error("pms revisions: provenance read failed mid-iteration; serving configuration only",
					"err", scanErr, "interface", id)
				break
			}
			for i := range out {
				if m, ok := byID[out[i].ID]; ok {
					out[i].AuthoredAt, out[i].PublishedAt = m.AuthoredAt, m.PublishedAt
					out[i].ActorID, out[i].ActorLabel, out[i].ReasonCode = m.ActorID, m.ActorLabel, m.ReasonCode
				}
			}
		}
	}

	// A query that succeeded but found nothing for any version is reported as available-and-empty, not as a failure.
	writeJSON(w, http.StatusOK, map[string]any{
		"revisions":         out,
		"provenance_status": provenanceStatus,
	})
}

type publishRevisionReq struct {
	RevisionID string `json:"revision_id"`
	// The Revision the operator believed was published: the optimistic check, so a concurrent publication is refused
	// rather than reverted. Empty means "nothing published yet".
	ExpectedRevisionID string `json:"expected_revision_id"`
	ReasonCode         string `json:"reason_code"`
	Password           string `json:"password"`
	// Must be true to publish a revision that would take room charge off this connection (the published revision
	// carries an onboarded posting target and the new one does not); otherwise room_charge_reset_unconfirmed.
	ConfirmRoomChargeReset bool `json:"confirm_room_charge_reset"`
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
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
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

	// Lock the interface so the compare-and-set below cannot be interleaved by a concurrent publication.
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
		// 409 with the current value, so the UI can show what changed.
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":               "revision_conflict",
			"message":             "another operator published a different revision while this form was open",
			"current_revision_id": current,
		})
		return
	}

	// The Revision must belong to THIS interface; otherwise guests would be resolved against a configuration written for a different PMS.
	var revNo int
	var newTarget string
	err = tx.QueryRow(ctx, `SELECT revision_no, COALESCE(posting_target_model,'') FROM iam_v2.pms_interface_revisions
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid AND id=$4::uuid`,
		s.tenantID, s.siteID, id, strings.TrimSpace(in.RevisionID)).Scan(&revNo, &newTarget)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusBadRequest, "revision_invalid", "that revision does not belong to this interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	// Room charge would be switched off: it is onboarded on a revision and a console-authored revision starts at
	// UNSET, so publishing over an onboarded one disables it until re-onboarded. Allowed only after the operator confirms.
	var curTarget string
	if current != "" {
		_ = tx.QueryRow(ctx, `SELECT COALESCE(posting_target_model,'') FROM iam_v2.pms_interface_revisions
			WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`, s.tenantID, s.siteID, current).Scan(&curTarget)
	}
	roomChargeReset := curTarget != "" && curTarget != postingTargetUnset &&
		(newTarget == "" || newTarget == postingTargetUnset)
	if roomChargeReset && !in.ConfirmRoomChargeReset {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "room_charge_reset_unconfirmed",
			"message": "Publishing this version turns room charge off for this connection: its posting target was " +
				"set by financial onboarding on the version in use, and this version has none. Guests will no " +
				"longer be offered charge to room until the connection is onboarded again. Confirm to publish anyway.",
			"current_posting_target_model": curTarget,
		})
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
		"room_charge_reset":    roomChargeReset,
	})
	writeJSON(w, http.StatusOK, map[string]any{"current_revision_id": strings.TrimSpace(in.RevisionID), "revision_no": revNo})
}

type rotateSecretReq struct {
	// A JSON string for protel-fias; for a REST connector, an object of the provider's credential fields (or a string containing one). Sealed, never echoed.
	Secret     json.RawMessage `json:"secret"`
	ReasonCode string          `json:"reason_code"`
	Password   string          `json:"password"`
}

// There is deliberately no GET: an endpoint returning the credential would make every session, cache and
// screenshot a place the PMS credential lives. The response says only which generation now applies.
func (s *server) rotatePMSInterfaceSecret(w http.ResponseWriter, r *http.Request) {
	var in rotateSecretReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	// A string is stored as before; an object is accepted only for a connector whose registry entry declares credential fields.
	rawSecret := []byte(strings.TrimSpace(string(in.Secret)))
	var secretStr string
	secretIsObject := false
	switch {
	case len(rawSecret) == 0 || string(rawSecret) == "null":
	case rawSecret[0] == '"':
		if err := json.Unmarshal(rawSecret, &secretStr); err != nil {
			jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
			return
		}
	case rawSecret[0] == '{':
		secretIsObject = true
	default:
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !secretIsObject && strings.TrimSpace(secretStr) == "" {
		jsonErr(w, http.StatusBadRequest, "secret_required", "a credential is required")
		return
	}
	if strings.TrimSpace(in.ReasonCode) == "" {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason code is required to rotate a credential")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "password confirmation required")
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

	// The keyring is the appliance's, not the request's; if unset, rotation is refused rather than storing plaintext.
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

	// Lock the interface so two concurrent rotations cannot compute the same next generation.
	var connectorKind string
	err = tx.QueryRow(ctx, `SELECT connector_kind FROM iam_v2.pms_interfaces
		WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid FOR UPDATE`,
		s.tenantID, s.siteID, id).Scan(&connectorKind)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	// The plaintext that is sealed: the string as submitted, or for a REST connector the canonical JSON of its credential fields; values never appear in a response or error.
	plaintext := []byte(secretStr)
	if prov, ok := pmsprovider.Get(connectorKind); ok && prov.IsREST() {
		raw := json.RawMessage(rawSecret)
		norm, verr := pmsprovider.NormalizeSecret(prov, raw)
		if verr != nil {
			jsonErr(w, http.StatusBadRequest, verr.Code, verr.Message)
			return
		}
		plaintext = norm
	} else if secretIsObject {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
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

	// The row id is chosen here because it is part of the AEAD's additional authenticated data: the ciphertext is
	// bound to its (tenant, site, interface, generation), so a copied row fails authentication.
	var generationID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&generationID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	sealed, err := pmsd.SealSecret(keyring, keyID, pmsd.Interface{
		TenantID: s.tenantID, SiteID: s.siteID, ID: id,
	}, generationID, plaintext)
	if err != nil {
		jsonErr(w, http.StatusServiceUnavailable, "encryption_unavailable", "the credential rotation was refused")
		return
	}

	// Supersede the previous generation and append the new one in ONE transaction: two live generations would leave the choice to an ORDER BY.
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

	// The audit records that a rotation happened and by whom, never the credential or a hash of it (a hash of a short chosen string is weak).
	s.audit(r, "pms_interface.secret_rotated", "pms_interface", id, map[string]any{
		"generation_no": generation, "reason_code": strings.TrimSpace(in.ReasonCode),
	})
	writeJSON(w, http.StatusOK, map[string]any{"generation_no": generation})
}

// The CLOSED set of reasons Room authentication cannot currently be served by an interface. Bounded codes, not
// sentences: free text built from runtime state is how PMS detail leaks; each maps to one clause of the server's feed-health rule.
const (
	roomAuthNotActive      = "INTERFACE_NOT_ACTIVE"
	roomAuthNoRevision     = "NO_PUBLISHED_REVISION"
	roomAuthContinuityGap  = "CONTINUITY_GAP"
	roomAuthContinuityNone = "CONTINUITY_NOT_ESTABLISHED"
	roomAuthNotInSync      = "NOT_IN_SYNC"
	roomAuthFeedSilent     = "FEED_SILENT"
	roomAuthRevisionUnpin  = "REVISION_NOT_PINNED"

	// The mirror has never been filled: no complete sync ever finished, so there is nothing local to fall back to while the transport is down.
	roomAuthNeverSynced = "MIRROR_NEVER_SYNCHRONIZED"

	// Kept as a known code for older consoles; no longer a readiness reason, since a resync builds an unpublished
	// generation while the published roster keeps answering (p3_feed_authorizes).
	roomAuthResyncInFlight = "RESYNC_IN_FLIGHT"

	// The guest list is arriving: a generation is published but not yet written into iam_v2.stays, so Room sign-in is briefly closed.
	roomAuthMaterializing = "MATERIALIZATION_BEHIND"
)

type interfaceHealth struct {
	InterfaceID string `json:"pms_interface_id"`

	// Room-auth feed readiness is decided here, not by the display: a PMS Auth Context is minted on either a live
	// feed or a trusted local mirror, and the live bound lives in the Revision config. Transport down is not a
	// reason on its own: with an intact mirror guests can still sign in, so readiness then comes from the mirror
	// (has a complete sync ever finished, is one in flight), the offline branch of iam_v2.p3_feed_authorizes. The
	// Stay-specific half (occupancy evidence, age, revision pin) is deliberately excluded.
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

	InHouseStays  int        `json:"in_house_stays"`
	LastStayEvent *time.Time `json:"last_stay_event_at,omitempty"`

	// Synchronization: Stage is a closed vocabulary the database enforces. There is deliberately no total,
	// percentage or remaining count, because FIAS provides no total before DE.
	MaterializationReady bool       `json:"materialization_ready"`
	SyncStage            string     `json:"sync_stage,omitempty"`
	SyncStageAt          *time.Time `json:"sync_stage_at,omitempty"`
	RecordsReceived      int64      `json:"sync_records_received"`
	RecordsSkipped       int64      `json:"sync_records_skipped"`
	SyncFailureCode      string     `json:"sync_failure_code,omitempty"`
	LastSyncInHouse      *int64     `json:"last_sync_in_house_count,omitempty"`
	ResyncRequestedBy    string     `json:"resync_command_reason,omitempty"`
	ResyncCommandAt      *time.Time `json:"resync_command_requested_at,omitempty"`

	// The ingestion queue. A large backlog is a busy morning; an hours-old oldest item is a stuck processor.
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
		       -- The RAW recorded count. It is refined below to "still outstanding" where the
		       -- disposition ledger exists; this query must keep working on a Phase-3-era schema that
		       -- has no such ledger, so the newer meaning is layered on rather than assumed here.
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
		         -- actually happened -- that is the whole of the offline branch of p3_feed_authorizes (0060).
		         -- A resync in flight does NOT close it: the published roster is untouched until a replacement
		         -- completes, so the last good roster keeps authorising guests, and saying "down" here sent
		         -- staff after an outage guests were not having (audit H-12).
		         WHEN COALESCE(rt.transport_status,'UNKNOWN') <> 'CONNECTED' THEN
		           CASE
		             WHEN rt.last_complete_sync_at IS NULL     THEN '`+roomAuthNeverSynced+`'
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

	// Refines "review" from recorded to outstanding where the disposition ledger exists: this field feeds the PMS
	// connection row and Property-management card. A failure keeps the raw count rather than reporting zero.
	if err == nil {
		var outstanding int
		if e2 := s.db.QueryRow(ctx,
			`SELECT count(*) FROM iam_v2.pms_unanswered_review_events WHERE pms_interface_id=$1::uuid`,
			id).Scan(&outstanding); e2 == nil {
			h.ReviewEvents = outstanding
		}
	}
	return h, err
}

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
	// The write path lives here, next to the read that explains the mapping: one guest network at a time, named in
	// the path, with the interface validated against this site and required to be publishable. Wiring it into the
	// network revision pipeline would make a routing typo a network outage.
	r.Put("/{guest_network_id}", s.setPMSRoute)
	r.Delete("/{guest_network_id}", s.clearPMSRoute)
	return r
}

type setPMSRouteReq struct {
	InterfaceID string `json:"pms_interface_id"`
	// MAPPED (resolves against exactly this interface) or ALL_ACTIVE_INTERFACES (not implemented). Defaults to MAPPED.
	RoutingMode string `json:"routing_mode"`
}

// Maps one guest network to one PMS Interface. An upsert that also REPLACES any other interface mapped to the
// network: a network resolving against two properties' PMS at once is not a valid configuration.
func (s *server) setPMSRoute(w http.ResponseWriter, r *http.Request) {
	gnID := strings.TrimSpace(chi.URLParam(r, "guest_network_id"))
	if !uuidRe.MatchString(gnID) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
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
	if !uuidRe.MatchString(ifaceID) {
		jsonErr(w, http.StatusNotFound, "not_found", "PMS interface not found at this site")
		return
	}
	mode := strings.TrimSpace(in.RoutingMode)
	if mode == "" {
		mode = "MAPPED"
	}
	// ALL_ACTIVE_INTERFACES is not offered: no resolver reads routing_mode (internal/pmsresolve uses mapped()), so
	// it would behave as MAPPED under a name promising fan-out. Existing rows keep resolving against their mapped interface.
	if mode != "MAPPED" {
		jsonErr(w, http.StatusBadRequest, "validation",
			"routing_mode must be MAPPED. Routing a client network to every active PMS is not implemented: "+
				"the resolver only ever consults the interface a network is mapped to.")
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

	// Both sides are checked against THIS site rather than trusted: a cross-site id would otherwise surface as an "internal" 23503.
	var gnName string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(name,'') FROM public.guest_networks
	     WHERE id=$1 AND tenant_id=$2 AND site_id=$3`, gnID, s.tenantID, s.siteID).Scan(&gnName); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
	var ifaceLabel, lifecycle string
	var published bool
	// Publication is `current_revision_id`, not a per-revision timestamp (pms_interface_revisions.published_at does not exist).
	if err := tx.QueryRow(ctx, `
	    SELECT COALESCE(i.display_label,''), COALESCE(i.lifecycle_state,''),
	           i.current_revision_id IS NOT NULL
	      FROM iam_v2.pms_interfaces i
	     WHERE i.id=$1 AND i.tenant_id=$2 AND i.site_id=$3`,
		ifaceID, s.tenantID, s.siteID).Scan(&ifaceLabel, &lifecycle, &published); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "PMS interface not found at this site")
		return
	}
	// An interface with no published revision has no endpoint; refuse with a message naming the missing step.
	if !published {
		jsonErr(w, http.StatusConflict, "validation",
			"PMS interface has no published revision — publish one before routing a client network to it")
		return
	}

	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.guest_network_pms_map
	     WHERE tenant_id=$1 AND site_id=$2 AND guest_network_id=$3 AND pms_interface_id <> $4`,
		s.tenantID, s.siteID, gnID, ifaceID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "route replace failed")
		return
	}
	// is_default=true: with exactly one mapping per network it is the default; the DELETE above runs first so the gnpm_one_default index sees one.
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

// Unmaps a guest network; resolving against nothing is legitimate (a staff VLAN) and listPMSRouting reports it as unmapped_guest_networks.
func (s *server) clearPMSRoute(w http.ResponseWriter, r *http.Request) {
	gnID := strings.TrimSpace(chi.URLParam(r, "guest_network_id"))
	if !uuidRe.MatchString(gnID) {
		jsonErr(w, http.StatusNotFound, "not_found", "client network not found at this site")
		return
	}
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

// The mapping decides which property's PMS a device on a VLAN is checked against; getting it wrong resolves a
// guest against a neighbouring property's occupancy. Writes (setPMSRoute / clearPMSRoute) are confined to
// site_admin by the `pms-routing` permWrite entry in rolePerms.
func (s *server) listPMSRouting(w http.ResponseWriter, r *http.Request) {
	routes, err := s.routesForInterface(r, "")
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	// A guest network with no mapping resolves against nothing, and the absence is invisible in a list of mapped ones.
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

type sourceConflictRow struct {
	ID              string `json:"id"`
	InterfaceA      string `json:"interface_a"`
	InterfaceALabel string `json:"interface_a_label,omitempty"`
	InterfaceB      string `json:"interface_b"`
	InterfaceBLabel string `json:"interface_b_label,omitempty"`
	Severity        string `json:"severity,omitempty"`
	Resolution      string `json:"resolution,omitempty"`
	// Derived now from the connections' published configuration (equal source fingerprints), not read from a
	// stored record: nothing ever wrote pms_source_conflicts.
	Detected bool `json:"detected,omitempty"`
}

func (s *server) pmsSourceConflictsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSourceConflicts)
	return r
}

// Where two interfaces claim authority over the same source, which makes a resolution AMBIGUOUS for reasons the guest cannot fix.
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
	rows.Close()
	recorded := map[string]bool{}
	for _, c := range out {
		recorded[c.InterfaceA+"/"+c.InterfaceB] = true
	}
	drows, err := s.db.Query(ctx, `
		SELECT a.id::text, COALESCE(a.display_label,''), b.id::text, COALESCE(b.display_label,'')
		  FROM iam_v2.pms_interfaces a
		  JOIN iam_v2.pms_interface_revisions ra ON ra.id = a.current_revision_id
		  JOIN iam_v2.pms_interfaces b
		    ON b.tenant_id = a.tenant_id AND b.site_id = a.site_id AND a.id < b.id
		  JOIN iam_v2.pms_interface_revisions rb ON rb.id = b.current_revision_id
		 WHERE a.tenant_id=$1 AND a.site_id=$2
		   AND a.lifecycle_state <> 'DECOMMISSIONED' AND b.lifecycle_state <> 'DECOMMISSIONED'
		   AND ra.source_fingerprint IS NOT NULL AND ra.source_fingerprint = rb.source_fingerprint
		 ORDER BY a.id, b.id LIMIT 100`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer drows.Close()
	for drows.Next() {
		var e sourceConflictRow
		if err := drows.Scan(&e.InterfaceA, &e.InterfaceALabel, &e.InterfaceB, &e.InterfaceBLabel); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
			return
		}
		if recorded[e.InterfaceA+"/"+e.InterfaceB] {
			continue
		}
		e.ID, e.Severity, e.Detected = "detected:"+e.InterfaceA+":"+e.InterfaceB, "DUPLICATE_SOURCE", true
		out = append(out, e)
	}
	if drows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": out})
}

// Loaded from the same environment pmsd uses, so a rotation here is decryptable by the connector. An unset key means rotation is refused, never plaintext.
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

type interfaceConnectionSettings struct {
	BackoffMinMs         int   `json:"backoff_min_ms"`
	BackoffMaxMs         int   `json:"backoff_max_ms"`
	StableResetSeconds   int   `json:"stable_reset_seconds"`
	LinkDownAlertSeconds int   `json:"link_down_alert_seconds"`
	BlockedAfterRefusals int   `json:"blocked_after_refusals"`
	ConfigVersion        int64 `json:"config_version"`
	// Distinguishes "nobody configured this" from "configured to the same numbers as the defaults".
	IsDefault bool `json:"is_default"`
}

// Refuses an id that is not this site's before it reaches the settings functions (the definer function is the
// authority); this gives a clean 404 and stops one site probing another's interface ids by error differences.
func (s *server) interfaceBelongsToSite(ctx context.Context, id string) bool {
	var ok bool
	if err := s.db.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM iam_v2.pms_interfaces
		                 WHERE id = $3::uuid AND tenant_id = $1 AND site_id = $2)`,
		s.tenantID, s.siteID, id).Scan(&ok); err != nil {
		return false
	}
	return ok
}

func (s *server) getInterfaceConnectionSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	if !s.interfaceBelongsToSite(ctx, id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	var c interfaceConnectionSettings
	if err := s.db.QueryRow(ctx, `SELECT backoff_min_ms, backoff_max_ms, stable_reset_seconds,
		       link_down_alert_seconds, blocked_after_refusals, config_version, is_default
		  FROM iam_v2.pms_connection_settings_get($1::uuid,$2::uuid,$3::uuid)`,
		s.tenantID, s.siteID, id).
		Scan(&c.BackoffMinMs, &c.BackoffMaxMs, &c.StableResetSeconds,
			&c.LinkDownAlertSeconds, &c.BlockedAfterRefusals, &c.ConfigVersion, &c.IsDefault); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// Every field is optional so a partial change touches nothing else; the definer function records who, why and the previous values.
func (s *server) updateInterfaceConnectionSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	var in struct {
		BackoffMinMs         *int   `json:"backoff_min_ms"`
		BackoffMaxMs         *int   `json:"backoff_max_ms"`
		StableResetSeconds   *int   `json:"stable_reset_seconds"`
		LinkDownAlertSeconds *int   `json:"link_down_alert_seconds"`
		BlockedAfterRefusals *int   `json:"blocked_after_refusals"`
		Reason               string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if !s.interfaceBelongsToSite(ctx, id) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	sess := sessFrom(r.Context())
	operator := "unknown"
	if sess != nil && strings.TrimSpace(sess.Email) != "" {
		operator = sess.Email
	} else if sess != nil {
		operator = sess.OperatorID
	}
	var version int64
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.pms_connection_settings_set($1::uuid,$2::uuid,$3::uuid,$4,$5,$6,$7,$8,$9,$10)`,
		s.tenantID, s.siteID, id, operator, in.Reason,
		in.BackoffMinMs, in.BackoffMaxMs, in.StableResetSeconds,
		in.LinkDownAlertSeconds, in.BlockedAfterRefusals).Scan(&version); err != nil {
		jsonErr(w, http.StatusBadRequest, "settings_rejected", err.Error())
		return
	}
	// The audit target is the interface, so two connections' changes stay distinguishable.
	s.audit(r, "pms_connection_settings.update", "pms_interface", id, map[string]any{
		"config_version": version,
	})
	writeJSON(w, http.StatusOK, map[string]any{"config_version": version})
}
