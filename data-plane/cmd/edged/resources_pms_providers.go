package main

// PMS PROVIDERS: THE CATALOGUE, PROVIDER CONFIGURATION, AND A ONE-OFF CONNECTION TEST.
//
// Everything here reads from internal/pmsprovider, the single registry pmsd's supported kinds also derive
// from. The Hotel Admin renders the connection form from GET /pms-providers, so the form can only offer a
// connector the runtime can run, and only the fields that connector's revision actually accepts.
//
// The connection test exists for the REST connectors only. It authenticates and performs ONE bounded read
// (page size 1) with a short timeout, writes no stay data, and never returns the credential. It deliberately
// does NOT dial a FIAS socket: Protel accepts one link, and a second one opened to "test" would compete with
// the live connector for it.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsd"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
	"github.com/stayconnect/enterprise/data-plane/internal/pmsrest"
)

// listPMSProviders serves GET /edge/v1/pms-providers.
func (s *server) listPMSProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"providers": pmsprovider.Providers()})
}

// providerMeta decorates an interface row with the registry's view of its connector kind. A kind the
// registry does not know (a legacy row) reports empty values rather than a guess.
func providerMeta(kind string) (label, transport, verification string) {
	if p, ok := pmsprovider.Get(kind); ok {
		return p.Label, string(p.Transport), string(p.Verification)
	}
	return "", "", ""
}

type testConnectionReq struct {
	RevisionID string `json:"revision_id"`
	Password   string `json:"password"`
}

type testConnectionResult struct {
	OK        bool           `json:"ok"`
	Stage     string         `json:"stage"`
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	LatencyMS int64          `json:"latency_ms"`
	Details   map[string]any `json:"details"`
}

// testConnectionMessages are the operator wording for each bounded code. Plain English; no provider text.
var testConnectionMessages = map[string]string{
	"OK":                        "Signed in to the PMS and read the in-house reservation list successfully.",
	"SOCKET_LINK_HEALTH_ONLY":   "This connection is a live socket link; its health is shown by the link status, not a one-off test.",
	"NO_REVISION":               "Save a configuration for this connection before testing it.",
	"REVISION_NOT_FOUND":        "That configuration does not belong to this connection.",
	"CONFIG_INVALID":            "The saved configuration for this connection cannot be used. Save it again.",
	"SECRET_MISSING":            "Store the connection's credential before testing it.",
	"SECRET_UNREADABLE":         "The stored credential could not be decrypted on this appliance. Store it again.",
	"ENCRYPTION_UNAVAILABLE":    "Credential encryption is not configured on this appliance, so the credential cannot be read for a test.",
	"PROVIDER_AUTH_FAILED":      "The PMS refused the credential. Check the stored credential and the configured property.",
	"PROVIDER_RATE_LIMITED":     "The PMS is limiting requests right now. Wait a minute and test again.",
	"PROVIDER_TIMEOUT":          "The PMS did not answer in time.",
	"PROVIDER_UNAVAILABLE":      "The PMS could not be reached or reported a server error.",
	"PROVIDER_RESPONSE_INVALID": "The PMS answered with something this connector does not understand. Check the configured address.",
	"PROVIDER_REQUEST_REJECTED": "The PMS rejected the request. Check the configured property or hotel identifier.",
}

func testResult(ok bool, stage, code string, started time.Time, details map[string]any) testConnectionResult {
	if details == nil {
		details = map[string]any{}
	}
	return testConnectionResult{OK: ok, Stage: stage, Code: code, Message: testConnectionMessages[code],
		LatencyMS: time.Since(started).Milliseconds(), Details: details}
}

// testPMSConnection serves POST /edge/v1/pms-interfaces/{id}/test-connection (write + step-up).
func (s *server) testPMSConnection(w http.ResponseWriter, r *http.Request) {
	var in testConnectionReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if !s.reauth(r, in.Password) {
		jsonErr(w, http.StatusUnauthorized, "reauth_required", "password confirmation required")
		return
	}
	id := chi.URLParam(r, "id")
	ctx, cancel := dbCtx(r)
	defer cancel()

	var kind, currentRev string
	err := s.db.QueryRow(ctx, `SELECT connector_kind, COALESCE(current_revision_id::text,'')
		FROM iam_v2.pms_interfaces WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`,
		s.tenantID, s.siteID, id).Scan(&kind, &currentRev)
	if errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such PMS interface")
		return
	}
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	started := time.Now()
	res, revID := s.runConnectionTest(ctx, id, kind, currentRev, strings.TrimSpace(in.RevisionID), started)
	s.audit(r, "pms_interface.connection_tested", "pms_interface", id, map[string]any{
		"revision_id": revID, "connector_kind": kind, "ok": res.OK, "stage": res.Stage, "code": res.Code,
	})
	writeJSON(w, http.StatusOK, res)
}

func (s *server) runConnectionTest(ctx context.Context, id, kind, currentRev, wantRev string, started time.Time) (testConnectionResult, string) {
	prov, ok := pmsprovider.Get(kind)
	if !ok || !prov.IsREST() {
		// A socket link is never dialled for a test: the PMS accepts one link and the connector holds it.
		return testResult(false, "CONFIG", "SOCKET_LINK_HEALTH_ONLY", started, nil), wantRev
	}
	revID := wantRev
	if revID == "" {
		revID = currentRev
	}
	if revID == "" {
		return testResult(false, "CONFIG", "NO_REVISION", started, nil), ""
	}
	var tz string
	var providerCfg string
	var readMs *int64
	err := s.db.QueryRow(ctx, `SELECT source_timezone, COALESCE((config->'provider')::text,''),
		       (config->>'read_timeout_ms')::bigint
		  FROM iam_v2.pms_interface_revisions
		 WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid AND id=$4::uuid`,
		s.tenantID, s.siteID, id, revID).Scan(&tz, &providerCfg, &readMs)
	if err != nil {
		return testResult(false, "CONFIG", "REVISION_NOT_FOUND", started, nil), revID
	}
	cfg, perr := pmsprovider.ParseStoredProvider(prov, []byte(providerCfg))
	loc, lerr := time.LoadLocation(tz)
	if perr != nil || lerr != nil {
		return testResult(false, "CONFIG", "CONFIG_INVALID", started, nil), revID
	}

	keyID, keyring := s.pmsSecretKeyring()
	if keyID == "" || keyring == nil {
		return testResult(false, "CONFIG", "ENCRYPTION_UNAVAILABLE", started, nil), revID
	}
	var sgID string
	if err := s.db.QueryRow(ctx, `SELECT id::text FROM iam_v2.pms_interface_secret_generations
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3::uuid AND superseded_at IS NULL
		ORDER BY generation_no DESC LIMIT 1`, s.tenantID, s.siteID, id).Scan(&sgID); err != nil {
		return testResult(false, "CONFIG", "SECRET_MISSING", started, nil), revID
	}
	secret, err := pmsd.NewPgSecretDecryptor(s.db, keyring)(ctx,
		pmsd.Interface{TenantID: s.tenantID, SiteID: s.siteID, ID: id}, pmsd.Revision{},
		pmsd.SecretGeneration{ID: sgID})
	if err != nil {
		return testResult(false, "CONFIG", "SECRET_UNREADABLE", started, nil), revID
	}
	defer secret.Zero()

	// Short and bounded: one attempt per request, at most 15 s per request, 20 s overall.
	perReq := 15 * time.Second
	if readMs != nil && *readMs > 0 && time.Duration(*readMs)*time.Millisecond < perReq {
		perReq = time.Duration(*readMs) * time.Millisecond
	}
	tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := pmsrest.New(prov.Kind, cfg, secret.Bytes(), pmsrest.Options{
		HTTP: &http.Client{Timeout: perReq}, Location: loc, Retries: 1,
	})
	if err != nil {
		return testResult(false, "CONFIG", "SECRET_UNREADABLE", started, nil), revID
	}
	probe, err := client.Probe(tctx)
	if err != nil {
		stage, code := "READ", "PROVIDER_UNAVAILABLE"
		switch pmsrest.KindOf(err) {
		case pmsrest.KindAuth:
			stage, code = "AUTH", "PROVIDER_AUTH_FAILED"
		case pmsrest.KindRateLimited:
			code = "PROVIDER_RATE_LIMITED"
		case pmsrest.KindTimeout:
			code = "PROVIDER_TIMEOUT"
		case pmsrest.KindInvalidResponse:
			code = "PROVIDER_RESPONSE_INVALID"
		case pmsrest.KindRequest:
			code = "PROVIDER_REQUEST_REJECTED"
		case pmsrest.KindConfig:
			stage, code = "CONFIG", "CONFIG_INVALID"
		default:
			if errors.Is(err, context.DeadlineExceeded) {
				code = "PROVIDER_TIMEOUT"
			}
		}
		if pmsrest.IsTokenOp(err) && stage == "READ" {
			stage = "AUTH" // the failure happened while signing in, before any reservation read
		}
		return testResult(false, stage, code, started, nil), revID
	}
	return testResult(true, "READ", "OK", started, map[string]any{
		"sample_reservations": probe.SampleReservations, "property": probe.Property,
	}), revID
}
