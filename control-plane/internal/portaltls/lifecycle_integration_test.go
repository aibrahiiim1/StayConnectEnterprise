package portaltls

import (
	"context"
	"errors"
	"testing"

	"github.com/stayconnect/enterprise/control-plane/internal/testdb"
)

func refusalCode(err error) string {
	var r *Refusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

func TestReleasedHostnameIsNeverRestored(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	var app string
	if err := pool.QueryRow(ctx, `INSERT INTO appliances (serial, lifecycle_state, public_key, activated_at)
        VALUES ('SC-PTLS-0001','assigned','k', now()) RETURNING id::text`).Scan(&app); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO portal_hostnames (appliance_id, label, fqdn, released_at)
        VALUES ($1,'abcdefgh','abcdefgh.gw.onegate.getsemantics.com', now() - interval '1 hour')`, app); err != nil {
		t.Fatal(err)
	}
	svc := New(pool, Options{SecretsDir: t.TempDir()})
	_, err := svc.Sync(ctx, app, SyncRequest{PortalIP: "10.0.0.1"})
	if refusalCode(err) != "retired" {
		t.Fatalf("a released hostname must stay released, got %v", err)
	}
	var released int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM portal_hostnames WHERE appliance_id=$1 AND released_at IS NOT NULL`, app).Scan(&released)
	if released != 1 {
		t.Fatal("the hostname row must remain released")
	}
	var restored int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM portal_tls_events WHERE kind=$1`, EvHostnameRestored).Scan(&restored)
	if restored != 0 {
		t.Fatal("no hostname may be restored")
	}
}

func TestCAAOrderingIsProductionFirst(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	svc := New(pool, Options{SecretsDir: t.TempDir()})
	if _, err := svc.ApplyCAA(ctx, "ops"); refusalCode(err) != "caa_needs_production" {
		t.Fatalf("CAA on staging must be refused before anything is contacted, got %v", err)
	}
	prod, staging := DirectoryProduction, DirectoryStaging
	if _, err := svc.UpdateSettings(ctx, SettingsUpdate{ACMEDirectory: &prod}, "ops"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE portal_tls_settings SET caa_applied_at = now() WHERE id`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UpdateSettings(ctx, SettingsUpdate{ACMEDirectory: &staging}, "ops"); refusalCode(err) != "caa_pins_production" {
		t.Fatalf("leaving production while CAA pins it must be refused, got %v", err)
	}
	st, _ := LoadSettings(ctx, pool)
	if st.ACMEDirectory != DirectoryProduction {
		t.Fatal("the directory must stay production")
	}
}
