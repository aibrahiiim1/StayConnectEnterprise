package portaltls

// Migration 0064 (onegate.work retired) against the Pebble lab's database; skipped unless PORTALTLS_PEBBLE=1.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPebbleMigration0064MovesNamesOffTheRetiredDomain(t *testing.T) {
	env := pebbleEnvOrSkip(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, env.pgURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	up, err := os.ReadFile("../../migrations/0064_portal_names_move_to_getsemantics.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	var def string
	if err := pool.QueryRow(ctx, `SELECT column_default FROM information_schema.columns
	        WHERE table_name='portal_tls_settings' AND column_name='zone_name'`).Scan(&def); err != nil || def != "'onegate.getsemantics.com'::text" {
		t.Fatalf("a fresh database defaults to the new base: %q %v", def, err)
	}

	for _, q := range []string{`DELETE FROM portal_certificate_orders`, `DELETE FROM portal_custom_domains`, `DELETE FROM portal_hostnames`} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var tenant, site string
	_ = pool.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('m64-'||substr(md5(random()::text),1,8), 'm64') RETURNING id::text`).Scan(&tenant)
	_ = pool.QueryRow(ctx, `INSERT INTO sites (tenant_id, code, name) VALUES ($1,'M64','m64') RETURNING id::text`, tenant).Scan(&site)
	app := func(serial string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO appliances (tenant_id, site_id, serial, lifecycle_state, activated_at)
		        VALUES ($1,$2,$3,'assigned',now()) RETURNING id::text`, tenant, site, serial+fmt.Sprint(time.Now().UnixNano())).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	live, gone := app("M64-L"), app("M64-R")
	reset := func() {
		for _, q := range []string{
			`UPDATE portal_tls_settings SET zone_name='onegate.work', zone_id='z-old' WHERE id`,
			`DELETE FROM portal_certificate_orders`, `DELETE FROM portal_hostnames`,
		} {
			if _, err := pool.Exec(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := pool.Exec(ctx, `INSERT INTO portal_hostnames (appliance_id, label, fqdn, portal_ip) VALUES
		        ($1,'lr01ehm0','lr01ehm0.gw.onegate.work','10.255.255.1'), ($2,'qq11ww22','qq11ww22.gw.onegate.work',NULL)`, live, gone); err != nil {
			t.Fatal(err)
		}
		_, _ = pool.Exec(ctx, `UPDATE portal_hostnames SET released_at=now() WHERE appliance_id=$1`, gone)
		if _, err := pool.Exec(ctx, `INSERT INTO portal_certificate_orders (appliance_id, hostname, kind, csr_der)
		        VALUES ($1,'lr01ehm0.gw.onegate.work','ONEGATE','\x00')`, live); err != nil {
			t.Fatal(err)
		}
	}

	// 1. Nothing published under the old domain: the names move, keeping their labels.
	reset()
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("0064: %v", err)
	}
	var zone, zid, fqdnLive, fqdnGone, status, orderErr string
	_ = pool.QueryRow(ctx, `SELECT zone_name, COALESCE(zone_id,'') FROM portal_tls_settings WHERE id`).Scan(&zone, &zid)
	_ = pool.QueryRow(ctx, `SELECT fqdn FROM portal_hostnames WHERE appliance_id=$1`, live).Scan(&fqdnLive)
	_ = pool.QueryRow(ctx, `SELECT fqdn FROM portal_hostnames WHERE appliance_id=$1`, gone).Scan(&fqdnGone)
	_ = pool.QueryRow(ctx, `SELECT status, COALESCE(error,'') FROM portal_certificate_orders WHERE appliance_id=$1`, live).Scan(&status, &orderErr)
	if zone != "onegate.getsemantics.com" || zid != "" {
		t.Fatalf("zone moved and its cached id cleared: %q %q", zone, zid)
	}
	if fqdnLive != "lr01ehm0.gw.onegate.getsemantics.com" || fqdnGone != "qq11ww22.gw.onegate.work" {
		t.Fatalf("a live name moves with its label, a released one stays history: %q %q", fqdnLive, fqdnGone)
	}
	if status != "FAILED" || orderErr == "" {
		t.Fatalf("an open order for the old name is cancelled: %s %q", status, orderErr)
	}
	var moved int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM portal_tls_events WHERE actor='migration 0064' AND kind='HOSTNAME_ALLOCATED'
	        AND hostname='lr01ehm0.gw.onegate.getsemantics.com' AND detail->>'moved_from'='lr01ehm0.gw.onegate.work'`).Scan(&moved)
	if moved < 1 {
		t.Fatal("the move is recorded")
	}

	// 2. A record published under the old domain: nothing moves.
	reset()
	_, _ = pool.Exec(ctx, `UPDATE portal_hostnames SET dns_record_id='rec1' WHERE appliance_id=$1`, live)
	if _, err := pool.Exec(ctx, string(up)); err != nil {
		t.Fatalf("0064 with a published record: %v", err)
	}
	_ = pool.QueryRow(ctx, `SELECT zone_name FROM portal_tls_settings WHERE id`).Scan(&zone)
	_ = pool.QueryRow(ctx, `SELECT fqdn FROM portal_hostnames WHERE appliance_id=$1`, live).Scan(&fqdnLive)
	if zone != "onegate.work" || fqdnLive != "lr01ehm0.gw.onegate.work" {
		t.Fatalf("with a published record nothing moves: %q %q", zone, fqdnLive)
	}

	// Leave the lab where the other tests expect it.
	for _, q := range []string{`DELETE FROM portal_certificate_orders`, `DELETE FROM portal_hostnames`,
		`UPDATE portal_tls_settings SET zone_name='onegate.getsemantics.com', zone_id=NULL WHERE id`} {
		_, _ = pool.Exec(ctx, q)
	}
}
