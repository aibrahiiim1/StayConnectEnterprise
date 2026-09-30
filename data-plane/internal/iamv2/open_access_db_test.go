package iamv2

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

func openAccess(t *testing.T) (*OpenAccess, seed) {
	t.Helper()
	db := p2DB(t)
	s := seedFreeCommerce(t, db, func(o *seedOpts) {
		// A free package a client may take ONCE (the device's history decides for an anonymous subject).
		o.rules = `[{"type":"PRIOR_PURCHASE","value":{"forbids_prior":true}}]`
	})
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return &OpenAccess{DB: db, Key: k, KeyID: "test"}, s
}

func TestOpenAccessMintsResumesAndRecovers(t *testing.T) {
	o, _ := openAccess(t)
	ctx := context.Background()
	req := OpenRequest{TenantID: p2Tenant, SiteID: p2Site, ApplianceID: p2Appl, MAC: "02:aa:00:00:00:01", IP: "10.77.0.9", GuestNetworkID: p2GN}
	first, err := o.Begin(ctx, req)
	if err != nil || first.SubjectID == "" || first.NewResumeToken == "" || first.NewRecoveryCode == "" || first.Resumed {
		t.Fatalf("mint: %+v %v", first, err)
	}
	// The subject is opaque: nothing about it is the MAC.
	var n int
	_ = o.DB.QueryRow(ctx, `SELECT count(*) FROM iam_v2.anonymous_access_subjects WHERE id=$1`, first.SubjectID).Scan(&n)
	if n != 1 || strings.Contains(first.SubjectID, "02:aa") {
		t.Fatal("the subject must be an opaque server-generated row")
	}
	// Only HMACs are stored.
	_ = o.DB.QueryRow(ctx, `SELECT count(*) FROM iam_v2.anonymous_subject_credentials WHERE secret_hmac = convert_to($1,'UTF8')`, first.NewResumeToken).Scan(&n)
	if n != 0 {
		t.Fatal("a resume token must never be stored in the clear")
	}
	// Another device resumes the same subject with the token, and with the recovery code (any spacing / case).
	other := req
	other.MAC = "02:aa:00:00:00:02"
	other.ResumeToken = first.NewResumeToken
	r2, err := o.Begin(ctx, other)
	if err != nil || r2.SubjectID != first.SubjectID || !r2.Resumed || r2.NewResumeToken != "" {
		t.Fatalf("resume by token: %+v %v", r2, err)
	}
	other.ResumeToken = ""
	other.RecoveryCode = strings.ToLower(strings.ReplaceAll(first.NewRecoveryCode, "-", " "))
	r3, err := o.Begin(ctx, other)
	if err != nil || r3.SubjectID != first.SubjectID {
		t.Fatalf("resume by recovery code: %+v %v", r3, err)
	}
	other.RecoveryCode = "AAAAA-AAAAA"
	if _, err := o.Begin(ctx, other); !errors.Is(err, ErrOpenRecoveryUnknown) {
		t.Fatalf("an unknown recovery code must be refused, got %v", err)
	}
}

// A new anonymous subject never resets a once-per-client limit: it is evaluated against the device.
func TestOpenAccessFreeLimitFollowsTheDevice(t *testing.T) {
	o, s := openAccess(t)
	ctx := context.Background()
	e := newEngine(t, o.DB, 5*time.Minute)
	req := OpenRequest{TenantID: p2Tenant, SiteID: p2Site, ApplianceID: p2Appl, MAC: "02:bb:00:00:00:01", IP: "10.77.0.10", GuestNetworkID: p2GN}
	first, err := o.Begin(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	q, err := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: first.AuthContextID,
		PackageID: s.packageID, DeviceID: first.DeviceID, GuestNetworkID: p2GN})
	if err != nil || q.QuoteID == "" {
		t.Fatalf("first free quote: %+v %v", q, err)
	}
	pr, err := e.ConfirmPurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: first.DeviceID, GuestNetworkID: p2GN})
	if err != nil || pr.EntitlementID == "" {
		t.Fatalf("first free grant: %+v %v", pr, err)
	}
	var subj *string
	_ = o.DB.QueryRow(ctx, `SELECT anonymous_subject_id::text FROM iam_v2.entitlements WHERE id=$1`, pr.EntitlementID).Scan(&subj)
	if subj == nil || *subj != first.SubjectID {
		t.Fatal("the entitlement subject must be the anonymous subject")
	}
	// The same device comes back: it rejoins its live access, no new subject is minted.
	again, err := o.Begin(ctx, req)
	if err != nil || again.LiveEntitlementID != pr.EntitlementID || again.SubjectID != first.SubjectID || again.NewResumeToken != "" {
		t.Fatalf("the device must rejoin its live entitlement: %+v %v", again, err)
	}
	// End that access; the device returns with no credential and gets a NEW subject -- but the package's
	// once-per-client rule still refuses it, because the device's history is what counts.
	if _, err := o.DB.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1::uuid,'TERMINATED',now(),'ADMIN')`, pr.EntitlementID); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	fresh, err := o.Begin(ctx, req)
	if err != nil || fresh.SubjectID == first.SubjectID {
		t.Fatalf("a device with no live access and no credential gets a new subject: %+v %v", fresh, err)
	}
	q2, _ := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: fresh.AuthContextID,
		PackageID: s.packageID, DeviceID: fresh.DeviceID, GuestNetworkID: p2GN})
	if q2.QuoteID != "" {
		t.Fatalf("a new anonymous subject must not reset a once-per-client free package: %+v", q2)
	}
}
