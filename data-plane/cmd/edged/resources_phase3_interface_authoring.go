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
	"context"
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
)

// pmsAllowedKinds is the set of connector kinds the CANONICAL PMS runtime supports.
//
// It holds exactly one entry, and that is the honest number: pmsd declares the same single supported kind
// (internal/pmsd.supportedConnectorKinds) and Revision.Validate refuses any other with REVISION_INVALID.
//
// So this is NOT the only thing standing between an unsupported kind and a bad connection — the runtime
// already refuses one. What it prevents is the unsupported configuration being AUTHORED at all. Without it
// an operator could create an Interface, configure a revision, publish it, and only then discover that the
// connector rejects the whole thing: work that could never succeed, failing at the last step, reported as a
// connection problem rather than as a connector this build does not implement.
//
// The legacy scd loader recognised six kinds (stub, protel-fias, opera-fias, fidelio-fias, mews, apaleo)
// and this path reused that list, which is how five options pmsd would refuse came to be offered on the
// canonical screen. Those implementations still exist and are not deleted here; they are simply not
// presented as PMS Interface capability, so the product surface matches what the runtime can run.
//
// REJECTION IS HERE, NOT ONLY IN THE DROPDOWN. Hiding an option in the UI leaves the API accepting it, and
// the API is what a script, a restored fixture or a future screen will use.
//
// A kind belongs in this map when pmsd declares support for it — the two lists are meant to agree.
var pmsAllowedKinds = map[string]bool{
	"protel-fias": true,
}

const (
	// folioStrategyUnset is the fail-closed default: while a revision carries it, PMS financial posting is
	// impossible by construction.
	folioStrategyUnset = "UNSET"
	// credentialModeNone matches the supported connector: the FIAS link carries no transport authentication.
	credentialModeNone = "NONE"
	// canonicalNormalizationVersion is the normalisation contract THIS BUILD implements. It changes when the
	// parsing/normalisation of the feed changes, which is a code change, never a configuration one.
	canonicalNormalizationVersion = 1
)

// folioIdentityStrategies and credentialModes were the validation allowlists for two fields this path no
// longer accepts from the caller. The schema still permits all four folio strategies and both credential
// modes — a future revision authored by a different, deliberate path may use them — but neither is a choice
// an operator makes on this form, so the allowlists here would only describe options nothing can select.
// The constants above carry the single value each field is now written with, and the reasons are recorded
// at the point of enforcement in validateRevisionConfig.

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

	// The interface must exist in THIS tenant and site, and must still be configurable.
	//
	// connector_kind used to be selected here and then never read -- a value fetched to prove existence and
	// thrown away. The lifecycle state is the one that actually decides whether authoring means anything:
	// DECOMMISSIONED is terminal, so a revision written against it is configuration that can never be
	// published or dialled, and accepting it silently is how an operator ends up believing a retired
	// interface was reconfigured.
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
	raw, _ := json.Marshal(cfg)

	// ALLOCATING revision_no IS A RACE, AND IT WAS LOSING.
	//
	// The number came from `(SELECT COALESCE(MAX(revision_no),0)+1 ...)` inside the INSERT. Under READ
	// COMMITTED that subquery takes its snapshot at statement start and takes no lock, so two operators
	// saving at the same moment both compute the same number. Proven deterministically against real
	// PostgreSQL by overlapping two transactions: both allocated revision 9, and the second died with
	//
	//     duplicate key value violates unique constraint "pms_interface_revisions_pms_interface_id_revision_no_key"
	//
	// which this handler reported as HTTP 500 -- an internal error for a situation that is neither internal
	// nor an error, and one that leaked an index name to the operator.
	//
	// A transaction-scoped advisory lock keyed on the interface makes allocation deterministic: the second
	// writer waits, then reads a MAX that includes the first row, and BOTH succeed with adjacent numbers.
	// The lock is per-interface, so authoring on one interface never blocks another, and it is released by
	// commit or rollback without any unlock path to forget.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "author failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The wait for that lock is BOUNDED, and running out of patience is not an internal error.
	//
	// An unbounded pg_advisory_xact_lock waits as long as it takes. Proven against a real database by holding
	// the same lock externally for six seconds: the request blocked for 5017ms and then returned HTTP 500 --
	// because dbCtx caps every statement at ten seconds and the context died first. The lock made allocation
	// correct and moved the 500 rather than removing it.
	//
	// lock_timeout makes the outcome deterministic: acquire it quickly, or fail fast with SQLSTATE 55P03 and
	// tell the operator something true and actionable. Three seconds is well inside the ten-second request
	// budget, so the answer is always the handler's own rather than a deadline nobody can interpret.
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

	// THE FINANCIAL CURRENCY PAIR IS A PHASE-4 COLUMN, AND THIS IS A PHASE-3 SURFACE.
	//
	// financial_base_currency and its exponent are added by migration 0011. Naming them unconditionally made
	// the whole endpoint fail on any deployment that stops at 0010 -- which is exactly what the Phase-3 CI
	// schema is, and it turned every authoring request there into a 500. The appliance carries every
	// migration, so the defect was invisible on it.
	//
	// So the columns are named only when the operator actually supplied a currency. Authoring without one --
	// the only sensible thing to do before Phase 4 exists -- works on both schemas, and asking for a currency
	// where the schema cannot store it is refused with the reason rather than an internal error.
	cur := strings.TrimSpace(in.FinancialBaseCurrency)

	// SOURCE FINGERPRINT — implementation-controlled, never an operator field.
	//
	// Phase-0 §7 detects duplicate sources by source_fingerprint equality: two Interfaces that turn out to be
	// the same physical PMS must be flagged, because PMS-settled purchases across such a pair are the case
	// where one property's charge lands on another's folio. The column existed and the detector read it, but
	// nothing ever wrote it, so every revision carried NULL — and NULL never equals NULL. The check was
	// running and structurally incapable of matching.
	//
	// It is derived, not asked for. An operator cannot be expected to know whether two host:port pairs are the
	// same PMS, and a fingerprint they could type is one they could mistype into a collision or out of a real
	// one. Connector kind is part of the input because the same endpoint reached by two different protocols is
	// not the same source, and the endpoint is lowercased so that Host:5003 and host:5003 fingerprint alike.
	// FAIL CLOSED IF THE SOURCE IDENTITY CANNOT BE FULLY RESOLVED. An earlier version swallowed the read
	// error and fingerprinted the endpoint alone, which is worse than not fingerprinting at all: two
	// interfaces that ARE the same physical PMS would still collide correctly, but an interface whose kind
	// failed to read would get a fingerprint no other revision of that same interface can ever reproduce —
	// so duplicate-source detection would quietly compare identities that were computed under different
	// rules and conclude, wrongly, that two sources differ.
	//
	// A fingerprint is a claim about identity. Refusing to make one from a partially-resolved identity is
	// the only safe answer; the operator retries and nothing has been written.
	connectorKind, kerr := interfaceConnectorKind(ctx, tx, id)
	if kerr != nil || strings.TrimSpace(connectorKind) == "" {
		jsonErr(w, http.StatusInternalServerError, "internal",
			"cannot resolve this interface's connector kind, so its source fingerprint cannot be derived; "+
				"no revision was written")
		return
	}
	fingerprint := pmsSourceFingerprint(connectorKind, in.Endpoint)

	var revID string
	var revNo int
	const revNoExpr = `(SELECT COALESCE(MAX(revision_no),0)+1 FROM iam_v2.pms_interface_revisions
	                     WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3)`
	if cur == "" {
		err = tx.QueryRow(ctx, `
		    INSERT INTO iam_v2.pms_interface_revisions
		      (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, folio_identity_strategy,
		       config, normalization_version, source_fingerprint)
		    VALUES ($1,$2,$3,`+revNoExpr+`,$4,$5,$6::jsonb,$7,$8)
		    RETURNING id::text, revision_no`,
			s.tenantID, s.siteID, id, in.SourceTimezone, in.FolioIdentityStrategy, string(raw),
			in.NormalizationVersion, fingerprint).Scan(&revID, &revNo)
	} else {
		err = tx.QueryRow(ctx, `
		    INSERT INTO iam_v2.pms_interface_revisions
		      (tenant_id, site_id, pms_interface_id, revision_no, source_timezone, folio_identity_strategy,
		       config, normalization_version, financial_base_currency, financial_base_currency_exponent,
		       source_fingerprint)
		    VALUES ($1,$2,$3,`+revNoExpr+`,$4,$5,$6::jsonb,$7,$8,$9,$10)
		    RETURNING id::text, revision_no`,
			s.tenantID, s.siteID, id, in.SourceTimezone, in.FolioIdentityStrategy, string(raw),
			in.NormalizationVersion, cur, in.FinancialCurrencyExp, fingerprint).Scan(&revID, &revNo)
		if isUndefinedColumn(err) {
			jsonErr(w, http.StatusBadRequest, "validation",
				"this deployment cannot store a financial base currency for a PMS interface: leave the "+
					"currency and exponent empty, or deploy the financial migrations first")
			return
		}
	}
	if err != nil {
		// The lock removes the race between two callers of THIS endpoint. A unique violation can still reach
		// here if a revision is inserted by some other path that does not take the lock, and that is a
		// conflict the operator can act on by retrying -- so it is reported as one, with no index name in it.
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
	// A NEW REVISION STARTS AT UNSET, AND THE OPERATOR DOES NOT CHOOSE OTHERWISE HERE.
	//
	// folio_identity_strategy decides how a folio number is interpreted across a stay, and getting it wrong
	// is how one guest's charges reach another guest's folio. It is a FINANCIAL determination that has to be
	// established by observing how the property's PMS actually reuses folio numbers — the Phase-0 contract
	// makes UNSET the fail-closed default precisely so that posting is impossible until somebody has done
	// that work and recorded a concrete strategy deliberately.
	//
	// The form used to offer all four values in a dropdown, which invited picking one that looked plausible.
	// Silently accepting GLOBALLY_UNIQUE from a form is not a configuration choice, it is a financial
	// assertion nobody verified, so this path now accepts only UNSET and says why.
	if strings.TrimSpace(in.FolioIdentityStrategy) == "" {
		in.FolioIdentityStrategy = folioStrategyUnset
	}
	if in.FolioIdentityStrategy != folioStrategyUnset {
		return nil, fmt.Errorf(
			"folio_identity_strategy must be %s for a revision authored here: a concrete strategy is a "+
				"financial determination made from observed PMS behaviour, not a form choice", folioStrategyUnset)
	}

	// IMPLEMENTATION-CONTROLLED, NOT OPERATOR-CONTROLLED.
	//
	// normalization_version identifies how THIS BUILD parses and normalises the feed, and resync support is
	// a property of the protocol adapter. Neither is a fact about the hotel, and neither is knowable from an
	// admin screen — an operator typing 2 into a normalization field does not change how the connector
	// parses anything; it just mislabels every event recorded under it. They are stamped from the
	// implementation and any submitted value is ignored rather than honoured.
	in.NormalizationVersion = canonicalNormalizationVersion
	resyncTrue := true
	in.ResyncSupported = &resyncTrue

	// CREDENTIAL MODE FOLLOWS THE CONNECTOR. The Protel FIAS link carries no transport authentication, so
	// NONE is the only truthful value and requiring the operator to state it added a way to be wrong: an
	// interface saved as AUTH_KEY waits for a secret that will never exist and never connects.
	if strings.TrimSpace(in.CredentialMode) == "" {
		in.CredentialMode = credentialModeNone
	}
	if in.CredentialMode != credentialModeNone {
		return nil, fmt.Errorf(
			"credential_mode must be %s: the supported PMS connector uses a link with no transport "+
				"authentication, so there is no credential to configure", credentialModeNone)
	}

	// READ-ONLY IS NOT OPTIONAL. pmsd refuses any revision whose read-only capability is absent or false, so
	// a write-capable PMS connection cannot be configured here at all -- and that refusal is the product
	// decision, not an oversight to work around. Defaulted rather than demanded: it is fixed, so making the
	// caller assert it was a formality that could only ever be got wrong.
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

// isLockNotAvailable reports PostgreSQL's lock_timeout expiry (SQLSTATE 55P03), which is contention rather
// than failure: the caller can simply try again.
func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

// isUndefinedColumn reports PostgreSQL's undefined_column (42703), which here means the deployment predates
// the migration that adds the column rather than anything the request got wrong about its own shape.
func isUndefinedColumn(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42703"
}

// pmsSourceFingerprint is the stable identity of the PHYSICAL source a revision points at: connector kind
// plus normalised endpoint, hashed. Two revisions — on the same appliance or on two Interfaces authored years
// apart — fingerprint identically exactly when they dial the same PMS the same way.
//
// SHA-256 rather than the endpoint verbatim: the fingerprint is compared, listed and reported, and a hash
// keeps a property's internal host and port out of screens and conflict records that only ever need to answer
// "same source or not". Truncated to 32 hex characters, which is far beyond collision risk for the handful of
// interfaces one appliance hosts and short enough to read in a comparison.
func pmsSourceFingerprint(connectorKind, endpoint string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(connectorKind)) + "|" +
		strings.ToLower(strings.TrimSpace(endpoint))))
	return hex.EncodeToString(sum[:])[:32]
}

// interfaceConnectorKind reads the interface's connector kind inside the authoring transaction.
//
// It returns the error rather than absorbing it. The kind is half of the source fingerprint's input, and a
// fingerprint derived from half an identity is not a weaker fingerprint — it is a different one, which is
// how a duplicate-source check ends up comparing values that were never comparable.
func interfaceConnectorKind(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT connector_kind FROM iam_v2.pms_interfaces WHERE id=$1`, id).Scan(&kind); err != nil {
		return "", err
	}
	return kind, nil
}
