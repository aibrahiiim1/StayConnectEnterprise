//go:build integration

package main

// "DISCONNECT THIS ACCOUNT'S DEVICES NOW" MUST DO WHAT IT PROMISES, AND NOTHING MORE.
//
// Two defects are closed here and both are asserted against a real database and a real scd channel:
//
//  1. The Admin Console's set-password call always sends disconnect_sessions, and the handler's request struct
//     did not have the field. decodeJSON uses DisallowUnknownFields, so SETTING A CLIENT ACCOUNT'S PASSWORD
//     ANSWERED 400 "bad body" — the whole action failed, which is why TestIntegration_SetPassword_BodyWithDisconnectFlagIsAccepted
//     exists as its own named case.
//
//  2. The row action ended sessions with a bare UPDATE from edged that set `ended` and left `state` at
//     'active'. The device kept its nftables authorization, so it kept its traffic, and the API still answered
//     with a count of "disconnected sessions". Ending now goes through scd — the one operator
//     session-termination path, the same POST /v1/sessions/revoke that resources_sessions.go's per-session
//     Disconnect uses — which denies the address on the device's bridge, deletes its shaping class and only
//     then ends the row.
//
// WHAT MUST SURVIVE A DISCONNECT is asserted as carefully as what must change: the Entitlement's status, its
// window, its consumed data and its consumed online seconds, the accounting ledger, and every OTHER account's
// sessions. A disconnect that silently expired the Entitlement or reset the quota would be a far worse defect
// than the one it replaced, because the guest would sign in again and find their allowance gone.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
)

// ---------------------------------------------------------------------------------------------------------
// A STAND-IN FOR scd, PLAYING ITS PART HONESTLY.
//
// It serves /v1/sessions/revoke on a unix socket exactly as scd does, records the addresses it was asked to
// revoke, and performs scd's own session-end statement (state='ended', ended, end_reason) against the real
// database. That is the point: edged must not end the row itself, so the row can only become 'ended' because
// something on the enforcement side did it. A test whose stub only answered 200 would pass against the broken
// implementation.
//
// When `refuse` holds an address, that revoke answers 500 and the row is left alone — which is how the
// "reported as a failure rather than folded into the success count" assertion is made.
type fakeSCD struct {
	mu     sync.Mutex
	ips    []string
	refuse map[string]bool
}

func newFakeSCD(t *testing.T, f *apiFixture, refuse ...string) (*fakeSCD, *scdClient) {
	t.Helper()
	fs := &fakeSCD{refuse: map[string]bool{}}
	for _, ip := range refuse {
		fs.refuse[ip] = true
	}
	dir, err := os.MkdirTemp("", "scd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/revoke" {
			http.NotFound(w, r)
			return
		}
		var body struct{ IP, Reason string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		fs.mu.Lock()
		fs.ips = append(fs.ips, body.IP)
		refused := fs.refuse[body.IP]
		fs.mu.Unlock()
		if refused {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"nft deny failed"}`))
			return
		}
		// scd's own endActiveSessionByIP, verbatim in shape: enforcement is withdrawn first and the row is
		// ended last, by the component that owns the kernel state.
		if _, err := f.pool.Exec(r.Context(), `
		    UPDATE iam_v2.sessions
		       SET state='ended', ended=GREATEST(now(), started), end_reason=$4
		     WHERE tenant_id=$1 AND site_id=$2 AND ip=$3::inet AND state IN ('active','PENDING_ENFORCEMENT')`,
			f.tenant, f.site, body.IP, body.Reason); err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"revoked"}`))
	})}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	return fs, newSCDClient(sock)
}

func (fs *fakeSCD) seen() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.ips...)
}

// ---------------------------------------------------------------------------------------------------------

// acctSession is one seeded live session: the row's id and the address enforcement is keyed on.
type acctSession struct {
	id string
	ip string
}

// seedAccountWithSessions creates a client account, an ACTIVE Entitlement whose subject is that account (with
// a real purchase, package revision and plan revision behind it, because the schema requires them), and one
// live session per address given. It returns the account id, the entitlement id and the sessions.
func seedAccountWithSessions(t *testing.T, f *apiFixture, username string, consumedBytes int64, ips ...string) (
	string, string, []acctSession) {
	t.Helper()
	ctx := context.Background()

	var svcPlan, svcRev, pkg, pkgRev, purchase string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.service_plans (tenant_id, site_id, code)
		VALUES ($1,$2,'plan-'||$3) RETURNING id::text`, f.tenant, f.site, username).Scan(&svcPlan); err != nil {
		t.Fatalf("seed service plan: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.service_plan_revisions
		(tenant_id, site_id, service_plan_id, revision_no, down_kbps, up_kbps, max_concurrent_devices,
		 data_quota_bytes, time_quota_seconds)
		VALUES ($1,$2,$3,1,10000,5000,4,5000000000,86400) RETURNING id::text`,
		f.tenant, f.site, svcPlan).Scan(&svcRev); err != nil {
		t.Fatalf("seed plan revision: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.internet_packages (tenant_id, site_id, code)
		VALUES ($1,$2,'pkg-'||$3) RETURNING id::text`, f.tenant, f.site, username).Scan(&pkg); err != nil {
		t.Fatalf("seed package: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.internet_package_revisions
		(tenant_id, site_id, package_id, revision_no, service_plan_revision_id, package_type)
		VALUES ($1,$2,$3,1,$4,'FREE_STAY') RETURNING id::text`,
		f.tenant, f.site, pkg, svcRev).Scan(&pkgRev); err != nil {
		t.Fatalf("seed package revision: %v", err)
	}
	if err := controlled(ctx, f.pool, []string{"commerce_intent"}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iam_v2.purchases (tenant_id, site_id, package_revision_id, trigger)
			VALUES ($1,$2,$3,'ADMIN_GRANT') RETURNING id::text`, f.tenant, f.site, pkgRev).Scan(&purchase)
	}); err != nil {
		t.Fatalf("seed purchase: %v", err)
	}

	var account string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts
		(tenant_id, site_id, username, password_hash)
		VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash') RETURNING id::text`,
		f.tenant, f.site, username).Scan(&account); err != nil {
		t.Fatalf("seed guest account: %v", err)
	}

	// The entitlement and its first transition are ONE transaction: status is backed by an append-only
	// history and a deferred constraint checks the two agree at COMMIT.
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var ent string
	if err := tx.QueryRow(ctx, `INSERT INTO iam_v2.entitlements
		(tenant_id, site_id, purchase_id, guest_account_id, policy_snapshot, service_plan_revision_id,
		 package_revision_id, time_accounting_mode, end_mode, window_ends_at,
		 consumed_data_bytes, consumed_online_seconds)
		VALUES ($1,$2,$3,$4,'{}'::jsonb,$5,$6,'VALIDITY_WINDOW','VALIDITY_WINDOW',
		        now() + interval '1 day', $7, 1234)
		RETURNING id::text`, f.tenant, f.site, purchase, account, svcRev, pkgRev, consumedBytes).
		Scan(&ent); err != nil {
		t.Fatalf("seed entitlement: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`SELECT iam_v2.apply_entitlement_transition($1,'ACTIVE',now() - interval '1 minute','ADMIN_GRANT')`,
		ent); err != nil {
		t.Fatalf("activate entitlement: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit entitlement: %v", err)
	}

	var out []acctSession
	for i, ip := range ips {
		mac := fmt.Sprintf("02:00:%02x:%02x:%02x:%02x",
			len(username)%256, i+1, int(ip[len(ip)-1])%256, (i*7+3)%256)
		var device string
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
			VALUES ($1,$2,gen_random_uuid(),$3::macaddr) RETURNING id::text`,
			f.tenant, f.site, mac).Scan(&device); err != nil {
			t.Fatalf("seed device: %v", err)
		}
		// A session is only storable for a device holding an authorization binding on its entitlement.
		if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.entitlement_devices
			(tenant_id,site_id,entitlement_id,device_id,status,first_authorized,last_authorized)
			VALUES ($1,$2,$3,$4,'AUTHORIZED',now(),now())`, f.tenant, f.site, ent, device); err != nil {
			t.Fatalf("seed entitlement device: %v", err)
		}
		if err := controlled(ctx, f.pool, []string{"device_auth"}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO iam_v2.entitlement_device_authorizations
				(tenant_id,site_id,entitlement_id,device_id,seq,authorized_at)
				VALUES ($1,$2,$3,$4,1,now())`, f.tenant, f.site, ent, device)
			return err
		}); err != nil {
			t.Fatalf("seed device authorization: %v", err)
		}
		var sess string
		if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.sessions
			(tenant_id, site_id, entitlement_id, device_id, ip, mac, state, credential_method, started, expires_at)
			VALUES ($1,$2,$3,$4,$5::inet,$6::macaddr,'active','ACCOUNT',now(), now() + interval '1 day')
			RETURNING id::text`, f.tenant, f.site, ent, device, ip, mac).Scan(&sess); err != nil {
			t.Fatalf("seed session: %v", err)
		}
		out = append(out, acctSession{id: sess, ip: ip})
	}
	return account, ent, out
}

// entitlementState reads everything a disconnect must NOT change.
type entState struct {
	status     string
	window     string
	dataUsed   int64
	timeUsed   int64
	accounting int
}

func readEntState(t *testing.T, f *apiFixture, ent string) entState {
	t.Helper()
	var e entState
	if err := f.pool.QueryRow(context.Background(), `
	    SELECT e.status, e.window_ends_at::text, e.consumed_data_bytes, e.consumed_online_seconds,
	           (SELECT count(*)::int FROM iam_v2.accounting_records a
	             JOIN iam_v2.sessions s ON s.id = a.session_id WHERE s.entitlement_id = e.id)
	      FROM iam_v2.entitlements e WHERE e.id = $1`, ent).
		Scan(&e.status, &e.window, &e.dataUsed, &e.timeUsed, &e.accounting); err != nil {
		t.Fatalf("read entitlement: %v", err)
	}
	return e
}

func sessionStates(t *testing.T, f *apiFixture, sessions []acctSession) map[string][2]string {
	t.Helper()
	out := map[string][2]string{}
	for _, s := range sessions {
		var state string
		var reason *string
		if err := f.pool.QueryRow(context.Background(),
			`SELECT state, end_reason FROM iam_v2.sessions WHERE id=$1`, s.id).Scan(&state, &reason); err != nil {
			t.Fatalf("read session %s: %v", s.id, err)
		}
		r := ""
		if reason != nil {
			r = *reason
		}
		out[s.ip] = [2]string{state, r}
	}
	return out
}

func auditCount(t *testing.T, f *apiFixture, action, target string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*)::int FROM public.audit_log
	     WHERE action=$1 AND target_id=$2`, action, target).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------------------------------------

func TestIntegration_AccountDisconnect_EndsExactlyThisAccountsSessionsThroughSCD(t *testing.T) {
	f := newAPI(t)
	fs, client := newFakeSCD(t, f)
	f.svr.scd = client

	mine, ent, sessions := seedAccountWithSessions(t, f, "disc-mine", 777_000, "10.90.0.11", "10.90.0.12")
	_, otherEnt, otherSessions := seedAccountWithSessions(t, f, "disc-other", 42, "10.90.0.21")

	before := readEntState(t, f, ent)
	otherBefore := readEntState(t, f, otherEnt)

	st, body := f.do(t, "POST", "/guest-accounts/"+mine+"/disconnect", nil)
	if st != 200 || body["disconnected_sessions"] != float64(2) || body["status"] != "disconnected" {
		t.Fatalf("disconnect = %d %v", st, body)
	}
	if _, bad := body["disconnect_failures"]; bad {
		t.Fatalf("a clean disconnect reported failures: %v", body)
	}

	// ENFORCEMENT WAS ACTUALLY ASKED, for exactly this account's two addresses and nothing else.
	seen := fs.seen()
	if len(seen) != 2 || !((seen[0] == "10.90.0.11" && seen[1] == "10.90.0.12") ||
		(seen[0] == "10.90.0.12" && seen[1] == "10.90.0.11")) {
		t.Fatalf("scd was asked to revoke %v, want exactly the account's two addresses", seen)
	}

	// The rows are ENDED, with the shared end-reason vocabulary — not left 'active' with a timestamp.
	for ip, got := range sessionStates(t, f, sessions) {
		if got[0] != "ended" || got[1] != "admin" {
			t.Fatalf("session %s = state %q reason %q, want ended/admin", ip, got[0], got[1])
		}
	}
	// The other account is untouched: still active, and scd was never asked about it.
	for ip, got := range sessionStates(t, f, otherSessions) {
		if got[0] != "active" {
			t.Fatalf("another account's session %s became %q", ip, got[0])
		}
	}

	// THE ENTITLEMENT AND THE USAGE SURVIVE. This is the promise behind "they can sign in again".
	if after := readEntState(t, f, ent); after != before {
		t.Fatalf("the disconnect changed the entitlement: %+v -> %+v", before, after)
	}
	if after := readEntState(t, f, otherEnt); after != otherBefore {
		t.Fatalf("the disconnect changed ANOTHER account's entitlement: %+v -> %+v", otherBefore, after)
	}
	// No history was removed: the sessions are still there, ended, not deleted.
	var remaining int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*)::int FROM iam_v2.sessions WHERE entitlement_id=$1`, ent).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Fatalf("%d session rows remain, want 2 — a disconnect must not delete history", remaining)
	}

	if n := auditCount(t, f, "guest_account.sessions_disconnected", mine); n != 1 {
		t.Fatalf("audit rows = %d, want 1", n)
	}
	var payload map[string]any
	if err := f.pool.QueryRow(context.Background(), `SELECT payload FROM public.audit_log
	     WHERE action='guest_account.sessions_disconnected' AND target_id=$1`, mine).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload["disconnected_sessions"] != float64(2) || payload["username"] != "disc-mine" ||
		payload["authority"] != "iam_v2" {
		t.Fatalf("audit payload = %v", payload)
	}

	// A SECOND DISCONNECT IS A NO-OP WITH A COUNT OF ZERO, and audits nothing: there is nothing online.
	st, body = f.do(t, "POST", "/guest-accounts/"+mine+"/disconnect", nil)
	if st != 200 || body["disconnected_sessions"] != float64(0) {
		t.Fatalf("second disconnect = %d %v", st, body)
	}
	if n := auditCount(t, f, "guest_account.sessions_disconnected", mine); n != 1 {
		t.Fatalf("a no-op disconnect was audited: %d rows", n)
	}
	if len(fs.seen()) != 2 {
		t.Fatalf("a no-op disconnect still called scd: %v", fs.seen())
	}
}

// A DEVICE scd COULD NOT TAKE OFF IS REPORTED, not folded into the success count.
func TestIntegration_AccountDisconnect_CountsOnlyWhatActuallyEnded(t *testing.T) {
	f := newAPI(t)
	fs, client := newFakeSCD(t, f, "10.91.0.12")
	f.svr.scd = client

	acct, ent, sessions := seedAccountWithSessions(t, f, "disc-partial", 5, "10.91.0.11", "10.91.0.12")
	before := readEntState(t, f, ent)

	st, body := f.do(t, "POST", "/guest-accounts/"+acct+"/disconnect", nil)
	if st != 200 || body["disconnected_sessions"] != float64(1) || body["disconnect_failures"] != float64(1) {
		t.Fatalf("partial disconnect = %d %v", st, body)
	}
	states := sessionStates(t, f, sessions)
	if states["10.91.0.11"][0] != "ended" {
		t.Fatalf("the revocable session was not ended: %v", states)
	}
	if states["10.91.0.12"][0] != "active" {
		t.Fatalf("the refused session was reported ended: %v", states)
	}
	if len(fs.seen()) != 2 {
		t.Fatalf("scd calls = %v, want both addresses attempted", fs.seen())
	}
	if after := readEntState(t, f, ent); after != before {
		t.Fatalf("a partial disconnect changed the entitlement: %+v -> %+v", before, after)
	}
}

// THE REGRESSION THAT MATTERS MOST: the Admin Console's body is accepted at all.
func TestIntegration_SetPassword_BodyWithDisconnectFlagIsAccepted(t *testing.T) {
	f := newAPI(t)
	st, body := f.do(t, "POST", "/guest-accounts/", map[string]any{"username": "flag", "password": "pw-one"})
	if st != 201 {
		t.Fatalf("create = %d %v", st, body)
	}
	id := body["account"].(map[string]any)["id"].(string)
	// EXACTLY what hotel-admin sends. Before the field existed, DisallowUnknownFields answered 400 and setting
	// a client account's password did not work at all.
	st, body = f.do(t, "POST", "/guest-accounts/"+id+"/set-password",
		map[string]any{"password": "pw-two", "generate": false, "disconnect_sessions": false})
	if st != 200 || body["status"] != "password_set" {
		t.Fatalf("set-password with disconnect_sessions = %d %v", st, body)
	}
}

func TestIntegration_SetPassword_DisconnectsOnlyWhenTicked(t *testing.T) {
	f := newAPI(t)
	fs, client := newFakeSCD(t, f)
	f.svr.scd = client

	acct, ent, sessions := seedAccountWithSessions(t, f, "pwdisc", 9_000_000, "10.92.0.11")
	before := readEntState(t, f, ent)

	var hash0 string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT password_hash FROM iam_v2.guest_access_accounts WHERE id=$1`, acct).Scan(&hash0); err != nil {
		t.Fatal(err)
	}

	// UNTICKED: the password changes and the device stays online. Nothing is reported and nothing is audited
	// as a disconnect.
	st, body := f.do(t, "POST", "/guest-accounts/"+acct+"/set-password",
		map[string]any{"password": "fresh-one", "disconnect_sessions": false})
	if st != 200 {
		t.Fatalf("set-password = %d %v", st, body)
	}
	if _, present := body["disconnected_sessions"]; present {
		t.Fatalf("an unticked reset reported a disconnect: %v", body)
	}
	if got := sessionStates(t, f, sessions)["10.92.0.11"][0]; got != "active" {
		t.Fatalf("an unticked reset disconnected the device (state %q)", got)
	}
	if len(fs.seen()) != 0 {
		t.Fatalf("an unticked reset called scd: %v", fs.seen())
	}
	if n := auditCount(t, f, "guest_account.sessions_disconnected", acct); n != 0 {
		t.Fatalf("an unticked reset audited a disconnect: %d rows", n)
	}
	var hash1 string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT password_hash FROM iam_v2.guest_access_accounts WHERE id=$1`, acct).Scan(&hash1); err != nil {
		t.Fatal(err)
	}
	if hash1 == hash0 {
		t.Fatal("the password did not change")
	}

	// TICKED, with a generated password: the device goes off, the count is reported, and what the guest
	// bought is untouched.
	st, body = f.do(t, "POST", "/guest-accounts/"+acct+"/set-password",
		map[string]any{"generate": true, "disconnect_sessions": true})
	if st != 200 || body["disconnected_sessions"] != float64(1) {
		t.Fatalf("ticked reset = %d %v", st, body)
	}
	if pw, _ := body["generated_password"].(string); pw == "" {
		t.Fatalf("no generated password returned: %v", body)
	}
	if got := sessionStates(t, f, sessions)["10.92.0.11"]; got[0] != "ended" || got[1] != "admin" {
		t.Fatalf("session = %v, want ended/admin", got)
	}
	if fs.seen()[0] != "10.92.0.11" {
		t.Fatalf("scd calls = %v", fs.seen())
	}
	if after := readEntState(t, f, ent); after != before {
		t.Fatalf("the reset changed the entitlement: %+v -> %+v", before, after)
	}
	if n := auditCount(t, f, "guest_account.sessions_disconnected", acct); n != 1 {
		t.Fatalf("disconnect audit rows = %d, want 1", n)
	}
	if n := auditCount(t, f, "guest_account.password_set", acct); n != 2 {
		t.Fatalf("password audit rows = %d, want 2", n)
	}
}

// A REFUSAL LEAVES EVERYTHING AS IT WAS. Without an enforcement channel nothing can be taken off the network,
// so the whole call refuses -- and because the refusal happens FIRST, the password is not changed either.
func TestIntegration_SetPassword_RefusesWhenEnforcementIsUnavailable(t *testing.T) {
	f := newAPI(t)
	f.svr.scd = nil

	acct, _, sessions := seedAccountWithSessions(t, f, "pwnoscd", 1, "10.93.0.11")
	var hash0 string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT password_hash FROM iam_v2.guest_access_accounts WHERE id=$1`, acct).Scan(&hash0); err != nil {
		t.Fatal(err)
	}

	st, body := f.do(t, "POST", "/guest-accounts/"+acct+"/set-password",
		map[string]any{"password": "never-applied", "disconnect_sessions": true})
	if st != 503 || body["error"] != "enforcement_unavailable" {
		t.Fatalf("refusal = %d %v", st, body)
	}
	var hash1 string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT password_hash FROM iam_v2.guest_access_accounts WHERE id=$1`, acct).Scan(&hash1); err != nil {
		t.Fatal(err)
	}
	if hash1 != hash0 {
		t.Fatal("a refused reset changed the password anyway")
	}
	if got := sessionStates(t, f, sessions)["10.93.0.11"][0]; got != "active" {
		t.Fatalf("a refused reset ended the session (state %q)", got)
	}

	// With NOTHING online the same unavailable channel is not a refusal: there is nothing to disconnect.
	acct2, _, _ := seedAccountWithSessions(t, f, "pwnoscd2", 1)
	if st, b := f.do(t, "POST", "/guest-accounts/"+acct2+"/set-password",
		map[string]any{"password": "applied-fine", "disconnect_sessions": true}); st != 200 ||
		b["disconnected_sessions"] != float64(0) {
		t.Fatalf("reset with nothing online = %d %v", st, b)
	}
}

// A LIVE SESSION WITH NO ADDRESS IS REFUSED, NOT SILENTLY SKIPPED. Enforcement is keyed on the address, so
// there is no honest way to take that device off from here -- and the refusal comes before anything else is
// disconnected, so the operator can retry from a known state.
func TestIntegration_AccountDisconnect_RefusesASessionWithNoAddress(t *testing.T) {
	f := newAPI(t)
	fs, client := newFakeSCD(t, f)
	f.svr.scd = client

	acct, _, sessions := seedAccountWithSessions(t, f, "noaddr", 1, "10.94.0.11", "10.94.0.12")
	// Blanking the address is not something any product path does — the guard on iam_v2.sessions says so, and
	// correctly — so the fixture reaches it as the schema owner. This is a fixture-only shape, seeded to prove
	// the handler refuses rather than guesses.
	if err := asOwner(context.Background(), f.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE iam_v2.sessions SET ip=NULL WHERE id=$1`, sessions[1].id)
		return err
	}); err != nil {
		t.Fatalf("blank the address: %v", err)
	}

	st, body := f.do(t, "POST", "/guest-accounts/"+acct+"/disconnect", nil)
	if st != 409 || body["error"] != "conflict" {
		t.Fatalf("refusal = %d %v", st, body)
	}
	msg, _ := body["message"].(string)
	if msg == "" || !strings.Contains(msg, "no recorded address") || !strings.Contains(msg, "Nothing was disconnected") {
		t.Fatalf("refusal message does not say what happened: %q", msg)
	}
	for ip, got := range sessionStates(t, f, sessions) {
		if got[0] != "active" {
			t.Fatalf("session %s was ended by a refused call: %v", ip, got)
		}
	}
	if len(fs.seen()) != 0 {
		t.Fatalf("a refused call still revoked something: %v", fs.seen())
	}
	if n := auditCount(t, f, "guest_account.sessions_disconnected", acct); n != 0 {
		t.Fatalf("a refused call was audited as a disconnect: %d rows", n)
	}
}

// THE LOCKOUT FICTION IS GONE FROM THE OPERATOR SURFACE.
//
// locked_until exists on the table and the authenticator honours it, but nothing in the product ever sets it:
// repeated-failure protection is per DEVICE (migration 0068). The list used to report a "locked" count and
// accept status=locked, which could only ever mean "0" and "nothing" -- an always-zero measurement an operator
// reads as reassurance.
func TestIntegration_GuestAccounts_ReportNoAccountLockout(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `INSERT INTO iam_v2.guest_access_accounts
		  (tenant_id, site_id, username, password_hash, enabled, locked_until, failed_attempts)
		VALUES ($1,$2,'lk-1','x',true, now() + interval '1 hour', 9),
		       ($1,$2,'lk-2','x',false,NULL,0)`, f.tenant, f.site); err != nil {
		t.Fatalf("seed: %v", err)
	}

	st, body := f.getAccounts(t, "", "")
	if st != 200 {
		t.Fatalf("list = %d %v", st, body)
	}
	sum := body["summary"].(map[string]any)
	if _, present := sum["locked"]; present {
		t.Fatalf("the summary still reports a lockout count: %v", sum)
	}
	if sum["total"] != float64(2) || sum["enabled"] != float64(1) || sum["disabled"] != float64(1) {
		t.Fatalf("summary = %v", sum)
	}
	for _, r := range body["data"].([]any) {
		if _, present := r.(map[string]any)["locked_until"]; present {
			t.Fatalf("a row still carries locked_until: %v", r)
		}
	}
	// The filter is refused rather than answered with an empty page: an empty page reads as a measurement.
	st, body = f.getAccounts(t, "?status=locked", "")
	if st != 400 {
		t.Fatalf("status=locked = %d %v", st, body)
	}
	// Even though the column HOLDS a value for lk-1, so this is not passing by accident.
	var stored int
	if err := f.pool.QueryRow(ctx, `SELECT count(*)::int FROM iam_v2.guest_access_accounts
	     WHERE tenant_id=$1 AND site_id=$2 AND locked_until IS NOT NULL`, f.tenant, f.site).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("the fixture stored %d locked_until values, want 1", stored)
	}
	// enabled/disabled still work.
	if st, b := f.getAccounts(t, "?status=disabled", ""); st != 200 || len(b["data"].([]any)) != 1 {
		t.Fatalf("disabled filter = %d %v", st, b)
	}
	if st, b := f.getAccounts(t, "?status=enabled", ""); st != 200 || len(b["data"].([]any)) != 1 {
		t.Fatalf("enabled filter = %d %v", st, b)
	}
}
