package main

// AUTHORING A PMS INTERFACE AND ITS CONFIGURATION.
//
// WHAT WAS MISSING
// ----------------
// The PMS Interfaces screen could list interfaces, show their revisions, publish an EXISTING revision and
// rotate a credential -- and nothing else. There was no way to create an interface, and no way to author a
// revision's configuration, so the ENDPOINT the connector dials (host:port), its timeouts, its heartbeat
// bounds, its credential mode and its timezone were unreachable from the product. Every interface on this
// appliance exists because a test or a seed script wrote it directly into the database.
//
// An operator handed this build could not connect a PMS at all. That is not a missing nicety: it is the
// entire configuration surface of the feature.
//
// WHAT THIS ADDS, AND WHAT IT REFUSES TO INVENT
// ---------------------------------------------
// Exactly the fields the IMPLEMENTED connector reads, taken from two places and nowhere else:
//
//   * pmsd.Revision.Validate()      -- endpoint, source timezone, normalization version, credential mode,
//                                      and the read-only capability, which must be TRUE;
//   * pgRepo.LoadInterface()        -- the config keys it projects: endpoint, dial/read/write timeouts,
//                                      heartbeat interval/timeout, feed freshness, complete-sync bound,
//                                      resync_supported, auth.credential_mode, auth.read_only.
//
// No protocol behaviour is invented here, no PMS topology is assumed, and no managed state is fabricated. A
// revision authored here is a DRAFT: it is written, and it is not published. Publishing stays exactly where
// it was -- a separate, password-confirmed action with its own reason code -- because publishing is what
// changes "what every guest is resolved against from this moment on", and that boundary was accepted as-is.

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"net/http"

	"github.com/go-chi/chi/v5"
)

// folioIdentityStrategies mirrors the schema CHECK constraint exactly. Listing them here rather than
// accepting free text means an operator gets a named refusal instead of a constraint violation from three
// layers down.
var folioIdentityStrategies = map[string]bool{
	"UNSET": true, "GLOBALLY_UNIQUE": true, "UNIQUE_PER_STAY": true, "REUSED_SEQUENTIAL": true,
}

// credentialModes is pmsd's supportedCredentialModes. A revision must state one explicitly: pmsd fails
// closed to AUTH_KEY when the value is empty, so an unstated mode silently becomes "a secret is required".
var credentialModes = map[string]bool{"NONE": true, "AUTH_KEY": true}

type createPMSInterfaceReq struct {
	ConnectorKind string `json:"connector_kind"`
	DisplayLabel  string `json:"display_label"`
}

func (s *server) createPMSInterface(w http.ResponseWriter, r *http.Request) {
	var in createPMSInterfaceReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	kind := strings.TrimSpace(in.ConnectorKind)
	if !pmsAllowedKinds[kind] {
		jsonErr(w, http.StatusBadRequest, "validation",
			"connector_kind must be one of the implemented connectors: "+allowedKindList())
		return
	}
	label := strings.TrimSpace(in.DisplayLabel)
	if label == "" || len(label) > 120 {
		jsonErr(w, http.StatusBadRequest, "validation", "display_label is required (1..120 characters)")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var id string
	// AUTH_DISABLED, not ACTIVE. A new interface has no published revision and therefore no endpoint to
	// reach, so calling it ACTIVE would advertise a connection that cannot exist. It becomes usable when a
	// revision is published, which is a separate deliberate act.
	if err := s.db.QueryRow(ctx, `
	    INSERT INTO iam_v2.pms_interfaces (tenant_id, site_id, connector_kind, display_label, lifecycle_state)
	    VALUES ($1,$2,$3,$4,'AUTH_DISABLED') RETURNING id::text`,
		s.tenantID, s.siteID, kind, label).Scan(&id); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "create failed")
		return
	}
	s.audit(r, "pms_interface.created", "pms_interface", id,
		map[string]any{"connector_kind": kind, "display_label": label})
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "connector_kind": kind, "display_label": label, "lifecycle_state": "AUTH_DISABLED",
	})
}

func allowedKindList() string {
	out := make([]string, 0, len(pmsAllowedKinds))
	for k := range pmsAllowedKinds {
		out = append(out, k)
	}
	// deterministic message, so a refusal reads the same way twice
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return strings.Join(out, ", ")
}

// authorRevisionReq is the complete configuration of a connector revision. Every field is one the running
// code reads; there are no placeholders and nothing here is decorative.
type authorRevisionReq struct {
	// Endpoint is the address the connector dials: host:port. This is the field whose absence from the UI
	// made the whole screen unusable.
	Endpoint              string `json:"endpoint"`
	SourceTimezone        string `json:"source_timezone"`
	FolioIdentityStrategy string `json:"folio_identity_strategy"`
	NormalizationVersion  int    `json:"normalization_version"`
	CredentialMode        string `json:"credential_mode"`
	ReadOnly              *bool  `json:"read_only"`
	ResyncSupported       *bool  `json:"resync_supported"`
	DialTimeoutMS         int64  `json:"dial_timeout_ms"`
	ReadTimeoutMS         int64  `json:"read_timeout_ms"`
	WriteTimeoutMS        int64  `json:"write_timeout_ms"`
	HeartbeatIntervalMS   int64  `json:"heartbeat_interval_ms"`
	HeartbeatTimeoutMS    int64  `json:"heartbeat_timeout_ms"`
	FeedFreshnessMS       int64  `json:"feed_freshness_ms"`
	CompleteSyncMS        int64  `json:"complete_sync_ms"`
	FinancialBaseCurrency string `json:"financial_base_currency"`
	FinancialCurrencyExp  *int   `json:"financial_base_currency_exponent"`
}

func (s *server) authorPMSInterfaceRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in authorRevisionReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	cfg, verr := validateRevisionConfig(&in)
	if verr != nil {
		jsonErr(w, http.StatusBadRequest, "validation", verr.Error())
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	var kind string
	if err := s.db.QueryRow(ctx,
		`SELECT connector_kind FROM iam_v2.pms_interfaces WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
		id, s.tenantID, s.siteID).Scan(&kind); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "interface not found")
		return
	}
	raw, _ := json.Marshal(cfg)

	var revID string
	var revNo int
	// revision_no is allocated inside the statement rather than read-then-written: two operators authoring
	// at once would otherwise compute the same number and one would lose its revision to a unique violation.
	err := s.db.QueryRow(ctx, `
	    INSERT INTO iam_v2.pms_interface_revisions
	      (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, folio_identity_strategy,
	       config, normalization_version, financial_base_currency, financial_base_currency_exponent)
	    VALUES ($1,$2,$3,
	            (SELECT COALESCE(MAX(revision_no),0)+1 FROM iam_v2.pms_interface_revisions
	              WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3),
	            $4,$5,$6::jsonb,$7,$8,$9)
	    RETURNING id::text, revision_no`,
		s.tenantID, s.siteID, id, in.SourceTimezone, in.FolioIdentityStrategy, string(raw),
		in.NormalizationVersion, nullIfBlankText(in.FinancialBaseCurrency), in.FinancialCurrencyExp,
	).Scan(&revID, &revNo)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "author failed: "+err.Error())
		return
	}
	// The endpoint is recorded in the audit; the credential never is, and this path never accepts one --
	// rotating a secret is its own endpoint with its own re-authentication.
	s.audit(r, "pms_interface_revision.authored", "pms_interface", id,
		map[string]any{"revision_id": revID, "revision_no": revNo, "endpoint": in.Endpoint})
	writeJSON(w, http.StatusCreated, map[string]any{
		"revision_id": revID, "revision_no": revNo, "published": false,
		"note": "Draft revision. Publish it to make it the interface's current configuration.",
	})
}

// nullIfBlankText, not nullIfEmpty: that name already belongs to a helper in the integration-tagged test
// files, and a plain `go build ./...` never compiles those, so the collision only appeared in CI.
func nullIfBlankText(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

// validateRevisionConfig enforces the SAME contract pmsd.Revision.Validate() enforces at connect time.
//
// Checking it here is not duplication for its own sake: without it an operator can save a revision that
// looks fine, publish it, and only then discover the connector refuses it -- with the failure surfacing as
// a connection problem rather than as the configuration mistake it is.
func validateRevisionConfig(in *authorRevisionReq) (map[string]any, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(in.Endpoint))
	if err != nil || host == "" {
		return nil, fmt.Errorf("endpoint must be host:port, for example pms.example.local:5010")
	}
	p, perr := strconv.Atoi(port)
	if perr != nil || p < 1 || p > 65535 {
		return nil, fmt.Errorf("endpoint port must be a number in 1..65535")
	}
	if strings.TrimSpace(in.SourceTimezone) == "" {
		return nil, fmt.Errorf("source_timezone is required")
	}
	// A real IANA zone, loaded rather than pattern-matched: the connector converts PMS timestamps with it,
	// and a zone the runtime cannot load turns every arrival and departure into the wrong moment.
	if _, lerr := time.LoadLocation(in.SourceTimezone); lerr != nil {
		return nil, fmt.Errorf("source_timezone %q is not a known IANA time zone", in.SourceTimezone)
	}
	if !folioIdentityStrategies[in.FolioIdentityStrategy] {
		return nil, fmt.Errorf("folio_identity_strategy must be UNSET, GLOBALLY_UNIQUE, UNIQUE_PER_STAY or REUSED_SEQUENTIAL")
	}
	if in.NormalizationVersion <= 0 {
		return nil, fmt.Errorf("normalization_version must be greater than 0")
	}
	if !credentialModes[in.CredentialMode] {
		return nil, fmt.Errorf("credential_mode must be stated explicitly as AUTH_KEY or NONE")
	}
	// READ-ONLY IS NOT OPTIONAL. pmsd refuses any revision whose read-only capability is absent or false, so
	// a write-capable PMS connection cannot be configured here at all -- and that refusal is the product
	// decision, not an oversight to work around.
	if in.ReadOnly == nil || !*in.ReadOnly {
		return nil, fmt.Errorf("read_only must be true: this connector is read-only and pmsd refuses any other revision")
	}
	type d struct {
		name string
		val  int64
	}
	for _, t := range []d{
		{"dial_timeout_ms", in.DialTimeoutMS}, {"read_timeout_ms", in.ReadTimeoutMS},
		{"write_timeout_ms", in.WriteTimeoutMS}, {"heartbeat_interval_ms", in.HeartbeatIntervalMS},
		{"heartbeat_timeout_ms", in.HeartbeatTimeoutMS}, {"feed_freshness_ms", in.FeedFreshnessMS},
		{"complete_sync_ms", in.CompleteSyncMS},
	} {
		if t.val <= 0 || t.val > 86_400_000 {
			return nil, fmt.Errorf("%s must be between 1 and 86400000 milliseconds", t.name)
		}
	}
	if in.HeartbeatTimeoutMS <= in.HeartbeatIntervalMS {
		return nil, fmt.Errorf("heartbeat_timeout_ms must be greater than heartbeat_interval_ms, " +
			"otherwise every heartbeat times out before the next one is due")
	}
	cur := strings.TrimSpace(in.FinancialBaseCurrency)
	if (cur == "") != (in.FinancialCurrencyExp == nil) {
		return nil, fmt.Errorf("financial_base_currency and financial_base_currency_exponent must be set together or both left empty")
	}
	if cur != "" {
		if len(cur) != 3 || strings.ToUpper(cur) != cur {
			return nil, fmt.Errorf("financial_base_currency must be a three-letter uppercase ISO code")
		}
		if *in.FinancialCurrencyExp < 0 || *in.FinancialCurrencyExp > 4 {
			return nil, fmt.Errorf("financial_base_currency_exponent must be between 0 and 4")
		}
	}
	resync := in.ResyncSupported != nil && *in.ResyncSupported
	// The shape pgRepo.LoadInterface projects, key for key. Anything else an operator sent is dropped rather
	// than stored: the config is read with ->> on named keys, so an unknown key is silently inert, and inert
	// configuration that looks saved is how an operator ends up debugging a setting that never applied.
	return map[string]any{
		"endpoint":              strings.TrimSpace(in.Endpoint),
		"dial_timeout_ms":       in.DialTimeoutMS,
		"read_timeout_ms":       in.ReadTimeoutMS,
		"write_timeout_ms":      in.WriteTimeoutMS,
		"heartbeat_interval_ms": in.HeartbeatIntervalMS,
		"heartbeat_timeout_ms":  in.HeartbeatTimeoutMS,
		"feed_freshness_ms":     in.FeedFreshnessMS,
		"complete_sync_ms":      in.CompleteSyncMS,
		"resync_supported":      resync,
		"auth": map[string]any{
			"credential_mode": in.CredentialMode,
			"read_only":       true,
		},
	}, nil
}
