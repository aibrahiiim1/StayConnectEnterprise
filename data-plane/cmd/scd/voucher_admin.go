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
	// EffectiveState is what the card IS right now, as opposed to what the row says. Nothing ever writes
	// REDEMPTION_EXPIRED -- expiry is enforced at sign-in by voucherRedeemable, from the window columns -- so
	// a card past its valid-until stays UNUSED in the table forever. Reading `state` alone, a screen shows
	// "not used yet" and offers to cancel a card that can no longer be redeemed. This is computed from the
	// same [from, until) rule the authenticator applies, at query time, and changes nothing stored.
	EffectiveState string `json:"effective_state"`
}

// THE EFFECTIVE STATE, as SQL, in exactly one place. The window rule is voucherRedeemable's
// (internal/iamv2/repo_pg.go): [from, until), NULL meaning unbounded. REDEMPTION_EXPIRED is still honoured
// if anything ever writes it.
const voucherEffectiveStateSQL = `CASE
	    WHEN state = 'REDEEMED' THEN 'redeemed'
	    WHEN state = 'REVOKED'  THEN 'cancelled'
	    WHEN state = 'REDEMPTION_EXPIRED' THEN 'expired'
	    WHEN redemption_valid_until IS NOT NULL AND redemption_valid_until <= now() THEN 'expired'
	    WHEN redemption_valid_from  IS NOT NULL AND redemption_valid_from  >  now() THEN 'not_yet_valid'
	    ELSE 'available' END`

// voucherListFilterSQL is the WHERE clause the list, its total and the filtered summary share, so the page,
// the count under it and the chip counts cannot disagree about what a filter means. Parameters:
// $1 tenant, $2 site, $3 state, $4 batch_id, $5 effective, $6 last4, $7 package_revision_id.
const voucherListFilterSQL = `
	     WHERE tenant_id = $1 AND site_id = $2
	       AND ($3 = '' OR state = $3)
	       AND ($4 = '' OR batch_id::text = $4)
	       AND ($5 = '' OR (` + voucherEffectiveStateSQL + `) = $5)
	       AND ($6 = '' OR strpos(upper(code_last4), $6) > 0)
	       AND ($7 = '' OR package_revision_id::text = $7)`

// voucherListFilters are the list filters. They are refused rather than ignored when malformed: a filter
// the server silently dropped would present the whole inventory as the filtered one.
type voucherListFilters struct {
	State, Batch, Effective, Last4, Package string
}

func parseVoucherListFilters(q interface{ Get(string) string }) (voucherListFilters, error) {
	f := voucherListFilters{
		State:     strings.ToUpper(strings.TrimSpace(q.Get("state"))),
		Batch:     strings.TrimSpace(q.Get("batch_id")),
		Effective: strings.ToLower(strings.TrimSpace(q.Get("effective"))),
		Last4:     strings.ToUpper(strings.TrimSpace(q.Get("last4"))),
		Package:   strings.TrimSpace(q.Get("package_revision_id")),
	}
	switch f.State {
	case "", "UNUSED", "REDEEMED", "REVOKED", "REDEMPTION_EXPIRED":
	default:
		return f, errors.New("state must be one of UNUSED, REDEEMED, REVOKED, REDEMPTION_EXPIRED")
	}
	switch f.Effective {
	case "", "available", "not_yet_valid", "expired", "redeemed", "cancelled":
	default:
		return f, errors.New("effective must be one of available, not_yet_valid, expired, redeemed, cancelled")
	}
	// The last four characters are the display hint the list already shows. Searching them is matching a card
	// in somebody's hand, not reading a code -- and never more than those four characters.
	if len(f.Last4) > 4 {
		return f, errors.New("last4 searches at most the last four characters of a code")
	}
	for _, c := range f.Last4 {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return f, errors.New("last4 may contain only letters and digits")
		}
	}
	return f, nil
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
	f, ferr := parseVoucherListFilters(q)
	if ferr != nil {
		httpErr(w, http.StatusBadRequest, ferr.Error())
		return
	}

	rows, err := s.db.Query(r.Context(), `
	    SELECT id::text, code_last4, state, package_revision_id::text, batch_id::text,
	           to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           to_char(redemption_valid_from  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           to_char(redemption_valid_until AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           notes, issued_by::text, `+voucherEffectiveStateSQL+`
	      FROM iam_v2.vouchers`+voucherListFilterSQL+`
	     ORDER BY created_at DESC, id
	     LIMIT $8 OFFSET $9`,
		s.tenID, s.siteID, f.State, f.Batch, f.Effective, f.Last4, f.Package, limit, offset)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "voucher list failed")
		return
	}
	defer rows.Close()
	out := make([]voucherRow, 0, limit)
	for rows.Next() {
		var v voucherRow
		if err := rows.Scan(&v.ID, &v.CodeLast4, &v.State, &v.PackageRevisionID, &v.BatchID,
			&v.CreatedAt, &v.ValidFrom, &v.ValidUntil, &v.Notes, &v.IssuedBy, &v.EffectiveState); err != nil {
			httpErr(w, http.StatusInternalServerError, "voucher list failed")
			return
		}
		out = append(out, v)
	}
	if rows.Err() != nil {
		httpErr(w, http.StatusInternalServerError, "voucher list failed")
		return
	}
	rows.Close()
	// THE TOTAL, under the same filter. Without it a list can only say "there may be more", and an operator
	// looking for one card in a 500-card batch cannot tell how far they have to page.
	var total int64
	if err := s.db.QueryRow(r.Context(), `SELECT count(*) FROM iam_v2.vouchers`+voucherListFilterSQL,
		s.tenID, s.siteID, f.State, f.Batch, f.Effective, f.Last4, f.Package).Scan(&total); err != nil {
		httpErr(w, http.StatusInternalServerError, "voucher list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authority": "iam_v2", "vouchers": out, "total": total})
}

// voucherSummary counts by state. This is what the dashboard tile reads: edged holds no privilege on
// iam_v2.vouchers, which is why its own query could never have worked.
//
// The four stored-state counts are unchanged. The effective counts beside them answer what the stored ones
// cannot: how many UNUSED cards can still actually be redeemed, how many have passed their valid-until
// (nothing writes REDEMPTION_EXPIRED, so that stored count stays zero), and how many are not valid yet.
// The batch, last4 and package filters of the list narrow the population counted, so a screen's filter
// counts describe the filtered view rather than the whole property.
func (s *server) voucherSummary(w http.ResponseWriter, r *http.Request) {
	f, ferr := parseVoucherListFilters(r.URL.Query())
	if ferr != nil {
		httpErr(w, http.StatusBadRequest, ferr.Error())
		return
	}
	// A summary answers "how many in each state", so a state filter on it would be a question with one
	// answer. Both state filters are ignored here on purpose.
	f.State, f.Effective = "", ""
	var unused, redeemed, revoked, expired int64
	var available, expiredUnused, notYetValid, total, lastWeek, batches int64
	err := s.db.QueryRow(r.Context(), `
	    SELECT count(*) FILTER (WHERE state = 'UNUSED'),
	           count(*) FILTER (WHERE state = 'REDEEMED'),
	           count(*) FILTER (WHERE state = 'REVOKED'),
	           count(*) FILTER (WHERE state = 'REDEMPTION_EXPIRED'),
	           count(*) FILTER (WHERE (`+voucherEffectiveStateSQL+`) = 'available'),
	           count(*) FILTER (WHERE (`+voucherEffectiveStateSQL+`) = 'expired'),
	           count(*) FILTER (WHERE (`+voucherEffectiveStateSQL+`) = 'not_yet_valid'),
	           count(*),
	           count(*) FILTER (WHERE created_at >= now() - interval '7 days'),
	           count(DISTINCT batch_id)
	      FROM iam_v2.vouchers`+voucherListFilterSQL,
		s.tenID, s.siteID, f.State, f.Batch, f.Effective, f.Last4, f.Package).Scan(
		&unused, &redeemed, &revoked, &expired,
		&available, &expiredUnused, &notYetValid, &total, &lastWeek, &batches)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "voucher summary failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authority": "iam_v2",
		"unused":    unused, "redeemed": redeemed, "revoked": revoked, "redemption_expired": expired,
		"available": available, "expired_unused": expiredUnused, "not_yet_valid": notYetValid,
		"total": total, "issued_last_7_days": lastWeek, "batches": batches,
	})
}

// voucherBatchRow is one print run, aggregated. A batch is not a row of its own under iam_v2 -- it is the
// set of vouchers sharing a batch_id -- so this is derived from the voucher table rather than stored.
type voucherBatchRow struct {
	BatchID           string  `json:"batch_id"`
	PackageRevisionID string  `json:"package_revision_id"`
	Count             int64   `json:"count"`
	Redeemed          int64   `json:"redeemed"`
	Cancelled         int64   `json:"cancelled"`
	Available         int64   `json:"available"`
	Expired           int64   `json:"expired"`
	NotYetValid       int64   `json:"not_yet_valid"`
	Unused            int64   `json:"unused"`
	CreatedAt         string  `json:"created_at"`
	IssuedBy          *string `json:"issued_by"`
	Notes             *string `json:"notes"`
	ValidFrom         *string `json:"redemption_valid_from"`
	ValidUntil        *string `json:"redemption_valid_until"`
}

// listVoucherBatches answers the batch list an export needs.
//
// Before this, the screen derived batches from whichever page of cards it had loaded, so a batch beyond the
// loaded page did not exist on screen, and a batch half on screen was labelled with the half it could see
// while the export pulled all of it. Read-only, no code material, and the same table the list already reads.
func (s *server) listVoucherBatches(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			httpErr(w, http.StatusBadRequest, "limit must be between 1 and 200")
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
	batch := strings.TrimSpace(q.Get("batch_id"))
	rows, err := s.db.Query(r.Context(), `
	    SELECT batch_id::text, min(package_revision_id::text), count(*),
	           count(*) FILTER (WHERE state = 'REDEEMED'),
	           count(*) FILTER (WHERE state = 'REVOKED'),
	           count(*) FILTER (WHERE (`+voucherEffectiveStateSQL+`) = 'available'),
	           count(*) FILTER (WHERE (`+voucherEffectiveStateSQL+`) = 'expired'),
	           count(*) FILTER (WHERE (`+voucherEffectiveStateSQL+`) = 'not_yet_valid'),
	           count(*) FILTER (WHERE state = 'UNUSED'),
	           to_char(min(created_at) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           min(issued_by::text), min(notes),
	           to_char(min(redemption_valid_from)  AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           to_char(max(redemption_valid_until) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
	      FROM iam_v2.vouchers
	     WHERE tenant_id = $1 AND site_id = $2 AND batch_id IS NOT NULL
	       AND ($3 = '' OR batch_id::text = $3)
	     GROUP BY batch_id
	     ORDER BY min(created_at) DESC, batch_id
	     LIMIT $4 OFFSET $5`, s.tenID, s.siteID, batch, limit, offset)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "batch list failed")
		return
	}
	defer rows.Close()
	out := make([]voucherBatchRow, 0, limit)
	for rows.Next() {
		var b voucherBatchRow
		if err := rows.Scan(&b.BatchID, &b.PackageRevisionID, &b.Count, &b.Redeemed, &b.Cancelled,
			&b.Available, &b.Expired, &b.NotYetValid, &b.Unused, &b.CreatedAt, &b.IssuedBy, &b.Notes,
			&b.ValidFrom, &b.ValidUntil); err != nil {
			httpErr(w, http.StatusInternalServerError, "batch list failed")
			return
		}
		out = append(out, b)
	}
	if rows.Err() != nil {
		httpErr(w, http.StatusInternalServerError, "batch list failed")
		return
	}
	rows.Close()
	// Cards printed before batches existed carry no batch_id. They are counted, not hidden: an export names a
	// batch, so those cards cannot be exported, and the screen should be able to say why.
	var total, unbatched int64
	if err := s.db.QueryRow(r.Context(), `
	    SELECT count(DISTINCT batch_id) FILTER (WHERE $3 = '' OR batch_id::text = $3),
	           count(*) FILTER (WHERE batch_id IS NULL)
	      FROM iam_v2.vouchers WHERE tenant_id = $1 AND site_id = $2`,
		s.tenID, s.siteID, batch).Scan(&total, &unbatched); err != nil {
		httpErr(w, http.StatusInternalServerError, "batch list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authority": "iam_v2", "batches": out, "total": total, "unbatched_vouchers": unbatched,
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
	// WHO IS CALLING. This route returns a guest credential in the clear, and the authorization and
	// password step-up that permit that happen in edged -- so a caller that is not edged has not passed
	// them. portald runs as the same user and is network-facing; it must not reach this.
	if !requireEdgedPeer(w, r) {
		return
	}
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
	// WHO IS CALLING. This route returns a guest credential in the clear, and the authorization and
	// password step-up that permit that happen in edged -- so a caller that is not edged has not passed
	// them. portald runs as the same user and is network-facing; it must not reach this.
	if !requireEdgedPeer(w, r) {
		return
	}
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

// ----- key-generation rotation -----------------------------------------------------------------------

// listVoucherKeyGenerations answers which code key generations exist and which one is active.
//
// No key material leaves this handler. hmac_key_ciphertext is the sealed blind-index key and the sealed
// form is no more publishable than the clear one; what an operator needs is the generation number, when it
// was created, whether it is active, and who retired it.
func (s *server) listVoucherKeyGenerations(w http.ResponseWriter, r *http.Request) {
	rows, err := s.db.Query(r.Context(), `
	    SELECT g.id::text, g.generation_no,
	           to_char(g.superseded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           g.supersede_reason,
	           (SELECT count(*) FROM iam_v2.vouchers v
	             WHERE v.tenant_id = g.tenant_id AND v.site_id = g.site_id
	               AND v.code_key_generation_id = g.id)                          AS vouchers,
	           (SELECT count(*) FROM iam_v2.vouchers v
	             WHERE v.tenant_id = g.tenant_id AND v.site_id = g.site_id
	               AND v.code_key_generation_id = g.id AND v.state = 'UNUSED')   AS unused
	      FROM iam_v2.voucher_code_key_generations g
	     WHERE g.tenant_id = $1 AND g.site_id = $2
	     ORDER BY g.generation_no DESC`, s.tenID, s.siteID)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "generation list failed")
		return
	}
	defer rows.Close()
	type gen struct {
		ID           string  `json:"id"`
		GenerationNo int     `json:"generation_no"`
		SupersededAt *string `json:"superseded_at"`
		Reason       *string `json:"supersede_reason"`
		Vouchers     int64   `json:"vouchers"`
		Unused       int64   `json:"unused_vouchers"`
		Active       bool    `json:"active"`
	}
	out := []gen{}
	for rows.Next() {
		var g gen
		if err := rows.Scan(&g.ID, &g.GenerationNo, &g.SupersededAt, &g.Reason, &g.Vouchers, &g.Unused); err != nil {
			httpErr(w, http.StatusInternalServerError, "generation list failed")
			return
		}
		g.Active = g.SupersededAt == nil
		out = append(out, g)
	}
	writeJSON(w, http.StatusOK, map[string]any{"generations": out})
}

// rotateVoucherKeyGeneration retires one generation. The NEXT issuance mints its successor.
//
// It does not create the replacement itself, and that is deliberate rather than lazy:
// ensureVoucherKeyGeneration already mints max(generation_no)+1 when a site has no active generation, and
// it does so inside the issuance path where the DEK is already open and the keyring already loaded. Minting
// here as well would be a second place that creates key material, which is one more than a system should
// have.
func (s *server) rotateVoucherKeyGeneration(w http.ResponseWriter, r *http.Request) {
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
	var genNo int
	if err := s.db.QueryRow(r.Context(),
		`SELECT iam_v2.voucher_code_generation_supersede($1::uuid,$2::uuid,$3::uuid,$4::uuid,$5)`,
		s.tenID, s.siteID, id, in.OperatorID, strings.TrimSpace(in.Reason)).Scan(&genNo); err != nil {
		if strings.Contains(err.Error(), "VOUCHER_GENERATION_NOT_ACTIVE") {
			httpErr(w, http.StatusConflict,
				"no ACTIVE code key generation with that id: it may already have been retired")
			return
		}
		httpErr(w, http.StatusInternalServerError, "rotation failed")
		return
	}
	// The authenticator's cached key set no longer describes this site.
	invalidateVoucherKeyCache(s.tenID, s.siteID)
	writeJSON(w, http.StatusOK, map[string]any{
		"generation_no": genNo, "state": "SUPERSEDED",
		"notice": "Codes already printed under this generation remain redeemable -- each voucher pins the " +
			"generation that indexed it. The next batch you print will mint a new generation.",
	})
}
