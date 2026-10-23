package iamv2

import (
	"context"
	"testing"
)

// A staff-shown code may create a new Client (marked staff_assisted, in no email-domain group) but never signs
// in to an existing one; the client's own later proof clears the marker. Needs IAMV2_TEST_DSN.
func TestAStaffShownCodeNeverReachesAnExistingClient(t *testing.T) {
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
	dev := DeviceContext{MAC: "02:00:00:00:00:78", ApplianceID: p2Appl, GuestNetworkID: testGN, IP: "10.50.0.10"}
	signIn := func(email string, assisted bool) Result {
		t.Helper()
		res, err := a.Authenticate(ctx, Request{Method: MethodOTP, TenantID: testTenant, SiteID: testSite,
			FactorType: "EMAIL", FactorValue: email, Credential: CredentialEmailOTP, StaffAssisted: assisted, Device: dev})
		if err != nil {
			t.Fatalf("sign in %s: %v", email, err)
		}
		return res
	}
	attrsOf := func(email string) map[string]any {
		t.Helper()
		var m map[string]any
		if err := db.QueryRow(ctx, `SELECT COALESCE(attrs,'{}'::jsonb) FROM iam_v2.guest_principal_identities
		     WHERE tenant_id=$1 AND factor_type='EMAIL' AND factor_value_norm=$2`, testTenant, email).Scan(&m); err != nil {
			t.Fatalf("attrs %s: %v", email, err)
		}
		return m
	}

	owner := signIn("ann@example.com", false)
	if owner.Decision != DecisionAllow || owner.ClientGroupID != gid {
		t.Fatalf("self-verified sign-in: %+v", owner)
	}
	if attrsOf("ann@example.com")[AssuranceKey] != AssuranceVerified {
		t.Fatal("a self-verified factor is marked verified")
	}
	var contexts int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.auth_contexts WHERE guest_principal_id=$1::uuid`, owner.Subject.PrincipalID).Scan(&contexts)

	// A staff-shown code for the same address is refused and the Client is unchanged.
	denied := signIn("Ann@Example.com", true)
	if denied.Decision != DecisionDeny || denied.Reason != "assisted_existing_client" {
		t.Fatalf("a staff-shown code must never sign in to an existing Client: %+v", denied)
	}
	var after int
	_ = db.QueryRow(ctx, `SELECT count(*) FROM iam_v2.auth_contexts WHERE guest_principal_id=$1::uuid`, owner.Subject.PrincipalID).Scan(&after)
	if after != contexts {
		t.Fatalf("no auth context may be minted for the existing Client (%d -> %d)", contexts, after)
	}
	if attrsOf("ann@example.com")[AssuranceKey] != AssuranceVerified {
		t.Fatal("the existing Client's factor must be left exactly as it was")
	}

	assisted := signIn("bob@example.com", true)
	if assisted.Decision != DecisionAllow || assisted.ClientGroupID != "" {
		t.Fatalf("a staff-assisted new Client is allowed but in no email-domain group: %+v", assisted)
	}
	if attrsOf("bob@example.com")[AssuranceKey] != AssuranceStaffAssisted {
		t.Fatal("the factor must be marked staff_assisted")
	}
	var cred string
	_ = db.QueryRow(ctx, `SELECT COALESCE(credential,'') FROM iam_v2.auth_contexts WHERE id=$1::uuid`, assisted.AuthContextID).Scan(&cred)
	if cred != "EMAIL_OTP_ASSISTED" {
		t.Fatalf("the sign-in is labelled as staff-assisted, got %q", cred)
	}

	proved := signIn("bob@example.com", false)
	if proved.Decision != DecisionAllow || proved.Subject.PrincipalID != assisted.Subject.PrincipalID || proved.ClientGroupID != gid {
		t.Fatalf("the client's own proof upgrades the same Client: %+v", proved)
	}
	if attrsOf("bob@example.com")[AssuranceKey] != AssuranceVerified {
		t.Fatal("the client's own proof clears the staff_assisted marker")
	}
}
