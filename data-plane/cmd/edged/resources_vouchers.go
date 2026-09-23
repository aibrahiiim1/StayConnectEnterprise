package main

// THE VOUCHER OPERATOR SURFACE, as Hotel Admin reaches it.
//
// Every code-touching operation PROXIES to scd, and that is not an accident of where the route terminates.
// scd owns the voucher DEK: it runs as root on a unix socket, edged runs as the unprivileged `stayconnect`
// user and serves HTTP. `svc_edged` holds no privilege at all on iam_v2.vouchers, deliberately, and this
// file does not ask for one. The only voucher material edged reads directly is the REVEAL AUDIT -- because
// "who has already taken a copy of these cards" is the question that audit exists to answer, and hiding it
// from the screen that performs a reveal would make the record ceremonial.
//
// THREE PERMISSION KEYS, BECAUSE THESE ARE THREE DIFFERENT POWERS:
//
//	vouchers               issue, list, revoke. The daily job: printing cards and cancelling a lost batch.
//	voucher-codes          reveal and export -- recovering a guest secret IN THE CLEAR. The narrowest power
//	                       here, and the only one that additionally requires a password step-up and a
//	                       bounded reason at the route.
//	voucher-code-settings  the 0085 code format. A property configuration decision, which is why it sits
//	                       with the same role that owns auth-methods rather than with the desk.
//
// One key for all three would mean that whoever may print a card may also read every code a property has
// ever issued, and that whoever may set the format must be able to read codes to do it. Neither follows.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// ----- the daily surface: issue, list, revoke -------------------------------------------------------

func (s *server) vouchersRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listVouchers)
	r.Get("/summary", s.voucherSummary)
	// WHAT A BATCH CAN GRANT. Without this the screen would have to ask an operator to paste the UUID of a
	// package revision -- a value that appears nowhere they can see, because package authoring lives behind
	// the Phase-2 commerce admin flag and this surface is deliberately not behind it. A form that demands
	// an identifier the product never shows is a form nobody can complete, which is its own kind of
	// unreachable feature.
	r.Get("/grantable", s.listGrantablePackageRevisions)
	r.Post("/issue", s.issueVouchers)
	r.Post("/{id}/revoke", s.revokeVoucher)
	return r
}

// listGrantablePackageRevisions lists the package revisions a batch can be printed against.
//
// edged reads this itself: svc_edged already holds SELECT on iam_v2.internet_packages and
// internet_package_revisions (Phase-2 commerce grants), and a package revision is not voucher material --
// no key, no code, nothing sealed. Proxying it to scd would have been ceremony.
//
// PUBLISHED REVISIONS ONLY, meaning the one each package currently points at. An arbitrary historical
// revision is still a legal target for the issuance path -- a voucher pins whatever revision it was issued
// against, on purpose, so republishing cannot retroactively change what a printed card is worth -- but
// offering a list of superseded revisions to choose from would invite printing cards against one by
// accident.
//
// AND ACTIVE PACKAGES ONLY. `active` is the operator's own deactivation switch, and a first version of this
// query ignored it: a package retired months ago still published a current revision, so it stayed in the
// dropdown and an operator could print a hundred cards against something the property had deliberately
// withdrawn. "Has a current revision" is a fact about history; "is active" is the fact an operator changed
// on purpose, and this list has to represent the second.
//
// AND NOT THE SYSTEM PACKAGES. Read off PRE-LIVE, where this list returned five packages and two of them
// were `__system_checkout_grace` and `__sys_emergency_grace_pkg__` -- is_system=true, active=true, both
// with a published revision, both therefore offered to an operator as something to print guest cards
// against. They are internal mechanisms: the grace a departing guest gets and the emergency grant, granted
// by the engine on its own initiative. A voucher printed against one would be a card that hands out an
// internal grace allocation, and the operator choosing it from a dropdown has no way to know that.
func (s *server) listGrantablePackageRevisions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
	    SELECT r.id::text, p.code, r.revision_no, r.package_type,
	           COALESCE(r.display->>'name', p.code) AS name,
	           r.price_minor, COALESCE(r.currency, '') AS currency
	      FROM iam_v2.internet_packages p
	      JOIN iam_v2.internet_package_revisions r
	        ON r.tenant_id = p.tenant_id AND r.site_id = p.site_id AND r.id = p.current_revision_id
	     WHERE p.tenant_id = $1 AND p.site_id = $2 AND p.active AND NOT p.is_system
	     ORDER BY p.code`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the package list could not be read")
		return
	}
	defer rows.Close()
	type rev struct {
		ID          string `json:"id"`
		PackageCode string `json:"package_code"`
		RevisionNo  int    `json:"revision_no"`
		PackageType string `json:"package_type"`
		Name        string `json:"name"`
		PriceMinor  int64  `json:"price_minor"`
		Currency    string `json:"currency"`
	}
	out := []rev{}
	for rows.Next() {
		var v rev
		if err := rows.Scan(&v.ID, &v.PackageCode, &v.RevisionNo, &v.PackageType, &v.Name,
			&v.PriceMinor, &v.Currency); err != nil {
			jsonErr(w, http.StatusInternalServerError, "query_failed", "the package list could not be read")
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"revisions": out})
}

func (s *server) listVouchers(w http.ResponseWriter, r *http.Request) {
	s.scd.proxy(w, r, http.MethodGet, "/v1/vouchers?"+r.URL.RawQuery, nil)
}

func (s *server) voucherSummary(w http.ResponseWriter, r *http.Request) {
	s.scd.proxy(w, r, http.MethodGet, "/v1/vouchers/summary", nil)
}

// issueVouchers mints a batch. NOT a step-up action: it creates a secret rather than revealing one, and the
// codes it returns have never existed before this request. The operator id still comes from the SESSION,
// because "who printed these" is a fact about the server's knowledge, not a field a client may choose.
func (s *server) issueVouchers(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PackageRevisionID string  `json:"package_revision_id"`
		Count             int     `json:"count"`
		Note              string  `json:"note,omitempty"`
		ValidFrom         *string `json:"valid_from,omitempty"`
		ValidUntil        *string `json:"valid_until,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	// Validated HERE as well as in scd, because a 400 an operator can read beats a 400 relayed from a
	// socket, and because the window rule is a product rule rather than a storage rule.
	if in.ValidFrom != nil || in.ValidUntil != nil {
		if err := validRedemptionWindow(in.ValidFrom, in.ValidUntil); err != nil {
			jsonErr(w, http.StatusBadRequest, "invalid_window", err.Error())
			return
		}
	}
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "no operator session")
		return
	}
	body := map[string]any{
		"package_revision_id": in.PackageRevisionID,
		"count":               in.Count,
		"issued_by":           sess.OperatorID,
	}
	if in.Note != "" {
		body["note"] = in.Note
	}
	if in.ValidFrom != nil {
		body["valid_from"] = *in.ValidFrom
	}
	if in.ValidUntil != nil {
		body["valid_until"] = *in.ValidUntil
	}
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/vouchers/issue", body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st == http.StatusCreated {
		// COUNT AND REVISION ONLY, NEVER A CODE -- not even a last4 in bulk. A batch log line is not the
		// audited reveal action, and an audit payload outlives the one-time response it would be copying.
		payload := map[string]any{"count": in.Count, "package_revision_id": in.PackageRevisionID}
		var out struct {
			BatchID string `json:"batch_id"`
		}
		if json.Unmarshal(resp, &out) == nil && out.BatchID != "" {
			payload["batch_id"] = out.BatchID
		}
		s.audit(r, "voucher.issued", "voucher_batch", "", payload)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// revokeVoucher cancels an UNUSED card.
//
// It sits under `vouchers` rather than under `voucher-codes` because cancelling a card is not reading a
// secret -- but it is destructive, so it carries the step-up and the bounded reason anyway. A batch is
// cancelled because it was lost or stolen, and "who cancelled two hundred cards, and why" is a question
// somebody will ask.
func (s *server) revokeVoucher(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason (4-500 characters) is required")
		return
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return
	}
	sess := sessFrom(r.Context())
	label := actor
	if sess != nil && sess.Email != "" {
		label = sess.Email
	}
	id := chi.URLParam(r, "id")
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/vouchers/"+id+"/revoke",
		map[string]any{"operator_id": actor, "operator_label": label, "reason": in.Reason})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st == http.StatusOK {
		s.audit(r, "voucher.revoked", "voucher", id, map[string]any{"reason": in.Reason})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// validRedemptionWindow bounds the optional validity window.
//
// The authenticator already enforces [from, until) with NULL meaning unbounded
// (internal/iamv2/repo_pg.go: voucherRedeemable), so this writes columns that already work. What it must
// refuse is a window that can never open: an operator who prints two hundred cards that expired yesterday
// finds out from a guest.
func validRedemptionWindow(from, until *string) error {
	var f, u time.Time
	var err error
	if from != nil && *from != "" {
		if f, err = time.Parse(time.RFC3339, *from); err != nil {
			return errors.New("valid_from must be an RFC3339 timestamp")
		}
	}
	if until != nil && *until != "" {
		if u, err = time.Parse(time.RFC3339, *until); err != nil {
			return errors.New("valid_until must be an RFC3339 timestamp")
		}
		if !u.After(time.Now()) {
			return errors.New("valid_until is already in the past: these vouchers could never be redeemed")
		}
	}
	if !f.IsZero() && !u.IsZero() && !u.After(f) {
		return errors.New("valid_until must be after valid_from")
	}
	return nil
}

// ----- the confidentiality surface: reveal and export ------------------------------------------------

func (s *server) voucherCodesRoutes() http.Handler {
	r := chi.NewRouter()
	// The audit of itself. Read-only, and the only voucher table edged touches directly.
	r.Get("/reveals", s.listVoucherReveals)
	r.Post("/{id}/reveal", s.revealVoucherCode)
	r.Post("/export", s.exportVoucherCodes)
	return r
}

type voucherCodeActionReq struct {
	// Password is the step-up. There is deliberately no actor field: an audit record whose author comes
	// from the request body is not an audit record.
	Password string `json:"password"`
	Reason   string `json:"reason"`
	// Export only.
	BatchID string `json:"batch_id,omitempty"`
	State   string `json:"state,omitempty"`
}

// revealVoucherCode recovers ONE code. Step-up, bounded reason, and an append-only row written by scd in
// the same transaction as the recovery.
func (s *server) revealVoucherCode(w http.ResponseWriter, r *http.Request) {
	in, actor, label, ok := s.voucherCodeStepUp(w, r)
	if !ok {
		return
	}
	id := chi.URLParam(r, "id")
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/vouchers/"+id+"/reveal",
		map[string]any{"operator_id": actor, "operator_label": label, "reason": in.Reason})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st == http.StatusOK {
		// The reason, never the code. scd has already written the authoritative append-only row; this is
		// the operator audit log the same action appears in, for the same reason every other mutation does.
		s.audit(r, "voucher.code_revealed", "voucher", id, map[string]any{"reason": in.Reason})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// exportVoucherCodes recovers a whole batch, under the same gate, with one audit row for the export.
func (s *server) exportVoucherCodes(w http.ResponseWriter, r *http.Request) {
	in, actor, label, ok := s.voucherCodeStepUp(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(in.BatchID) == "" {
		jsonErr(w, http.StatusBadRequest, "batch_required",
			"an export names a batch: 'everything' is not a selection an operator can be held to")
		return
	}
	st, resp, err := s.scd.call(r.Context(), http.MethodPost, "/v1/vouchers/export", map[string]any{
		"operator_id": actor, "operator_label": label, "reason": in.Reason,
		"batch_id": strings.TrimSpace(in.BatchID), "state": in.State,
	})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st == http.StatusOK {
		var out struct {
			Count int `json:"count"`
		}
		_ = json.Unmarshal(resp, &out)
		s.audit(r, "voucher.codes_exported", "voucher_batch", strings.TrimSpace(in.BatchID),
			map[string]any{"reason": in.Reason, "count": out.Count, "state": in.State})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// voucherCodeStepUp is the gate both code-recovering routes pass through: a bounded reason, then the
// password re-verified against the SESSION's operator, then the operator's own label for the record.
func (s *server) voucherCodeStepUp(w http.ResponseWriter, r *http.Request) (voucherCodeActionReq, string, string, bool) {
	var in voucherCodeActionReq
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return in, "", "", false
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason (4-500 characters) is required: an unexplained reveal is indistinguishable "+
				"afterwards from an unauthorized one")
		return in, "", "", false
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return in, "", "", false
	}
	sess := sessFrom(r.Context())
	label := actor
	if sess != nil {
		if sess.Email != "" {
			label = sess.Email
		} else if sess.DisplayName != "" {
			label = sess.DisplayName
		}
	}
	return in, actor, label, true
}

// listVoucherReveals reads the append-only audit. edged holds SELECT on this one table and nothing else in
// the voucher domain.
func (s *server) listVoucherReveals(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
	    SELECT to_char(revealed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           action, voucher_id::text, voucher_count, operator_label, reason, selection::text
	      FROM iam_v2.voucher_code_reveals
	     WHERE tenant_id = $1 AND site_id = $2
	     ORDER BY revealed_at DESC
	     LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "reveal history unavailable")
		return
	}
	defer rows.Close()
	type reveal struct {
		At        string  `json:"revealed_at"`
		Action    string  `json:"action"`
		VoucherID *string `json:"voucher_id"`
		Count     int     `json:"voucher_count"`
		Operator  string  `json:"operator_label"`
		Reason    string  `json:"reason"`
		Selection *string `json:"selection"`
	}
	out := []reveal{}
	for rows.Next() {
		var v reveal
		if err := rows.Scan(&v.At, &v.Action, &v.VoucherID, &v.Count, &v.Operator, &v.Reason, &v.Selection); err != nil {
			jsonErr(w, http.StatusInternalServerError, "query_failed", "reveal history unavailable")
			return
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"reveals": out})
}

// ----- the format setting (0085). edged does this one itself. ----------------------------------------

func (s *server) voucherCodeSettingsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getVoucherCodeSettings)
	r.Put("/", s.setVoucherCodeSettings)
	r.Get("/changes", s.listVoucherCodeSettingChanges)
	// KEY ROTATION SITS HERE, not under `vouchers` and not under `voucher-codes`.
	//
	// It is key-lifecycle management, so it belongs to the role that already owns what a code looks like --
	// the same reasoning that puts the format itself here. It is deliberately NOT under `voucher-codes`:
	// retiring a generation reads no code and reveals nothing, and a desk role that may read one card's
	// code has no business retiring the key that indexes every card in the building.
	r.Get("/key-generations", s.listVoucherKeyGenerations)
	r.Post("/key-generations/{id}/supersede", s.rotateVoucherKeyGeneration)
	return r
}

// listVoucherKeyGenerations proxies to scd, which owns the generations table. No key material is returned
// by that route -- the sealed blind-index key is no more publishable than the clear one.
func (s *server) listVoucherKeyGenerations(w http.ResponseWriter, r *http.Request) {
	s.scd.proxy(w, r, http.MethodGet, "/v1/voucher-key-generations", nil)
}

// rotateVoucherKeyGeneration retires a generation under a password step-up and a bounded reason.
//
// Step-up because of what it is, not because of what it reads: rotation is a key-lifecycle action on the
// material that indexes every voucher this property has ever issued, and "who retired generation 3, and
// why" is a question somebody will ask. Codes already printed keep working -- each voucher pins the
// generation that indexed it -- which is exactly why this is safe to offer at all.
func (s *server) rotateVoucherKeyGeneration(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Reason   string `json:"reason"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required",
			"a bounded reason (4-500 characters) is required")
		return
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return
	}
	sess := sessFrom(r.Context())
	label := actor
	if sess != nil && sess.Email != "" {
		label = sess.Email
	}
	id := chi.URLParam(r, "id")
	st, resp, err := s.scd.call(r.Context(), http.MethodPost,
		"/v1/voucher-key-generations/"+id+"/supersede",
		map[string]any{"operator_id": actor, "operator_label": label, "reason": in.Reason})
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "scd_unreachable", err.Error())
		return
	}
	if st == http.StatusOK {
		s.audit(r, "voucher.code_key_rotated", "voucher_code_key_generation", id,
			map[string]any{"reason": in.Reason})
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(resp)
}

// getVoucherCodeSettings answers what a code looks like at this property. It always answers: a site with no
// saved row gets the column defaults with config_version 0, which is how "nobody has chosen" is
// distinguishable from "somebody chose exactly these".
func (s *server) getVoucherCodeSettings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	var mode string
	var length int
	var version int64
	var updated *time.Time
	if err := s.db.QueryRow(ctx,
		`SELECT code_mode, code_length, config_version, updated_at
		   FROM iam_v2.voucher_code_settings_get($1::uuid, $2::uuid)`,
		s.tenantID, s.siteID).Scan(&mode, &length, &version, &updated); err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the voucher code format could not be read")
		return
	}
	out := map[string]any{"code_mode": mode, "code_length": length, "config_version": version}
	if updated != nil {
		out["updated_at"] = updated.UTC().Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, out)
}

// setVoucherCodeSettings stores the format. The bounds are checked here, in the setter function and by the
// table CHECK -- three places, because the first two stop a bad value being STORED and the third stops one
// that was hand-edited in being USED.
func (s *server) setVoucherCodeSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode   string `json:"code_mode"`
		Length int    `json:"code_length"`
		Reason string `json:"reason,omitempty"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	if in.Mode != "numbers" && in.Mode != "mixed" {
		jsonErr(w, http.StatusBadRequest, "invalid_mode",
			"code_mode must be numbers (digits only) or mixed (digits and letters)")
		return
	}
	// The Product-Owner requirement: not more than eight characters, for either mode.
	if in.Length < 6 || in.Length > 8 {
		jsonErr(w, http.StatusBadRequest, "invalid_length",
			"code_length must be between 6 and 8 characters")
		return
	}
	// The operator label comes from the SESSION. The setter function refuses an empty one -- "somebody
	// changed it" is not an audit record -- and this is where the name it records comes from.
	sess := sessFrom(r.Context())
	if sess == nil || sess.OperatorID == "" {
		jsonErr(w, http.StatusUnauthorized, "unauthorized", "no operator session")
		return
	}
	label := sess.Email
	if label == "" {
		label = sess.DisplayName
	}
	if label == "" {
		label = sess.OperatorID
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var version int64
	if err := s.db.QueryRow(ctx,
		`SELECT iam_v2.voucher_code_settings_set($1::uuid, $2::uuid, $3, $4, $5, $6)`,
		s.tenantID, s.siteID, in.Mode, in.Length, label, nullIfBlank(in.Reason)).Scan(&version); err != nil {
		jsonErr(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	s.audit(r, "voucher.code_format_changed", "voucher_code_settings", "", map[string]any{
		"code_mode": in.Mode, "code_length": in.Length, "config_version": version, "reason": in.Reason,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"code_mode": in.Mode, "code_length": in.Length, "config_version": version,
		// Said here because the screen must say it: there is no restart, rebuild or deployment involved,
		// and no already-printed card changes.
		"notice": "This takes effect on the next batch you print. Codes already issued keep the format they " +
			"were printed with and remain redeemable.",
	})
}

// listVoucherCodeSettingChanges reads the append-only history of the format.
func (s *server) listVoucherCodeSettingChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
	    SELECT to_char(changed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
	           changed_by, change_reason, old_code_mode, old_code_length,
	           new_code_mode, new_code_length, new_config_version
	      FROM iam_v2.voucher_code_settings_changes
	     WHERE tenant_id = $1 AND site_id = $2
	     ORDER BY changed_at DESC LIMIT 100`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the format history could not be read")
		return
	}
	defer rows.Close()
	type change struct {
		At        string  `json:"changed_at"`
		By        string  `json:"changed_by"`
		Reason    *string `json:"change_reason"`
		OldMode   *string `json:"old_code_mode"`
		OldLength *int    `json:"old_code_length"`
		NewMode   string  `json:"new_code_mode"`
		NewLength int     `json:"new_code_length"`
		Version   int64   `json:"new_config_version"`
	}
	out := []change{}
	for rows.Next() {
		var c change
		if err := rows.Scan(&c.At, &c.By, &c.Reason, &c.OldMode, &c.OldLength,
			&c.NewMode, &c.NewLength, &c.Version); err != nil {
			jsonErr(w, http.StatusInternalServerError, "query_failed", "the format history could not be read")
			return
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": out})
}

func nullIfBlank(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.TrimSpace(v)
}
