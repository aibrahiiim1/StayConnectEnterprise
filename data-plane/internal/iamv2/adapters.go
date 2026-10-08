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
		id, hash, enabled, vf, vu, locked, err := tx.LookupAccount(ctx, req.TenantID, req.SiteID, strings.ToLower(strings.TrimSpace(req.Username)))
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
	if a.vhmac == nil && a.vhmacAll == nil {
		return Result{}, &Error{Code: ErrConfig, Msg: "no voucher HMAC configured"}
	}
	now := a.now()
	// ROTATION. A voucher's stored index was computed under the key of the generation it pins, so after a
	// rotation the active key alone cannot match a voucher issued under the previous one. Every usable
	// generation therefore contributes a candidate index, newest first, and the first that resolves wins.
	// With a single generation this is exactly the old single-index behaviour.
	var candidates [][]byte
	if a.vhmacAll != nil {
		var err error
		if candidates, err = a.vhmacAll(ctx, req.TenantID, req.SiteID, req.Secret); err != nil {
			return Result{}, &Error{Code: ErrConfig, Msg: "voucher hmac"}
		}
	} else {
		h, err := a.vhmac(ctx, req.TenantID, req.SiteID, req.Secret)
		if err != nil {
			return Result{}, &Error{Code: ErrConfig, Msg: "voucher hmac"}
		}
		candidates = [][]byte{h}
	}
	if len(candidates) == 0 {
		return deny(MethodVoucher, "invalid_code"), nil
	}
	var res Result
	err := a.repo.WithTx(ctx, func(tx Tx) error {
		var id string
		var redeemable bool
		for _, h := range candidates {
			var err error
			id, redeemable, err = tx.ResolveVoucherByHMAC(ctx, req.TenantID, req.SiteID, h, now)
			if err != nil {
				return err
			}
			if id != "" {
				break
			}
		}
		if id == "" {
			res = deny(MethodVoucher, "invalid_code")
			return nil
		}
		if !redeemable {
			res = deny(MethodVoucher, "not_redeemable")
			return nil
		}
		var ferr error
		res, ferr = a.finalize(ctx, tx, MethodVoucher, Subject{VoucherID: id}, req, now)
		return ferr
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
	primary := OTPFactor(ft, req.FactorValue)
	for k, v := range req.FactorAttrs {
		primary.Attrs[k] = v
	}
	return a.resolvePrincipalAndFinalize(ctx, MethodOTP, primary, req.SecondaryFactors, req)
}

// authSocialIdentity resolves/creates a principal from a verified social identity: the (issuer, sub) subject
// is the identity; a trusted issuer's verified email is a second factor on the same Client and is what links
// a Google sign-in to the Client an email code created (identity.go). The Stub provider is refused in
// production before this is reached.
func (a *Authenticator) authSocialIdentity(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.FactorIssuer) == "" || strings.TrimSpace(req.FactorValue) == "" {
		return deny(MethodSocial, "missing_identity"), nil
	}
	primary := FactorClaim{Type: "SOCIAL_SUBJECT", Issuer: strings.ToLower(strings.TrimSpace(req.FactorIssuer)),
		Value: strings.TrimSpace(req.FactorValue), Attrs: map[string]string{"source": strings.ToLower(strings.TrimSpace(req.FactorIssuer))}}
	for k, v := range req.FactorAttrs {
		primary.Attrs[k] = v
	}
	return a.resolvePrincipalAndFinalize(ctx, MethodSocial, primary, req.SecondaryFactors, req)
}

// authResumedIdentity mints an auth context for a Client whose device-bound resume credential scd has already
// verified (contract §2.3). No factor is re-proven here; the Client's STORED factors decide the group, so a
// remembered device gets exactly the eligibility a fresh code would.
func (a *Authenticator) authResumedIdentity(ctx context.Context, req Request) (Result, error) {
	now := a.now()
	var res Result
	err := a.repo.WithTx(ctx, func(tx Tx) error {
		factors, err := tx.LoadPrincipalFactors(ctx, req.TenantID, req.ResumedPrincipalID)
		if err != nil {
			return err
		}
		if len(factors) == 0 {
			res = deny(req.Method, "subject_resolve") // a principal with no verified factor is not a Client
			return nil
		}
		res, err = a.finalizeWithGroup(ctx, tx, req.Method, req.ResumedPrincipalID, factors, req, now)
		return err
	})
	if err != nil {
		return Result{}, &Error{Code: ErrRepo, Msg: string(req.Method)}
	}
	res.Reason = "resumed"
	return res, nil
}

func (a *Authenticator) resolvePrincipalAndFinalize(ctx context.Context, m Method, primary FactorClaim, secondary []FactorClaim, req Request) (Result, error) {
	now := a.now()
	var res Result
	err := a.repo.WithTx(ctx, func(tx Tx) error {
		rs, err := tx.ResolvePrincipalByFactors(ctx, req.TenantID, primary, secondary, now)
		if err != nil {
			return err
		}
		if rs.PrincipalID == "" {
			res = deny(m, "subject_resolve")
			return nil
		}
		factors, err := tx.LoadPrincipalFactors(ctx, req.TenantID, rs.PrincipalID)
		if err != nil {
			return err
		}
		if len(factors) == 0 {
			// The repository contract says the primary factor is stored by now; a fake that stores nothing
			// still gets a usable answer from the factor just proven.
			factors = []FactorClaim{primary}
		}
		res, err = a.finalizeWithGroup(ctx, tx, m, rs.PrincipalID, factors, req, now)
		if err != nil {
			return err
		}
		res.IdentityLabel = MaskIdentity(primary)
		res.IdentityLinked, res.IdentityConflicts = rs.Linked, rs.Conflicts
		if rs.Created {
			a.obs.Event("iamv2.identity.created", map[string]string{"factor": primary.Key()})
		}
		for _, l := range rs.Linked {
			a.obs.Event("iamv2.identity.linked", map[string]string{"factor": l, "via": primary.Key()})
		}
		for _, c := range rs.Conflicts {
			a.obs.Event("iamv2.identity.link_conflict", map[string]string{"factor": c, "via": primary.Key()})
		}
		return nil
	})
	if err != nil {
		return Result{}, &Error{Code: ErrRepo, Msg: string(m)}
	}
	return res, nil
}

// finalizeWithGroup decides the Client Group from the Client's verified factors, then creates the auth context
// with that decision pinned on it. A group lookup failure is a repository error (fail closed), never "Public".
func (a *Authenticator) finalizeWithGroup(ctx context.Context, tx Tx, m Method, principalID string, factors []FactorClaim, req Request, now time.Time) (Result, error) {
	groups, err := tx.LoadClientGroups(ctx, req.TenantID, req.SiteID)
	if err != nil {
		return Result{}, err
	}
	var spec groupPin
	if d, ok := EvaluateClientGroup(groups, factors); ok {
		spec = groupPin{id: d.GroupID, name: d.GroupName, evidence: d.Evidence}
	}
	res, err := a.finalizeWithPin(ctx, tx, m, Subject{PrincipalID: principalID}, req, now, spec)
	if err != nil {
		return Result{}, err
	}
	if len(factors) > 0 && res.IdentityLabel == "" {
		res.IdentityLabel = MaskIdentity(primaryFactorFor(m, factors))
	}
	return res, nil
}

type groupPin struct {
	id, name string
	evidence map[string]any
}

// primaryFactorFor picks the factor to label a resumed Client by: the kind the method proved.
func primaryFactorFor(m Method, factors []FactorClaim) FactorClaim {
	want := "SOCIAL_SUBJECT"
	if m == MethodOTP {
		want = "EMAIL"
	}
	for _, f := range factors {
		if f.Type == want {
			return f
		}
	}
	for _, f := range factors {
		if m == MethodOTP && f.Type == "PHONE" {
			return f
		}
	}
	return factors[0]
}

// finalize upserts the device and creates the one-time auth_context, returning an allow Result.
func (a *Authenticator) finalize(ctx context.Context, tx Tx, m Method, subj Subject, req Request, now time.Time) (Result, error) {
	return a.finalizeWithPin(ctx, tx, m, subj, req, now, groupPin{})
}

// finalizeWithPin is finalize with a Client Group decision pinned on the context (empty for subjects that have
// no identity factors: voucher, account, open selection).
func (a *Authenticator) finalizeWithPin(ctx context.Context, tx Tx, m Method, subj Subject, req Request, now time.Time, pin groupPin) (Result, error) {
	// Validate the caller-supplied pins (tenant/site/device/guest-network/TTL/subject) up front so a
	// bad request never reaches the DB as a NOT NULL / FK violation. A device MAC and guest network are
	// mandatory because auth_contexts pins both NOT NULL.
	if req.Device.MAC == "" {
		return Result{}, &Error{Code: ErrInvalidInput, Msg: "auth_context: missing device"}
	}
	if req.Device.GuestNetworkID == "" {
		return Result{}, &Error{Code: ErrInvalidInput, Msg: "auth_context: missing guest network"}
	}
	if _, ok := subj.subjectFor(m); !ok {
		return Result{}, &Error{Code: ErrInvalidInput, Msg: "auth_context: subject not compatible with method"}
	}
	if a.ttl <= 0 {
		return Result{}, &Error{Code: ErrInvalidInput, Msg: "auth_context: non-positive TTL"}
	}
	id, err := tx.UpsertDevice(ctx, req.TenantID, req.SiteID, req.Device.ApplianceID, req.Device.MAC, req.Device.GuestNetworkID, req.Device.IP, now)
	if err != nil {
		return Result{}, err
	}
	deviceID := id
	spec := AuthContextSpec{
		TenantID: req.TenantID, SiteID: req.SiteID, Method: m, Subject: subj,
		DeviceID: deviceID, GuestNetworkID: req.Device.GuestNetworkID, TTL: a.ttl, Now: now,
		ClientGroupID: pin.id, ClientGroupEvidence: pin.evidence,
	}
	if err := spec.validate(); err != nil {
		return Result{}, err
	}
	acID, err := tx.CreateAuthContext(ctx, spec)
	if err != nil {
		return Result{}, err
	}
	a.obs.Event("iamv2.allow", map[string]string{"method": string(m)})
	return Result{Decision: DecisionAllow, Method: m, Subject: subj, DeviceID: deviceID, AuthContextID: acID, Reason: "ok",
		ClientGroupID: pin.id, ClientGroupName: pin.name}, nil
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
