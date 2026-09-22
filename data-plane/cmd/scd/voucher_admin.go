package main

// THE VOUCHER ADMIN SURFACE, IN scd BECAUSE THE DEK IS IN scd.
//
// Issuance already lived here for that reason (voucher_issue_iamv2.go) and had no caller. These are the
// rest of the operations an operator surface needs, and they live here for the same reason: recovering a
// code requires the DEK, and the DEK belongs to the root, unix-socket-only process rather than to the
// unprivileged one that serves HTTP.
//
// EVERY ROUTE HERE TRUSTS ITS CALLER FOR AUTHORIZATION AND FOR NOTHING ELSE. edged has already
// authenticated the operator, checked the resource permission, and -- for reveal and export -- re-verified
// the password and taken a bounded reason. This socket is not reachable from the network. What these
// handlers must NOT do is invent any of that: the operator id and label arrive in the body because edged
// resolved them from the SESSION, and every one of these handlers refuses a body that omits them rather
// than substituting a default. An audit row whose author is optional is not an audit row.
//
// THE ASYMMETRY THAT SHAPES THE REVEAL. The post-stay PIN and the guest-account password are HASHED, so
// "shown once" is enforced by arithmetic. A voucher code is encrypted and RECOVERABLE. Single-use therefore
// cannot be enforced by the crypto and is not claimed to be: what makes a reveal safe is that it cannot
// happen unseen. The audit row and the code recovery are written in ONE transaction, so there is no
// ordering in which a code is produced without a record of it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

// voucherActor is the part of every mutating request that edged resolved from the session.
type voucherActor struct {
	OperatorID    string `json:"operator_id"`
	OperatorLabel string `json:"operator_label"`
	Reason        string `json:"reason"`
}

// validate refuses rather than defaulting. A missing actor is a caller defect, not a guest-facing one.
func (a voucherActor) validate(needReason bool) error {
	if strings.TrimSpace(a.OperatorID) == "" || strings.TrimSpace(a.OperatorLabel) == "" {
		return errors.New("operator_id and operator_label are required: they are resolved by edged from the session")
	}
	if needReason {
		n := len(strings.TrimSpace(a.Reason))
		if n < 4 || n > 500 {
			return errors.New("a bounded reason (4-500 characters) is required")
		}
	}
	return nil
}

type voucherRow struct {
	ID                string  `json:"id"`
	CodeLast4         string  `json:"code_last4"`
	State             string  `json:"state"`
	PackageRevisionID string  `json:"package_revision_id"`
	BatchID           *string `json:"batch_id"`
	CreatedAt         string  `json:"created_at"`
	ValidFrom         *string `json:"redemption_valid_from"`
	ValidUntil        *string `json:"redemption_valid_until"`
	Notes             *string `json:"notes"`
	IssuedBy          *string `json:"issued_by"`
}

// listVouchers answers the operator list. It returns code_last4 and never a code: the list is the screen an
// operator leaves open, and a code on it would be a reveal with no step-up, no reason and no audit row.
func (s *server) listVouchers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			httpErr(w, http.StatusBadRequest, "limit must be between 1 and 500")
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			httpErr(w, http.StatusBadRequest, "offset must not be negative")
			return
		}
		offset = n
	}
	state := strings.ToUpper(strings.TrimSpace(q.Get("state")))
	switch state {
	case "", "UNUSED", "REDEEMED", "REVOKED", "REDEMPTION_EXPIRED":
	default:
		httpErr(w, http.StatusBadRequest,
			"state must be one of UNUSED, REDEEMED, REVOKED, REDEMPTION_EXPIRED")
		return
	}
	batch := strings.TrimSpace(q.Get("batch_id"))

	rows, err := s.db.Query(r.Context(), `
	    SELECT id::text, code_last4, state, package_revision_id::text, batch_id::text,
	           to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           to_char(redemption_valid_from  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           to_char(redemption_valid_until AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           notes, issued_by::text
	      FROM iam_v2.vouchers
	     WHERE tenant_id = $1 AND site_id = $2
	       AND ($3 = '' OR state = $3)
	       AND ($4 = '' OR batch_id::text = $4)
	     ORDER BY created_at DESC, id
	     LIMIT $5 OFFSET $6`,
		s.tenID, s.siteID, state, batch, limit, offset)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "voucher list failed")
		return
	}
	defer rows.Close()
	out := make([]voucherRow, 0, limit)
	for rows.Next() {
		var v voucherRow
		if err := rows.Scan(&v.ID, &v.CodeLast4, &v.State, &v.PackageRevisionID, &v.BatchID,
			&v.CreatedAt, &v.ValidFrom, &v.ValidUntil, &v.Notes, &v.IssuedBy); err != nil {
			httpErr(w, http.StatusInternalServerError, "voucher list failed")
			return
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		httpErr(w, http.StatusInternalServerError, "voucher list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authority": "iam_v2", "vouchers": out})
}

// voucherSummary counts by state. This is what the dashboard tile reads: edged holds no privilege on
// iam_v2.vouchers, which is why its own query could never have worked.
func (s *server) voucherSummary(w http.ResponseWriter, r *http.Request) {
	var unused, redeemed, revoked, expired int64
	err := s.db.QueryRow(r.Context(), `
	    SELECT count(*) FILTER (WHERE state = 'UNUSED'),
	           count(*) FILTER (WHERE state = 'REDEEMED'),
	           count(*) FILTER (WHERE state = 'REVOKED'),
	           count(*) FILTER (WHERE state = 'REDEMPTION_EXPIRED')
	      FROM iam_v2.vouchers WHERE tenant_id = $1 AND site_id = $2`,
		s.tenID, s.siteID).Scan(&unused, &redeemed, &revoked, &expired)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "voucher summary failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authority": "iam_v2",
		"unused":    unused, "redeemed": redeemed, "revoked": revoked, "redemption_expired": expired,
	})
}

// openOneCode recovers one code inside tx and writes the audit row. It is the ONLY place in this process
// that turns ciphertext into a code, and it cannot be called without writing the record.
func (s *server) openOneCode(ctx context.Context, tx pgx.Tx, kr iamv2.VoucherKeyring,
	voucherID string, a voucherActor, action string, count int, selection []byte) (string, error) {
	var genID, keyID string
	var ct, nonce []byte
	// The AAD binds the voucher row id AND its generation, so the generation must come from the ROW rather
	// than from whichever generation happens to be active now. A voucher issued before a rotation opens
	// with the key it was sealed under or not at all.
	err := tx.QueryRow(ctx, `
	    SELECT v.code_key_generation_id::text, g.encryption_key_id::text, v.code_ciphertext, v.code_nonce
	      FROM iam_v2.vouchers v
	      JOIN iam_v2.voucher_code_key_generations g
	        ON g.tenant_id = v.tenant_id AND g.site_id = v.site_id AND g.id = v.code_key_generation_id
	     WHERE v.tenant_id = $1 AND v.site_id = $2 AND v.id = $3`,
		s.tenID, s.siteID, voucherID).Scan(&genID, &keyID, &ct, &nonce)
	if err != nil {
		return "", err
	}
	code, oerr := iamv2.OpenVoucherCode(kr, keyID, s.tenID, s.siteID, voucherID, genID, ct, nonce)
	if oerr != nil {
		return "", oerr
	}
	// Written in the SAME transaction as the recovery. There is no ordering in which a code leaves this
	// function without a row saying who took it and why.
	var vid any = voucherID
	if action == "EXPORT" {
		vid = nil
	}
	if _, err := tx.Exec(ctx, `
	    INSERT INTO iam_v2.voucher_code_reveals
	           (tenant_id, site_id, voucher_id, action, voucher_count, selection,
	            operator_id, operator_label, reason)
	    VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9)`,
		s.tenID, s.siteID, vid, action, count, nullIfEmptyBytes(selection),
		a.OperatorID, a.OperatorLabel, strings.TrimSpace(a.Reason)); err != nil {
		return "", err
	}
	return code, nil
}

func nullIfEmptyBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// revealVoucherCode returns ONE code, audited. edged has already taken the password and the reason.
func (s *server) revealVoucherCode(w http.ResponseWriter, r *http.Request) {
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
	kr, err := s.voucherKeyring()
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "voucher encryption key unavailable")
		return
	}
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "reveal failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sel, _ := json.Marshal(map[string]any{"voucher_id": id})
	code, err := s.openOneCode(ctx, tx, kr, id, in, "REVEAL", 1, sel)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			httpErr(w, http.StatusNotFound, "no such voucher")
			return
		}
		httpErr(w, http.StatusInternalServerError, "reveal failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		// The code was recovered but the record was not written. Refusing to return it is the only honest
		// outcome: an unrecorded reveal is exactly what this table exists to make impossible.
		httpErr(w, http.StatusInternalServerError,
			"reveal refused: the audit record could not be written, so the code is not returned")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"voucher_id": id, "code": code,
		"notice": "This code was recorded as revealed, with your name and your reason. It can be revealed " +
			"again -- unlike a PIN, a voucher code is recoverable -- and every reveal is recorded.",
	})
}

// exportVoucherCodes returns a selection of codes, with ONE audit row naming the count and the selection.
func (s *server) exportVoucherCodes(w http.ResponseWriter, r *http.Request) {
	var in struct {
		voucherActor
		BatchID string `json:"batch_id,omitempty"`
		State   string `json:"state,omitempty"`
	}
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
	// A selection is REQUIRED. "Export everything" is not a selection an operator can be held to, and an
	// unbounded export of every code a property has ever printed is the one request this surface should
	// make somebody think about first.
	if strings.TrimSpace(in.BatchID) == "" {
		httpErr(w, http.StatusBadRequest,
			"batch_id is required: an export names a batch, because 'everything' is not a selection")
		return
	}
	state := strings.ToUpper(strings.TrimSpace(in.State))
	switch state {
	case "", "UNUSED", "REDEEMED", "REVOKED", "REDEMPTION_EXPIRED":
	default:
		httpErr(w, http.StatusBadRequest, "state filter is not a known voucher state")
		return
	}
	kr, err := s.voucherKeyring()
	if err != nil {
		httpErr(w, http.StatusServiceUnavailable, "voucher encryption key unavailable")
		return
	}
	ctx := r.Context()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "export failed")
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ids := []string{}
	rows, err := tx.Query(ctx, `
	    SELECT id::text FROM iam_v2.vouchers
	     WHERE tenant_id = $1 AND site_id = $2 AND batch_id::text = $3
	       AND ($4 = '' OR state = $4)
	     ORDER BY created_at, id`, s.tenID, s.siteID, strings.TrimSpace(in.BatchID), state)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "export failed")
		return
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			httpErr(w, http.StatusInternalServerError, "export failed")
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	if rows.Err() != nil {
		httpErr(w, http.StatusInternalServerError, "export failed")
		return
	}
	if len(ids) == 0 {
		httpErr(w, http.StatusNotFound, "that selection contains no vouchers")
		return
	}

	sel, _ := json.Marshal(map[string]any{"batch_id": strings.TrimSpace(in.BatchID), "state": state})
	// ONE audit row for the whole export, written first, naming the count. Then the codes.
	if _, err := tx.Exec(ctx, `
	    INSERT INTO iam_v2.voucher_code_reveals
	           (tenant_id, site_id, voucher_id, action, voucher_count, selection,
	            operator_id, operator_label, reason)
	    VALUES ($1, $2, NULL, 'EXPORT', $3, $4::jsonb, $5, $6, $7)`,
		s.tenID, s.siteID, len(ids), string(sel),
		in.OperatorID, in.OperatorLabel, strings.TrimSpace(in.Reason)); err != nil {
		httpErr(w, http.StatusInternalServerError, "export failed")
		return
	}
	type exported struct {
		ID   string `json:"id"`
		Code string `json:"code"`
	}
	out := make([]exported, 0, len(ids))
	for _, id := range ids {
		var genID, keyID string
		var ct, nonce []byte
		if err := tx.QueryRow(ctx, `
		    SELECT v.code_key_generation_id::text, g.encryption_key_id::text, v.code_ciphertext, v.code_nonce
		      FROM iam_v2.vouchers v
		      JOIN iam_v2.voucher_code_key_generations g
		        ON g.tenant_id = v.tenant_id AND g.site_id = v.site_id AND g.id = v.code_key_generation_id
		     WHERE v.tenant_id = $1 AND v.site_id = $2 AND v.id = $3`,
			s.tenID, s.siteID, id).Scan(&genID, &keyID, &ct, &nonce); err != nil {
			httpErr(w, http.StatusInternalServerError, "export failed")
			return
		}
		code, oerr := iamv2.OpenVoucherCode(kr, keyID, s.tenID, s.siteID, id, genID, ct, nonce)
		if oerr != nil {
			httpErr(w, http.StatusInternalServerError, "export failed")
			return
		}
		out = append(out, exported{ID: id, Code: code})
	}
	if err := tx.Commit(ctx); err != nil {
		httpErr(w, http.StatusInternalServerError,
			"export refused: the audit record could not be written, so no codes are returned")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"batch_id": strings.TrimSpace(in.BatchID), "count": len(out), "vouchers": out,
		"notice": "This export was recorded with your name, your reason and the size of the selection.",
	})
}

// revokeVoucher ends an UNUSED voucher through the kernel. svc_scd holds no UPDATE on iam_v2.vouchers, by
// the same decision 0084 made about the redemption burn.
func (s *server) revokeVoucher(w http.ResponseWriter, r *http.Request) {
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
	if _, err := s.db.Exec(r.Context(),
		`SELECT iam_v2.voucher_revoke($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5)`,
		s.tenID, s.siteID, id, in.OperatorID, strings.TrimSpace(in.Reason)); err != nil {
		// VOUCHER_NOT_REVOCABLE is a CONFLICT, not a fault: the voucher is already spent, already revoked,
		// or is not this site's. "Nothing happened" and "it was already spent" are different answers.
		if strings.Contains(err.Error(), "VOUCHER_NOT_REVOCABLE") {
			httpErr(w, http.StatusConflict,
				"no UNUSED voucher with that id: a redeemed voucher has already granted access, and ending "+
					"that access is an entitlement action rather than a card action")
			return
		}
		httpErr(w, http.StatusInternalServerError, fmt.Sprintf("revoke failed: %v", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"voucher_id": id, "state": "REVOKED"})
}
