package iamv2

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/passwordhash"
)

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
			// Constant work: verify against a dummy hash to limit user enumeration.
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
		pin, err := accountGroupPin(ctx, tx, req, id)
		if err != nil {
			return err
		}
		res, err = a.finalizeWithPin(ctx, tx, MethodAccount, Subject{GuestAccountID: id}, req, now, pin)
		return err
	})
	if err != nil {
		return Result{}, &Error{Code: ErrRepo, Msg: "account"}
	}
	return res, nil
}

type accountEmailReader interface {
	AccountVerifiedEmail(ctx context.Context, tenantID, siteID, accountID string) (string, error)
}

// accountGroupPin decides an Email account client's Client Group from its VERIFIED address only; an
// unverified address or a staff-created username decides nothing (Public). A lookup failure is a repository
// error, never "Public".
func accountGroupPin(ctx context.Context, tx Tx, req Request, accountID string) (groupPin, error) {
	r, ok := tx.(accountEmailReader)
	if !ok {
		return groupPin{}, nil
	}
	email, err := r.AccountVerifiedEmail(ctx, req.TenantID, req.SiteID, accountID)
	if err != nil || email == "" {
		return groupPin{}, err
	}
	groups, err := tx.LoadClientGroups(ctx, req.TenantID, req.SiteID)
	if err != nil {
		return groupPin{}, err
	}
	d, ok := EvaluateClientGroup(groups, []FactorClaim{{Type: "EMAIL", Value: email,
		Attrs: map[string]string{"source": "email_account"}}})
	if !ok {
		return groupPin{}, nil
	}
	return groupPin{id: d.GroupID, name: d.GroupName, evidence: d.Evidence}, nil
}

func (a *Authenticator) authVoucher(ctx context.Context, req Request) (Result, error) {
	if req.Secret == "" {
		return deny(MethodVoucher, "missing_code"), nil
	}
	if a.vhmac == nil && a.vhmacAll == nil {
		return Result{}, &Error{Code: ErrConfig, Msg: "no voucher HMAC configured"}
	}
	now := a.now()
	// A voucher's index is computed under the key generation it pins, so every usable generation contributes
	// a candidate index, newest first; the first that resolves wins.
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

// authOTPIdentity maps an OTP factor already verified upstream to a principal identity.
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
	if req.StaffAssisted {
		primary.Attrs[AssuranceKey] = AssuranceStaffAssisted
		req.Credential = AssistedCredential(req.Credential)
	}
	return a.resolvePrincipalAndFinalize(ctx, MethodOTP, primary, req.SecondaryFactors, req)
}

// authSocialIdentity: the (issuer, sub) subject is the identity; a trusted issuer's verified email is a second
// factor that links the sign-in to an existing Client. The Stub provider is refused in production upstream.
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

// authResumedIdentity: the resume credential was verified by scd; no factor is re-proven, and the Client's
// STORED factors decide the group.
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
		// A staff-shown code never signs in to an existing Client: the client did not prove the address.
		// Rolled back so nothing the resolution touched (an attrs merge) survives.
		if req.StaffAssisted && !rs.Created {
			res = deny(m, "assisted_existing_client")
			return errAssistedRollback
		}
		factors, err := tx.LoadPrincipalFactors(ctx, req.TenantID, rs.PrincipalID)
		if err != nil {
			return err
		}
		if len(factors) == 0 {
			// A repository that stored nothing still gets an answer from the factor just proven.
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
	if errors.Is(err, errAssistedRollback) {
		return res, nil
	}
	if err != nil {
		return Result{}, &Error{Code: ErrRepo, Msg: string(m)}
	}
	return res, nil
}

var errAssistedRollback = errors.New("iamv2: staff-assisted sign-in reached an existing client")

// finalizeWithGroup pins the Client Group decision on the auth context. A group lookup failure is a
// repository error (fail closed), never "Public".
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

func (a *Authenticator) finalize(ctx context.Context, tx Tx, m Method, subj Subject, req Request, now time.Time) (Result, error) {
	return a.finalizeWithPin(ctx, tx, m, subj, req, now, groupPin{})
}

// finalizeWithPin pins a Client Group decision (empty for voucher, account and open selection).
func (a *Authenticator) finalizeWithPin(ctx context.Context, tx Tx, m Method, subj Subject, req Request, now time.Time, pin groupPin) (Result, error) {
	// Validate up front so a bad request never reaches the DB as a NOT NULL / FK violation.
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
		Credential: NormalizeCredential(req.Credential),
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

// dummyArgon2 gives unknown accounts the same verification work as known ones.
var dummyArgon2 = passwordhash.Dummy()

// verifyArgon2id reports an unusable stored hash as an invalid credential, never a match or a "try later" fault.
func verifyArgon2id(pw, encoded string) (bool, error) {
	ok, err := passwordhash.Verify(pw, encoded)
	if err != nil {
		return false, &Error{Code: ErrInvalidCred, Msg: err.Error()}
	}
	return ok, nil
}
