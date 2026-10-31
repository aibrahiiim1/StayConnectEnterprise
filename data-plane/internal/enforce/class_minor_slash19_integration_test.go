package enforce

import (
	"context"
	"net"
	"testing"

	netdshape "github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// The database re-derives a session's class minor to refuse an observation whose class does not describe it. On a /19 client
// network that derivation must agree with netd's (internal/shape/minor.go) for EVERY address, including the upper half; before
// migration 0200 it used the low 12 bits for all addresses and refused every session at host index 4096 or above.
func TestIntegration_ClassMinorAgreesWithNetdOnASlash19(t *testing.T) {
	p := pool(t)
	defer p.Close()
	ctx := context.Background()
	f := seed(t, p, 1000, 1000, 0)

	tx, err := p.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO public.guest_networks
		(id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
		VALUES (gen_random_uuid(),$1,$2,'wide','q'||substr(md5(gen_random_uuid()::text),1,12),'c'||substr(md5(gen_random_uuid()::text),1,12),
		        '10.77.0.1/19'::inet,'10.77.0.1'::inet,'10.77.0.0/19'::cidr,true)`, f.tenant, f.site); err != nil {
		t.Fatalf("wide network: %v", err)
	}
	_, n, _ := net.ParseCIDR("10.77.0.0/19")
	for _, ip := range []string{"10.77.0.2", "10.77.15.254", "10.77.16.0", "10.77.16.1", "10.77.20.100", "10.77.31.250"} {
		want, ok := netdshape.MinorInNet(net.ParseIP(ip), n)
		if !ok {
			t.Fatalf("netd names no class for %s", ip)
		}
		var got int
		if err := tx.QueryRow(ctx, `SELECT iam_v2.p3_expected_class_minor($1::inet)`, ip).Scan(&got); err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
		if got != want {
			t.Fatalf("%s: the database expects class %#x, netd uses %#x", ip, got, want)
		}
	}
	// An address outside every /17../19 network keeps the original rule.
	var legacy int
	if err := tx.QueryRow(ctx, `SELECT iam_v2.p3_expected_class_minor('172.31.200.9'::inet)`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if want := 4096 + ((200&15)<<8 | 9); legacy != want {
		t.Fatalf("legacy rule changed: %d, want %d", legacy, want)
	}
}
