package main

// Staff End Package / Reset Package (migration 0202). The database functions do the work under the stay and entitlement locks and
// are idempotent per request id; this authorises (package-controls WRITE), takes the password step-up and the bounded reason, writes
// the audit row, and for an End withdraws each ended session's kernel authorisation at once (scd /v1/admin/sessions/withdraw).
// A reset never moves money: it is a zero-price ADMIN_GRANT purchase, and no payment, posting, refund or redemption path is called.

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *server) packageControlsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSubjectPackages)
	r.Get("/{id}", s.packageControlPreview)
	r.Post("/{id}/end", s.endPackage)
	r.Post("/{id}/reset", s.resetPackage)
	return r
}

// packageSubjectKinds are the views that carry the actions: the query parameter names the subject, never a room number.
var packageSubjectKinds = []string{"stay", "account", "voucher", "principal", "session", "device", "entitlement"}

func (s *server) listSubjectPackages(w http.ResponseWriter, r *http.Request) {
	kind, id := "", ""
	for _, k := range packageSubjectKinds {
		if v := strings.TrimSpace(r.URL.Query().Get(k)); v != "" {
			kind, id = k, v
			break
		}
	}
	if kind == "" || !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid", "name one stay, account, voucher, principal, session, device or entitlement by id")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var doc []byte
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.package_control_list($1,$2,$3,$4::uuid)::text`,
		s.tenantID, s.siteID, kind, id).Scan(&doc); err != nil {
		slog.Error("package controls: list unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the packages could not be read")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": json.RawMessage(doc)})
}

func (s *server) packageControlPreview(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "package not found")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var preview, history []byte
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.package_control_preview($1,$2,$3::uuid)::text,
		iam_v2.package_admin_history($1,$2,$3::uuid)::text`, s.tenantID, s.siteID, id).Scan(&preview, &history); err != nil {
		slog.Error("package controls: preview unreadable", "err", err)
		jsonErr(w, http.StatusInternalServerError, "query_failed", "the package could not be read")
		return
	}
	if len(preview) == 0 || string(preview) == "null" {
		jsonErr(w, http.StatusNotFound, "not_found", "package not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"package": json.RawMessage(preview), "history": json.RawMessage(history)})
}

type packageActionReq struct {
	// Password is the step-up; there is no actor field: the operator comes from the session.
	Password string `json:"password"`
	Reason   string `json:"reason"`
	// RequestID is generated once per confirmation dialog: a retried or double-clicked request is the same request.
	RequestID string `json:"request_id"`
}

func (s *server) readPackageAction(w http.ResponseWriter, r *http.Request) (string, packageActionReq, string, bool) {
	id := chi.URLParam(r, "id")
	var in packageActionReq
	if !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusNotFound, "not_found", "package not found")
		return "", in, "", false
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return "", in, "", false
	}
	if !uuidRe.MatchString(in.RequestID) {
		jsonErr(w, http.StatusBadRequest, "invalid", "a request id is required")
		return "", in, "", false
	}
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return "", in, "", false
	}
	actor, ok := s.stepUpActor(w, r, in.Password)
	if !ok {
		return "", in, "", false
	}
	return id, in, actor, true
}

func (s *server) endPackage(w http.ResponseWriter, r *http.Request) {
	id, in, actor, ok := s.readPackageAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var actionID, outcome string
	var sessions []string
	var devices int
	err := s.db.QueryRow(ctx, `SELECT action_id::text, outcome, sessions::text[], devices
		FROM iam_v2.admin_end_package($1,$2,$3::uuid,$4::uuid,$5,$6::uuid)`,
		s.tenantID, s.siteID, id, actor, in.Reason, in.RequestID).Scan(&actionID, &outcome, &sessions, &devices)
	if err != nil {
		packageOpErr(w, err)
		return
	}
	// The entitlement is ended in the database whatever happens next; the kernel withdrawal is reported exactly as it went.
	kernel := s.withdrawKernel(r, sessions)
	s.audit(r, "package.ended", "entitlement", id, map[string]any{
		"reason": in.Reason, "action_id": actionID, "outcome": outcome, "sessions_ended": len(sessions),
		"devices_disconnected": devices, "kernel": kernel["summary"],
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"action_id": actionID, "outcome": outcome, "sessions_ended": len(sessions), "devices_disconnected": devices,
		"kernel": kernel,
	})
}

// withdrawKernel asks scd to withdraw the ended sessions' addresses and summarises what it actually confirmed.
func (s *server) withdrawKernel(r *http.Request, sessions []string) map[string]any {
	out := map[string]any{"requested": len(sessions), "withdrawn": 0, "summary": "nothing to withdraw"}
	if len(sessions) == 0 {
		return out
	}
	code, raw, err := s.scd.call(r.Context(), http.MethodPost, "/v1/admin/sessions/withdraw", map[string]any{"sessions": sessions})
	if err != nil || code != http.StatusOK {
		out["summary"] = "not confirmed: the enforcement service could not be reached; enforcement withdraws the access from the database on its next pass"
		return out
	}
	var res struct {
		Results []struct {
			State string `json:"state"`
		} `json:"results"`
	}
	_ = json.Unmarshal(raw, &res)
	counts := map[string]int{}
	for _, x := range res.Results {
		counts[x.State]++
	}
	out["withdrawn"], out["states"] = counts["withdrawn"], counts
	if counts["withdrawn"] == len(sessions) {
		out["summary"] = "withdrawn"
	} else {
		out["summary"] = "partly confirmed: the rest is withdrawn by enforcement from the database on its next pass"
	}
	return out
}

func (s *server) resetPackage(w http.ResponseWriter, r *http.Request) {
	id, in, actor, ok := s.readPackageAction(w, r)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var actionID, newEnt, purchase string
	var rebound, devices int
	var window *string
	err := s.db.QueryRow(ctx, `SELECT action_id::text, new_entitlement::text, purchase::text, sessions_rebound, devices,
		to_char(window_ends AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM iam_v2.admin_reset_package($1,$2,$3::uuid,$4::uuid,$5,$6::uuid)`,
		s.tenantID, s.siteID, id, actor, in.Reason, in.RequestID).Scan(&actionID, &newEnt, &purchase, &rebound, &devices, &window)
	if err != nil {
		packageOpErr(w, err)
		return
	}
	s.audit(r, "package.reset", "entitlement", id, map[string]any{
		"reason": in.Reason, "action_id": actionID, "new_entitlement_id": newEnt, "purchase_id": purchase,
		"price_minor": 0, "sessions_rebound": rebound, "devices": devices, "window_ends_at": window,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"action_id": actionID, "new_entitlement_id": newEnt, "purchase_id": purchase, "price_minor": 0,
		"sessions_rebound": rebound, "devices": devices, "window_ends_at": window,
	})
}

// packageRefusals maps the database's PKG_<CODE> refusals to the operator's error codes; every one is a conflict, not a fault.
var packageRefusals = map[string]int{
	"PKG_NOT_AUTHORIZED": http.StatusBadRequest, "PKG_NOT_FOUND": http.StatusNotFound, "PKG_REQUEST_REUSED": http.StatusConflict,
	"PKG_SUPERSEDED": http.StatusConflict, "PKG_NOT_STARTED": http.StatusConflict, "PKG_SUSPENDED": http.StatusConflict,
	"PKG_NOT_RESETTABLE": http.StatusConflict, "PKG_STAY_NOT_CHECKED_OUT": http.StatusConflict,
	"PKG_STAY_NOT_IN_HOUSE": http.StatusConflict, "PKG_ACCOUNT_DISABLED": http.StatusConflict,
	"PKG_VOUCHER_NOT_REDEEMED": http.StatusConflict, "PKG_WINDOW_PASSED": http.StatusConflict,
	"PKG_SUBJECT_HAS_LIVE_ACCESS": http.StatusConflict,
}

func packageOpErr(w http.ResponseWriter, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		code, msg, _ := strings.Cut(pgErr.Message, ": ")
		if status, ok := packageRefusals[code]; ok {
			jsonErr(w, status, strings.ToLower(strings.TrimPrefix(code, "PKG_")), msg)
			return
		}
		if pgErr.Code == "23505" {
			// A unique index on live access (one per subject): another grant won the race.
			jsonErr(w, http.StatusConflict, "subject_has_live_access", "this guest gained other live access a moment ago; reload and try again")
			return
		}
		if pgErr.Code == "40P01" || pgErr.Code == "55P03" {
			jsonErr(w, http.StatusConflict, "busy", "this package is being changed by another request; try again")
			return
		}
	}
	slog.Error("package control failed", "err", err)
	jsonErr(w, http.StatusInternalServerError, "package_control_failed", "the change could not be completed; nothing was changed")
}
