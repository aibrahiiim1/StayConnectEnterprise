package iamv2

// Open package selection: a client chooses a package without signing in. The entitlement subject is a
// server-generated opaque anonymous subject, never a MAC (a MAC identifies a Device only).
//
// A client comes back in this order: (1) the device with a live anonymous entitlement rejoins it, minting no
// new subject; (2) a resume token or recovery code resolves to its subject; (3) otherwise a new subject is
// minted and its token and code returned once. Only keyed HMACs are stored. Free-package limits are evaluated
// against the device's history, so a new subject never resets them.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/writerguard"
)

type OpenAccess struct {
	DB                 *pgxpool.Pool
	Key                []byte // HMAC key for resume tokens and recovery codes (anonymous_access.key)
	KeyID              string
	Now                func() time.Time
	CredentialLifetime time.Duration
	ContextTTL         time.Duration
}

// OpenRequest carries server-derived identity only: the MAC and IP come from the connection, never the body.
type OpenRequest struct {
	TenantID, SiteID, ApplianceID string
	MAC, IP, GuestNetworkID       string
	ResumeToken, RecoveryCode     string
}

// NewResumeToken and NewRecoveryCode are set only when a subject was minted.
type OpenResult struct {
	AuthContextID     string
	DeviceID          string
	SubjectID         string
	LiveEntitlementID string
	NewResumeToken    string
	NewRecoveryCode   string
	Resumed           bool
}

// ErrOpenRecoveryUnknown: the caller rate-limits these.
var ErrOpenRecoveryUnknown = errors.New("open access: recovery code not recognised")

func (o *OpenAccess) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

func (o *OpenAccess) mac(kind, secret string) []byte {
	h := hmac.New(sha256.New, o.Key)
	h.Write([]byte("onegate-anonymous-access:v1\x00" + kind + "\x00" + secret))
	return h.Sum(nil)
}

// recoveryAlphabet omits ambiguous characters (0/O, 1/I/L).
const recoveryAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

func newRecoveryCode() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 0, 11)
	for i, c := range b {
		if i == 5 {
			out = append(out, '-')
		}
		out = append(out, recoveryAlphabet[int(c)%len(recoveryAlphabet)])
	}
	return string(out), nil
}

func NormalizeRecoveryCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "-", "").Replace(s)
	if len(s) != 10 {
		return ""
	}
	return s[:5] + "-" + s[5:]
}

// resumeSecret keys a resume token by the token AND the device. The og_resume cookie travels in clear text on
// the HTTP fallback, so keyed by the token alone, a copied cookie would resume the subject from another
// device. The MAC comes from the kernel neighbour table, never the browser; another device uses the recovery code.
func resumeSecret(token, mac string) string {
	return token + "\x00device:" + strings.ToLower(strings.TrimSpace(mac))
}

func newResumeToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (o *OpenAccess) Begin(ctx context.Context, req OpenRequest) (OpenResult, error) {
	if len(o.Key) < 32 || req.TenantID == "" || req.SiteID == "" || req.MAC == "" || req.GuestNetworkID == "" {
		return OpenResult{}, errors.New("open access: not configured")
	}
	now := o.now()
	life := o.CredentialLifetime
	if life <= 0 {
		life = 30 * 24 * time.Hour
	}
	ttl := o.ContextTTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	var res OpenResult
	tx, err := o.DB.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := writerguard.Open(ctx, tx, writerguard.CapAuthContext); err != nil {
		return res, err
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac, first_seen, last_seen, last_ip)
		 VALUES ($1,$2,nullif($3,'')::uuid,$4::macaddr,$6,$6,nullif($5,'')::inet)
		 ON CONFLICT (tenant_id, site_id, appliance_id, mac)
		 DO UPDATE SET last_seen=$6, last_ip=nullif($5,'')::inet
		 RETURNING id::text`,
		req.TenantID, req.SiteID, req.ApplianceID, req.MAC, req.IP, now).Scan(&res.DeviceID); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO iam_v2.device_network_appearances (tenant_id, site_id, device_id, guest_network_id, first_seen, last_seen)
		 VALUES ($1,$2,$3,$4,$5,$5)
		 ON CONFLICT (device_id, guest_network_id) DO UPDATE SET last_seen=$5`,
		req.TenantID, req.SiteID, res.DeviceID, req.GuestNetworkID, now); err != nil {
		return res, err
	}
	var liveEnt, liveSubj *string
	err = tx.QueryRow(ctx, `
		SELECT e.id::text, e.anonymous_subject_id::text
		  FROM iam_v2.entitlements e
		  JOIN iam_v2.purchases pu     ON pu.id = e.purchase_id
		  JOIN iam_v2.auth_contexts ac ON ac.id = pu.auth_context_id
		 WHERE e.tenant_id=$1 AND e.site_id=$2 AND ac.device_id=$3 AND e.anonymous_subject_id IS NOT NULL
		   AND e.status='ACTIVE' AND (e.window_ends_at IS NULL OR e.window_ends_at > now())
		 ORDER BY e.activated_at DESC NULLS LAST LIMIT 1`, req.TenantID, req.SiteID, res.DeviceID).Scan(&liveEnt, &liveSubj)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return res, err
	}
	if liveSubj != nil {
		res.SubjectID, res.LiveEntitlementID, res.Resumed = *liveSubj, *liveEnt, true
	}
	resolve := func(kind, secret string) (string, error) {
		var subj string
		err := tx.QueryRow(ctx, `
			SELECT c.subject_id::text FROM iam_v2.anonymous_subject_credentials c
			 WHERE c.tenant_id=$1 AND c.site_id=$2 AND c.kind=$3 AND c.secret_hmac=$4
			   AND c.revoked_at IS NULL AND c.expires_at > $5`, req.TenantID, req.SiteID, kind, o.mac(kind, secret), now).Scan(&subj)
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return subj, err
	}
	if res.SubjectID == "" && req.ResumeToken != "" {
		s, err := resolve("RESUME", resumeSecret(req.ResumeToken, req.MAC))
		if err != nil {
			return res, err
		}
		res.SubjectID, res.Resumed = s, s != ""
	}
	if res.SubjectID == "" && req.RecoveryCode != "" {
		code := NormalizeRecoveryCode(req.RecoveryCode)
		if code == "" {
			return res, ErrOpenRecoveryUnknown
		}
		s, err := resolve("RECOVERY", code)
		if err != nil {
			return res, err
		}
		if s == "" {
			return res, ErrOpenRecoveryUnknown
		}
		res.SubjectID, res.Resumed = s, true
	}
	if res.SubjectID != "" {
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.anonymous_access_subjects SET last_resumed_at=$2 WHERE id=$1`, res.SubjectID, now); err != nil {
			return res, err
		}
		// A resumed subject with live access on ANOTHER device is joined through activation's own-sign-in path.
		if res.LiveEntitlementID == "" {
			var eid *string
			_ = tx.QueryRow(ctx, `SELECT id::text FROM iam_v2.entitlements WHERE anonymous_subject_id=$1
				AND status='ACTIVE' AND (window_ends_at IS NULL OR window_ends_at > now()) LIMIT 1`, res.SubjectID).Scan(&eid)
			if eid != nil {
				res.LiveEntitlementID = *eid
			}
		}
	} else {
		if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.anonymous_access_subjects (tenant_id, site_id) VALUES ($1,$2) RETURNING id::text`,
			req.TenantID, req.SiteID).Scan(&res.SubjectID); err != nil {
			return res, err
		}
		tok, err := newResumeToken()
		if err != nil {
			return res, err
		}
		code, err := newRecoveryCode()
		if err != nil {
			return res, err
		}
		for _, c := range []struct{ kind, secret string }{{"RESUME", resumeSecret(tok, req.MAC)}, {"RECOVERY", code}} {
			if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.anonymous_subject_credentials
				(tenant_id, site_id, subject_id, kind, secret_hmac, key_id, expires_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				req.TenantID, req.SiteID, res.SubjectID, c.kind, o.mac(c.kind, c.secret), o.KeyID, now.Add(life)); err != nil {
				return res, err
			}
		}
		res.NewResumeToken, res.NewRecoveryCode = tok, code
	}
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts
		(tenant_id, site_id, method, anonymous_subject_id, device_id, guest_network_id, expires_at, credential)
		VALUES ($1,$2,'OPEN',$3::uuid,$4::uuid,$5::uuid,$6,$7) RETURNING id::text`,
		req.TenantID, req.SiteID, res.SubjectID, res.DeviceID, req.GuestNetworkID, now.Add(ttl), CredentialOpen).Scan(&res.AuthContextID); err != nil {
		return res, err
	}
	if err := tx.Commit(ctx); err != nil {
		return OpenResult{}, err
	}
	return res, nil
}
