package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgconn"
)

// internalErr answers a failure the operator cannot act on with a fixed message and logs the detail with the
// request id (audit P9 L1): driver messages, function names, socket URLs and file paths stay in the appliance log.
// A refusal written for operators by a database function (RAISE with a data, integrity or raise SQLSTATE) is
// still shown as written, because it says what to change.
func internalErr(w http.ResponseWriter, r *http.Request, status int, code string, err error) {
	if msg, ok := operatorRefusal(err); ok {
		jsonErr(w, status, code, msg)
		return
	}
	reqID := ""
	if r != nil {
		reqID = middleware.GetReqID(r.Context())
	}
	slog.Error("request failed", "code", code, "status", status, "request_id", reqID, "err", err)
	msg := "the appliance could not complete this request"
	if status == http.StatusBadGateway || status == http.StatusServiceUnavailable {
		msg = "an appliance service needed for this request could not be reached"
	}
	if reqID != "" {
		msg += "; the details are in the appliance log under request " + reqID
	}
	jsonErr(w, status, code, msg)
}

// operatorRefusal returns the message of a database refusal that was written for operators.
func operatorRefusal(err error) (string, bool) {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return "", false
	}
	// 22: data exception, 23: integrity constraint, P0: PL/pgSQL raise. Not 42 (undefined function or column),
	// 08 (connection), 53/57/58 (resources, operator intervention, system): those are internal.
	if strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "23") || strings.HasPrefix(pg.Code, "P0") {
		return pg.Message, true
	}
	return "", false
}
