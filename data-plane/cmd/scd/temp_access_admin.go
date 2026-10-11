package main

// STAFF HELP FOR CLIENTS WHO ASKED FOR A CODE (Product Owner, 2026-10-12; contract §6.7, migration 0113).
//
// Three admin-socket routes, reached only through edged, which checks the operator's permission, takes the
// password step-up for the two actions and writes the audit record:
//
//	GET  /v1/admin/temporary-access            recent code requests: the device, the address, the code's state,
//	                                           the device's temporary-internet window and its limits
//	POST /v1/admin/temporary-access/clear      let a device try again: its ENDED windows stop counting towards
//	                                           the limits and the wait (the rows stay; a live window is untouched)
//	POST /v1/admin/otp/{id}/reveal             read a LIVE code to the client it was sent to
//
// THE CODE COPY. A code is stored as a keyed hash and cannot be read back, so each code that was SENT also gets
// a sealed copy (sealbox, domain otp-code-escrow:v1, the appliance's local notify key, bound to tenant, site and
// challenge). It exists only while the code can still be used: scd deletes it when the code is verified and
// prunes it once the code has expired, been used or been locked by wrong attempts. A revealed code still works
// only on the device that asked (0110), once, within its ten minutes. Revealing it is an operator vouching for
// the client: the address was not proven by the client reading their own mail. That trade-off is the Product
// Owner's decision and is written in the contract.

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
)

const otpEscrowDomain = "otp-code-escrow:v1"

func (s *server) tempAccessAdminRoutes(r chi.Router) {
	r.Get("/v1/admin/temporary-access", s.listCodeRequests)
	r.Post("/v1/admin/temporary-access/clear", s.clearTemporaryAccess)
	r.Post("/v1/admin/otp/{id}/reveal", s.revealOTPCode)
}

// escrowOTPCode keeps a sealed copy of a code that was just sent. Failure is logged and costs nothing but the
// operator's ability to read this one code.
func (s *server) escrowOTPCode(ctx context.Context, challengeID, code string, expires time.Time) {
	if s.db == nil || !s.otpEscrowKey.Present() || challengeID == "" || code == "" {
		return
	}
	sealed, err := s.otpEscrowKey.Seal([]byte(code), s.tenID, s.siteID, challengeID)
	if err != nil {
		slog.Warn("code copy: seal failed", "err", err)
		return
	}
	if _, err := s.db.Exec(ctx, `INSERT INTO iam_v2.otp_code_escrow
	      (challenge_id, tenant_id, site_id, key_id, nonce, ciphertext, cipher_version, expires_at)
	    VALUES ($1::uuid,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (challenge_id) DO NOTHING`,
		challengeID, s.tenID, s.siteID, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, sealed.CipherVersion, expires); err != nil {
		slog.Warn("code copy: store failed", "err", err)
	}
}

// dropOTPEscrow removes a code's copy the moment the code is used.
func (s *server) dropOTPEscrow(ctx context.Context, challengeID string) {
	if s.db == nil || challengeID == "" {
		return
	}
	_, _ = s.db.Exec(ctx, `DELETE FROM iam_v2.otp_code_escrow WHERE challenge_id=$1::uuid`, challengeID)
}

// pruneOTPEscrow removes every copy whose code can no longer be used: expired, used, or locked.
func (s *server) pruneOTPEscrow(ctx context.Context) {
	if s.db == nil {
		return
	}
	if _, err := s.db.Exec(ctx, `DELETE FROM iam_v2.otp_code_escrow e
	     WHERE e.expires_at <= now()
	        OR NOT EXISTS (SELECT 1 FROM public.auth_otps o WHERE o.id = e.challenge_id
	                        AND o.consumed_at IS NULL AND o.expires_at > now() AND o.attempts < o.max_attempts)`); err != nil {
		slog.Warn("code copy: prune failed", "err", err)
	}
}

type codeRequestWindow struct {
	StartedAt time.Time  `json:"started_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	EndReason string     `json:"end_reason,omitempty"`
	UsedBytes int64      `json:"used_bytes"`
	CapBytes  int64      `json:"data_cap_bytes"`
	ClearedAt *time.Time `json:"cleared_at,omitempty"`
	ClearedBy string     `json:"cleared_by,omitempty"`
	LiveNow   bool       `json:"live"`
	DownKbps  int        `json:"down_kbps"`
	UpKbps    int        `json:"up_kbps"`
}

type codeRequest struct {
	ChallengeID   string             `json:"challenge_id"`
	Channel       string             `json:"channel"`
	Destination   string             `json:"destination"`
	IssuedAt      time.Time          `json:"issued_at"`
	ExpiresAt     time.Time          `json:"expires_at"`
	ConsumedAt    *time.Time         `json:"consumed_at,omitempty"`
	Attempts      int                `json:"attempts"`
	MaxAttempts   int                `json:"max_attempts"`
	State         string             `json:"state"` // PENDING | USED | EXPIRED | LOCKED
	DeviceMAC     string             `json:"device_mac,omitempty"`
	CodeAvailable bool               `json:"code_available"`
	Window        *codeRequestWindow `json:"window,omitempty"`
	// What the device would meet if it asked for a window now.
	DeviceWindows24h int `json:"device_windows_24h"`
	RetryWaitSeconds int `json:"retry_wait_seconds"`
}

// GET /v1/admin/temporary-access?hours=N (1..72, default 24)
func (s *server) listCodeRequests(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		httpErr(w, http.StatusServiceUnavailable, "no database")
		return
	}
	hours := 24
	if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v >= 1 && v <= 72 {
		hours = v
	}
	set := s.temporaryAccessSettings(r.Context())
	rows, err := s.db.Query(r.Context(), `
	  SELECT o.id::text, o.channel, o.destination, o.issued_at, o.expires_at, o.consumed_at, o.attempts, o.max_attempts,
	         COALESCE(b.device_mac::text, ''),
	         EXISTS (SELECT 1 FROM iam_v2.otp_code_escrow e WHERE e.challenge_id = o.id),
	         g.started_at, g.expires_at, g.ended_at, COALESCE(g.end_reason, ''), COALESCE(g.used_bytes, 0),
	         COALESCE(g.data_cap_bytes, 0), g.cleared_at, COALESCE(g.cleared_by, ''), COALESCE(g.down_kbps, 0), COALESCE(g.up_kbps, 0),
	         CASE WHEN b.device_mac IS NULL THEN 0 ELSE (SELECT count(*) FROM iam_v2.verification_access_grants x
	                WHERE x.tenant_id=$1 AND x.site_id=$2 AND x.device_mac=b.device_mac AND x.cleared_at IS NULL
	                  AND x.started_at > now() - interval '24 hours') END,
	         CASE WHEN b.device_mac IS NULL THEN 0 ELSE GREATEST(0, COALESCE(EXTRACT(EPOCH FROM (
	                (SELECT max(x.ended_at) FROM iam_v2.verification_access_grants x
	                  WHERE x.tenant_id=$1 AND x.site_id=$2 AND x.device_mac=b.device_mac AND x.cleared_at IS NULL
	                    AND x.end_reason <> 'CANCELLED') + make_interval(mins => $4) - now())), 0)) END
	    FROM public.auth_otps o
	    LEFT JOIN iam_v2.otp_device_bindings b ON b.challenge_id = o.id
	    LEFT JOIN iam_v2.verification_access_grants g ON g.challenge_id = o.id AND g.tenant_id=$1 AND g.site_id=$2
	   WHERE o.tenant_id = $1 AND o.issued_at > now() - make_interval(hours => $3)
	   ORDER BY o.issued_at DESC
	   LIMIT 200`, s.tenID, s.siteID, hours, set.RetryAfterMinutes)
	if err != nil {
		slog.Error("code requests: list", "err", err)
		httpErr(w, http.StatusInternalServerError, "list failed")
		return
	}
	defer rows.Close()
	now := time.Now()
	out := []codeRequest{}
	for rows.Next() {
		var c codeRequest
		var gs, ge, gend, gclr *time.Time
		var greason, gby string
		var gused, gcap int64
		var gdown, gup int
		var wait float64
		if err := rows.Scan(&c.ChallengeID, &c.Channel, &c.Destination, &c.IssuedAt, &c.ExpiresAt, &c.ConsumedAt,
			&c.Attempts, &c.MaxAttempts, &c.DeviceMAC, &c.CodeAvailable, &gs, &ge, &gend, &greason, &gused, &gcap,
			&gclr, &gby, &gdown, &gup, &c.DeviceWindows24h, &wait); err != nil {
			slog.Error("code requests: scan", "err", err)
			httpErr(w, http.StatusInternalServerError, "list failed")
			return
		}
		switch {
		case c.ConsumedAt != nil:
			c.State = "USED"
		case c.Attempts >= c.MaxAttempts:
			c.State = "LOCKED"
		case !c.ExpiresAt.After(now):
			c.State = "EXPIRED"
		default:
			c.State = "PENDING"
		}
		c.CodeAvailable = c.CodeAvailable && c.State == "PENDING"
		c.RetryWaitSeconds = int(wait)
		if gs != nil && ge != nil {
			c.Window = &codeRequestWindow{StartedAt: *gs, ExpiresAt: *ge, EndedAt: gend, EndReason: greason,
				UsedBytes: gused, CapBytes: gcap, ClearedAt: gclr, ClearedBy: gby, DownKbps: gdown, UpKbps: gup,
				LiveNow: gend == nil && ge.After(now)}
		}
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requests": out,
		"limits": map[string]int{
			"per_device_daily": set.PerDeviceDaily, "per_destination_daily": set.PerDestinationDaily,
			"retry_after_minutes": set.RetryAfterMinutes, "max_concurrent": set.MaxConcurrent,
		},
		"code_copy_available": s.otpEscrowKey.Present(),
	})
}

func decodeJSONBody(r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<10)).Decode(dst)
}

// POST /v1/admin/temporary-access/clear {device_mac, operator, reason}
func (s *server) clearTemporaryAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceMAC string `json:"device_mac"`
		Operator  string `json:"operator"`
		Reason    string `json:"reason"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		httpErr(w, http.StatusBadRequest, "bad body")
		return
	}
	hw, err := net.ParseMAC(strings.TrimSpace(req.DeviceMAC))
	op := strings.TrimSpace(req.Operator)
	if err != nil || op == "" || len(op) > 200 || s.db == nil {
		httpErr(w, http.StatusBadRequest, "device_mac and operator are required")
		return
	}
	tag, err := s.db.Exec(r.Context(), `UPDATE iam_v2.verification_access_grants SET cleared_at=now(), cleared_by=$4
	     WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr AND ended_at IS NOT NULL AND cleared_at IS NULL`,
		s.tenID, s.siteID, hw.String(), op)
	if err != nil {
		slog.Error("temporary access: clear", "err", err)
		httpErr(w, http.StatusInternalServerError, "clear failed")
		return
	}
	var live bool
	_ = s.db.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM iam_v2.verification_access_grants
	     WHERE tenant_id=$1 AND site_id=$2 AND device_mac=$3::macaddr AND ended_at IS NULL)`, s.tenID, s.siteID, hw.String()).Scan(&live)
	slog.Info("temporary access: history cleared by an operator", "windows", tag.RowsAffected())
	writeJSON(w, http.StatusOK, map[string]any{"cleared_windows": tag.RowsAffected(), "window_live": live})
}

// POST /v1/admin/otp/{id}/reveal {operator}
func (s *server) revealOTPCode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Operator string `json:"operator"`
	}
	if err := decodeJSONBody(r, &req); err != nil || strings.TrimSpace(req.Operator) == "" {
		httpErr(w, http.StatusBadRequest, "operator is required")
		return
	}
	id := chi.URLParam(r, "id")
	if s.db == nil || !s.otpEscrowKey.Present() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "CODE_COPY_UNAVAILABLE"})
		return
	}
	var keyID string
	var ver int
	var nonce, ct []byte
	var expires time.Time
	var mac string
	err := s.db.QueryRow(r.Context(), `SELECT e.key_id, e.cipher_version, e.nonce, e.ciphertext, o.expires_at,
	         COALESCE((SELECT b.device_mac::text FROM iam_v2.otp_device_bindings b WHERE b.challenge_id=o.id), '')
	    FROM iam_v2.otp_code_escrow e JOIN public.auth_otps o ON o.id = e.challenge_id
	   WHERE e.challenge_id=$1::uuid AND e.tenant_id=$2 AND e.site_id=$3
	     AND o.consumed_at IS NULL AND o.expires_at > now() AND o.attempts < o.max_attempts`,
		id, s.tenID, s.siteID).Scan(&keyID, &ver, &nonce, &ct, &expires, &mac)
	if errors.Is(err, pgx.ErrNoRows) {
		// Used, expired, locked, never sent, or not this site's: one answer for all of them.
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "CODE_NOT_AVAILABLE"})
		return
	}
	if err != nil {
		slog.Error("code reveal: read", "err", err)
		httpErr(w, http.StatusInternalServerError, "reveal failed")
		return
	}
	plain, err := s.otpEscrowKey.Open(sealbox.Sealed{Ciphertext: ct, Nonce: nonce, KeyID: keyID, CipherVersion: ver},
		s.tenID, s.siteID, id)
	if err != nil {
		slog.Warn("code reveal: cannot open the sealed copy")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "CODE_COPY_UNAVAILABLE"})
		return
	}
	slog.Info("code revealed to an operator") // never the code, never the address
	writeJSON(w, http.StatusOK, map[string]any{"code": string(plain), "expires_at": expires, "device_mac": mac})
}
