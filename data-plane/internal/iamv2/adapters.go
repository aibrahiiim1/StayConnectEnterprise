package iamv2

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

// authAccount validates a username/password against iam_v2.guest_access_accounts (argon2id) and,
// on success, creates a one-time ACCOUNT auth_context. Scratch/enabled only.
func (a *Authenticator) authAccount(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Username) == "" || req.Secret == "" {
		return deny(MethodAccount, "missing_credentials"), nil
	}
	now := a.now()
	var res Result
	err := a.repo.WithTx(ctx, func(tx Tx) error {
		id, hash, enabled, vf, vu, locked, err := tx.LookupAccount(ctx, req.TenantID, strings.ToLower(strings.TrimSpace(req.Username)))
		if err != nil {
			return err
		}
		if id == "" || !enabled {
			// constant-work: still verify against a dummy hash to reduce user enumeration signal
			_, _ = verifyArgon2id(req.Secret, dummyArgon2)
			res = deny(MethodAccount, "invalid_credentials")
			return nil
		}
		if locked != nil && locked.After(now) {
			res = deny(MethodAccount, "locked")
			return nil
		}
		if (vf != nil && now.Before(*vf)) || (vu != nil && now.After(*vu)) {
			res = deny(MethodAccount, "expired")
			return nil
		}
		ok, err := verifyArgon2id(req.Secret, hash)
		if err != nil || !ok {
			res = deny(MethodAccount, "invalid_credentials")
			return nil
		}
		res, err = a.finalize(ctx, tx, MethodAccount, Subject{GuestAccountID: id}, req, now)
		return err
	})
	if err != nil {
		return Result{}, &Error{Code: ErrRepo, Msg: "account"}
	}
	return res, nil
}

// authVoucher validates a voucher code (blind-index HMAC) against iam_v2.vouchers and creates a
// one-time VOUCHER auth_context. Scratch/enabled only.
func (a *Authenticator) authVoucher(ctx context.Context, req Request) (Result, error) {
	if req.Secret == "" {
		return deny(MethodVoucher, "missing_code"), nil
	}
	if a.vhmac == nil {
		return Result{}, &Error{Code: ErrConfig, Msg: "no voucher HMAC configured"}
	}
	now := a.now()
	h, err := a.vhmac(ctx, req.TenantID, req.SiteID, req.Secret)
	if err != nil {
		return Result{}, &Error{Code: ErrConfig, Msg: "voucher hmac"}
	}
	var res Result
	err = a.repo.WithTx(ctx, func(tx Tx) error {
		id, redeemable, err := tx.ResolveVoucherByHMAC(ctx, req.TenantID, req.SiteID, h, now)
		if err != nil {
			return err
		}
		if id == "" {
			res = deny(MethodVoucher, "invalid_code")
			return nil
		}
		if !redeemable {
			res = deny(MethodVoucher, "not_redeemable")
			return nil
		}
		res, err = a.finalize(ctx, tx, MethodVoucher, Subject{VoucherID: id}, req, now)
		return err
	})
	if err != nil {
		return Result{}, &Error{Code: ErrRepo, Msg: "voucher"}
	}
	return res, nil
}

// authOTPIdentity resolves/creates a principal from an already-verified OTP factor (EMAIL/PHONE) and
// creates a one-time OTP auth_context. The OTP challenge itself is verified upstream (public.auth_otps
// stays transient per D2); this adapter only maps the verified factor to an iam_v2 principal identity.
func (a *Authenticator) authOTPIdentity(ctx context.Context, req Request) (Result, error) {
	ft := strings.ToUpper(strings.TrimSpace(req.FactorType))
	if ft != "EMAIL" && ft != "PHONE" {
		return Result{}, &Error{Code: ErrInvalidInput, Msg: "otp factor type"}
	}
	if strings.TrimSpace(req.FactorValue) == "" {
		return deny(MethodOTP, "missing_identity"), nil
	}
	return a.resolvePrincipalAndFinalize(ctx, MethodOTP, ft, "", req)
}

// authSocialIdentity resolves/creates a principal from a verified social identity (issuer-scoped).
// The Stub provider is refused in production before this is reached.
func (a *Authenticator) authSocialIdentity(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.FactorIssuer) == "" || strings.TrimSpace(req.FactorValue) == "" {
		return deny(MethodSocial, "missing_identity"), nil
	}
	return a.resolvePrincipalAndFinalize(ctx, MethodSocial, "SOCIAL_SUBJECT", req.FactorIssuer, req)
}

func (a *Authenticator) resolvePrincipalAndFinalize(ctx context.Context, m Method, factorType, issuer string, req Request) (Result, error) {
	now := a.now()
	var res Result
	err := a.repo.WithTx(ctx, func(tx Tx) error {
		pid, err := tx.ResolvePrincipalByIdentity(ctx, req.TenantID, factorType, issuer,
			strings.ToLower(strings.TrimSpace(req.FactorValue)), now)
		if err != nil {
			return err
		}
		if pid == "" {
			res = deny(m, "subject_resolve")
			return nil
		}
		res, err = a.finalize(ctx, tx, m, Subject{PrincipalID: pid}, req, now)
		return err
	})
	if err != nil {
		return Result{}, &Error{Code: ErrRepo, Msg: string(m)}
	}
	return res, nil
}

// finalize upserts the device and creates the one-time auth_context, returning an allow Result.
func (a *Authenticator) finalize(ctx context.Context, tx Tx, m Method, subj Subject, req Request, now time.Time) (Result, error) {
	var deviceID string
	if req.Device.MAC != "" {
		id, err := tx.UpsertDevice(ctx, req.TenantID, req.SiteID, req.Device.ApplianceID, req.Device.MAC, req.Device.GuestNetworkID, req.Device.IP, now)
		if err != nil {
			return Result{}, err
		}
		deviceID = id
	}
	acID, err := tx.CreateAuthContext(ctx, AuthContextSpec{
		TenantID: req.TenantID, SiteID: req.SiteID, Method: m, Subject: subj,
		DeviceID: deviceID, GuestNetworkID: req.Device.GuestNetworkID, TTL: a.ttl, Now: now,
	})
	if err != nil {
		return Result{}, err
	}
	a.obs.Event("iamv2.allow", map[string]string{"method": string(m)})
	return Result{Decision: DecisionAllow, Method: m, Subject: subj, DeviceID: deviceID, AuthContextID: acID, Reason: "ok"}, nil
}

func deny(m Method, reason string) Result {
	return Result{Decision: DecisionDeny, Method: m, Reason: reason}
}

// ---- argon2id (PHC $argon2id$v=19$m=..,t=..,p=..$salt$hash) — matches the existing scd format ----

// dummyArgon2 is a valid hash used for constant-work verification of unknown accounts.
var dummyArgon2 = mustDummy()

func mustDummy() string {
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey([]byte("x"), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", 64*1024, 1, 4,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

// Argon2id parameter bounds — sized around the hashes StayConnect actually issues (m=65536 KiB,
// t=1, p=4, 16-byte salt, 32-byte digest) with headroom, and capped to prevent resource exhaustion
// / integer overflow from a malformed or hostile hash.
const (
	argonMinMem    = 8      // KiB
	argonMaxMem    = 262144 // 256 MiB KiB
	argonMinT      = 1
	argonMaxT      = 10
	argonMinP      = 1
	argonMaxP      = 16
	argonMinSalt   = 8
	argonMaxSalt   = 64
	argonMinDigest = 16
	argonMaxDigest = 64
)

func verifyArgon2id(pw, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, &Error{Code: ErrInvalidCred, Msg: "hash format"}
	}
	if parts[2] != "v=19" {
		return false, &Error{Code: ErrInvalidCred, Msg: "hash version"}
	}
	var m, t, p uint32
	if n, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || n != 3 {
		return false, &Error{Code: ErrInvalidCred, Msg: "hash params"}
	}
	// Bound every parameter BEFORE calling argon2.IDKey (avoid memory/CPU exhaustion, uint8 overflow).
	if m < argonMinMem || m > argonMaxMem || t < argonMinT || t > argonMaxT || p < argonMinP || p > argonMaxP {
		return false, &Error{Code: ErrInvalidCred, Msg: "hash params out of range"}
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < argonMinSalt || len(salt) > argonMaxSalt {
		return false, &Error{Code: ErrInvalidCred, Msg: "hash salt"}
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < argonMinDigest || len(want) > argonMaxDigest {
		return false, &Error{Code: ErrInvalidCred, Msg: "hash digest"}
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, uint8(p), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var _ = strconv.Itoa // reserved for future numeric parsing
