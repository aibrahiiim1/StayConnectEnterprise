package iamv2

// AN UNUSED PRINTED VOUCHER SURVIVES A CLIENT-NETWORK REPLACEMENT.
//
// THE DEFECT. A voucher is pinned at issuance to the package REVISION it was printed against, and redeems that
// revision directly — deliberately, so republishing or deactivating a package never invalidates printed codes.
// A revision is immutable and names client networks by id. Replacing a client network — moving a LAN port onto a
// tagged trunk, changing a VLAN id, re-addressing a subnet — gives that network a NEW id. So from the moment a
// cable moved, a SITE_NETWORK rule in the pinned revision named an id the guest could not possibly be on: the
// original is disabled, and the appliance can only resolve the source IP to the successor. The card answered
// "unavailable", the front desk had nothing to go on, and the operator's only remedy would have been to reissue
// every valid unused voucher after an ordinary cabling change.
//
// The forward republish performed at confirm cannot fix this. It publishes a LATER revision, and the card is
// pinned to the earlier one on purpose.
//
// WHAT FIXES IT is the successor keeping the retired network's LOGICAL identity (migration 0106): a rule naming
// a predecessor is satisfied by a device on the network that replaced it. History is not rewritten and the
// retired id is not re-pointed.

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAnUnusedVoucherStillRedeemsAfterItsClientNetworkWasReplaced(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()

	// The package is limited to ONE client network: the one that is about to be replaced.
	var retired string
	if err := db.QueryRow(ctx, `INSERT INTO public.guest_networks
		  (tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
		VALUES ($1,$2,'lobby-retired','br-lan','br-g-retired','10.78.0.1/24','10.78.0.1','10.78.0.0/24',false)
		RETURNING id::text`, p2Tenant, p2Site).Scan(&retired); err != nil {
		t.Fatalf("seed retired network: %v", err)
	}
	s := seedFreeCommerce(t, db, func(o *seedOpts) {
		o.rules = `[{"type":"SITE_NETWORK","value":{"guest_network_ids":["` + retired + `"]}}]`
	})

	// A card printed against that revision, still UNUSED and inside its window.
	keyGen := mustOne(t, db, `INSERT INTO iam_v2.voucher_code_key_generations
		  (tenant_id,site_id,generation_no,hmac_key_ciphertext,aead_params,encryption_key_id)
		VALUES ($1,$2,1,'\x00','{}'::jsonb,gen_random_uuid()) RETURNING id::text`, p2Tenant, p2Site)
	voucherID := mustOne(t, db, `INSERT INTO iam_v2.vouchers
		  (tenant_id,site_id,code_hmac,code_ciphertext,code_nonce,code_key_generation_id,code_last4,
		   package_revision_id,state,redemption_valid_from,redemption_valid_until)
		VALUES ($1,$2,$3,'\x00','\x00',$4::uuid,'1234',$5,'UNUSED', now()-interval '1 day', now()+interval '30 days')
		RETURNING id::text`, p2Tenant, p2Site, []byte("voucher-blind-index-survives-replacement"), keyGen, s.pkgRevID)

	// The guest is on the SUCCESSOR network — the only one the appliance can resolve now.
	voucherCtx := mustOneGuarded(t, db, "auth_context", `INSERT INTO iam_v2.auth_contexts
		  (tenant_id,site_id,method,voucher_id,device_id,guest_network_id,expires_at)
		VALUES ($1,$2,'VOUCHER',$3::uuid,$4::uuid,$5::uuid, now()+interval '10 min') RETURNING id::text`,
		p2Tenant, p2Site, voucherID, s.deviceID, p2GN)

	e := newEngine(t, db, 5*time.Minute)
	quote := func() QuoteResult {
		q, err := e.CreateQuote(ctx, QuoteRequest{TenantID: p2Tenant, SiteID: p2Site, AuthContextID: voucherCtx,
			PackageID: s.packageID, DeviceID: s.deviceID, GuestNetworkID: p2GN})
		if err != nil {
			t.Fatalf("quote: %v", err)
		}
		return q
	}

	// WITHOUT a recorded replacement the refusal is correct and must stay correct: this network is simply not
	// the one the card is for. If this ever starts passing, the lineage has stopped being scoped and is
	// widening eligibility to networks nobody connected.
	if q := quote(); q.QuoteID != "" || q.Reason != "ineligible:guest_network_not_allowed" {
		t.Fatalf("with no replacement recorded, quote = %+v; a card for another network must be refused", q)
	}

	// Now record that the guest's network REPLACED the one the card names, applied and confirmed.
	replID := mustOne(t, db, `INSERT INTO iam_v2.guest_network_replacements
		  (tenant_id,site_id,original_network_id,successor_network_id,state,network_type,parent_interface,
		   vlan_id,reason,created_by,applied_at,settled_at)
		VALUES ($1,$2,$3::uuid,$4::uuid,'CONFIRMED','vlan','br-lan',70,'port moved to a trunk','operator@test',now(),now())
		RETURNING id::text`, p2Tenant, p2Site, retired, p2GN)

	q := quote()
	if q.QuoteID == "" || q.Reason != "ok" {
		t.Fatalf("after the replacement the printed card must still redeem, got %+v", q)
	}

	// ...and the lineage is scoped to a replacement that actually took effect. Rolled back means it never
	// happened on the wire, and the card is for a network that is live again somewhere else.
	if _, err := db.Exec(ctx, `UPDATE iam_v2.guest_network_replacements SET state='REVERTED' WHERE id=$1`, replID); err != nil {
		t.Fatal(err)
	}
	if q := quote(); q.QuoteID != "" || q.Reason != "ineligible:guest_network_not_allowed" {
		t.Fatalf("a REVERTED replacement must continue nothing, got %+v", q)
	}

	// A PENDING one has not happened either: nothing is on the wire until the operator applies it.
	if _, err := db.Exec(ctx, `UPDATE iam_v2.guest_network_replacements
		   SET state='PENDING', successor_network_id=NULL WHERE id=$1`, replID); err != nil {
		t.Fatal(err)
	}
	if q := quote(); q.QuoteID != "" || q.Reason != "ineligible:guest_network_not_allowed" {
		t.Fatalf("a PENDING replacement must continue nothing, got %+v", q)
	}

	// Through the whole test the card was never burned: a refused quote must not consume it.
	var state string
	if err := db.QueryRow(ctx, `SELECT state FROM iam_v2.vouchers WHERE id=$1`, voucherID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "UNUSED" {
		t.Fatalf("voucher state = %s, want UNUSED: quoting must never redeem", state)
	}
}

// TRANSITIVELY. A network replaced twice still continues the first one, because a hotel that moves a port onto a
// trunk and later re-addresses it has not changed who the package was written for either time.
func TestLineageIsTransitiveAcrossSeveralReplacements(t *testing.T) {
	db := p2DB(t)
	ctx := context.Background()

	first := mustOne(t, db, `INSERT INTO public.guest_networks
		  (tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
		VALUES ($1,$2,'gen1','br-lan','br-g-gen1','10.81.0.1/24','10.81.0.1','10.81.0.0/24',false) RETURNING id::text`,
		p2Tenant, p2Site)
	second := mustOne(t, db, `INSERT INTO public.guest_networks
		  (tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
		VALUES ($1,$2,'gen2','br-lan','br-g-gen2','10.82.0.1/24','10.82.0.1','10.82.0.0/24',false) RETURNING id::text`,
		p2Tenant, p2Site)
	for _, pair := range [][2]string{{first, second}, {second, p2GN}} {
		if _, err := db.Exec(ctx, `INSERT INTO iam_v2.guest_network_replacements
			  (tenant_id,site_id,original_network_id,successor_network_id,state,network_type,parent_interface,
			   vlan_id,reason,created_by,applied_at,settled_at)
			VALUES ($1,$2,$3::uuid,$4::uuid,'CONFIRMED','vlan','br-lan',71,'successive changes','operator@test',now(),now())`,
			p2Tenant, p2Site, pair[0], pair[1]); err != nil {
			t.Fatalf("record replacement %s -> %s: %v", pair[0], pair[1], err)
		}
	}

	rows, err := db.Query(ctx, `SELECT ancestor_id::text, depth FROM iam_v2.guest_network_lineage
		 WHERE tenant_id=$1 AND site_id=$2 AND network_id=$3 ORDER BY depth`, p2Tenant, p2Site, p2GN)
	if err != nil {
		t.Fatalf("lineage: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id string
		var depth int
		if err := rows.Scan(&id, &depth); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	if len(got) != 2 || got[0] != second || got[1] != first {
		t.Fatalf("lineage of the current network = %v, want [%s %s] (nearest predecessor first)", got, second, first)
	}
}

// mustOneGuarded is mustOne inside an open controlled operation. The auth_context family refuses a write from
// any caller that has not declared one (p3_auth_context_controlled_writer), which is the invariant that keeps a
// context from being forged outside the authenticator -- a test is no exception.
func mustOneGuarded(t *testing.T, db *pgxpool.Pool, op, q string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, op); err != nil {
		t.Fatalf("open controlled operation %s: %v", op, err)
	}
	var out string
	if err := tx.QueryRow(ctx, q, args...).Scan(&out); err != nil {
		t.Fatalf("guarded query failed: %v -- %s", err, q)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return out
}

// mustOne runs a one-column query that has to produce exactly one row.
func mustOne(t *testing.T, db *pgxpool.Pool, q string, args ...any) string {
	t.Helper()
	var out string
	if err := db.QueryRow(context.Background(), q, args...).Scan(&out); err != nil {
		t.Fatalf("query failed: %v\n%s", err, q)
	}
	return out
}
