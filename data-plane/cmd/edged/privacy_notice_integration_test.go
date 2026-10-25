//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Every sign-in records the privacy notice in force (stamped by the database, immutable), and the guest export
// lists the notices a guest was shown.
func TestIntegration_PrivacyNotice_EverySignInIsStampedAndExported(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())

	var before []byte
	if err := f.pool.QueryRow(ctx, `SELECT branding FROM tenants WHERE id=$1`, f.tenant).Scan(&before); err != nil {
		t.Fatalf("read branding: %v", err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `UPDATE tenants SET branding=$2 WHERE id=$1`, f.tenant, before) })
	setNotice := func(design map[string]any) {
		t.Helper()
		doc, _ := json.Marshal(map[string]any{"design": design, "revisions": []any{}})
		if _, err := f.pool.Exec(ctx, `UPDATE tenants SET branding=$2::jsonb WHERE id=$1`, f.tenant, string(doc)); err != nil {
			t.Fatalf("set design: %v", err)
		}
	}

	acct, device, _ := seedEndedAccount(t, f, "notice-"+sfx+"@example.com", time.Hour, "10.88.0.30")
	gn, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, "Notice VLAN "+sfx, "10.97.0.0/24")
	signIn := func(forged *string) (id string, stamp *string) {
		t.Helper()
		if err := controlled(ctx, f.pool, []string{"auth_context"}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts
				(tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at,privacy_notice_version_id)
				VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid,now()+interval '10 minutes',$6::uuid)
				RETURNING id::text, privacy_notice_version_id::text`,
				f.tenant, f.site, acct, device, gn, forged).Scan(&id, &stamp)
		}); err != nil {
			t.Fatalf("sign in: %v", err)
		}
		return id, stamp
	}
	text := func(version string) (s string) {
		_ = f.pool.QueryRow(ctx, `SELECT COALESCE(privacy_text,'<none>') FROM public.privacy_notice_versions WHERE id=$1::uuid`, version).Scan(&s)
		return s
	}

	setNotice(map[string]any{"privacy_text": "Notice one " + sfx, "terms_url": "https://hotel.example/terms"})
	ctx1, v1 := signIn(nil)
	if v1 == nil || text(*v1) != "Notice one "+sfx {
		t.Fatalf("the first sign-in was not stamped with the notice in force: %v", v1)
	}

	// The writer cannot choose the stamp, and an unchanged notice is one version.
	setNotice(map[string]any{"privacy_text": "Notice two " + sfx, "privacy_url": "https://hotel.example/privacy"})
	_, v2 := signIn(v1)
	if v2 == nil || *v2 == *v1 || text(*v2) != "Notice two "+sfx {
		t.Fatalf("a writer-supplied stamp was kept, or the new notice was not recorded: %v", v2)
	}
	if _, v2b := signIn(nil); v2b == nil || *v2b != *v2 {
		t.Fatalf("the same notice became a second version: %v vs %v", v2b, v2)
	}

	// With no hotel notice, the built-in one (privacy.default) is shown and recorded.
	setNotice(map[string]any{"hotel_name": "Hotel"})
	_, v0 := signIn(nil)
	if v0 == nil || !strings.HasPrefix(text(*v0), "[built-in notice]") {
		t.Fatalf("a sign-in with no notice of the hotel's own was not recorded as the built-in notice: %v", v0)
	}

	// Stamps and versions are immutable.
	err := controlled(ctx, f.pool, []string{"auth_context"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iam_v2.auth_contexts SET privacy_notice_version_id=$2::uuid WHERE id=$1::uuid`, ctx1, *v2)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("the stamp of a sign-in was changed: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE public.privacy_notice_versions SET privacy_text='edited' WHERE id=$1::uuid`, *v1); err == nil {
		t.Fatal("a recorded notice was edited")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM public.privacy_notice_versions WHERE id=$1::uuid`, *v1); err == nil {
		t.Fatal("a recorded notice was deleted")
	}

	// Another guest's sign-in on another device stays out of this guest's export.
	var other, otherDevice string
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.guest_access_accounts (tenant_id, site_id, username, password_hash)
		VALUES ($1,$2,$3,'$argon2id$fixture$not-a-real-hash') RETURNING id::text`, f.tenant, f.site, "other-"+sfx+"@example.com").Scan(&other); err != nil {
		t.Fatalf("seed other account: %v", err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac)
		VALUES ($1,$2,gen_random_uuid(),'02:aa:bb:cc:dd:31'::macaddr) RETURNING id::text`, f.tenant, f.site).Scan(&otherDevice); err != nil {
		t.Fatalf("seed other device: %v", err)
	}
	if err := controlled(ctx, f.pool, []string{"auth_context"}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
			VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid,now()+interval '10 minutes')`, f.tenant, f.site, other, otherDevice, gn)
		return err
	}); err != nil {
		t.Fatalf("other guest's sign-in: %v", err)
	}

	st, raw := f.doRaw(t, "POST", "/guest-data-rights/export",
		map[string]any{"kind": "account", "id": acct, "reason": "guest asked", "password": f.password})
	if st != 200 {
		t.Fatalf("export = %d %s", st, raw)
	}
	var doc struct {
		Shown []struct {
			At     *string `json:"signed_in_at"`
			Method string  `json:"sign_in_method"`
			Notice string  `json:"notice"`
		} `json:"privacy_notices_shown"`
		Notices []struct {
			Notice string  `json:"notice"`
			Text   *string `json:"text"`
			Link   *string `json:"link"`
			Terms  *string `json:"terms_link"`
		} `json:"privacy_notices"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("export is not JSON: %v", err)
	}
	if len(doc.Shown) != 4 || len(doc.Notices) != 3 {
		t.Fatalf("export lists %d sign-ins and %d notices, want 4 and 3: %s", len(doc.Shown), len(doc.Notices), raw)
	}
	texts := map[string]bool{}
	for _, n := range doc.Notices {
		if n.Text != nil {
			texts[*n.Text] = true
		}
		if len(n.Notice) != 12 {
			t.Fatalf("notice reference %q", n.Notice)
		}
	}
	if !texts["Notice one "+sfx] || !texts["Notice two "+sfx] {
		t.Fatalf("the export does not carry the notices' text: %s", raw)
	}
	for _, s := range doc.Shown {
		if s.Method != "ACCOUNT" || s.At == nil {
			t.Fatalf("sign-in method %q at %v", s.Method, s.At)
		}
	}
}

// A network with its own portal profile shows that profile's notice, so the sign-in is recorded under it, not under the site's.
func TestIntegration_PrivacyNotice_ASignInOnAProfileNetworkIsRecordedUnderTheProfilesNotice(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	sfx := fmt.Sprint(time.Now().UnixNano())

	var before []byte
	if err := f.pool.QueryRow(ctx, `SELECT branding FROM tenants WHERE id=$1`, f.tenant).Scan(&before); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.pool.Exec(ctx, `UPDATE tenants SET branding=$2 WHERE id=$1`, f.tenant, before) })
	site, _ := json.Marshal(map[string]any{"design": map[string]any{"privacy_text": "Site notice " + sfx}})
	if _, err := f.pool.Exec(ctx, `UPDATE tenants SET branding=$2::jsonb WHERE id=$1`, f.tenant, string(site)); err != nil {
		t.Fatal(err)
	}

	acct, device, _ := seedEndedAccount(t, f, "pn-"+sfx+"@example.com", time.Hour, "10.88.0.31")
	plain, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, "Plain "+sfx, "10.98.0.0/24")
	special, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, "Special "+sfx, "10.99.0.0/24")
	prof := f.newProfile(t, "Notice "+sfx, "default")
	doc, _ := json.Marshal(map[string]any{"design": map[string]any{"privacy_text": "Profile notice " + sfx}})
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.portal_profiles SET doc=$2::jsonb WHERE id=$1::uuid`, prof, string(doc)); err != nil {
		t.Fatal(err)
	}
	if st, _ := f.do(t, "PUT", "/portal-branding/profiles/assignments/"+special, map[string]any{"profile_id": prof}); st != 200 {
		t.Fatalf("assign = %d", st)
	}

	var lastVersion string
	noticeOf := func(network string) string {
		t.Helper()
		var version *string
		if err := controlled(ctx, f.pool, []string{"auth_context"}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO iam_v2.auth_contexts (tenant_id,site_id,method,guest_account_id,device_id,guest_network_id,expires_at)
				VALUES ($1,$2,'ACCOUNT',$3::uuid,$4::uuid,$5::uuid,now()+interval '10 minutes') RETURNING privacy_notice_version_id::text`,
				f.tenant, f.site, acct, device, network).Scan(&version)
		}); err != nil || version == nil {
			t.Fatalf("sign in on %s: %v %v", network, err, version)
		}
		var text string
		_ = f.pool.QueryRow(ctx, `SELECT COALESCE(privacy_text,'<none>') FROM public.privacy_notice_versions WHERE id=$1::uuid`, *version).Scan(&text)
		lastVersion = *version
		return text
	}
	if got := noticeOf(plain); got != "Site notice "+sfx {
		t.Fatalf("a network without a profile was recorded under %q", got)
	}
	if got := noticeOf(special); got != "Profile notice "+sfx {
		t.Fatalf("a network with a profile was recorded under %q", got)
	}
	// The profile's stamp must be the version the site-wide function would record for the same design, or the two would drift apart.
	profileVersion := lastVersion
	if _, err := f.pool.Exec(ctx, `UPDATE tenants SET branding=$2::jsonb WHERE id=$1`, f.tenant, string(doc)); err != nil {
		t.Fatal(err)
	}
	var viaSite string
	if err := f.pool.QueryRow(ctx, `SELECT public.privacy_notice_in_force($1::uuid)::text`, f.tenant).Scan(&viaSite); err != nil || viaSite != profileVersion {
		t.Fatalf("the profile stamp %s differs from the site function's %s (%v)", profileVersion, viaSite, err)
	}
	// And a profile with no notice of its own records the built-in one, as the site does.
	bare, _ := json.Marshal(map[string]any{"design": map[string]any{"hotel_name": "Hotel"}})
	if _, err := f.pool.Exec(ctx, `UPDATE iam_v2.portal_profiles SET doc=$2::jsonb WHERE id=$1::uuid`, prof, string(bare)); err != nil {
		t.Fatal(err)
	}
	if got := noticeOf(special); !strings.HasPrefix(got, "[built-in notice]") {
		t.Fatalf("a profile with no notice was recorded under %q", got)
	}
}
