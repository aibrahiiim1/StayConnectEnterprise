package main

// STAFF HELP FOR CLIENTS WHO ASKED FOR A CODE (Product Owner, 2026-10-12; contract §6.7, migration 0113).
//
// Two admin-socket routes, reached only through edged, which checks the operator's permission, takes the
// password step-up for the reveal and writes the audit record:
//
//	GET  /v1/admin/code-requests               recent SMS / WhatsApp code requests: the device, the number and
//	                                           the code's state (also answered at its old path,
//	                                           /v1/admin/temporary-access)
//	POST /v1/admin/otp/{id}/reveal             read a LIVE code to the client it was sent to
//
// The temporary-internet window, its columns, its limits and the "clear" action are REMOVED with the email code
// they served (contract docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md §1). Their history rows
// (verification_access_grants) are kept and no longer read.
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
	// /v1/admin/code-requests is the name for what the list now is; the old path answers the same so an edged
	// that has not moved yet keeps working.
	r.Get("/v1/admin/code-requests", s.listCodeRequests)
	r.Get("/v1/admin/temporary-access", s.listCodeRequests)
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
	_, _ = s.db.Exec(ctx, `DELETE FROM iam_v2.otp_code_reveals WHERE revealed_at < now() - interval '1 day'`)
	// A code's device binding (0110) is pruned when the next code is issued, so a site where nobody asks for a
	// code kept every device MAC it ever bound. Swept here too, on the same one-day rule (GDPR plan C1 gap).
	_, _ = s.db.Exec(ctx, `DELETE FROM iam_v2.otp_device_bindings WHERE issued_at < now() - interval '1 day'`)
	if _, err := s.db.Exec(ctx, `DELETE FROM iam_v2.otp_code_escrow e
	     WHERE e.expires_at <= now()
	        OR NOT EXISTS (SELECT 1 FROM public.auth_otps o WHERE o.id = e.challenge_id
	                        AND o.consumed_at IS NULL AND o.expires_at > now() AND o.attempts < o.max_attempts)`); err != nil {
		slog.Warn("code copy: prune failed", "err", err)
	}
}

type codeRequest struct {
	ChallengeID   string     `json:"challenge_id"`
	Channel       string     `json:"channel"`
	Destination   string     `json:"destination"`
	IssuedAt      time.Time  `json:"issued_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
	Attempts      int        `json:"attempts"`
	MaxAttempts   int        `json:"max_attempts"`
	State         string     `json:"state"` // PENDING | USED | EXPIRED | LOCKED
	DeviceMAC     string     `json:"device_mac,omitempty"`
	CodeAvailable bool       `json:"code_available"`
	// ExistingClient: the number already belongs to a Client here. A staff-shown code cannot sign it in
	// (contract §6.7), so the code is not offered for showing.
	ExistingClient bool `json:"existing_client"`
	// StaffRevealed: a staff member has already shown this code.
	StaffRevealed bool `json:"staff_revealed"`
}

// GET /v1/admin/code-requests?hours=N (1..72, default 24); also /v1/admin/temporary-access
//
// The path keeps its historical name so edged's proxy does not move; what it lists is the SMS and WhatsApp code
// requests. Email code requests are history only (the email channel is removed) and are not listed, and the
// temporary-internet window columns, its limits and the "clear" action are removed with the window itself
// (contract docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md §1).
func (s *server) listCodeRequests(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		httpErr(w, http.StatusServiceUnavailable, "no database")
		return
	}
	hours := 24
	if v, err := strconv.Atoi(r.URL.Query().Get("hours")); err == nil && v >= 1 && v <= 72 {
		hours = v
	}
	rows, err := s.db.Query(r.Context(), `
	  SELECT o.id::text, o.channel, o.destination, o.issued_at, o.expires_at, o.consumed_at, o.attempts, o.max_attempts,
	         COALESCE(b.device_mac::text, ''),
	         EXISTS (SELECT 1 FROM iam_v2.otp_code_escrow e WHERE e.challenge_id = o.id),
	         EXISTS (SELECT 1 FROM iam_v2.guest_principal_identities i WHERE i.tenant_id = o.tenant_id
	                   AND i.factor_type = 'PHONE'
	                   AND coalesce(i.factor_issuer, '') = '' AND i.factor_value_norm = lower(btrim(o.destination))),
	         EXISTS (SELECT 1 FROM iam_v2.otp_code_reveals v WHERE v.challenge_id = o.id)
	    FROM public.auth_otps o
	    LEFT JOIN iam_v2.otp_device_bindings b ON b.challenge_id = o.id
	   WHERE o.tenant_id = $1 AND o.issued_at > now() - make_interval(hours => $2)
	     AND o.channel IN ('sms','whatsapp')
	   ORDER BY o.issued_at DESC
	   LIMIT 200`, s.tenID, hours)
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
		if err := rows.Scan(&c.ChallengeID, &c.Channel, &c.Destination, &c.IssuedAt, &c.ExpiresAt, &c.ConsumedAt,
			&c.Attempts, &c.MaxAttempts, &c.DeviceMAC, &c.CodeAvailable, &c.ExistingClient, &c.StaffRevealed); err != nil {
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
		c.CodeAvailable = c.CodeAvailable && c.State == "PENDING" && !c.ExistingClient
		out = append(out, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requests":            out,
		"code_copy_available": s.otpEscrowKey.Present(),
	})
}

func decodeJSONBody(r *http.Request, dst any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 8<<10)).Decode(dst)
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
	var existing bool
	err := s.db.QueryRow(r.Context(), `SELECT e.key_id, e.cipher_version, e.nonce, e.ciphertext, o.expires_at,
	         COALESCE((SELECT b.device_mac::text FROM iam_v2.otp_device_bindings b WHERE b.challenge_id=o.id), ''),
	         EXISTS (SELECT 1 FROM iam_v2.guest_principal_identities i WHERE i.tenant_id = o.tenant_id
	                   AND i.factor_type = 'PHONE'
	                   AND coalesce(i.factor_issuer, '') = '' AND i.factor_value_norm = lower(btrim(o.destination)))
	    FROM iam_v2.otp_code_escrow e JOIN public.auth_otps o ON o.id = e.challenge_id
	   WHERE e.challenge_id=$1::uuid AND e.tenant_id=$2 AND e.site_id=$3
	     AND o.consumed_at IS NULL AND o.expires_at > now() AND o.attempts < o.max_attempts AND o.channel IN ('sms','whatsapp')`,
		id, s.tenID, s.siteID).Scan(&keyID, &ver, &nonce, &ct, &expires, &mac, &existing)
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
	// AN EXISTING CLIENT'S ADDRESS: a shown code could not sign them in (the verification refuses it), and
	// showing it would only put another person's code in the desk's hands. Refused before anything is opened.
	if existing {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "EXISTING_CLIENT"})
		return
	}
	plain, err := s.otpEscrowKey.Open(sealbox.Sealed{Ciphertext: ct, Nonce: nonce, KeyID: keyID, CipherVersion: ver},
		s.tenID, s.siteID, id)
	if err != nil {
		slog.Warn("code reveal: cannot open the sealed copy")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "CODE_COPY_UNAVAILABLE"})
		return
	}
	// RECORDED BEFORE THE CODE LEAVES (0114): the verification this code completes is staff-assisted. If the
	// record cannot be written the code is not shown.
	if _, err := s.db.Exec(r.Context(), `INSERT INTO iam_v2.otp_code_reveals (challenge_id, tenant_id, site_id, revealed_by)
	     VALUES ($1::uuid,$2,$3,$4) ON CONFLICT (challenge_id) DO NOTHING`, id, s.tenID, s.siteID, strings.TrimSpace(req.Operator)); err != nil {
		slog.Error("code reveal: record failed", "err", err)
		httpErr(w, http.StatusInternalServerError, "reveal failed")
		return
	}
	slog.Info("code revealed to an operator") // never the code, never the address
	writeJSON(w, http.StatusOK, map[string]any{"code": string(plain), "expires_at": expires, "device_mac": mac})
}
