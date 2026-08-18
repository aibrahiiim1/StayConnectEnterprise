package main

// IAM-v2 VOUCHER ISSUANCE. Lives in scd, not edged, and that placement is the point.
//
// The voucher DEK is the root of voucher confidentiality: everything else in the database is sealed under
// it, so a database compromise alone yields no code. edged runs as the unprivileged `stayconnect` user and
// serves HTTP; scd runs as root and listens only on a protected unix socket. Putting issuance in edged
// would have meant handing that DEK to the more exposed of the two processes, purely because that is where
// the admin route happens to terminate. So the route stays in edged and PROXIES here -- the same
// arrangement already used for the license store, which scd also owns.
//
// Nothing in the runtime could issue an IAM-v2 voucher: iam_v2.vouchers and iam_v2.voucher_code_key_generations
// were both empty, and the only INSERTs into either in the whole repository were test fixtures. VOUCHER was
// therefore enabled by flag, correctly refusing every login, and structurally incapable of ever succeeding.
//
// The accepted model (FINAL contract key hierarchy + the Phase-1B voucher design) is implemented here as it
// is written, not approximated:
//
//   appliance secret store -> DEK (AES-256-GCM, named by encryption_key_id)
//                          -> per-generation voucher code key, stored ONLY as hmac_key_ciphertext
//                          -> per-voucher blind index (code_hmac) + recoverable ciphertext (code_ciphertext,
//                             code_nonce) + display hint (code_last4), pinned to code_key_generation_id
//
// Key separation is real: the DEK encrypts and never indexes; the per-generation HMAC key indexes and never
// encrypts, and is never persisted in the clear. Rotation is expressible because vouchers pin the generation
// that indexed them, so superseding a generation leaves unredeemed vouchers usable -- which a single raw key
// could not express at all.
//
// The plaintext code exists only in memory and is returned to the operator ONCE, in the issuance response.
// It is never logged, never stored in a column, and never placed in an audit payload. Recovering it later is
// the separate reveal/export action the contract puts behind operator re-authentication and audit; this file
// deliberately does not expose it as a side effect of issuing.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"path/filepath"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
)

const (
	voucherDEKFile = "voucher_dek.key"
	// encryption_key_id is a uuid column, so the DEK id is a fixed UUID rather than a readable label.
	// Stable by contract: changing it orphans every generation sealed under the old id, and every voucher
	// pinned to those generations becomes unrecoverable -- which the design says is reissue, not repair.
	voucherDEKID = "0a5c1e00-0000-4000-8000-0000000000d1"
)

// voucherAlphabet excludes I, L, O, 0, 1 and U: the characters guests misread aloud or mistype from a
// printed card. It is the same readability rule the legacy voucher generator applies.
const voucherAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

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

func randomVoucherCode(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(voucherAlphabet)))
	for i := range out {
		k, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		out[i] = voucherAlphabet[k.Int64()]
	}
	return string(out), nil
}

// ensureVoucherKeyGeneration returns the active generation and its clear HMAC key, creating generation 1 if
// the site has none. It never overwrites or reuses an existing generation's key: doing so would orphan every
// voucher already indexed under it.
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

	// No usable generation yet: mint generation 1.
	genNo = 1
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
	return genID, genNo, clear, nil
}

// issueVouchersIAMv2 mints N vouchers against a pinned package revision.
//
// The package revision is REQUIRED and is pinned on the voucher row: what a voucher grants is fixed at
// issuance by an immutable revision, so republishing a package later cannot retroactively change what an
// already-printed card is worth.
func (s *server) issueVouchersIAMv2(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PackageRevisionID string `json:"package_revision_id"`
		Count             int    `json:"count"`
		Length            int    `json:"length,omitempty"`
		Note              string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
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
	if in.Length == 0 {
		in.Length = 8
	}
	if in.Length < 6 || in.Length > 24 {
		httpErr(w, http.StatusBadRequest, "length must be between 6 and 24")
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

	codes := make([]string, 0, in.Count)
	for i := 0; i < in.Count; i++ {
		code, cerr := randomVoucherCode(in.Length)
		if cerr != nil {
			httpErr(w, http.StatusInternalServerError, "code generation failed")
			return
		}
		// The row id is generated up front because the code ciphertext's AAD binds it: the ciphertext
		// cannot be moved to another voucher row and still open.
		var vid string
		if err := s.db.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&vid); err != nil {
			httpErr(w, http.StatusInternalServerError, "id allocation failed")
			return
		}
		ct, nonce, serr := iamv2.SealVoucherCode(kr, voucherDEKID, s.tenID, s.siteID, vid, genID, code)
		if serr != nil {
			httpErr(w, http.StatusInternalServerError, "code seal failed")
			return
		}
		idx := iamv2.VoucherCodeHMAC(hmacKey, s.tenID, s.siteID, code)
		if _, err := s.db.Exec(ctx, `
		    INSERT INTO iam_v2.vouchers
		           (id, tenant_id, site_id, package_revision_id, code_hmac, code_ciphertext, code_nonce,
		            code_key_generation_id, code_last4, notes)
		    VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			vid, s.tenID, s.siteID, in.PackageRevisionID, idx, ct, nonce, genID,
			iamv2.Last4(code), nullIfEmpty(in.Note)); err != nil {
			httpErr(w, http.StatusInternalServerError, "voucher insert failed")
			return
		}
		codes = append(codes, code)
	}
	// The audit records how many were issued against which revision -- NEVER a code, not even its last4 in
	// bulk, because a batch audit line is not the audited reveal action.
	// Count and revision only -- never a code, not even in bulk: a batch log line is not the audited reveal.
	slog.Info("iamv2 vouchers issued", "count", in.Count,
		"package_revision_id", in.PackageRevisionID, "generation", genID)
	// ONE-TIME return of the plaintext codes. This is the only moment they exist outside memory.
	writeJSON(w, http.StatusCreated, map[string]any{
		"authority":           "iam_v2",
		"count":               len(codes),
		"package_revision_id": in.PackageRevisionID,
		"codes":               codes,
	})
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
