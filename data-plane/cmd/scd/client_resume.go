package main

// The remembered device ("Welcome back"; docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md 2.3): after a factor is proven on a device,
// scd issues a 256-bit device-bound resume credential stored only as a keyed HMAC; the plaintext lives in the portal's HttpOnly cookie.
// Skipping the code is safe because it works only from the issuing device (MAC resolved from the kernel neighbour table; a mismatch refuses AND revokes),
// exists only after a verified factor, expires per the site's Remember-devices setting (0 disables), and is revoked by sign-out, expiry, mismatch or operator disconnect.
// It never decides eligibility: stored factors decide the group at resume time as a fresh code would.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
)

// Same key as open selection's resume tokens under its own domain string, so neither credential family can be presented as the other.
const (
	clientResumeKeyFile = "anonymous_access.key"
	clientResumeDomain  = "onegate-client-resume:v1"
	clientResumeKeyID   = "client-resume-1"
)

func (s *server) initClientResume(secretsDir string) {
	key, err := localkeys.LoadExistingKey(filepath.Join(secretsDir, clientResumeKeyFile))
	if err != nil {
		slog.Warn("remembered devices unavailable: key missing (run keybootstrap at deploy)", "err", err)
		return
	}
	s.resumeKey = key
	s.resumeLimiter = &recoveryLimiter{hits: map[string][]time.Time{}}
}

func (s *server) resumeHMAC(token string) []byte {
	h := hmac.New(sha256.New, s.resumeKey)
	h.Write([]byte(clientResumeDomain + "\x00" + token))
	return h.Sum(nil)
}

func newClientResumeToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *server) rememberDeviceDays(ctx context.Context) int {
	var cfg *tenantcfg.AuthMethods
	var err error
	if s.methodSwitches != nil {
		cfg, err = s.methodSwitches(ctx)
	} else if s.db != nil {
		cfg, err = tenantcfg.Load(ctx, s.db, s.tenID)
	}
	if err != nil || cfg == nil {
		return 0 // unreadable settings: do not remember (fail closed), do not break the sign-in
	}
	return cfg.RememberDeviceDays()
}

// issueResumeCredential returns the plaintext token for the portal cookie, or "" when devices are not remembered; it revokes the previous credential so one device holds one.
func (s *server) issueResumeCredential(ctx context.Context, principalID, deviceID string, method iamv2.Method, credential string) string {
	if len(s.resumeKey) < 32 || s.db == nil || principalID == "" || deviceID == "" {
		return ""
	}
	if method != iamv2.MethodOTP && method != iamv2.MethodSocial {
		return ""
	}
	days := s.rememberDeviceDays(ctx)
	if days <= 0 {
		return ""
	}
	token, err := newClientResumeToken()
	if err != nil {
		return ""
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ""
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.principal_device_credentials SET revoked_at = now()
	     WHERE tenant_id=$1 AND site_id=$2 AND guest_principal_id=$3::uuid AND device_id=$4::uuid AND revoked_at IS NULL`,
		s.tenID, s.siteID, principalID, deviceID); err != nil {
		slog.Warn("remembered device: revoke previous failed", "err", err)
		return ""
	}
	// The proven credential travels with the remembered device so a resumed sign-in reports as what it originally was.
	var cred *string
	if c := iamv2.NormalizeCredential(credential); c != "" {
		cred = &c
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.principal_device_credentials
	     (tenant_id, site_id, guest_principal_id, device_id, method, secret_hmac, key_id, expires_at, credential)
	     VALUES ($1,$2,$3::uuid,$4::uuid,$5,$6,$7, now() + ($8::int * interval '1 day'), $9)`,
		s.tenID, s.siteID, principalID, deviceID, string(method), s.resumeHMAC(token), clientResumeKeyID, days, cred); err != nil {
		slog.Warn("remembered device: issue failed", "err", err)
		return ""
	}
	if err := tx.Commit(ctx); err != nil {
		return ""
	}
	return token
}

type resumeRow struct {
	id, principalID, deviceID, method, deviceMAC string
	// credential is what the device proved when remembered; "" if issued before it was recorded.
	credential string
}

var errResumeUnknown = errors.New("resume credential unknown")

func (s *server) lookupResume(ctx context.Context, token string) (resumeRow, error) {
	var r resumeRow
	err := s.db.QueryRow(ctx, `SELECT c.id::text, c.guest_principal_id::text, c.device_id::text, c.method, d.mac::text, COALESCE(c.credential,'')
	      FROM iam_v2.principal_device_credentials c
	      JOIN iam_v2.devices d ON d.tenant_id=c.tenant_id AND d.site_id=c.site_id AND d.id=c.device_id
	     WHERE c.tenant_id=$1 AND c.site_id=$2 AND c.secret_hmac=$3 AND c.revoked_at IS NULL AND c.expires_at > now()`,
		s.tenID, s.siteID, s.resumeHMAC(token)).Scan(&r.id, &r.principalID, &r.deviceID, &r.method, &r.deviceMAC, &r.credential)
	if errors.Is(err, pgx.ErrNoRows) {
		return resumeRow{}, errResumeUnknown
	}
	return r, err
}

func (s *server) revokeResumeByID(ctx context.Context, id string) {
	_, _ = s.db.Exec(ctx, `UPDATE iam_v2.principal_device_credentials SET revoked_at = now() WHERE id=$1::uuid AND revoked_at IS NULL`, id)
}

// resumeMethodOffered: a site that turned its code methods off must not keep letting remembered devices in.
func (s *server) resumeMethodOffered(ctx context.Context, method string) bool {
	var cfg *tenantcfg.AuthMethods
	var err error
	if s.methodSwitches != nil {
		cfg, err = s.methodSwitches(ctx)
	} else {
		cfg, err = tenantcfg.Load(ctx, s.db, s.tenID)
	}
	if err != nil || cfg == nil {
		return false
	}
	s.applyLicenseToMethods(cfg)
	on := func(m *tenantcfg.AuthMethod) bool { return m != nil && m.Enabled }
	switch method {
	case string(iamv2.MethodOTP):
		return on(cfg.SMS) || on(cfg.WhatsApp)
	case string(iamv2.MethodSocial):
		for _, m := range cfg.Social {
			if on(m) {
				return true
			}
		}
	}
	return false
}

func (s *server) principalIdentityLabel(ctx context.Context, principalID, method string) string {
	rows, err := s.db.Query(ctx, `SELECT factor_type, factor_issuer, factor_value_norm, COALESCE(attrs,'{}'::jsonb)::text
	      FROM iam_v2.guest_principal_identities WHERE tenant_id=$1 AND guest_principal_id=$2::uuid ORDER BY verified_at, id`,
		s.tenID, principalID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	var factors []iamv2.FactorClaim
	for rows.Next() {
		var f iamv2.FactorClaim
		var raw string
		if rows.Scan(&f.Type, &f.Issuer, &f.Value, &raw) != nil {
			continue
		}
		f.Attrs = map[string]string{}
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			for k, v := range m {
				if sv, ok := v.(string); ok {
					f.Attrs[k] = sv
				}
			}
		}
		factors = append(factors, f)
	}
	if len(factors) == 0 {
		return ""
	}
	want := "SOCIAL_SUBJECT"
	if method == string(iamv2.MethodOTP) {
		want = "EMAIL"
	}
	for _, f := range factors {
		if f.Type == want {
			return iamv2.MaskIdentity(f)
		}
	}
	for _, f := range factors {
		if method == string(iamv2.MethodOTP) && f.Type == "PHONE" {
			return iamv2.MaskIdentity(f)
		}
	}
	return iamv2.MaskIdentity(factors[0])
}

type resumeReq struct {
	IP          string `json:"ip"`
	MAC         string `json:"mac"`
	ResumeToken string `json:"resume_token"`
}

// resumeBinding binds a credential to the calling device; on a MAC mismatch it is revoked.
func (s *server) resumeBinding(w http.ResponseWriter, r *http.Request) (resumeReq, resumeRow, net.IP, bool) {
	var req resumeReq
	if len(s.resumeKey) < 32 || s.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "not_available"})
		return req, resumeRow{}, nil, false
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.ResumeToken) == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "no_credential"})
		return req, resumeRow{}, nil, false
	}
	ip := net.ParseIP(req.IP)
	mac, merr := net.ParseMAC(req.MAC)
	if ip == nil || ip.To4() == nil || merr != nil {
		httpErr(w, http.StatusBadRequest, "bad device")
		return req, resumeRow{}, nil, false
	}
	if s.resumeLimiter != nil && !s.resumeLimiter.allow(mac.String()) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "reason": "too_many_attempts"})
		return req, resumeRow{}, nil, false
	}
	row, err := s.lookupResume(r.Context(), strings.TrimSpace(req.ResumeToken))
	if err != nil {
		if !errors.Is(err, errResumeUnknown) {
			slog.Warn("remembered device: lookup failed", "err", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "unknown"})
		return req, resumeRow{}, nil, false
	}
	if !strings.EqualFold(row.deviceMAC, mac.String()) {
		s.revokeResumeByID(r.Context(), row.id)
		slog.Info("remembered device: credential presented from another device; revoked")
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "other_device"})
		return req, resumeRow{}, nil, false
	}
	if !s.resumeMethodOffered(r.Context(), row.method) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "method_off"})
		return req, resumeRow{}, nil, false
	}
	return req, row, ip, true
}

// POST /v1/sessions/resume-peek -- is this device remembered, and as whom? Mints nothing.
func (s *server) resumePeek(w http.ResponseWriter, r *http.Request) {
	_, row, _, ok := s.resumeBinding(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "method": row.method,
		"identity_label": s.principalIdentityLabel(r.Context(), row.principalID, row.method)})
}

// POST /v1/sessions/authorize-resume -- "Connect" on the Welcome-back line: an ordinary auth context for the
// remembered Client, through the single guest authority.
func (s *server) authorizeResume(w http.ResponseWriter, r *http.Request) {
	_, row, ip, ok := s.resumeBinding(w, r)
	if !ok {
		return
	}
	method := iamv2.Method(row.method)
	if !s.iamv2MethodEnabled(method) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "IAMV2_UNAVAILABLE", "authority": "iam_v2"})
		return
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE iam_v2.principal_device_credentials SET last_used_at = now() WHERE id=$1::uuid`, row.id)
	s.authorizeViaIAMv2(w, r, method, iamv2.Request{
		ResumedPrincipalID: row.principalID,
		Credential:         row.credential,
		Device:             iamv2.DeviceContext{MAC: row.deviceMAC},
	}, ip)
}

// POST /v1/sessions/resume-revoke -- sign-out, or "Not you?".
func (s *server) resumeRevoke(w http.ResponseWriter, r *http.Request) {
	var req resumeReq
	if len(s.resumeKey) < 32 || s.db == nil || json.NewDecoder(r.Body).Decode(&req) != nil || req.ResumeToken == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	_, _ = s.db.Exec(r.Context(), `UPDATE iam_v2.principal_device_credentials SET revoked_at = now()
	     WHERE tenant_id=$1 AND site_id=$2 AND secret_hmac=$3 AND revoked_at IS NULL`, s.tenID, s.siteID, s.resumeHMAC(req.ResumeToken))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
