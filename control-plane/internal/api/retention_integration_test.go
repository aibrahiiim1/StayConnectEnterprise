package api

import (
	"net/http"
	"testing"
)

// Migration 0054: with the default periods (audit log 24 months, people in the history 24 months, history and
// licences 6 years, disabled accounts 24 months), old records go or lose the people in them, recent ones are
// untouched, open alerts and live licences stay, and a second run finds nothing.
func TestRetentionAppliesCentralPeriods(t *testing.T) {
	f := newFixture(t)
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := f.pool.QueryRow(f.ctx, sql+" RETURNING id::text", args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	ago := func(s string) string { return "now() - interval '" + s + "'" }

	// The audit log.
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO audit_log (ts, actor_type, action, ip) VALUES
        (`+ago("25 months")+`, 'operator', 'old.thing', '198.51.100.1'), (`+ago("23 months")+`, 'operator', 'recent.thing', '198.51.100.2')`); err != nil {
		t.Fatal(err)
	}

	// Appliances: one retired 7 years ago, one retired a year ago, one live.
	oldApp := exec(`INSERT INTO appliances (serial, lifecycle_state, wan_mac, lan_mac, hostname, last_public_ip, updated_at)
        VALUES ('RET-OLD','decommissioned','02:00:00:00:00:01','02:00:00:00:00:02','old-box','203.0.113.9',`+ago("7 years")+`)`)
	newApp := exec(`INSERT INTO appliances (serial, lifecycle_state, wan_mac, hostname, updated_at)
        VALUES ('RET-NEW','revoked','02:00:00:00:00:03','new-box',`+ago("1 year")+`)`)
	liveApp := exec(`INSERT INTO appliances (serial, lifecycle_state, wan_mac, updated_at)
        VALUES ('LIVE-1','assigned','02:00:00:00:00:04',`+ago("8 years")+`)`)

	// History on the live appliance.
	exec(`INSERT INTO appliance_lifecycle_events (appliance_id, to_state, actor, source_ip, created_at)
        VALUES ($1,'assigned','someone@central.test','198.51.100.3',`+ago("7 years")+`)`, liveApp)
	midEvent := exec(`INSERT INTO appliance_lifecycle_events (appliance_id, to_state, actor, source_ip, created_at)
        VALUES ($1,'assigned','someone@central.test','198.51.100.3',`+ago("3 years")+`)`, liveApp)
	sysEvent := exec(`INSERT INTO appliance_lifecycle_events (appliance_id, to_state, actor, created_at)
        VALUES ($1,'assigned','system',`+ago("3 years")+`)`, liveApp)
	recentEvent := exec(`INSERT INTO appliance_lifecycle_events (appliance_id, to_state, actor, source_ip, created_at)
        VALUES ($1,'assigned','someone@central.test','198.51.100.3',`+ago("1 year")+`)`, liveApp)
	exec(`INSERT INTO appliance_certificate_events (appliance_id, event, actor, source_ip, created_at)
        VALUES ($1,'issued','someone@central.test','198.51.100.4',`+ago("3 years")+`)`, liveApp)
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO portal_tls_events (appliance_id, kind, actor, at) VALUES
        ($1,'old','someone@central.test',`+ago("7 years")+`), ($1,'mid','someone@central.test',`+ago("3 years")+`)`, liveApp); err != nil {
		t.Fatal(err)
	}

	// Alerts: an old closed one, an old open one, a middle-aged acknowledged one.
	exec(`INSERT INTO appliance_security_alerts (appliance_id, kind, status, resolved, source_ip, created_at)
        VALUES ($1,'x','resolved',true,'198.51.100.5',`+ago("7 years")+`)`, liveApp)
	openOld := exec(`INSERT INTO appliance_security_alerts (appliance_id, kind, status, source_ip, created_at)
        VALUES ($1,'x','open','198.51.100.5',`+ago("7 years")+`)`, liveApp)
	midAlert := exec(`INSERT INTO appliance_security_alerts (appliance_id, kind, status, source_ip, acknowledged_by, detail, created_at)
        VALUES ($1,'x','acknowledged','198.51.100.5',$2::uuid,'{"last_source_ip":"198.51.100.6","repeats":3}',`+ago("3 years")+`)`,
		liveApp, f.session.OperatorID)

	// Licences: one revoked 7 years ago, one superseded 7 years ago by a still-live one, one expired a year ago.
	cust, site := f.customerSite("Retention Hotel")
	lic := func(status, extra string, args ...any) string {
		return exec(`INSERT INTO licenses (tenant_id, site_id, status, valid_until, signed_envelope, key_id, created_at`+extra,
			append([]any{cust, site, status}, args...)...)
	}
	revoked := lic("revoked", `, revoked_at) VALUES ($1,$2,$3, now() + interval '1 year','e','k',`+ago("8 years")+`,`+ago("7 years")+`)`)
	superseded := lic("superseded", `) VALUES ($1,$2,$3, now() + interval '1 year','e','k',`+ago("8 years")+`)`)
	live := lic("active", `, supersedes_license_id) VALUES ($1,$2,$3, now() + interval '1 year','e','k',`+ago("7 years")+`,$4::uuid)`, superseded)
	expiredRecently := lic("active", `) VALUES ($1,$2,$3,`+ago("1 year")+`,'e','k',`+ago("2 years")+`)`)

	// Accounts: one disabled 25 months ago (with authenticator and role), one disabled a month ago.
	oldOp := exec(`INSERT INTO operators (email, display_name, password_hash, status) VALUES ('gone@central.test','Gone','h','disabled')`)
	if _, err := f.pool.Exec(f.ctx, `UPDATE operators SET disabled_at = `+ago("25 months")+` WHERE id=$1`, oldOp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_roles (operator_id, role) VALUES ($1,'viewer')`, oldOp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `INSERT INTO operator_mfa (operator_id, key_id, nonce, ciphertext, cipher_version)
        VALUES ($1,'k','\x00'::bytea,'\x00'::bytea,1)`, oldOp); err != nil {
		t.Fatal(err)
	}
	recentOp := exec(`INSERT INTO operators (email, password_hash, status) VALUES ('left@central.test','h','active')`)
	if _, err := f.pool.Exec(f.ctx, `UPDATE operators SET status='disabled' WHERE id=$1`, recentOp); err != nil {
		t.Fatal(err)
	}
	if f.count(`SELECT count(*) FROM operators WHERE id=$1 AND disabled_at > now() - interval '1 minute'`, recentOp) != 1 {
		t.Fatal("disabling an account must record when")
	}

	done, err := ApplyRetention(f.ctx, f.base)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("first run: %v", done)

	checks := []struct {
		what string
		sql  string
		args []any
		want int
	}{
		{"old audit entry deleted", `SELECT count(*) FROM audit_log WHERE action='old.thing'`, nil, 0},
		{"recent audit entry kept", `SELECT count(*) FROM audit_log WHERE action='recent.thing'`, nil, 1},
		{"the run is audited", `SELECT count(*) FROM audit_log WHERE action='retention.applied'`, nil, 1},

		{"7-year-old lifecycle event deleted", `SELECT count(*) FROM appliance_lifecycle_events WHERE appliance_id=$1 AND created_at < now() - interval '6 years'`, []any{liveApp}, 0},
		{"3-year-old event kept without the person", `SELECT count(*) FROM appliance_lifecycle_events WHERE id=$1 AND actor='removed' AND source_ip IS NULL`, []any{midEvent}, 1},
		{"system actor kept", `SELECT count(*) FROM appliance_lifecycle_events WHERE id=$1 AND actor='system'`, []any{sysEvent}, 1},
		{"recent event untouched", `SELECT count(*) FROM appliance_lifecycle_events WHERE id=$1 AND actor='someone@central.test' AND source_ip IS NOT NULL`, []any{recentEvent}, 1},
		{"certificate event anonymised", `SELECT count(*) FROM appliance_certificate_events WHERE appliance_id=$1 AND actor='removed' AND source_ip IS NULL`, []any{liveApp}, 1},
		{"old portal event deleted despite append-only", `SELECT count(*) FROM portal_tls_events WHERE appliance_id=$1 AND kind='old'`, []any{liveApp}, 0},
		{"portal event anonymised", `SELECT count(*) FROM portal_tls_events WHERE appliance_id=$1 AND kind='mid' AND actor='removed'`, []any{liveApp}, 1},

		{"old closed alert deleted", `SELECT count(*) FROM appliance_security_alerts WHERE appliance_id=$1 AND status='resolved'`, []any{liveApp}, 0},
		{"old open alert kept", `SELECT count(*) FROM appliance_security_alerts WHERE id=$1`, []any{openOld}, 1},
		{"alert loses IPs and acknowledger", `SELECT count(*) FROM appliance_security_alerts WHERE id=$1 AND source_ip IS NULL
            AND acknowledged_by IS NULL AND NOT detail ? 'last_source_ip' AND detail->>'repeats' = '3'`, []any{midAlert}, 1},

		{"revoked licence deleted", `SELECT count(*) FROM licenses WHERE id=$1`, []any{revoked}, 0},
		{"superseded licence deleted", `SELECT count(*) FROM licenses WHERE id=$1`, []any{superseded}, 0},
		{"live licence kept", `SELECT count(*) FROM licenses WHERE id=$1`, []any{live}, 1},
		{"recently expired licence kept", `SELECT count(*) FROM licenses WHERE id=$1`, []any{expiredRecently}, 1},

		{"old retired appliance cleared, row kept", `SELECT count(*) FROM appliances WHERE id=$1 AND wan_mac IS NULL AND lan_mac IS NULL
            AND hostname IS NULL AND last_public_ip IS NULL AND serial='RET-OLD'`, []any{oldApp}, 1},
		{"recently retired appliance untouched", `SELECT count(*) FROM appliances WHERE id=$1 AND wan_mac IS NOT NULL`, []any{newApp}, 1},
		{"live appliance untouched", `SELECT count(*) FROM appliances WHERE id=$1 AND wan_mac IS NOT NULL`, []any{liveApp}, 1},

		{"old disabled account anonymised", `SELECT count(*) FROM operators WHERE id=$1 AND anonymised_at IS NOT NULL
            AND email LIKE 'removed-%@removed.invalid' AND display_name IS NULL AND password_hash IS NULL`, []any{oldOp}, 1},
		{"its roles removed", `SELECT count(*) FROM operator_roles WHERE operator_id=$1`, []any{oldOp}, 0},
		{"its authenticator removed", `SELECT count(*) FROM operator_mfa WHERE operator_id=$1`, []any{oldOp}, 0},
		{"recently disabled account kept", `SELECT count(*) FROM operators WHERE id=$1 AND email='left@central.test'`, []any{recentOp}, 1},
	}
	for _, c := range checks {
		if got := f.count(c.sql, c.args...); got != c.want {
			t.Errorf("%s: got %d, want %d", c.what, got, c.want)
		}
	}

	// An anonymised account cannot come back.
	if _, err := f.pool.Exec(f.ctx, `UPDATE operators SET status='active' WHERE id=$1`, oldOp); err == nil {
		t.Error("an anonymised account was enabled again")
	}
	// The portal history is still append-only for everyone else.
	if _, err := f.pool.Exec(f.ctx, `DELETE FROM portal_tls_events WHERE appliance_id=$1`, liveApp); err == nil {
		t.Error("portal_tls_events accepted a delete outside retention")
	}

	if again, err := ApplyRetention(f.ctx, f.base); err != nil || len(again) != 0 {
		t.Fatalf("a second run must find nothing: %v %v", again, err)
	}
}

// The periods are settings: bounded, needing a reason, audited with from and to.
func TestRetentionSettings(t *testing.T) {
	f := newFixture(t)
	rec := f.operatorCall(f.base.getRetentionSettings, http.MethodGet, "", nil)
	v := decode(t, rec)
	if rec.Code != 200 || v["audit_log_months"].(float64) != 24 || v["history_personal_months"].(float64) != 24 ||
		v["history_years"].(float64) != 6 || v["disabled_account_months"].(float64) != 24 {
		t.Fatalf("defaults: %d %s", rec.Code, rec.Body.String())
	}
	for _, bad := range []map[string]any{
		{"audit_log_months": 5, "reason": "x"}, {"audit_log_months": 121, "reason": "x"},
		{"history_years": 0, "reason": "x"}, {"history_years": 11, "reason": "x"},
		{"disabled_account_months": 0, "reason": "x"}, {"history_personal_months": 200, "reason": "x"},
		{"audit_log_months": 12}, {"reason": "nothing to change"},
	} {
		if rec := f.operatorCall(f.base.patchRetentionSettings, http.MethodPatch, "", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v must be refused, got %d", bad, rec.Code)
		}
	}
	rec = f.operatorCall(f.base.patchRetentionSettings, http.MethodPatch, "", map[string]any{"audit_log_months": 36, "reason": "auditor asked"})
	if rec.Code != 200 || decode(t, rec)["audit_log_months"].(float64) != 36 {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM audit_log WHERE action='settings.retention_updated'
                      AND payload->'audit_log_months'->>'from' = '24' AND payload->'audit_log_months'->>'to' = '36'
                      AND NOT payload ? 'history_years'`); n != 1 {
		t.Fatal("the change must be audited with from/to, and only what changed")
	}
}
