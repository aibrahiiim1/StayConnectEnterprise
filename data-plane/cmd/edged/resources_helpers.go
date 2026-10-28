package main

// Shared helpers for the edged resource handlers.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Paging contract for growing lists: ?page= (1-based) and ?page_size= (1..200, default 50); out-of-bounds values
// get a 400, not a clamp. Handlers fetch page_size+1 rows so meta.has_more needs no second query.
// Search text never travels in the URL (the request logger writes every request line and operators type guest
// names there); each list reads its own X-...-Search header.
const (
	pageSizeDefault = 50
	pageSizeMax     = 200
	searchMaxLen    = 100
	// pageMax keeps (page-1)*page_size far from overflow; a larger page was a 500 (audit P9 L12).
	pageMax = 100000
)

type pageReq struct {
	Page int
	Size int
}

func (p pageReq) Offset() int { return (p.Page - 1) * p.Size }

// The LIMIT to query with: one more than the page holds.
func (p pageReq) Fetch() int { return p.Size + 1 }

// legacyMax > 0 keeps an older ?limit= working: when page_size is absent, a valid limit becomes the page size, capped at legacyMax.
func parsePage(q url.Values, legacyMax int) (pageReq, error) {
	p := pageReq{Page: 1, Size: pageSizeDefault}
	if v := q.Get("page"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > pageMax {
			return p, fmt.Errorf("page must be a whole number from 1 to %d", pageMax)
		}
		p.Page = n
	}
	if v := q.Get("page_size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > pageSizeMax {
			return p, fmt.Errorf("page_size must be between 1 and %d", pageSizeMax)
		}
		p.Size = n
	} else if legacyMax > 0 {
		if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 {
			p.Size = min(n, legacyMax)
		}
	}
	return p, nil
}

// Reports whether the caller asked for paging; lists whose callers expect the whole set page only when asked.
func hasPageParams(q url.Values) bool {
	return q.Get("page") != "" || q.Get("page_size") != ""
}

func readPage(w http.ResponseWriter, r *http.Request, legacyMax int) (pageReq, bool) {
	p, err := parsePage(r.URL.Query(), legacyMax)
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return p, false
	}
	return p, true
}

// readSearch reads a list's free-text search from its header, trimmed and bounded; ok=false means a 400 was written.
// The console percent-encodes it because browsers refuse non-ISO-8859-1 characters in headers; a value that is not
// valid percent-encoding is taken as typed.
// headerSearchText is the decoded, trimmed header search text for lists that bound and refuse it themselves.
func headerSearchText(r *http.Request, header string) string {
	v := r.Header.Get(header)
	if d, err := url.PathUnescape(v); err == nil {
		v = d
	}
	return strings.TrimSpace(v)
}

func readSearch(w http.ResponseWriter, r *http.Request, header string) (string, bool) {
	v := r.Header.Get(header)
	if d, err := url.PathUnescape(v); err == nil {
		v = d
	}
	v = strings.TrimSpace(v)
	if len([]rune(v)) > searchMaxLen {
		jsonErr(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("search is limited to %d characters", searchMaxLen))
		return "", false
	}
	return v, true
}

// likePattern turns search text into an ILIKE "contains" pattern, or nil (SQL NULL) when there is no search.
func likePattern(v string) any {
	if v == "" {
		return nil
	}
	return "%" + likeEscape(v) + "%"
}

func trimPage[T any](rows []T, p pageReq) ([]T, bool) {
	if rows == nil {
		rows = []T{}
	}
	if len(rows) > p.Size {
		return rows[:p.Size], true
	}
	return rows, false
}

func slicePage[T any](all []T, p pageReq) ([]T, bool) {
	start := min(p.Offset(), len(all))
	end := min(start+p.Size, len(all))
	out := all[start:end]
	if out == nil {
		out = []T{}
	}
	return out, end < len(all)
}

type pagedList[T any] struct {
	Data     []T      `json:"data"`
	Meta     listMeta `json:"meta"`
	Page     int      `json:"page"`
	PageSize int      `json:"page_size"`
	Total    *int     `json:"total,omitempty"`
}

func newPagedList[T any](rows []T, more bool, p pageReq, total *int) pagedList[T] {
	if rows == nil {
		rows = []T{}
	}
	return pagedList[T]{Data: rows, Meta: listMeta{HasMore: more}, Page: p.Page, PageSize: p.Size, Total: total}
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// The second return is false when the key is absent (absent = no limit enforced).
func (s *server) intLimit(ctx context.Context, key string) (int64, bool, error) {
	var v *int64
	err := s.db.QueryRow(ctx, `
        SELECT int_value FROM tenant_effective_limits
         WHERE tenant_id = $1 AND key = $2 AND value_type = 'int'
    `, s.tenantID, key).Scan(&v)
	if isNoRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if v == nil {
		return 0, false, nil
	}
	return *v, true, nil
}

// Missing key, -1 and 0 all mean "unlimited". Writes 403 limit_exceeded and returns false when breached.
func (s *server) enforceLimit(ctx context.Context, w http.ResponseWriter, key string, delta int64, countQuery string, countArgs ...any) bool {
	lim, ok, err := s.intLimit(ctx, key)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "limits lookup failed")
		return false
	}
	if !ok || lim == -1 || lim == 0 {
		return true
	}
	var count int64
	if err := s.db.QueryRow(ctx, countQuery, countArgs...).Scan(&count); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "limits count failed")
		return false
	}
	if count+delta > lim {
		jsonErr(w, http.StatusForbidden, "limit_exceeded", "license limit reached for "+key)
		return false
	}
	return true
}

// Best-effort: failures are logged, never surfaced; scd picks the change up from the DB on its next restart.
func (s *server) scdReloadWarn(path string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, _, err := s.scd.call(ctx, http.MethodPost, path, nil)
	if err != nil || st >= 300 {
		slog.Warn("scd reload failed", "path", path, "status", st, "err", err)
	}
}
