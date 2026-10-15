//go:build integration

package main

// EMAIL ACCOUNTS END TO END AGAINST THE FULL APPLIANCE SCHEMA (migration 0117), through the handlers scd serves.
// Needs PHASE3_TEST_DSN (scripts/lib-fullschema-testdb.sh / scripts/sitedb-dev.sh); skipped otherwise.
//
// THE SERVER RUNS AS svc_scd. Its pool sets ROLE svc_scd on every connection, so each statement the product
// issues -- registration, the outbox, the sweep, grace, verify, reset, activation -- is executed with exactly the
// privileges Gate-P gives scd. A test that ran as the owner could not find a missing grant; this one fails on it.
// Seeding (packages, entitlements, sessions) and the clock moves use a separate owner pool, and never a code
// path under test.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
	"github.com/stayconnect/enterprise/data-plane/internal/metrics"
	"github.com/stayconnect/enterprise/data-plane/internal/tenantcfg"
	lic "github.com/stayconnect/enterprise/license"
)

type ssFixture struct {
	s        *server
	owner    *pgxpool.Pool
	mail     *capturedMail
	tenant   string
	site     string
	network  string
	subnet   string // first three octets, e.g. "10.117.42"
	switches *tenantcfg.AuthMethods
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newSSFixture(t *testing.T) *ssFixture {
	t.Helper()
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	var has bool
	if err := owner.QueryRow(ctx, `SELECT to_regclass('iam_v2.account_email_tokens') IS NOT NULL`).Scan(&has); err != nil || !has {
		t.Skip("migration 0117 is not applied on this database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, `SET ROLE svc_scd`)
		return err
	}
	svc, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)

	f := &ssFixture{owner: owner, mail: &capturedMail{}}
	if err := owner.QueryRow(ctx, `
	    WITH t AS (INSERT INTO public.tenants(id,slug,name) SELECT g, g::text, 'ss' FROM gen_random_uuid() g RETURNING id),
	         si AS (INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, t.id, g::text, 'ss' FROM t, gen_random_uuid() g RETURNING id, tenant_id)
	    SELECT (SELECT tenant_id FROM si)::text, (SELECT id FROM si)::text`).Scan(&f.tenant, &f.site); err != nil {
		t.Fatalf("seed tenant/site: %v", err)
	}
	// A /24 no other suite uses: 10.117.<random>.0/24.
	for i := 0; i < 20 && f.network == ""; i++ {
		third := int(randHex(1)[0])%200 + 20
		f.subnet = fmt.Sprintf("10.117.%d", third)
		_ = owner.QueryRow(ctx, `
		    INSERT INTO public.guest_networks(id,tenant_id,site_id,name,enabled,parent_interface,bridge_name,
		      gateway_cidr,gateway_ip,subnet_cidr)
		    SELECT g,$1,$2,'ss',true,'p'||substr(md5(g::text),1,12),'b'||substr(md5(g::text),1,12),
		           ($3||'.1/24')::inet, ($3||'.1')::inet, ($3||'.0/24')::cidr
		      FROM gen_random_uuid() g
		     WHERE NOT EXISTS (SELECT 1 FROM public.guest_networks WHERE subnet_cidr && ($3||'.0/24')::cidr)
		    RETURNING id::text`, f.tenant, f.site, f.subnet).Scan(&f.network)
	}
	if f.network == "" {
		t.Fatal("could not seed a guest network")
	}
	t.Cleanup(func() { f.cleanup(t) })

	f.switches = &tenantcfg.AuthMethods{EmailAccount: &tenantcfg.AuthMethod{Enabled: true}, GuestAccount: &tenantcfg.AuthMethod{Enabled: true}}
	auth, err := iamv2.New(iamv2.Config{MasterEnabled: true, Methods: map[iamv2.Method]bool{iamv2.MethodAccount: true}},
		iamv2.NewPgRepository(svc), iamv2.NopObserver{})
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	appl := "5a5a5a5a-0000-4000-8000-" + randHex(6)
	f.s = &server{
		db: svc, tenID: f.tenant, siteID: f.site, applianceID: appl, applID: appl,
		lic: licenceWithModules(t, lic.ModuleEmailOTP), met: metrics.New("test", prometheus.Labels{}),
		mail: f.mail, resumeKey: key, iamv2Auth: auth,
		iamv2Cfg:  iamv2.Config{MasterEnabled: true, Methods: map[iamv2.Method]bool{iamv2.MethodAccount: true}},
		portalTLS: portalTLSWith(httpsOn),
		methodSwitches: func(context.Context) (*tenantcfg.AuthMethods, error) {
			return f.switches, nil
		},
	}
	f.s.initSelfService(func(string) string { return "" })
	return f
}

func (f *ssFixture) cleanup(t *testing.T) {
	ctx := context.Background()
	tx, err := f.owner.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, fam := range []string{"auth_context", "device_auth", "session_binding", "commerce_intent"} {
		_, _ = tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, fam)
	}
	_, _ = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`) // bypass append-only guards on teardown only
	for _, q := range []string{
		`DELETE FROM iam_v2.sessions WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.session_entitlement_bindings WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.entitlement_device_authorizations WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.entitlement_devices WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.entitlement_state_transitions WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.entitlements WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.purchases WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.internet_package_revisions WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.internet_packages WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.service_plan_revisions WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.service_plans WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.auth_contexts WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.device_network_appearances WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.devices WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.account_email_tokens WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.guest_access_accounts WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.site_self_service_settings WHERE tenant_id=$1`,
		`DELETE FROM iam_v2.self_service_settings_changes WHERE tenant_id=$1`,
		`DELETE FROM public.guest_networks WHERE tenant_id=$1`,
		`DELETE FROM public.sites WHERE tenant_id=$1`,
		`DELETE FROM public.tenants WHERE id=$1`,
	} {
		if _, err := tx.Exec(ctx, `SAVEPOINT c`); err != nil {
			return
		}
		if _, err := tx.Exec(ctx, q, f.tenant); err != nil {
			_, _ = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT c`)
			t.Logf("cleanup: %s: %v", q, err)
		}
	}
	_ = tx.Commit(ctx)
}

func (f *ssFixture) ip(n int) string { return fmt.Sprintf("%s.%d", f.subnet, n) }

func (f *ssFixture) post(t *testing.T, h http.HandlerFunc, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// owner runs one statement as the schema owner (clock moves and seeds only).
func (f *ssFixture) ownerExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := f.owner.Exec(context.Background(), q, args...); err != nil {
		t.Fatalf("owner exec %q: %v", q, err)
	}
}

// lastLink drains the outbox once and returns the token of the newest message's link (and its subject).
func (f *ssFixture) sendAndTakeToken(t *testing.T, wantPath string) string {
	t.Helper()
	before := len(f.mail.msgs)
	f.s.sendDueAccountEmails(context.Background())
	if len(f.mail.msgs) != before+1 {
		t.Fatalf("expected exactly one message from the outbox, got %d", len(f.mail.msgs)-before)
	}
	text := f.mail.msgs[len(f.mail.msgs)-1].Text
	i := strings.Index(text, "https://ab12cd34.gw.onegate.work"+wantPath+"?t=")
	if i < 0 {
		t.Fatalf("the message must link to %s on the canonical portal:\n%s", wantPath, text)
	}
	line := strings.Fields(text[i:])[0]
	u, err := url.Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	tok := u.Query().Get("t")
	if len(tok) != 43 {
		t.Fatalf("token %q", tok)
	}
	var stored int
	_ = f.owner.QueryRow(context.Background(), `SELECT count(*) FROM iam_v2.account_email_tokens
	     WHERE tenant_id=$1 AND (encode(token_hmac,'escape') LIKE '%'||$2||'%' OR key_id = $2 OR last_send_error = $2)`, f.tenant, tok).Scan(&stored)
	if stored != 0 {
		t.Fatal("the plaintext token must never be stored")
	}
	return tok
}

// seedEntitlementSession gives the account an ACTIVE entitlement with one live session on a device of its own.
func (f *ssFixture) seedEntitlementSession(t *testing.T, accountID, ip, mac string) (ent, device, session string) {
	t.Helper()
	ctx := context.Background()
	p := f.owner
	tag := randHex(4)
	var svcPlan, svcRev, pkg, pkgRev, purchase string
	must := func(err error, what string) {
		if err != nil {
			t.Fatalf("seed %s: %v", what, err)
		}
	}
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.service_plans (tenant_id, site_id, code) VALUES ($1,$2,'plan-'||$3) RETURNING id::text`,
		f.tenant, f.site, tag).Scan(&svcPlan), "plan")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions (tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps,
		max_concurrent_devices, data_quota_bytes, time_quota_seconds) VALUES ($1,$2,$3,1,10000,5000,4,5000000000,86400) RETURNING id::text`,
		f.tenant, f.site, svcPlan).Scan(&svcRev), "plan revision")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code) VALUES ($1,$2,'pkg-'||$3) RETURNING id::text`,
		f.tenant, f.site, tag).Scan(&pkg), "package")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.internet_package_revisions (tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type)
		VALUES ($1,$2,$3,1,$4,'FREE_STAY') RETURNING id::text`, f.tenant, f.site, pkg, svcRev).Scan(&pkgRev), "package revision")
	tx, err := p.Begin(ctx)
	must(err, "tx")
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('commerce_intent')`)
	must(err, "commerce scope")
	must(tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, trigger) VALUES ($1,$2,$3,'ADMIN_GRANT') RETURNING id::text`,
		f.tenant, f.site, pkgRev).Scan(&purchase), "purchase")
	must(tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements (tenant_id, site_id, purchase_id, guest_account_id, policy_snapshot,
		service_plan_revision_id, package_revision_id, time_accounting_mode, end_mode, window_ends_at)
		VALUES ($1,$2,$3,$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','VALIDITY_WINDOW', now() + interval '1 day') RETURNING id::text`,
		f.tenant, f.site, purchase, accountID, svcRev, pkgRev).Scan(&ent), "entitlement")
	_, err = tx.Exec(ctx, `SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - interval '1 minute','ADMIN_GRANT')`, ent)
	must(err, "activate entitlement")
	must(tx.Commit(ctx), "commit entitlement")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac) VALUES ($1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`,
		f.tenant, f.site, mac).Scan(&device), "device")
	_, err = p.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices (tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
		VALUES ($1,$2,$3,$4,'AUTHORIZED',now(),now())`, f.tenant, f.site, ent, device)
	must(err, "entitlement device")
	tx2, err := p.Begin(ctx)
	must(err, "tx2")
	defer func() { _ = tx2.Rollback(ctx) }()
	_, err = tx2.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('device_auth')`)
	must(err, "device scope")
	_, err = tx2.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations (tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
		VALUES ($1,$2,$3,$4,1,now())`, f.tenant, f.site, ent, device)
	must(err, "device authorization")
	must(tx2.Commit(ctx), "commit authorization")
	must(p.QueryRow(ctx, `INSERT INTO iam_v2.sessions (tenant_id, site_id, entitlement_id, device_id, ip, mac, state, credential_method, started, expires_at)
		VALUES ($1,$2,$3,$4,$5::inet,$6::macaddr,'active','EMAIL_ACCOUNT',now(), now() + interval '1 day') RETURNING id::text`,
		f.tenant, f.site, ent, device, ip, mac).Scan(&session), "session")
	return ent, device, session
}

func (f *ssFixture) sessionEnd(t *testing.T, id string) (string, string) {
	t.Helper()
	var state string
	var reason *string
	if err := f.owner.QueryRow(context.Background(), `SELECT state, end_reason FROM iam_v2.sessions WHERE id=$1::uuid`, id).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if reason == nil {
		return state, ""
	}
	return state, *reason
}

func accountOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	a, ok := out["account"].(map[string]any)
	if !ok {
		t.Fatalf("no account block: %v", out)
	}
	return a
}

// REGISTER -> SIGN IN -> SUSPENSION -> GRACE -> VERIFY, with every step's privilege exercised as svc_scd.
func TestIntegration_EmailAccountLifecycle(t *testing.T) {
	f := newSSFixture(t)
	s := f.s
	ctx := context.Background()
	email := "ann." + randHex(3) + "@example.com"
	const pw = "correct horse battery"
	mac := "02:17:00:00:" + randHex(1) + ":01"

	// 1. Register: signed in at once, verification pending, nothing suspended.
	code, out := f.post(t, s.accountRegister, map[string]string{"email": strings.ToUpper(email), "password": pw, "ip": f.ip(10), "mac": mac, "lang": "en"})
	if code != 200 || out["auth_context_id"] == "" || out["method"] != "ACCOUNT" || out["account_token"] == "" || out["authority"] != "iam_v2" {
		t.Fatalf("register: %d %v", code, out)
	}
	a := accountOf(t, out)
	if a["verified"] != false || a["verification_required"] != true || a["suspended"] != false || a["deadline"] == nil || a["grace_available"] != false {
		t.Fatalf("a new account is unverified, with a deadline, not suspended: %v", a)
	}
	for _, k := range []string{"password", "password_hash", "token"} {
		if _, leaked := out[k]; leaked {
			t.Fatalf("the reply must never carry %s", k)
		}
	}
	var accountID, username, credential string
	var selfReg bool
	if err := f.owner.QueryRow(ctx, `SELECT a.id::text, a.username, a.self_registered, ac.credential
	      FROM iam_v2.guest_access_accounts a JOIN iam_v2.auth_contexts ac ON ac.guest_account_id = a.id
	     WHERE ac.id = $1::uuid`, out["auth_context_id"]).Scan(&accountID, &username, &selfReg, &credential); err != nil {
		t.Fatal(err)
	}
	if username != email || !selfReg || credential != "EMAIL_ACCOUNT" {
		t.Fatalf("the account is the lowercased address, self-registered, signed in as EMAIL_ACCOUNT: %q %v %q", username, selfReg, credential)
	}
	accountToken := out["account_token"].(string)

	// 2. The verification email leaves through the outbox, with a link to the canonical portal.
	verifyTok := f.sendAndTakeToken(t, "/account/verify")
	var sent, attempts int
	_ = f.owner.QueryRow(ctx, `SELECT count(sent_at), max(send_attempts) FROM iam_v2.account_email_tokens WHERE account_id=$1::uuid`, accountID).Scan(&sent, &attempts)
	if sent != 1 || attempts != 1 {
		t.Fatalf("one message sent once: sent=%d attempts=%d", sent, attempts)
	}
	f.s.sendDueAccountEmails(ctx)
	if len(f.mail.msgs) != 1 {
		t.Fatal("a sent message is never sent again")
	}

	// 3. The address is taken now; and sign-in returns the account block.
	if code, out := f.post(t, s.accountRegister, map[string]string{"email": email, "password": pw, "ip": f.ip(11), "mac": "02:17:00:00:99:02"}); code != 409 || out["error"] != "email_taken" {
		t.Fatalf("a second registration of the address: %d %v", code, out)
	}
	code, out = f.post(t, s.authorizeGuestAccount, map[string]string{"username": email, "password": pw, "ip": f.ip(10), "mac": mac})
	if code != 200 || out["account_token"] == "" || accountOf(t, out)["suspended"] != false {
		t.Fatalf("sign-in: %d %v", code, out)
	}
	if code, out := f.post(t, s.authorizeGuestAccount, map[string]string{"username": email, "password": "wrong password!", "ip": f.ip(10), "mac": mac}); code != 403 || out["reason"] != "invalid_credentials" {
		t.Fatalf("wrong password: %d %v", code, out)
	}

	// 4. Past the deadline: the sweep ends the live session, the entitlement is untouched, activation refuses.
	ent, device, session := f.seedEntitlementSession(t, accountID, f.ip(10), mac)
	s.selfServiceSweep(ctx)
	if st, _ := f.sessionEnd(t, session); st != "active" {
		t.Fatal("before the deadline nothing is ended")
	}
	f.ownerExec(t, `UPDATE iam_v2.guest_access_accounts SET verification_deadline = now() - interval '1 minute' WHERE id=$1::uuid`, accountID)
	s.selfServiceSweep(ctx)
	if st, reason := f.sessionEnd(t, session); st != "ended" || reason != endReasonVerificationSuspended {
		t.Fatalf("a suspended account's session must end: %s %s", st, reason)
	}
	var entStatus string
	_ = f.owner.QueryRow(ctx, `SELECT status FROM iam_v2.entitlements WHERE id=$1::uuid`, ent).Scan(&entStatus)
	if entStatus != "ACTIVE" {
		t.Fatalf("suspension never touches the entitlement: %s", entStatus)
	}
	activate := func() (int, map[string]any) {
		return f.post(t, s.activateIAMv2Session, map[string]string{"entitlement_id": ent, "device_id": device, "ip": f.ip(10), "mac": mac})
	}
	if code, out := activate(); code != 403 || out["error"] != "VERIFICATION_REQUIRED" || len(out) != 1 {
		t.Fatalf("activation of a suspended account: %d %v", code, out)
	}
	code, out = f.post(t, s.accountStatus, map[string]string{"account_token": accountToken, "ip": f.ip(10), "mac": mac})
	if code != 200 || accountOf(t, out)["suspended"] != true || accountOf(t, out)["grace_available"] != true {
		t.Fatalf("status while suspended: %d %v", code, out)
	}
	if code, _ := f.post(t, s.accountStatus, map[string]string{"account_token": accountToken, "ip": f.ip(12), "mac": "02:17:00:00:99:03"}); code != 401 {
		t.Fatal("the account token belongs to the device it was issued to")
	}

	// 5. Grace: the password again, once.
	if code, out := f.post(t, s.accountGrace, map[string]string{"email": email, "password": "wrong password!", "ip": f.ip(10), "mac": mac}); code != 401 || out["error"] != "invalid_credentials" {
		t.Fatalf("grace with a wrong password: %d %v", code, out)
	}
	code, out = f.post(t, s.accountGrace, map[string]string{"email": email, "password": pw, "ip": f.ip(10), "mac": mac})
	if code != 200 || out["auth_context_id"] == "" || accountOf(t, out)["suspended"] != false || accountOf(t, out)["grace_until"] == nil {
		t.Fatalf("grace: %d %v", code, out)
	}
	var used int
	_ = f.owner.QueryRow(ctx, `SELECT grace_extensions_used FROM iam_v2.guest_access_accounts WHERE id=$1::uuid`, accountID).Scan(&used)
	if used != 1 {
		t.Fatalf("one grace used, got %d", used)
	}
	if _, act := activate(); act["error"] == "VERIFICATION_REQUIRED" {
		t.Fatal("during grace activation is not refused for verification")
	}
	if code, out := f.post(t, s.accountGrace, map[string]string{"email": email, "password": pw, "ip": f.ip(10), "mac": mac}); code != 409 || out["error"] != "not_suspended" {
		t.Fatalf("grace while grace runs: %d %v", code, out)
	}
	f.ownerExec(t, `UPDATE iam_v2.guest_access_accounts SET grace_until = now() - interval '1 second' WHERE id=$1::uuid`, accountID)
	if code, out := f.post(t, s.accountGrace, map[string]string{"email": email, "password": pw, "ip": f.ip(10), "mac": mac}); code != 409 || out["error"] != "grace_unavailable" {
		t.Fatalf("a second grace with the default allowance of one: %d %v", code, out)
	}
	if code, out := activate(); code != 403 || out["error"] != "VERIFICATION_REQUIRED" {
		t.Fatalf("grace over, unverified: refused again: %d %v", code, out)
	}

	// 6. Resend: the cooldown, then a fresh link that supersedes the first.
	if code, out := f.post(t, s.accountResend, map[string]string{"account_token": accountToken, "ip": f.ip(10), "mac": mac}); code != 429 || out["error"] != "resend_wait" || out["retry_after_seconds"] == nil {
		t.Fatalf("resend inside the cooldown: %d %v", code, out)
	}
	f.ownerExec(t, `UPDATE iam_v2.account_email_tokens SET created_at = created_at - interval '2 minutes' WHERE account_id=$1::uuid`, accountID)
	if code, out := f.post(t, s.accountResend, map[string]string{"account_token": accountToken, "ip": f.ip(10), "mac": mac}); code != 202 {
		t.Fatalf("resend after the cooldown: %d %v", code, out)
	}
	newTok := f.sendAndTakeToken(t, "/account/verify")
	if code, out := f.post(t, s.accountVerify, map[string]string{"token": verifyTok}); code != 410 || out["error"] != "invalid_or_used" {
		t.Fatalf("a superseded link must not verify: %d %v", code, out)
	}

	// 7. Verify: normal status at once, single use.
	if code, out := f.post(t, s.accountVerify, map[string]string{"token": newTok}); code != 200 || out["verified"] != true {
		t.Fatalf("verify: %d %v", code, out)
	}
	if code, _ := f.post(t, s.accountVerify, map[string]string{"token": newTok}); code != 410 {
		t.Fatal("a link verifies once")
	}
	code, out = f.post(t, s.accountStatus, map[string]string{"account_token": accountToken, "ip": f.ip(10), "mac": mac})
	if code != 200 || accountOf(t, out)["verified"] != true || accountOf(t, out)["suspended"] != false {
		t.Fatalf("status after verifying: %d %v", code, out)
	}
	if _, act := activate(); act["error"] == "VERIFICATION_REQUIRED" {
		t.Fatal("a verified account is never refused for verification")
	}
	if code, out := f.post(t, s.accountResend, map[string]string{"account_token": accountToken, "ip": f.ip(10), "mac": mac}); code != 409 || out["error"] != "already_verified" {
		t.Fatalf("resend after verifying: %d %v", code, out)
	}
	// The operator's resend answers the same way and refuses an unknown or a Personal account.
	r := chi404(t, s, "/v1/admin/accounts/"+accountID+"/resend-verification")
	if r.Code != 409 || !strings.Contains(r.Body.String(), "already_verified") {
		t.Fatalf("admin resend of a verified account: %d %s", r.Code, r.Body.String())
	}
	if r := chi404(t, s, "/v1/admin/accounts/0f8b9a0e-0000-4000-8000-000000000000/resend-verification"); r.Code != 404 {
		t.Fatalf("admin resend of an unknown account: %d", r.Code)
	}
}

func newSelfServiceTestRouter(s *server) http.Handler {
	r := chi.NewRouter()
	r.Post("/v1/admin/accounts/{id}/resend-verification", s.adminAccountResendVerification)
	return r
}

// chi404 drives the admin route through a router so {id} is bound exactly as in main.go.
func chi404(t *testing.T, s *server, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := newSelfServiceTestRouter(s)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"operator":"desk@example.com"}`)))
	return rec
}

// A PASSWORD RESET TAKES THE ACCOUNT BACK: the owner of the address proves it, the squatter's password and
// connection go, the address is verified, every outstanding link is revoked.
func TestIntegration_PasswordResetTakesOverASquattedAccount(t *testing.T) {
	f := newSSFixture(t)
	s := f.s
	ctx := context.Background()
	email := "victim." + randHex(3) + "@example.com"
	const squatterPW = "squatter password"
	mac := "02:17:00:01:" + randHex(1) + ":01"
	if code, out := f.post(t, s.accountRegister, map[string]string{"email": email, "password": squatterPW, "ip": f.ip(20), "mac": mac}); code != 200 {
		t.Fatalf("register: %d %v", code, out)
	}
	var accountID string
	_ = f.owner.QueryRow(ctx, `SELECT id::text FROM iam_v2.guest_access_accounts WHERE tenant_id=$1 AND username=$2`, f.tenant, email).Scan(&accountID)
	f.sendAndTakeToken(t, "/account/verify") // the squatter cannot read it, but it was sent
	_, _, session := f.seedEntitlementSession(t, accountID, f.ip(20), mac)

	// Forgot: always 202; an unknown address sends nothing.
	if code, _ := f.post(t, s.accountForgot, map[string]string{"email": "nobody." + randHex(3) + "@example.com", "ip": f.ip(21), "mac": "02:17:00:01:99:02"}); code != 202 {
		t.Fatal("forgot for an unknown address answers 202")
	}
	s.sendDueAccountEmails(ctx)
	if len(f.mail.msgs) != 1 {
		t.Fatal("nothing is sent for an unknown address")
	}
	if code, _ := f.post(t, s.accountForgot, map[string]string{"email": email, "ip": f.ip(21), "mac": "02:17:00:01:99:02"}); code != 202 {
		t.Fatal("forgot answers 202")
	}
	resetTok := f.sendAndTakeToken(t, "/account/reset")
	if f.mail.msgs[len(f.mail.msgs)-1].To != email {
		t.Fatal("the reset link goes to the address itself")
	}
	// A second request inside the cooldown queues nothing.
	_, _ = f.post(t, s.accountForgot, map[string]string{"email": email, "ip": f.ip(21), "mac": "02:17:00:01:99:02"})
	s.sendDueAccountEmails(ctx)
	if len(f.mail.msgs) != 2 {
		t.Fatal("the cooldown applies to reset links too")
	}

	if code, out := f.post(t, s.accountReset, map[string]string{"token": resetTok, "password": "short"}); code != 422 || out["error"] != "weak_password" {
		t.Fatalf("a weak password: %d %v", code, out)
	}
	if code, out := f.post(t, s.accountReset, map[string]string{"token": resetTok, "password": "the real owner's password"}); code != 200 || out["reset"] != true {
		t.Fatalf("reset (the weak attempt must not have spent the link): %d %v", code, out)
	}
	if code, _ := f.post(t, s.accountReset, map[string]string{"token": resetTok, "password": "the real owner's password"}); code != 410 {
		t.Fatal("a reset link works once")
	}
	if st, reason := f.sessionEnd(t, session); st != "ended" || reason != endReasonPasswordReset {
		t.Fatalf("the squatter's connection must end: %s %s", st, reason)
	}
	var verified bool
	var live int
	_ = f.owner.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL,
	     (SELECT count(*) FROM iam_v2.account_email_tokens WHERE account_id=$1::uuid AND used_at IS NULL AND superseded_at IS NULL)
	     FROM iam_v2.guest_access_accounts WHERE id=$1::uuid`, accountID).Scan(&verified, &live)
	if !verified || live != 0 {
		t.Fatalf("a completed reset verifies the address and revokes every link: verified=%v live=%d", verified, live)
	}
	if code, _ := f.post(t, s.authorizeGuestAccount, map[string]string{"username": email, "password": squatterPW, "ip": f.ip(20), "mac": mac}); code != 403 {
		t.Fatal("the squatter's password no longer works")
	}
	if code, out := f.post(t, s.authorizeGuestAccount, map[string]string{"username": email, "password": "the real owner's password", "ip": f.ip(22), "mac": "02:17:00:01:99:03"}); code != 200 || accountOf(t, out)["verified"] != true {
		t.Fatalf("the owner signs in: %d %v", code, out)
	}
}

// USERNAMES ARE UNIQUE PER SITE (0117), the device cap counts per device, and the switches govern their kinds.
func TestIntegration_PerSiteUniquenessDeviceCapAndSwitches(t *testing.T) {
	f := newSSFixture(t)
	s := f.s
	ctx := context.Background()
	var otherSite string
	if err := f.owner.QueryRow(ctx, `INSERT INTO public.sites(id,tenant_id,code,name) SELECT g, $1, g::text, 'ss2' FROM gen_random_uuid() g RETURNING id::text`,
		f.tenant).Scan(&otherSite); err != nil {
		t.Fatal(err)
	}
	const user = "same@example.com"
	f.ownerExec(t, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash) VALUES ($1,$2,$3,'x')`, f.tenant, f.site, user)
	f.ownerExec(t, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash) VALUES ($1,$2,$3,'x')`, f.tenant, otherSite, user)
	if _, err := f.owner.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash) VALUES ($1,$2,upper($3),'x')`, f.tenant, f.site, user); err == nil {
		t.Fatal("one site cannot hold the same username twice, in any case")
	}
	if _, err := f.owner.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash, self_registered) VALUES ($1,$2,'Mixed@Example.com','x',true)`, f.tenant, f.site); err == nil {
		t.Fatal("a self-registered username must be the lowercased address")
	}

	// The device cap: 1 registration per device per day.
	if _, err := f.owner.Exec(ctx, `SELECT iam_v2.self_service_settings_set($1,$2,true,24,'SUSPEND',4,60,5,60,1,1,'test','device cap of one')`, f.tenant, f.site); err != nil {
		t.Fatal(err)
	}
	mac := "02:17:00:02:" + randHex(1) + ":01"
	if code, out := f.post(t, s.accountRegister, map[string]string{"email": "first." + randHex(2) + "@example.com", "password": "correct horse", "ip": f.ip(30), "mac": mac}); code != 200 {
		t.Fatalf("first registration: %d %v", code, out)
	}
	if code, out := f.post(t, s.accountRegister, map[string]string{"email": "second." + randHex(2) + "@example.com", "password": "correct horse", "ip": f.ip(30), "mac": mac}); code != 429 || out["error"] != "registration_limit" {
		t.Fatalf("the device cap: %d %v", code, out)
	}

	// Switches: Email accounts off refuses registration and the sign-in of a self-registered account, with the
	// wrong-password answer, while Personal accounts keep signing in.
	var self string
	_ = f.owner.QueryRow(ctx, `SELECT username FROM iam_v2.guest_access_accounts WHERE tenant_id=$1 AND site_id=$2 AND self_registered`, f.tenant, f.site).Scan(&self)
	f.switches = &tenantcfg.AuthMethods{GuestAccount: &tenantcfg.AuthMethod{Enabled: true}}
	if code, out := f.post(t, s.accountRegister, map[string]string{"email": "third@example.com", "password": "correct horse", "ip": f.ip(31), "mac": "02:17:00:02:99:02"}); code != 403 || out["error"] != "METHOD_DISABLED" {
		t.Fatalf("register with Email accounts off: %d %v", code, out)
	}
	if code, out := f.post(t, s.authorizeGuestAccount, map[string]string{"username": self, "password": "correct horse", "ip": f.ip(30), "mac": mac}); code != 403 || out["reason"] != "invalid_credentials" {
		t.Fatalf("a self-registered account with Email accounts off: %d %v", code, out)
	}
	f.switches = &tenantcfg.AuthMethods{EmailAccount: &tenantcfg.AuthMethod{Enabled: true}}
	if code, out := f.post(t, s.authorizeGuestAccount, map[string]string{"username": self, "password": "correct horse", "ip": f.ip(30), "mac": mac}); code != 200 {
		t.Fatalf("a self-registered account with only Email accounts on: %d %v", code, out)
	}
	f.switches = &tenantcfg.AuthMethods{}
	if code, out := f.post(t, s.authorizeGuestAccount, map[string]string{"username": self, "password": "correct horse", "ip": f.ip(30), "mac": mac}); code != 403 || out["error"] != "METHOD_DISABLED" {
		t.Fatalf("both switches off: %d %v", code, out)
	}
}

// THE GRANTS, stated directly as svc_scd: what the product needs, and nothing that would let scd change an
// account's enablement, validity or lockout, or write the settings without the audited setter.
func TestIntegration_SelfServiceGrantsAsSvcScd(t *testing.T) {
	f := newSSFixture(t)
	ctx := context.Background()
	var id string
	if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash) VALUES ($1,$2,$3,'x') RETURNING id::text`,
		f.tenant, f.site, "grant."+randHex(2)+"@example.com").Scan(&id); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE iam_v2.guest_access_accounts SET enabled = false WHERE id = $1::uuid`,
		`UPDATE iam_v2.guest_access_accounts SET locked_until = NULL WHERE id = $1::uuid`,
		`UPDATE iam_v2.guest_access_accounts SET valid_until = now() WHERE id = $1::uuid`,
		`DELETE FROM iam_v2.guest_access_accounts WHERE id = $1::uuid`,
		`DELETE FROM iam_v2.account_email_tokens WHERE account_id = $1::uuid`,
		`UPDATE iam_v2.site_self_service_settings SET grace_minutes = 15 WHERE $1::uuid IS NOT NULL`,
		`SELECT iam_v2.self_service_settings_set(gen_random_uuid(),gen_random_uuid(),true,24,'SUSPEND',4,60,5,60,1,3,'x','reason') WHERE $1::uuid IS NOT NULL`,
	} {
		if _, err := f.s.db.Exec(ctx, q, id); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("svc_scd must be refused: %s (%v)", q, err)
		}
	}
	for _, q := range []string{
		`UPDATE iam_v2.guest_access_accounts SET email_verified_at = now(), grace_until = NULL, grace_extensions_used = 0,
		        verification_deadline = now(), verification_reminder_sent_at = NULL, password_hash = password_hash WHERE id = $1::uuid`,
		`SELECT * FROM iam_v2.self_service_settings_get(gen_random_uuid(), $1::uuid)`,
	} {
		if _, err := f.s.db.Exec(ctx, q, id); err != nil {
			t.Errorf("svc_scd must be allowed: %s (%v)", q, err)
		}
	}
}
