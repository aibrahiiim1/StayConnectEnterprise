package iamv2

import (
	"context"
	"strings"
	"testing"
	"time"
)

// THE CONTRACT, AGAINST A REAL DATABASE (docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md).
// Needs IAMV2_TEST_DSN / PHASE2_TEST_DSN (scripts/sitedb-dev.sh up); skipped otherwise.

func TestAVerifiedEmailLinksAGoogleSignInToTheSameClient(t *testing.T) {
	db := scratchDB(t)
	ctx := context.Background()
	repo := NewPgRepository(db)
	now := time.Now()
	resolve := func(p FactorClaim, sec []FactorClaim) PrincipalResolution {
		t.Helper()
		var rs PrincipalResolution
		if err := repo.WithTx(ctx, func(tx Tx) error {
			var err error
			rs, err = tx.ResolvePrincipalByFactors(ctx, testTenant, p, sec, now)
			return err
		}); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		return rs
	}
	factors := func(pid string) []FactorClaim {
		t.Helper()
		var out []FactorClaim
		_ = repo.WithTx(ctx, func(tx Tx) error {
			var err error
			out, err = tx.LoadPrincipalFactors(ctx, testTenant, pid)
			return err
		})
		return out
	}

	// 1. An email code creates the Client.
	alice := resolve(OTPFactor("EMAIL", "Alice@Example.com"), nil)
	if alice.PrincipalID == "" || !alice.Created {
		t.Fatalf("first code must create: %+v", alice)
	}
	// 2. Google, asserting the same verified email: the SAME Client, with the subject now attached.
	p, sec := SocialFactors("google", "sub-alice", "alice@example.com", true, map[string]string{"hd": "example.com"})
	g := resolve(p, sec)
	if g.PrincipalID != alice.PrincipalID || g.Created {
		t.Fatalf("google must link to the email Client, got %+v (alice %s)", g, alice.PrincipalID)
	}
	fs := factors(alice.PrincipalID)
	var social, email int
	for _, f := range fs {
		switch f.Type {
		case "SOCIAL_SUBJECT":
			social++
			if f.Value != "sub-alice" || f.Attrs["hd"] != "example.com" || f.Attrs["email"] != "alice@example.com" {
				t.Fatalf("the subject row must carry the verified claims: %+v", f)
			}
		case "EMAIL":
			email++
		}
	}
	if social != 1 || email != 1 {
		t.Fatalf("one Client, two factors; got %+v", fs)
	}
	// 3. The same subject again, now without an email at all (a provider that stopped sharing it): still her.
	again := resolve(FactorClaim{Type: "SOCIAL_SUBJECT", Issuer: "google", Value: "sub-alice"}, nil)
	if again.PrincipalID != alice.PrincipalID {
		t.Fatal("the subject is the identity; the email was only ever a second factor")
	}
	// 4. Facebook with the same email links NOTHING: it is a different Client, keyed by its own subject.
	fp, fsec := SocialFactors("facebook", "fb-1", "alice@example.com", true, nil)
	fb := resolve(fp, fsec)
	if fb.PrincipalID == alice.PrincipalID || !fb.Created {
		t.Fatalf("facebook must not be believed about a mailbox: %+v", fb)
	}
	// 5. A conflict is recorded, never merged: carol signs in with Google (sub-carol, carol@); bob exists by
	// code; then sub-carol turns up asserting bob's address. Sub wins; bob's Client is untouched.
	cp, csec := SocialFactors("google", "sub-carol", "carol@example.com", true, nil)
	carol := resolve(cp, csec)
	bob := resolve(OTPFactor("EMAIL", "bob@example.com"), nil)
	cp2, csec2 := SocialFactors("google", "sub-carol", "bob@example.com", true, nil)
	c2 := resolve(cp2, csec2)
	if c2.PrincipalID != carol.PrincipalID {
		t.Fatalf("the proven subject must win: %+v", c2)
	}
	if len(c2.Conflicts) != 1 || c2.Conflicts[0] != "EMAIL" {
		t.Fatalf("the foreign email must be a recorded conflict, got %+v", c2)
	}
	bobAgain := resolve(OTPFactor("EMAIL", "bob@example.com"), nil)
	if bobAgain.PrincipalID != bob.PrincipalID {
		t.Fatal("bob's Client must not have been moved or merged")
	}
	// 6. A row the previous code wrote (the subject keyed by the email) still finds its Client.
	var legacyPID string
	if err := db.QueryRow(ctx, `INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`, testTenant).Scan(&legacyPID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO iam_v2.guest_principal_identities (tenant_id, guest_principal_id, factor_type, factor_issuer, factor_value_norm, verified_at)
		VALUES ($1,$2,'SOCIAL_SUBJECT','google','legacy@example.com',now())`, testTenant, legacyPID); err != nil {
		t.Fatal(err)
	}
	lp, lsec := SocialFactors("google", "sub-legacy", "legacy@example.com", true, nil)
	legacy := resolve(lp, lsec)
	if legacy.PrincipalID != legacyPID || legacy.Created {
		t.Fatalf("a pre-0107 row must be honoured, got %+v want %s", legacy, legacyPID)
	}
	// ...and nothing re-created the legacy-shaped row: the lookup-only claim is never inserted.
	var n int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.guest_principal_identities WHERE factor_value_norm='legacy@example.com'`).Scan(&n)
	if n != 2 { // the legacy SOCIAL row + the new EMAIL factor
		t.Fatalf("expected the legacy row plus one EMAIL factor, got %d rows", n)
	}
}

func TestTheGroupIsDecidedFromVerifiedFactorsAndPinnedOnTheContext(t *testing.T) {
	db := scratchDB(t)
	seedGuestNetwork(t, db)
	ctx := context.Background()
	_, _ = db.Exec(ctx, `DELETE FROM iam_v2.client_groups WHERE tenant_id=$1`, testTenant)
	var gid string
	if err := db.QueryRow(ctx, `INSERT INTO iam_v2.client_groups (tenant_id, site_id, name, priority) VALUES ($1,$2,'Employees',10) RETURNING id::text`, testTenant, testSite).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO iam_v2.client_group_rules (tenant_id, site_id, group_id, rule_type, rule_value)
		VALUES ($1,$2,$3::uuid,'EMAIL_DOMAIN','{"domains":["example.com"]}'::jsonb)`, testTenant, testSite, gid); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{MasterEnabled: true, Methods: map[Method]bool{MethodOTP: true}}, NewPgRepository(db), NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	signIn := func(email string) Result {
		t.Helper()
		res, err := a.Authenticate(ctx, Request{Method: MethodOTP, TenantID: testTenant, SiteID: testSite,
			FactorType: "EMAIL", FactorValue: email,
			Device: DeviceContext{MAC: "02:00:00:00:00:77", ApplianceID: p2Appl, GuestNetworkID: testGN, IP: "10.50.0.9"}})
		if err != nil || res.Decision != DecisionAllow {
			t.Fatalf("sign in %s: %+v %v", email, res, err)
		}
		return res
	}
	staff := signIn("ann@example.com")
	if staff.ClientGroupID != gid || staff.ClientGroupName != "Employees" || staff.IdentityLabel != "a•••@example.com" {
		t.Fatalf("Employees must be decided at sign-in: %+v", staff)
	}
	pub := signIn("zed@gmail.com")
	if pub.ClientGroupID != "" {
		t.Fatalf("a public address is in no group: %+v", pub)
	}
	// The pin is on the row, with evidence that names no address, and the commerce reader sees it.
	var pinned string
	var evidence string
	if err := db.QueryRow(ctx, `SELECT COALESCE(client_group_id::text,''), COALESCE(client_group_evidence::text,'') FROM iam_v2.auth_contexts WHERE id=$1`, staff.AuthContextID).Scan(&pinned, &evidence); err != nil {
		t.Fatal(err)
	}
	if pinned != gid || evidence == "" || strings.Contains(evidence, "ann@") {
		t.Fatalf("pin %q evidence %q", pinned, evidence)
	}
	crepo := NewPgCommerceRepository(db)
	_ = crepo.WithTx(ctx, func(tx CommerceTx) error {
		row, err := tx.LoadAuthContext(ctx, testTenant, testSite, staff.AuthContextID)
		if err != nil || row.ClientGroupID != gid {
			t.Fatalf("the commerce reader must see the pin: %+v %v", row, err)
		}
		return nil
	})
	// DELETING THE GROUP CLEARS ONLY THE PIN: the context survives as Public with its evidence intact, and the
	// referential action is allowed under the auth_context operation scope (what edged opens).
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('auth_context')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.client_groups WHERE id=$1::uuid`, gid); err != nil {
		t.Fatalf("deleting a pinned group must be allowed: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var pinAfter *string
	var evAfter string
	if err := db.QueryRow(ctx, `SELECT client_group_id::text, client_group_evidence::text FROM iam_v2.auth_contexts WHERE id=$1`, staff.AuthContextID).Scan(&pinAfter, &evAfter); err != nil {
		t.Fatal(err)
	}
	if pinAfter != nil || !strings.Contains(evAfter, "Employees") {
		t.Fatalf("after delete: pin %v evidence %q", pinAfter, evAfter)
	}
	if err := db.QueryRow(ctx, `INSERT INTO iam_v2.client_groups (tenant_id, site_id, name, priority) VALUES ($1,$2,'Employees',10) RETURNING id::text`, testTenant, testSite).Scan(&gid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO iam_v2.client_group_rules (tenant_id, site_id, group_id, rule_type, rule_value)
		VALUES ($1,$2,$3::uuid,'EMAIL_DOMAIN','{"domains":["example.com"]}'::jsonb)`, testTenant, testSite, gid); err != nil {
		t.Fatal(err)
	}
	// A remembered device resumes with the SAME decision, from stored factors.
	resumed, err := a.Authenticate(ctx, Request{Method: MethodOTP, TenantID: testTenant, SiteID: testSite,
		ResumedPrincipalID: staff.Subject.PrincipalID,
		Device:             DeviceContext{MAC: "02:00:00:00:00:77", ApplianceID: p2Appl, GuestNetworkID: testGN, IP: "10.50.0.9"}})
	if err != nil || resumed.Decision != DecisionAllow || resumed.ClientGroupID != gid || resumed.Reason != "resumed" {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
}

func TestFreeAllowanceIsKeyedOnThePackageAndCountsTheDevice(t *testing.T) {
	db := p2DB(t)
	s := seedFreeCommerce(t, db, func(o *seedOpts) {
		o.rules = `[{"type":"PRIOR_PURCHASE","value":{"forbids_prior":true}}]`
	})
	e := newEngine(t, db, 5*time.Minute)
	ctx := context.Background()
	list := func(ac, dev string) PackageListResult {
		t.Helper()
		l, err := e.ListEligiblePackages(ctx, PackageListRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: ac, DeviceID: dev, GuestNetworkID: p2GN})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return l
	}
	if l := list(s.authCtxID, s.deviceID); len(l.Packages) != 1 || l.FreeAllowanceUsed {
		t.Fatalf("first visit must offer the free package: %+v", l)
	}
	q, err := e.CreateQuote(ctx, req(s))
	if err != nil || q.QuoteID == "" {
		t.Fatalf("quote: %+v %v", q, err)
	}
	pr, err := e.ConfirmPurchase(ctx, ConfirmRequest{TenantID: p2Tenant, SiteID: p2Site, QuoteID: q.QuoteID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
	if err != nil || pr.EntitlementID == "" {
		t.Fatalf("confirm: %+v %v", pr, err)
	}
	// The same account, a fresh context: the free allowance is spent, and the list says so.
	ac2 := scan1Guarded(t, db, "auth_context", `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`, p2Tenant, p2Site, s.accountID, s.deviceID, p2GN)
	if l := list(ac2, s.deviceID); len(l.Packages) != 0 || !l.FreeAllowanceUsed {
		t.Fatalf("spent allowance must be withheld AND explained: %+v", l)
	}
	// REPUBLISHING DOES NOT RESET IT: a new revision of the same package, same rule.
	rev2 := scan1(t, db, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id,site_id,package_id,revision_no,service_plan_revision_id,package_type,price_minor,currency,currency_exponent,settlement_methods,duration_policy,display)
		VALUES ($1,$2,$3,2,$4,'GENERAL',0,'USD',2,'{NOT_REQUIRED}','{"end_mode":"MANUAL_END"}'::jsonb,'{"name":"Free WiFi v2"}'::jsonb) RETURNING id::text`,
		p2Tenant, p2Site, s.packageID, s.planRevID)
	if err := execErr(t, db, `INSERT INTO iam_v2.package_eligibility_rules (tenant_id,site_id,package_revision_id,rule_type,rule_value) VALUES ($1,$2,$3,'PRIOR_PURCHASE','{"forbids_prior":true}'::jsonb)`, p2Tenant, p2Site, rev2); err != nil {
		t.Fatal(err)
	}
	if err := execErr(t, db, `INSERT INTO iam_v2.package_grant_tiers (tenant_id,site_id,package_revision_id,tier_order,grant_value) VALUES ($1,$2,$3,10,'{"down_kbps":5000}'::jsonb)`, p2Tenant, p2Site, rev2); err != nil {
		t.Fatal(err)
	}
	if err := execErr(t, db, `UPDATE iam_v2.internet_packages SET current_revision_id=$1 WHERE id=$2`, rev2, s.packageID); err != nil {
		t.Fatal(err)
	}
	ac3 := scan1Guarded(t, db, "auth_context", `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`, p2Tenant, p2Site, s.accountID, s.deviceID, p2GN)
	if l := list(ac3, s.deviceID); len(l.Packages) != 0 || !l.FreeAllowanceUsed {
		t.Fatalf("a republish must not hand out a second free allowance: %+v", l)
	}
	// ANOTHER IDENTITY ON THE SAME DEVICE: still spent (the device counts on a free package by default).
	pid := scan1(t, db, `INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`, p2Tenant)
	ac4 := scan1Guarded(t, db, "auth_context", `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_principal_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'OTP',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`, p2Tenant, p2Site, pid, s.deviceID, p2GN)
	if l := list(ac4, s.deviceID); len(l.Packages) != 0 || !l.FreeAllowanceUsed {
		t.Fatalf("a second email on the same phone is not a second allowance: %+v", l)
	}
	// A NEW CLIENT ON A NEW DEVICE: offered.
	dev2 := scan1(t, db, `INSERT INTO iam_v2.devices (tenant_id,site_id,appliance_id,mac) VALUES ($1,$2,$3::uuid,'02:00:00:fe:00:01') RETURNING id::text`, p2Tenant, p2Site, p2Appl)
	pid2 := scan1(t, db, `INSERT INTO iam_v2.guest_principals (tenant_id) VALUES ($1) RETURNING id::text`, p2Tenant)
	ac5 := scan1Guarded(t, db, "auth_context", `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_principal_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'OTP',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`, p2Tenant, p2Site, pid2, dev2, p2GN)
	if l := list(ac5, dev2); len(l.Packages) != 1 || l.FreeAllowanceUsed {
		t.Fatalf("a new Client on a new device is offered the free package: %+v", l)
	}
	// And the quote agrees with the list: the spent account cannot quote what the list withheld.
	if q, err := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: ac3, PackageID: s.packageID, DeviceID: s.deviceID, GuestNetworkID: p2GN}); err != nil || q.QuoteID != "" {
		t.Fatalf("a quote must never pass what the list withheld: %+v %v", q, err)
	}
}
