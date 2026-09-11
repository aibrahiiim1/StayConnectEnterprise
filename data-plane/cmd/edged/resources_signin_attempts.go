package main

// GUEST SIGN-IN ATTEMPTS — the operator surface over iam_v2.sign_in_attempts.
//
// TWO PERMISSIONS, NOT ONE, AND THE SPLIT IS THE POINT.
//
//	guest-signin-attempts     (View_Guest_SignIn_Attempts)    — the list, the rooms, the results, the reasons
//	guest-signin-credentials  (View_Guest_SignIn_Credentials) — what the guest typed and what we would accept
//
// The first answers "is sign-in working, and for whom is it not"; the second answers one guest's specific
// question at the desk and is, unavoidably, their credential material. A role that needs the first does not
// automatically need the second, and collapsing them would mean every operator who can look at a dashboard
// can also read every value every guest has typed for the last thirty days.
//
// THE CREDENTIAL ROUTE IS A PROXY, AND THAT IS DELIBERATE. edged holds no sealing key. It checks the
// permission, records who asked, and then asks scd — which runs as root behind a unix socket and holds the
// key — to open that one row. The check and the key sit on opposite sides of a process boundary, so neither a
// stolen edged session nor a copy of the database is sufficient on its own.
//
// NOTHING HERE TRUSTS THE CLIENT FOR SCOPE. There is no tenant or site parameter on any of these routes; both
// are bound from the appliance's own signed assignment into every query, exactly as every other edged
// resource does it. A request cannot name another site to read.

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/signinattempt"
)

// signInAttemptRow is the LIST projection: everything an operator may see with the first permission alone.
// Note what is absent — no submitted value, no accepted value, no ciphertext. Those are not "hidden by the
// UI"; they are not in this struct and never reach the response.
type signInAttemptRow struct {
	ID           string    `json:"id"`
	OccurredAt   time.Time `json:"occurred_at"`
	Room         string    `json:"room"`
	GuestNetwork string    `json:"guest_network"`
	Result       string    `json:"result"`
	ResultLabel  string    `json:"result_label"`
	Succeeded    bool      `json:"succeeded"`
	VerifierKind string    `json:"verifier_kind"`
	// MirrorAgeSeconds is how stale the local PMS roster was at that instant. It is the first thing to look
	// at when several guests fail at once, which is why it is a list column rather than a detail field.
	MirrorAgeSeconds *int64 `json:"mirror_age_seconds,omitempty"`
	TransportStatus  string `json:"pms_transport_status,omitempty"`
	DeviceIP         string `json:"device_ip,omitempty"`
	DeviceMAC        string `json:"device_mac,omitempty"`
	RequestID        string `json:"request_id,omitempty"`
	LatencyMS        *int64 `json:"latency_ms,omitempty"`
}

// signInAttemptDetail adds the comparison context — still without a single credential value.
type signInAttemptDetail struct {
	signInAttemptRow
	MatchedField           string     `json:"matched_field,omitempty"`
	MatchedStayID          string     `json:"matched_stay_id,omitempty"`
	RoomInMirror           *bool      `json:"room_in_mirror,omitempty"`
	EligibleStayCandidates *int       `json:"eligible_stay_candidates,omitempty"`
	MirrorLastCompleteSync *time.Time `json:"mirror_last_complete_sync_at,omitempty"`
	EntitlementID          string     `json:"entitlement_id,omitempty"`
	SessionID              string     `json:"session_id,omitempty"`
	// CredentialsAvailable says whether a sealed half exists at all, so the panel can distinguish "you may
	// not see this" from "there is nothing to see because the key was missing when it was recorded".
	CredentialsAvailable bool `json:"credentials_available"`
}

func (s *server) signInAttemptsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listSignInAttempts)
	r.Get("/{id}", s.getSignInAttempt)
	return r
}

// signInCredentialsRoutes is mounted under its OWN resource key, which is what makes the second permission a
// real, server-enforced boundary rather than a flag the UI consults. A caller holding only
// guest-signin-attempts is refused by middleware here, before this handler runs — so hiding the button in the
// browser and calling the API directly are the same answer.
func (s *server) signInCredentialsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/{id}", s.getSignInAttemptCredentials)
	return r
}

func (s *server) listSignInAttempts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()

	q := r.URL.Query()
	limit := 200
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	// Filters are applied in SQL rather than in the browser: a desk looking for one room must not have to
	// pull thirty days of rows to find it, and the empty-string default means "no filter" without a second
	// query shape to keep correct.
	rows, err := s.db.Query(ctx, `
		SELECT a.id::text, a.occurred_at, COALESCE(a.submitted_room,''), COALESCE(a.guest_network_name,''),
		       a.result, COALESCE(a.verifier_kind,''), a.mirror_age_seconds,
		       COALESCE(a.pms_transport_status,''), COALESCE(host(a.device_ip),''),
		       COALESCE(a.device_mac::text,''), COALESCE(a.request_id::text,''), a.latency_ms
		  FROM iam_v2.sign_in_attempts a
		 WHERE a.tenant_id=$1 AND a.site_id=$2
		   AND ($3 = '' OR a.submitted_room = $3)
		   AND ($4 = '' OR a.result = $4)
		   AND ($5 = '' OR a.guest_network_name = $5)
		   AND ($6 = '' OR a.verifier_kind = $6)
		   AND ($7::timestamptz IS NULL OR a.occurred_at >= $7::timestamptz)
		   AND ($8::timestamptz IS NULL OR a.occurred_at <= $8::timestamptz)
		 ORDER BY a.occurred_at DESC
		 LIMIT $9`,
		s.tenantID, s.siteID,
		strings.ToUpper(strings.TrimSpace(q.Get("room"))), strings.TrimSpace(q.Get("result")),
		strings.TrimSpace(q.Get("network")), strings.TrimSpace(q.Get("credential_type")),
		nullableTime(q.Get("from")), nullableTime(q.Get("to")), limit)
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
		// The plain-language reason comes from the SAME Go map the codes are declared in, so the screen and
		// the recorder cannot drift. A label rendered in the browser would be a second list to maintain, and
		// the failure mode of forgetting is a raw enum in front of someone helping a guest.
		e.ResultLabel = signinattempt.Result(e.Result).Label()
		e.Succeeded = signinattempt.Result(e.Result).Succeeded()
		out = append(out, e)
	}
	writeList(w, out)
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
		// An id from another site and an id that never existed answer identically: the site scope is in the
		// WHERE clause, so both are simply "no row", and an operator cannot probe for the existence of
		// another site's attempts by watching which ids fail differently.
		jsonErr(w, http.StatusNotFound, "not_found", "no such attempt")
		return
	}
	e.ResultLabel = signinattempt.Result(e.Result).Label()
	e.Succeeded = signinattempt.Result(e.Result).Succeeded()
	e.CredentialsAvailable = sealedLen > 0
	writeJSON(w, http.StatusOK, e)
}

// getSignInAttemptCredentials returns the submitted and accepted values IN FULL, unmasked.
//
// Unmasked is the Product Owner's decision and it is not a concession: these are the same operators who
// already hold the guest's room, stay and access credentials, and a panel showing "OK••••••" beside
// "OK•••••••" answers nothing. What protects the guest is not asterisks — it is that the values are sealed at
// rest, that a distinct permission is required, that the response is uncacheable, and that every read is
// recorded against the operator who made it.
func (s *server) getSignInAttemptCredentials(w http.ResponseWriter, r *http.Request) {
	// NO-STORE FIRST, BEFORE ANY WORK, so it is present on EVERY exit from this handler and not only on the
	// one that carries values. An error body on this route still names an attempt id an operator asked
	// about, and a 503 cached by a proxy is a 503 the operator keeps seeing after the fault is fixed.
	// Setting it last is the version of this that looks correct and leaves the failure paths cacheable.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")

	id := strings.TrimSpace(chi.URLParam(r, "id"))
	if id == "" {
		jsonErr(w, http.StatusBadRequest, "bad_request", "attempt id is required")
		return
	}

	// THE AUDIT IS WRITTEN BEFORE THE VALUES ARE FETCHED, not after. A read that fails downstream is still a
	// read that was ATTEMPTED by a named operator against a named attempt, and that is what an audit trail is
	// for; recording only successes would leave the interesting case — someone probing ids — invisible.
	s.audit(r, "guest_signin_attempt.credentials_viewed", "sign_in_attempt", id, map[string]any{
		"site_id": s.siteID,
	})

	// scd is where the key lives, so with no client there is nothing to ask and nothing to show. It is a
	// clean refusal rather than a crash: a nil dereference here would drop the operator's connection and
	// look, from the browser, exactly like a network fault on the appliance.
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

	// The no-store headers were set at the top of this handler. Without them this response can sit in a
	// browser cache, a corporate proxy or a disk image long after the thirty-day purge has removed the row it
	// came from — which would make the retention promise false by a mechanism nobody on the appliance can see.
	writeJSON(w, http.StatusOK, out)
}

// nullableTime turns an optional RFC3339 query parameter into a value the query can treat as "no bound".
// Anything unparseable is treated as absent rather than as an error: a filter a human mistyped should show
// them everything, not an error page.
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
