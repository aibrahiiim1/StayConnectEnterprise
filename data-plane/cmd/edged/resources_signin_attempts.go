package main

// Guest sign-in attempts over iam_v2.sign_in_attempts, under two permissions:
//
//	guest-signin-attempts     (View_Guest_SignIn_Attempts)    — the list, rooms, results, reasons
//	guest-signin-credentials  (View_Guest_SignIn_Credentials) — what the guest typed and what would be accepted
//
// They are separate so a dashboard-level role cannot read guests' credential material.
// The credential route is a proxy: edged holds no sealing key, so it checks the permission, records who asked, and asks scd to open the row.
// There is no tenant or site parameter; both come from the appliance's signed assignment.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

// signInAttemptRow is the LIST projection: no submitted value, accepted value or ciphertext is in this struct.
type signInAttemptRow struct {
	ID           string    `json:"id"`
	OccurredAt   time.Time `json:"occurred_at"`
	Room         string    `json:"room"`
	GuestNetwork string    `json:"guest_network"`
	Result       string    `json:"result"`
	ResultLabel  string    `json:"result_label"`
	Succeeded    bool      `json:"succeeded"`
	VerifierKind string    `json:"verifier_kind"`
	// MirrorAgeSeconds is how stale the local PMS roster was then; the first thing to check when several guests fail at once.
	MirrorAgeSeconds *int64 `json:"mirror_age_seconds,omitempty"`
	TransportStatus  string `json:"pms_transport_status,omitempty"`
	DeviceIP         string `json:"device_ip,omitempty"`
	DeviceMAC        string `json:"device_mac,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	LatencyMS        *int64 `json:"latency_ms,omitempty"`
}

type signInAttemptDetail struct {
	signInAttemptRow
	MatchedField           string     `json:"matched_field,omitempty"`
	MatchedStayID          string     `json:"matched_stay_id,omitempty"`
	RoomInMirror           *bool      `json:"room_in_mirror,omitempty"`
	EligibleStayCandidates *int       `json:"eligible_stay_candidates,omitempty"`
	MirrorLastCompleteSync *time.Time `json:"mirror_last_complete_sync_at,omitempty"`
	EntitlementID          string     `json:"entitlement_id,omitempty"`
	SessionID              string     `json:"session_id,omitempty"`
	// CredentialsAvailable distinguishes "not permitted" from "nothing sealed because the key was missing".
	CredentialsAvailable bool `json:"credentials_available"`
}

func (s *server) signInAttemptsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSignInAttempts)
	r.Get("/{id}", s.getSignInAttempt)
	return r
}

// signInCredentialsRoutes is mounted under its own resource key so the second permission is enforced by middleware, not the UI.
// The reveal is a POST with the operator's password in the body (never URL or header); revealing is the resource's only action, hence "write".
func (s *server) signInCredentialsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{id}", s.getSignInAttemptCredentials)
	return r
}

// The attempts list is paged and searched on the server; the search text travels in X-Signin-Search, never the URL (edged logs request lines).
const signInSearchHeader = "X-Signin-Search"

// signInAttemptFilter is the WHERE clause the page, its total and its tiles share. $1 tenant, $2 site, $3 room,
// $4 result, $5 network, $6 credential type, $7 from, $8 to.
const signInAttemptFilter = `WHERE a.tenant_id=$1 AND a.site_id=$2
		   AND ($3 = '' OR a.submitted_room = $3)
		   AND ($4 = '' OR a.result = $4)
		   AND ($5 = '' OR a.guest_network_name = $5)
		   AND ($6 = '' OR a.verifier_kind = $6)
		   AND ($7::timestamptz IS NULL OR a.occurred_at >= $7::timestamptz)
		   AND ($8::timestamptz IS NULL OR a.occurred_at <= $8::timestamptz)`

// signInSearchMatch is the search. $9 the ILIKE pattern (NULL for no search), $10 the result codes whose reason text contains the search.
const signInSearchMatch = `($9::text IS NULL
		   OR a.submitted_room ILIKE $9 OR a.guest_network_name ILIKE $9 OR a.result ILIKE $9
		   OR host(a.device_ip) ILIKE $9 OR a.device_mac::text ILIKE $9 OR a.request_id::text ILIKE $9
		   OR a.result = ANY($10))`

var (
	signInMismatchResults = []string{"CREDENTIAL_MISMATCH", "ROOM_NOT_IN_MIRROR"}
	signInSystemicResults = []string{"MIRROR_STALE_OR_MISSING_CHANGE", "ROUTING_OR_INTERFACE_FAILURE", "SERVICE_UNAVAILABLE"}
)

func signInSucceededResults() []string {
	out := []string{}
	for _, r := range signinattempt.AllResults {
		if r.Succeeded() {
			out = append(out, string(r))
		}
	}
	return out
}

// signInResultsLabelled returns the result codes whose reason contains q, case-insensitively.
func signInResultsLabelled(q string) []string {
	out := []string{}
	if q == "" {
		return out
	}
	q = strings.ToLower(q)
	for _, r := range signinattempt.AllResults {
		if strings.Contains(strings.ToLower(r.Label()), q) {
			out = append(out, string(r))
		}
	}
	return out
}

// signInAttemptsSummary counts every attempt the filters select, ignoring page and search.
type signInAttemptsSummary struct {
	Total    int `json:"total"`
	Failed   int `json:"failed"`
	Mismatch int `json:"mismatch"`
	Systemic int `json:"systemic"`
}

type signInAttemptsPage struct {
	pagedList[signInAttemptRow]
	Summary signInAttemptsSummary `json:"summary"`
}

func (s *server) listSignInAttempts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	// ?limit= still sizes the page for an older client, up to the 500 it always allowed.
	pg, ok := readPage(w, r, 500)
	if !ok {
		return
	}
	search, ok := readSearch(w, r, signInSearchHeader)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()

	// Filters are applied in SQL; the empty-string default means no filter.
	args := []any{s.tenantID, s.siteID,
		strings.ToUpper(strings.TrimSpace(q.Get("room"))), strings.TrimSpace(q.Get("result")),
		strings.TrimSpace(q.Get("network")), strings.TrimSpace(q.Get("credential_type")),
		nullableTime(q.Get("from")), nullableTime(q.Get("to")),
		likePattern(search), signInResultsLabelled(search)}

	var sum signInAttemptsSummary
	var total int
	if err := s.db.QueryRow(ctx, `SELECT count(*)::int,
		       count(*) FILTER (WHERE a.result <> ALL($11))::int,
		       count(*) FILTER (WHERE a.result = ANY($12))::int,
		       count(*) FILTER (WHERE a.result = ANY($13))::int,
		       count(*) FILTER (WHERE `+signInSearchMatch+`)::int
		  FROM iam_v2.sign_in_attempts a
		 `+signInAttemptFilter,
		append(args, signInSucceededResults(), signInMismatchResults, signInSystemicResults)...).
		Scan(&sum.Total, &sum.Failed, &sum.Mismatch, &sum.Systemic, &total); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}

	rows, err := s.db.Query(ctx, `
		SELECT a.id::text, a.occurred_at, COALESCE(a.submitted_room,''), COALESCE(a.guest_network_name,''),
		       a.result, COALESCE(a.verifier_kind,''), a.mirror_age_seconds,
		       COALESCE(a.pms_transport_status,''), COALESCE(host(a.device_ip),''),
		       COALESCE(a.device_mac::text,''), COALESCE(a.request_id::text,''), a.latency_ms
		  FROM iam_v2.sign_in_attempts a
		 `+signInAttemptFilter+`
		   AND `+signInSearchMatch+`
		 ORDER BY a.occurred_at DESC, a.id
		 LIMIT $11 OFFSET $12`,
		append(args, pg.Fetch(), pg.Offset())...)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()

	out := []signInAttemptRow{}
	for rows.Next() {
		var e signInAttemptRow
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.Room, &e.GuestNetwork, &e.Result, &e.VerifierKind,
			&e.MirrorAgeSeconds, &e.TransportStatus, &e.DeviceIP, &e.DeviceMAC, &e.RequestID,
			&e.LatencyMS); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		// The reason comes from the same Go map the codes are declared in, so the screen and recorder cannot drift.
		e.ResultLabel = signinattempt.Result(e.Result).Label()
		e.Succeeded = signinattempt.Result(e.Result).Succeeded()
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	out, more := trimPage(out, pg)
	writeJSON(w, http.StatusOK, signInAttemptsPage{pagedList: newPagedList(out, more, pg, &total), Summary: sum})
}

func (s *server) getSignInAttempt(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	id := strings.TrimSpace(chi.URLParam(r, "id"))

	var e signInAttemptDetail
	var sealedLen int
	err := s.db.QueryRow(ctx, `
		SELECT a.id::text, a.occurred_at, COALESCE(a.submitted_room,''), COALESCE(a.guest_network_name,''),
		       a.result, COALESCE(a.verifier_kind,''), a.mirror_age_seconds,
		       COALESCE(a.pms_transport_status,''), COALESCE(host(a.device_ip),''),
		       COALESCE(a.device_mac::text,''), COALESCE(a.request_id::text,''), a.latency_ms,
		       COALESCE(a.matched_field,''), COALESCE(a.matched_stay_id::text,''),
		       a.room_in_mirror, a.eligible_stay_candidates, a.mirror_last_complete_sync_at,
		       COALESCE(a.entitlement_id::text,''), COALESCE(a.session_id::text,''),
		       COALESCE(length(a.sensitive_ciphertext), 0)
		  FROM iam_v2.sign_in_attempts a
		 WHERE a.tenant_id=$1 AND a.site_id=$2 AND a.id=$3::uuid`,
		s.tenantID, s.siteID, id).
		Scan(&e.ID, &e.OccurredAt, &e.Room, &e.GuestNetwork, &e.Result, &e.VerifierKind, &e.MirrorAgeSeconds,
			&e.TransportStatus, &e.DeviceIP, &e.DeviceMAC, &e.RequestID, &e.LatencyMS,
			&e.MatchedField, &e.MatchedStayID, &e.RoomInMirror, &e.EligibleStayCandidates,
			&e.MirrorLastCompleteSync, &e.EntitlementID, &e.SessionID, &sealedLen)
	if err != nil {
		// Another site's id and a nonexistent id answer identically (the site scope is in the WHERE), so ids cannot be probed.
		jsonErr(w, http.StatusNotFound, "not_found", "no such attempt")
		return
	}
	e.ResultLabel = signinattempt.Result(e.Result).Label()
	e.Succeeded = signinattempt.Result(e.Result).Succeeded()
	e.CredentialsAvailable = sealedLen > 0
	writeJSON(w, http.StatusOK, e)
}

// getSignInAttemptCredentials returns the submitted and accepted values unmasked (Product Owner decision).
// Protection is sealing at rest, the distinct permission, no-store, and an audit row per read.
func (s *server) getSignInAttemptCredentials(w http.ResponseWriter, r *http.Request) {
	// Set no-store first so every exit path carries it, including errors.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "attempt id is required")
		return
	}

	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "your password is required to see what a client typed")
		return
	}

	// The audit is written before the values are fetched: a failed read is still an attempted read, and id probing must be visible.
	s.audit(r, "guest_signin_attempt.credentials_viewed", "sign_in_attempt", id, map[string]any{
		"site_id": s.siteID,
	})

	// scd holds the key; with no client, refuse cleanly rather than nil-deref.
	if s.scd == nil {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable",
			"the sign-in credential store is not reachable")
		return
	}

	ctx, cancel := dbCtx(r)
	defer cancel()
	status, body, err := s.scd.call(ctx, http.MethodPost, "/v1/phase3/signin-attempts/credentials",
		map[string]any{"attempt_id": id})
	if err != nil || status != http.StatusOK {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable",
			"the sign-in credential store is not reachable")
		return
	}
	var out map[string]any
	if json.Unmarshal(body, &out) != nil {
		jsonErr(w, http.StatusServiceUnavailable, "unavailable", "unreadable response")
		return
	}

	// no-store (set at the top) keeps this out of caches that would outlive the retention purge.
	writeJSON(w, http.StatusOK, out)
}

// nullableTime turns an optional RFC3339 query parameter into a bound; unparseable means absent.
func nullableTime(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil
	}
	return &t
}
