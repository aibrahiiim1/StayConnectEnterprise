package main

// Authoring a PMS interface and its revision configuration, using only the fields the implemented connector
// reads: pmsd.Revision.Validate() and pgRepo.LoadInterface(). No protocol behaviour or managed state is invented.
// A revision authored here is a DRAFT; publishing stays a separate, password-confirmed action because it changes
// what every guest is resolved against from that moment.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/pmsprovider"
)

// The connector kinds the canonical PMS runtime supports; it agrees with pmsd by construction (both derive from
// the provider registry, internal/pmsprovider). Rejection lives here, not only in the dropdown, so the API
// refuses an unsupported kind and an operator never publishes a configuration the connector would reject.
var pmsAllowedKinds = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range pmsprovider.Kinds() {
		m[k] = true
	}
	return m
}()

const (
	// The fail-closed default: while a revision carries it, PMS financial posting is impossible. Only financial onboarding records RESERVATION.
	postingTargetUnset = "UNSET"
	credentialModeNone = "NONE"
	// The normalisation contract this build implements; it changes with a code change, never configuration.
	canonicalNormalizationVersion = 1
)

// The posting target and credential mode are not accepted from the caller; each is written with the single value above (see validateRevisionConfig).

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
	// AUTH_DISABLED, not ACTIVE: a new interface has no published revision and no endpoint, so ACTIVE would advertise a connection that cannot exist.
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
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return strings.Join(out, ", ")
}

type authorRevisionReq struct {
	Endpoint             string `json:"endpoint"`
	SourceTimezone       string `json:"source_timezone"`
	PostingTargetModel   string `json:"posting_target_model"`
	NormalizationVersion int    `json:"normalization_version"`
	CredentialMode       string `json:"credential_mode"`
	ReadOnly             *bool  `json:"read_only"`
	ResyncSupported      *bool  `json:"resync_supported"`
	DialTimeoutMS        int64  `json:"dial_timeout_ms"`
	ReadTimeoutMS        int64  `json:"read_timeout_ms"`
	WriteTimeoutMS       int64  `json:"write_timeout_ms"`
	HeartbeatIntervalMS  int64  `json:"heartbeat_interval_ms"`
	HeartbeatTimeoutMS   int64  `json:"heartbeat_timeout_ms"`
	FeedFreshnessMS      int64  `json:"feed_freshness_ms"`
	CompleteSyncMS       int64  `json:"complete_sync_ms"`
	// Operator override for how long PMS-derived Stay evidence may still authorise a Room sign-in. Absent, the bound
	// is one complete_sync_ms cadence plus the heartbeat allowance, which also limits authentication while the
	// transport is down. 0 or absent means no override, so it is outside the mandatory-bounds loop.
	MaxAuthCacheAgeSeconds int64  `json:"max_auth_cache_age_seconds"`
	FinancialBaseCurrency  string `json:"financial_base_currency"`
	FinancialCurrencyExp   *int   `json:"financial_base_currency_exponent"`
	// TLS on the FIAS link, off unless asked for; the fingerprint pins a self-signed certificate.
	TLSEnabled    bool   `json:"tls_enabled"`
	TLSCertSHA256 string `json:"tls_cert_sha256"`
	// Connector settings keyed by the field keys GET /pms-providers publishes. For protel-fias, present keys are the
	// top-level field names and override them before the same validation; for a REST connector it is required and
	// validated against the provider's schema (unknown keys refused), stored under config.provider.
	ProviderConfig map[string]any `json:"provider_config"`
}

func (s *server) authorPMSInterfaceRevision(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in authorRevisionReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	// Without provider_config validation is unchanged and runs before any database read; with it the connector kind
	// decides the schema, so validation waits for the interface lookup.
	var cfg map[string]any
	// Presence, not length: "provider_config": {} is valid for a REST draft with all defaults, and maps nothing for protel-fias.
	hasProviderCfg := in.ProviderConfig != nil
	if !hasProviderCfg {
		var verr error
		cfg, verr = validateRevisionConfig(&in)
		if verr != nil {
			jsonErr(w, http.StatusBadRequest, "validation", verr.Error())
			return
		}
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	// The interface must exist in THIS tenant and site and be configurable: DECOMMISSIONED is terminal, so a
	// revision against it could never be published or dialled.
	var lifecycle string
	if err := s.db.QueryRow(ctx,
		`SELECT lifecycle_state FROM iam_v2.pms_interfaces WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
		id, s.tenantID, s.siteID).Scan(&lifecycle); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "interface not found")
		return
	}
	if lifecycle == "DECOMMISSIONED" {
		jsonErr(w, http.StatusConflict, "conflict",
			"this interface is decommissioned: it cannot be configured, and a revision authored against it "+
				"could never be published")
		return
	}
	var restCfg *pmsprovider.RESTConfig
	var ignored []string
	{
		var kind string
		if err := s.db.QueryRow(ctx,
			`SELECT connector_kind FROM iam_v2.pms_interfaces WHERE id=$1 AND tenant_id=$2 AND site_id=$3`,
			id, s.tenantID, s.siteID).Scan(&kind); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "author failed")
			return
		}
		prov, known := pmsprovider.Get(kind)
		switch {
		case known && prov.IsREST():
			if !hasProviderCfg {
				jsonErr(w, http.StatusBadRequest, "provider_config_required",
					"provider_config is required for a "+prov.Label+" connection: its settings are listed by GET /pms-providers")
				return
			}
			rc, perr := validateRESTRevision(prov, &in)
			if perr != nil {
				jsonErr(w, http.StatusBadRequest, perr.Code, perr.Message)
				return
			}
			restCfg = &rc
			cfg = rc.StoredConfig()
			ignored = ignoredTopLevelFields(&in)
		case hasProviderCfg:
			// protel-fias (or a legacy kind): provider_config keys are the top-level fields, then the same validation.
			if perr := applyFIASProviderConfig(&in); perr != nil {
				jsonErr(w, http.StatusBadRequest, perr.Code, perr.Message)
				return
			}
			var verr error
			cfg, verr = validateRevisionConfig(&in)
			if verr != nil {
				jsonErr(w, http.StatusBadRequest, "validation", verr.Error())
				return
			}
		}
	}
	raw, _ := json.Marshal(cfg)

	// Allocating revision_no is serialized: MAX()+1 inside the INSERT takes no lock under READ COMMITTED, so two
	// concurrent saves computed the same number and one died on the unique constraint as a 500. A
	// transaction-scoped advisory lock per interface makes the second writer wait and read the new MAX; it never
	// blocks other interfaces and is released by commit or rollback.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "author failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The lock wait is bounded: an unbounded wait outlives dbCtx's ten-second cap and surfaces as a 500. lock_timeout
	// (three seconds) fails fast with SQLSTATE 55P03 so the handler returns a true, actionable answer.
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '3s'`); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "author failed")
		return
	}
	if _, err := tx.Exec(ctx,
		`SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, id); err != nil {
		if isLockNotAvailable(err) {
			jsonErr(w, http.StatusConflict, "conflict",
				"another revision is being created for this interface right now: try again in a moment")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "author failed")
		return
	}

	// financial_base_currency and its exponent come from migration 0011, so they are named only when the operator
	// supplied a currency; otherwise authoring would fail on any schema that stops at 0010. A currency the schema cannot store is refused with the reason.
	cur := strings.TrimSpace(in.FinancialBaseCurrency)

	// Source fingerprint: implementation-controlled, never an operator field. Duplicate-source detection compares
	// source_fingerprint equality (two interfaces that are the same physical PMS must be flagged, since PMS-settled
	// purchases could land on another property's folio); NULL never equals NULL, so it must always be written. It
	// is derived from connector kind plus lowercased endpoint. Fail closed if the identity cannot be fully
	// resolved: a fingerprint from partial identity would be computed under different rules and wrongly conclude
	// sources differ. The operator retries and nothing has been written.
	connectorKind, kerr := interfaceConnectorKind(ctx, tx, id)
	if kerr != nil || strings.TrimSpace(connectorKind) == "" {
		jsonErr(w, http.StatusInternalServerError, "internal",
			"cannot resolve this interface's connector kind, so its source fingerprint cannot be derived; "+
				"no revision was written")
		return
	}
	fingerprint := pmsSourceFingerprint(connectorKind, in.Endpoint)
	if restCfg != nil {
		identity := restCfg.SourceIdentity()
		if !restCfg.SourceIdentityComplete() {
			var nonce [8]byte
			_, _ = rand.Read(nonce[:])
			identity += "|unscoped|" + hex.EncodeToString(nonce[:])
		}
		fingerprint = pmsprovider.SourceFingerprint(connectorKind, identity)
		in.Endpoint = restCfg.Endpoint()
	}

	var revID string
	var revNo int
	const revNoExpr = `(SELECT COALESCE(MAX(revision_no),0)+1 FROM iam_v2.pms_interface_revisions
	                     WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3)`
	if cur == "" {
		err = tx.QueryRow(ctx, `
		    INSERT INTO iam_v2.pms_interface_revisions
		      (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, posting_target_model,
		       config, normalization_version, source_fingerprint)
		    VALUES ($1,$2,$3,`+revNoExpr+`,$4,$5,$6::jsonb,$7,$8)
		    RETURNING id::text, revision_no`,
			s.tenantID, s.siteID, id, in.SourceTimezone, in.PostingTargetModel, string(raw),
			in.NormalizationVersion, fingerprint).Scan(&revID, &revNo)
	} else {
		err = tx.QueryRow(ctx, `
		    INSERT INTO iam_v2.pms_interface_revisions
		      (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, posting_target_model,
		       config, normalization_version, financial_base_currency, financial_base_currency_exponent,
		       source_fingerprint)
		    VALUES ($1,$2,$3,`+revNoExpr+`,$4,$5,$6::jsonb,$7,$8,$9,$10)
		    RETURNING id::text, revision_no`,
			s.tenantID, s.siteID, id, in.SourceTimezone, in.PostingTargetModel, string(raw),
			in.NormalizationVersion, cur, in.FinancialCurrencyExp, fingerprint).Scan(&revID, &revNo)
		if isUndefinedColumn(err) {
			jsonErr(w, http.StatusBadRequest, "validation",
				"this deployment cannot store a financial base currency for a PMS interface: leave the "+
					"currency and exponent empty, or deploy the financial migrations first")
			return
		}
	}
	if err != nil {
		// The lock only covers callers of THIS endpoint; a unique violation from another path is a conflict the operator can retry, reported without an index name.
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict",
				"another revision was created for this interface at the same moment: reload and save again")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "author failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "author failed")
		return
	}
	// The endpoint is audited; the credential never is, and this path never accepts one (rotation is its own endpoint with re-authentication).
	s.audit(r, "pms_interface_revision.authored", "pms_interface", id,
		map[string]any{"revision_id": revID, "revision_no": revNo, "endpoint": in.Endpoint})
	resp := map[string]any{
		"revision_id": revID, "revision_no": revNo, "published": false,
		"note": "Draft revision. Publish it to make it the interface's current configuration.",
	}
	if restCfg != nil {
		// Said out loud rather than silently dropped: for a REST connector these values derive from provider_config.
		resp["ignored_fields"] = ignored
	}
	writeJSON(w, http.StatusCreated, resp)
}

// Top-level source_timezone and max_auth_cache_age_seconds are honoured when provider_config lacks them; the
// posting target stays UNSET and no financial currency is accepted (posting exists only on the FIAS path).
func validateRESTRevision(prov pmsprovider.Provider, in *authorRevisionReq) (pmsprovider.RESTConfig, *pmsprovider.ValidationError) {
	if s := strings.TrimSpace(in.PostingTargetModel); s != "" && s != postingTargetUnset {
		return pmsprovider.RESTConfig{}, &pmsprovider.ValidationError{Code: "validation", Field: "posting_target_model",
			Message: "posting_target_model must be " + postingTargetUnset + " for a revision authored here"}
	}
	if strings.TrimSpace(in.FinancialBaseCurrency) != "" || in.FinancialCurrencyExp != nil {
		return pmsprovider.RESTConfig{}, &pmsprovider.ValidationError{Code: "validation", Field: "financial_base_currency",
			Message: "a financial base currency applies only to the FIAS posting path; leave it empty for " + prov.Label}
	}
	if in.ReadOnly != nil && !*in.ReadOnly {
		return pmsprovider.RESTConfig{}, &pmsprovider.ValidationError{Code: "validation", Field: "read_only",
			Message: "read_only must be true: this connector is read-only and pmsd refuses any other revision"}
	}
	rc, verr := pmsprovider.NormalizeREST(prov, in.ProviderConfig, in.SourceTimezone, in.MaxAuthCacheAgeSeconds)
	if verr != nil {
		return pmsprovider.RESTConfig{}, verr
	}
	in.PostingTargetModel = postingTargetUnset
	in.NormalizationVersion = canonicalNormalizationVersion
	in.SourceTimezone = rc.SourceTimezone
	in.CredentialMode = pmsprovider.CredentialAuthKey
	return rc, nil
}

func ignoredTopLevelFields(in *authorRevisionReq) []string {
	out := []string{}
	add := func(name string, set bool) {
		if set {
			out = append(out, name)
		}
	}
	add("endpoint", strings.TrimSpace(in.Endpoint) != "")
	add("credential_mode", strings.TrimSpace(in.CredentialMode) != "" && in.CredentialMode != pmsprovider.CredentialAuthKey)
	add("dial_timeout_ms", in.DialTimeoutMS != 0)
	add("read_timeout_ms", in.ReadTimeoutMS != 0)
	add("write_timeout_ms", in.WriteTimeoutMS != 0)
	add("heartbeat_interval_ms", in.HeartbeatIntervalMS != 0)
	add("heartbeat_timeout_ms", in.HeartbeatTimeoutMS != 0)
	add("feed_freshness_ms", in.FeedFreshnessMS != 0)
	add("complete_sync_ms", in.CompleteSyncMS != 0)
	return out
}

// Maps a protel-fias provider_config onto the top-level fields, each checked against the registry; unknown keys
// are refused and validateRevisionConfig then runs unchanged, so storage matches a top-level request.
func applyFIASProviderConfig(in *authorRevisionReq) *pmsprovider.ValidationError {
	prov, _ := pmsprovider.Get(pmsprovider.KindProtelFIAS)
	for k, raw := range in.ProviderConfig {
		f, ok := prov.Field(k)
		if !ok {
			return &pmsprovider.ValidationError{Code: pmsprovider.CodeUnknownField, Field: k,
				Message: "\"" + k + "\" is not a setting of the " + prov.Label + " connector"}
		}
		if raw == nil {
			continue
		}
		if s, isStr := raw.(string); isStr && strings.TrimSpace(s) == "" && f.Type != pmsprovider.FieldString {
			continue
		}
		v, verr := pmsprovider.Coerce(f, raw)
		if verr != nil {
			return verr
		}
		switch k {
		case "endpoint":
			in.Endpoint = v.(string)
		case "source_timezone":
			in.SourceTimezone = v.(string)
		case "dial_timeout_ms":
			in.DialTimeoutMS = v.(int64)
		case "read_timeout_ms":
			in.ReadTimeoutMS = v.(int64)
		case "write_timeout_ms":
			in.WriteTimeoutMS = v.(int64)
		case "heartbeat_interval_ms":
			in.HeartbeatIntervalMS = v.(int64)
		case "heartbeat_timeout_ms":
			in.HeartbeatTimeoutMS = v.(int64)
		case "feed_freshness_ms":
			in.FeedFreshnessMS = v.(int64)
		case "complete_sync_ms":
			in.CompleteSyncMS = v.(int64)
		case "max_auth_cache_age_seconds":
			in.MaxAuthCacheAgeSeconds = v.(int64)
		case "tls_enabled":
			in.TLSEnabled = v.(bool)
		case "tls_cert_sha256":
			in.TLSCertSHA256 = v.(string)
		case "financial_base_currency":
			in.FinancialBaseCurrency = v.(string)
		case "financial_base_currency_exponent":
			n := int(v.(int64))
			in.FinancialCurrencyExp = &n
		}
	}
	return nil
}

// Enforces the same contract as pmsd.Revision.Validate() at connect time, so a mistake surfaces here and not as a connection problem after publishing.
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
	// A real IANA zone, loaded rather than pattern-matched: a zone the runtime cannot load shifts every arrival and departure.
	if _, lerr := time.LoadLocation(in.SourceTimezone); lerr != nil {
		return nil, fmt.Errorf("source_timezone %q is not a known IANA time zone", in.SourceTimezone)
	}
	// A new revision starts UNSET and this path accepts only UNSET: RESERVATION (RN + G#) is a financial
	// determination (the vendor confirms reservation numbers are unique and never reused) recorded by financial
	// onboarding, so posting stays impossible until done deliberately.
	if strings.TrimSpace(in.PostingTargetModel) == "" {
		in.PostingTargetModel = postingTargetUnset
	}
	if in.PostingTargetModel != postingTargetUnset {
		return nil, fmt.Errorf(
			"posting_target_model must be %s for a revision authored here: the posting target is recorded only by "+
				"financial onboarding, with the vendor's confirmation, never by a form", postingTargetUnset)
	}

	// Implementation-controlled: normalization_version and resync support describe this build's parser and adapter,
	// not the hotel. Stamped from the implementation; any submitted value is ignored.
	in.NormalizationVersion = canonicalNormalizationVersion
	resyncTrue := true
	in.ResyncSupported = &resyncTrue

	// Credential mode follows the connector: the Protel FIAS link has no transport authentication, so NONE is the
	// only truthful value (AUTH_KEY would wait for a secret that never exists).
	if strings.TrimSpace(in.CredentialMode) == "" {
		in.CredentialMode = credentialModeNone
	}
	if in.CredentialMode != credentialModeNone {
		return nil, fmt.Errorf(
			"credential_mode must be %s: the supported PMS connector uses a link with no transport "+
				"authentication, so there is no credential to configure", credentialModeNone)
	}

	// Read-only is not optional: pmsd refuses any revision without it, so a write-capable PMS connection cannot be configured here. Defaulted, since it is fixed.
	if in.ReadOnly == nil {
		readOnlyTrue := true
		in.ReadOnly = &readOnlyTrue
	}
	if !*in.ReadOnly {
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
	// Bounded independently of the millisecond timeouts (this is seconds; 72 hours is 259200). The upper bound
	// matches iam_v2.p3_feed_authorizes, which silently ignores a larger value that the operator would believe saved.
	if in.MaxAuthCacheAgeSeconds < 0 || in.MaxAuthCacheAgeSeconds > 604800 {
		return nil, fmt.Errorf("max_auth_cache_age_seconds must be between 0 and 604800 seconds (7 days); " +
			"0 or omitted means the default complete-sync bound applies")
	}
	pin := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(in.TLSCertSHA256), ":", ""))
	if pin != "" {
		if !in.TLSEnabled {
			return nil, fmt.Errorf("tls_cert_sha256 is set but tls_enabled is off: turn encryption on, or clear the fingerprint")
		}
		if b, err := hex.DecodeString(pin); err != nil || len(b) != sha256.Size {
			return nil, fmt.Errorf("tls_cert_sha256 must be the 64-hex-digit SHA-256 fingerprint of the PMS certificate")
		}
	}
	resync := in.ResyncSupported != nil && *in.ResyncSupported
	// The shape pgRepo.LoadInterface projects, key for key. Unknown keys are dropped: config is read with ->> on
	// named keys, so an unknown key would be inert yet look saved.
	cfg := map[string]any{
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
	}
	// Written only when set, so an unset override leaves the key absent rather than "0", which would read as a setting.
	if in.MaxAuthCacheAgeSeconds > 0 {
		cfg["max_auth_cache_age_seconds"] = in.MaxAuthCacheAgeSeconds
	}
	// Absent unless on.
	if in.TLSEnabled {
		tlsCfg := map[string]any{"enabled": true}
		if pin != "" {
			tlsCfg["cert_sha256"] = pin
		}
		cfg["tls"] = tlsCfg
	}
	return cfg, nil
}

// Lock_timeout expiry (SQLSTATE 55P03): contention, not failure; the caller can retry.
func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// Undefined column (42703): the deployment predates the migration that adds it.
func isUndefinedColumn(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42703"
}

// Stable identity of the physical source: connector kind plus normalised endpoint, hashed (SHA-256 truncated to 32
// hex) so a property's internal host and port stay out of screens and conflict records. Lives in the provider
// registry so pmsd, edged and every connector share one.
func pmsSourceFingerprint(connectorKind, endpoint string) string {
	return pmsprovider.SourceFingerprint(connectorKind, endpoint)
}

// Returns the error rather than absorbing it: the kind is half the fingerprint's input, and a fingerprint from half an identity is a different one.
func interfaceConnectorKind(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT connector_kind FROM iam_v2.pms_interfaces WHERE id=$1`, id).Scan(&kind); err != nil {
		return "", err
	}
	return kind, nil
}
