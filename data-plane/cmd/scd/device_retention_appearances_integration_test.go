//go:build integration

package main

import (
	"context"
	"testing"
)

// 0211: a device's own network appearances no longer keep it forever. Idle is judged only from recorded timestamps (the device's
// or its appearances'), a device with none is kept, and the run goes through the production role. Needs PHASE3_TEST_DSN.
func TestIntegration_PruneIdleDevicesRemovesIdleDevicesWithTheirOwnAppearances(t *testing.T) {
	f := newSSFixture(t)
	ctx := context.Background()
	var applied bool
	if err := f.owner.QueryRow(ctx, `SELECT position('device_network_appearances x USING doomed' IN pg_get_functiondef('iam_v2.prune_idle_devices(uuid,uuid,integer)'::regprocedure)) > 0`).Scan(&applied); err != nil || !applied {
		t.Skip("migration 0211 is not applied on this database")
	}
	seed := func(mac, devSeen, appSeen string) string {
		t.Helper()
		var id string
		if err := f.owner.QueryRow(ctx, `INSERT INTO iam_v2.devices (tenant_id, site_id, appliance_id, mac, first_seen, last_seen)
			VALUES ($1,$2,gen_random_uuid(),$3::macaddr, now() - $4::interval, now() - $4::interval) RETURNING id::text`,
			f.tenant, f.site, mac, nullable(devSeen)).Scan(&id); err != nil {
			t.Fatalf("seed device: %v", err)
		}
		f.ownerExec(t, `INSERT INTO iam_v2.device_network_appearances (tenant_id, site_id, device_id, guest_network_id, first_seen, last_seen)
			VALUES ($1,$2,$3,$4, now() - $5::interval, now() - $5::interval)`, f.tenant, f.site, id, f.network, nullable(appSeen))
		return id
	}
	idle := seed("02:00:00:00:0b:01", "", "200 days")         // only the appearance recorded when it was seen: 200 days ago
	recentOnNetwork := seed("02:00:00:00:0b:02", "", "1 day") // its appearance says yesterday
	noEvidence := seed("02:00:00:00:0b:03", "", "")           // nothing recorded: never judged idle
	recentDevice := seed("02:00:00:00:0b:04", "1 day", "200 days")

	f.s.pruneIdleDevices(ctx)

	exists := func(table, col, id string) bool {
		var ok bool
		if err := f.owner.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM iam_v2.`+table+` WHERE `+col+` = $1::uuid)`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if exists("devices", "id", idle) || exists("device_network_appearances", "device_id", idle) {
		t.Fatal("a device idle past retention, referenced only by its own appearance, was not removed with it")
	}
	for name, id := range map[string]string{"seen on a network yesterday": recentOnNetwork, "with no recorded timestamp": noEvidence, "seen yesterday": recentDevice} {
		if !exists("devices", "id", id) || !exists("device_network_appearances", "device_id", id) {
			t.Fatalf("the device %s was removed", name)
		}
	}
	var blank int
	if err := f.owner.QueryRow(ctx, `SELECT count(*) FROM iam_v2.devices WHERE id = $1::uuid AND first_seen IS NULL AND last_seen IS NULL`, noEvidence).Scan(&blank); err != nil || blank != 1 {
		t.Fatalf("retention wrote a timestamp onto a device that had none (%v)", err)
	}
}

func nullable(interval string) any {
	if interval == "" {
		return nil
	}
	return interval
}
