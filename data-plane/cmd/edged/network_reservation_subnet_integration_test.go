//go:build integration

package main

import (
	"context"
	"testing"
)

// P9 M5: a DHCP reservation whose address is outside its client network's subnet is refused when saved, naming the
// field, instead of failing later at apply.
func TestIntegration_ReservationOutsideTheSubnetIsRefusedWhenSaved(t *testing.T) {
	f := newAPI(t, "site_admin")
	net, _ := seedGuestNetwork(t, f.pool, f.tenant, f.site, "Res", "10.231.4.0/24")
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM dhcp_reservations WHERE guest_network_id=$1::uuid`, net)
	})

	st, body := f.do(t, "POST", "/network/dhcp/reservations", map[string]any{
		"guest_network_id": net, "mac": "02:00:5e:10:00:01", "reserved_ip": "10.231.5.10"})
	if st != 400 || body["error"] != "reservation_outside_subnet" {
		t.Fatalf("outside the subnet = %d %v, want 400 reservation_outside_subnet", st, body)
	}
	st, body = f.do(t, "POST", "/network/dhcp/reservations", map[string]any{
		"guest_network_id": net, "mac": "02:00:5e:10:00:01", "reserved_ip": "10.231.4.10"})
	if st != 201 {
		t.Fatalf("inside the subnet = %d %v", st, body)
	}
	id, _ := body["id"].(string)
	if st, body := f.do(t, "PUT", "/network/dhcp/reservations/"+id, map[string]any{"reserved_ip": "192.0.2.10"}); st != 400 || body["error"] != "reservation_outside_subnet" {
		t.Fatalf("moving it outside = %d %v", st, body)
	}
	if st, body := f.do(t, "PUT", "/network/dhcp/reservations/"+id, map[string]any{"reserved_ip": "10.231.4.11"}); st != 200 {
		t.Fatalf("moving it inside = %d %v", st, body)
	}
	if st, _ := f.do(t, "PUT", "/network/dhcp/reservations/"+id, map[string]any{"enabled": false}); st != 200 {
		t.Fatalf("an update without an address is not re-checked: %d", st)
	}
}
