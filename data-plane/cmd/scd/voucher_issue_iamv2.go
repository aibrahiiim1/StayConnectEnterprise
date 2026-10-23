package main

// IAM-v2 voucher issuance lives in scd, not edged, on purpose: the voucher DEK is the root of voucher confidentiality, and scd (root, protected unix socket)
// is less exposed than edged (unprivileged, serves HTTP). The route stays in edged and PROXIES here.
// Key hierarchy: appliance secret store -> DEK (AES-256-GCM, encryption_key_id) -> per-generation HMAC key stored ONLY as hmac_key_ciphertext -> per-voucher
// blind index (code_hmac) + recoverable ciphertext + display hint (code_last4), pinned to code_key_generation_id. The DEK encrypts and never indexes; the HMAC
// key indexes and never encrypts. Vouchers pin their generation, so rotation leaves unredeemed vouchers usable.
// The plaintext code exists only in memory and is returned ONCE in the issuance response; never logged, stored in a column or put in an audit payload.
// Recovery is the separate reveal/export action behind re-authentication and audit.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/stayconnect/enterprise/data-plane/internal/codegen"
	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
)

const (
	voucherDEKFile = "voucher_dek.key"
	// encryption_key_id is a uuid column, so the DEK id is a fixed UUID. Stable by contract: changing it orphans every generation sealed under the old id.
	voucherDEKID = "0a5c1e00-0000-4000-8000-0000000000d1"
)

func (s *server) voucherKeyring() (iamv2.VoucherKeyring, error) {
	dir := os.Getenv("SCD_SECRETS_DIR")
	if dir == "" {
		dir = "/etc/stayconnect/secrets"
	}
	dek, err := localkeys.LoadExistingKey(filepath.Join(dir, voucherDEKFile))
	if err != nil {
		return nil, err
	}
	return iamv2.MapVoucherKeyring{voucherDEKID: dek}, nil
}

// Returns the active generation and its clear HMAC key, creating generation 1 if the site has none. Never overwrites an existing generation's key:
// that would orphan every voucher already indexed under it.
func (s *server) ensureVoucherKeyGeneration(ctx context.Context, kr iamv2.VoucherKeyring) (genID string, genNo int, hmacKey []byte, err error) {
	var sealed []byte
	var nonceB64, keyID string
	err = s.db.QueryRow(ctx, `
	    SELECT id::text, generation_no, encryption_key_id::text, hmac_key_ciphertext,
	           COALESCE(aead_params->>'nonce_b64','')
	      FROM iam_v2.voucher_code_key_generations
	     WHERE tenant_id=$1 AND site_id=$2 AND superseded_at IS NULL
	     ORDER BY generation_no DESC LIMIT 1`,
		s.tenID, s.siteID).Scan(&genID, &genNo, &keyID, &sealed, &nonceB64)
	if err == nil {
		nonce, derr := base64.StdEncoding.DecodeString(nonceB64)
		if derr != nil {
			return "", 0, nil, derr
		}
		clear, oerr := iamv2.OpenVoucherHMACKey(kr, keyID, s.tenID, s.siteID, genNo, sealed, nonce)
		if oerr != nil {
			return "", 0, nil, oerr
		}
		return genID, genNo, clear, nil
	}

	// No ACTIVE generation: mint max(generation_no)+1, not 1. Hardcoding 1 collided with the superseded generation 1 and killed issuance at the first rotation.
	if err := s.db.QueryRow(ctx, `
	    SELECT COALESCE(MAX(generation_no), 0) + 1
	      FROM iam_v2.voucher_code_key_generations
	     WHERE tenant_id=$1 AND site_id=$2`, s.tenID, s.siteID).Scan(&genNo); err != nil {
		return "", 0, nil, err
	}
	clear, sealedNew, nonce, kerr := iamv2.NewVoucherHMACKey(kr, voucherDEKID, s.tenID, s.siteID, genNo)
	if kerr != nil {
		return "", 0, nil, kerr
	}
	params, _ := json.Marshal(map[string]any{
		"alg":        "AES-256-GCM",
		"nonce_b64":  base64.StdEncoding.EncodeToString(nonce),
		"aad":        "tenant|site|generation_no",
		"hmac":       "HMAC-SHA256",
		"key_source": "appliance secret store DEK",
	})
	if err = s.db.QueryRow(ctx, `
	    INSERT INTO iam_v2.voucher_code_key_generations
	           (tenant_id, site_id, generation_no, hmac_key_ciphertext, aead_params, encryption_key_id)
	    VALUES ($1, $2, $3, $4, $5::jsonb, $6)
	 RETURNING id::text`,
		s.tenID, s.siteID, genNo, sealedNew, string(params), voucherDEKID).Scan(&genID); err != nil {
		return "", 0, nil, err
	}
	// Drop the authenticator's cached key set so a voucher under this new generation authenticates immediately rather than after the cache TTL.
	invalidateVoucherKeyCache(s.tenID, s.siteID)
	return genID, genNo, clear, nil
}

// The package revision is REQUIRED and pinned on the voucher row, so republishing a package cannot retroactively change what a printed card is worth.
func (s *server) issueVouchersIAMv2(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PackageRevisionID string `json:"package_revision_id"`
		Count             int    `json:"count"`
		Note              string `json:"note,omitempty"`
		// The SESSION-resolved operator, required so a batch found in a drawer can be traced to whoever made it.
		IssuedBy string `json:"issued_by"`
		// Optional redemption window, [from, until) with NULL unbounded, as the authenticator enforces (internal/iamv2/repo_pg.go).
		ValidFrom  string `json:"valid_from,omitempty"`
		ValidUntil string `json:"valid_until,omitempty"`
	}
	// DisallowUnknownFields so that `length` is REFUSED rather than ignored: the format is the site setting, and accepting and discarding a field would
	// issue vouchers in a format the caller did not choose while answering 201.
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest,
			"bad body (the code format is the site setting iam_v2.site_voucher_code_settings, not a field on this request)")
		return
	}
	if in.PackageRevisionID == "" {
		httpErr(w, http.StatusBadRequest, "package_revision_id is required: a voucher pins the immutable revision it grants")
		return
	}
	if in.Count < 1 || in.Count > 500 {
		httpErr(w, http.StatusBadRequest, "count must be between 1 and 500")
		return
	}
	if strings.TrimSpace(in.IssuedBy) == "" {
		httpErr(w, http.StatusBadRequest,
			"issued_by is required: it is the operator edged resolved from the session, and a batch with no "+
				"author is a batch nobody can be asked about")
		return
	}
	validFrom, validUntil, werr := parseRedemptionWindow(in.ValidFrom, in.ValidUntil)
	if werr != nil {
		httpErr(w, http.StatusBadRequest, werr.Error())
		return
	}
	// A card that could never be redeemed is not issued: the issuance gate cannot see that the package rules exclude every voucher client or that its
	// allowance is computed per stay night.
	if why, rerr := s.voucherRevisionBlocker(r.Context(), in.PackageRevisionID); rerr != nil {
		httpErr(w, http.StatusServiceUnavailable, "the package's conditions could not be read; vouchers were not issued")
		return
	} else if why != "" {
		httpErr(w, http.StatusConflict, why)
		return
	}
	// The format comes from the site setting (defaults if never saved); a read failure is fatal on purpose, see voucherCodeFormatFor.
	format, ferr := s.voucherCodeFormatFor(r.Context())
	if ferr != nil {
		httpErr(w, http.StatusServiceUnavailable,
			"the voucher code format for this site could not be read; issuance will not guess one")
		return
	}
	opts, oerr := format.voucherCodeOptions()
	if oerr != nil {
		httpErr(w, http.StatusUnprocessableEntity,
			"the stored voucher code format is not usable: "+oerr.Error())
		return
	}
	kr, err := s.voucherKeyring()
	if err != nil {
		// Fail closed. Issuing without the DEK would mean storing a code we cannot seal.
		httpErr(w, http.StatusServiceUnavailable,
			"voucher encryption key unavailable; run keybootstrap at deploy")
		return
	}
	ctx := r.Context()
	genID, _, hmacKey, err := s.ensureVoucherKeyGeneration(ctx, kr)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "voucher key generation unavailable")
		return
	}

	// Generated as a BATCH: codegen.GenerateN guarantees in-batch uniqueness and refuses a batch too large for its code space, whereas catching
	// collisions through UNIQUE (code_hmac) surfaced as a failure after part of the batch had committed.
	codes, gerr := codegen.GenerateN(in.Count, opts)
	if gerr != nil {
		httpErr(w, http.StatusUnprocessableEntity, "code generation refused: "+gerr.Error())
		return
	}
	// ONE BATCH PER CALL (batch_id), so export selection and bulk recall have something to name. ATOMIC: the batch row and every voucher commit
	// together; the issuance gate refuses, in the same transaction, a package that is inactive, not acquirable by Voucher or not on its current revision.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "issuance could not start")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var batchID string
	if err := tx.QueryRow(ctx, `
	    INSERT INTO iam_v2.voucher_batches (tenant_id, site_id, package_revision_id, label)
	    VALUES ($1, $2, $3, $4) RETURNING id::text`,
		s.tenID, s.siteID, in.PackageRevisionID, nullIfEmpty(in.Note)).Scan(&batchID); err != nil {
		httpErr(w, http.StatusInternalServerError, "batch could not be recorded")
		return
	}
	for _, code := range codes {
		// The row id is generated up front because the ciphertext AAD binds it, so the ciphertext cannot move to another voucher row.
		var vid string
		if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&vid); err != nil {
			httpErr(w, http.StatusInternalServerError, "id allocation failed")
			return
		}
		ct, nonce, serr := iamv2.SealVoucherCode(kr, voucherDEKID, s.tenID, s.siteID, vid, genID, code)
		if serr != nil {
			httpErr(w, http.StatusInternalServerError, "code seal failed")
			return
		}
		idx := iamv2.VoucherCodeHMAC(hmacKey, s.tenID, s.siteID, code)
		if _, err := tx.Exec(ctx, `
		    INSERT INTO iam_v2.vouchers
		           (id, tenant_id, site_id, package_revision_id, code_hmac, code_ciphertext, code_nonce,
		            code_key_generation_id, code_last4, notes, batch_id, issued_by,
		            redemption_valid_from, redemption_valid_until)
		    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
			vid, s.tenID, s.siteID, in.PackageRevisionID, idx, ct, nonce, genID,
			iamv2.Last4(code), nullIfEmpty(in.Note), batchID, strings.TrimSpace(in.IssuedBy),
			validFrom, validUntil); err != nil {
			if isVoucherIssuanceRefusal(err) {
				httpErr(w, http.StatusConflict, voucherIssuanceRefusal(err))
				return
			}
			httpErr(w, http.StatusInternalServerError, "voucher insert failed")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		httpErr(w, http.StatusInternalServerError, "issuance could not be committed")
		return
	}
	// The audit records how many were issued, against which revision and in which format, NEVER a code (not even last4 in bulk: a batch log line is
	// not the audited reveal). config_version says whether the format came from a saved setting or the defaults.
	slog.Info("iamv2 vouchers issued", "count", in.Count, "batch_id", batchID,
		"package_revision_id", in.PackageRevisionID, "generation", genID,
		"code_mode", format.Mode, "code_length", format.Length, "format_version", format.Version)
	// ONE-TIME return of the plaintext codes. This is the only moment they exist outside memory.
	writeJSON(w, http.StatusCreated, map[string]any{
		"authority":           "iam_v2",
		"count":               len(codes),
		"batch_id":            batchID,
		"package_revision_id": in.PackageRevisionID,
		"codes":               codes,
	})
}

// Asks iamv2.VoucherBlocker whether a voucher for this revision could ever be redeemed; an unknown revision is left to the issuance gate.
func (s *server) voucherRevisionBlocker(ctx context.Context, revID string) (string, error) {
	if !validRequestID(revID) {
		return "That is not a package version id.", nil
	}
	var mode string
	err := s.db.QueryRow(ctx, `SELECT COALESCE(data_allocation_policy->>'mode','FIXED')
		  FROM iam_v2.internet_package_revisions WHERE tenant_id=$1 AND site_id=$2 AND id=$3`,
		s.tenID, s.siteID, revID).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	rows, err := s.db.Query(ctx, `SELECT rule_type, rule_value FROM iam_v2.package_eligibility_rules
		 WHERE package_revision_id=$1`, revID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var rules []iamv2.EligibilityRule
	for rows.Next() {
		var ru iamv2.EligibilityRule
		var raw []byte
		if err := rows.Scan(&ru.Type, &raw); err != nil {
			return "", err
		}
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &ru.Value)
		}
		rules = append(rules, ru)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return iamv2.VoucherBlocker(mode, rules), nil
}

// Refuses a window that can never open rather than storing it: nothing downstream would complain and the guest would see "that code is not valid", the same as a wrong code.
func parseRedemptionWindow(from, until string) (any, any, error) {
	var vf, vu any
	var fromT, untilT time.Time
	var err error
	if strings.TrimSpace(from) != "" {
		if fromT, err = time.Parse(time.RFC3339, strings.TrimSpace(from)); err != nil {
			return nil, nil, errors.New("valid_from must be an RFC3339 timestamp")
		}
		vf = fromT
	}
	if strings.TrimSpace(until) != "" {
		if untilT, err = time.Parse(time.RFC3339, strings.TrimSpace(until)); err != nil {
			return nil, nil, errors.New("valid_until must be an RFC3339 timestamp")
		}
		if !untilT.After(time.Now()) {
			return nil, nil, errors.New("valid_until is already in the past: these vouchers could never be redeemed")
		}
		vu = untilT
	}
	if !fromT.IsZero() && !untilT.IsZero() && !untilT.After(fromT) {
		return nil, nil, errors.New("valid_until must be after valid_from")
	}
	return vf, vu, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isVoucherIssuanceRefusal(err error) bool {
	m := err.Error()
	for _, k := range []string{"VOUCHER_NOT_ACCEPTED", "VOUCHER_PACKAGE_INACTIVE", "VOUCHER_REVISION_NOT_CURRENT", "VOUCHER_PACKAGE_UNKNOWN"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// voucherIssuanceRefusal turns the issuance gate's refusal into the operator's sentence.
func voucherIssuanceRefusal(err error) string {
	m := err.Error()
	switch {
	case strings.Contains(m, "VOUCHER_NOT_ACCEPTED"):
		return "This package is not acquirable by Voucher. Edit the package and tick Voucher first."
	case strings.Contains(m, "VOUCHER_PACKAGE_INACTIVE"):
		return "This package is inactive. Vouchers already issued keep working; new ones cannot be issued."
	case strings.Contains(m, "VOUCHER_REVISION_NOT_CURRENT"):
		return "That is not the package's current version. Issue against the current one."
	}
	return "Vouchers cannot be issued for this package."
}

// revokeVoucherBatch ends every UNUSED voucher of one batch, with the operator and reason recorded once in
// iam_v2.voucher_revocations. Redeemed vouchers are untouched: their access is an entitlement.
func (s *server) revokeVoucherBatch(w http.ResponseWriter, r *http.Request) {
	var in voucherActor
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	if err := in.validate(true); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := chi.URLParam(r, "id")
	var n int
	if err := s.db.QueryRow(r.Context(),
		`SELECT iam_v2.voucher_batch_revoke($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5)`,
		s.tenID, s.siteID, id, in.OperatorID, strings.TrimSpace(in.Reason)).Scan(&n); err != nil {
		httpErr(w, http.StatusBadRequest, "batch revoke refused")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"batch_id": id, "revoked": n})
}
