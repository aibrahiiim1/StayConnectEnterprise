//go:build integration

package main

// The device's client network must decide the same things on the room sign-in path as elsewhere:
//
//  1. offersFor receives the server-derived network id (an empty id drops every SITE_NETWORK package).
//  2. A replaced client network keeps its logical identity via the lineage view, scoped to replacements
//     that took effect (a reverted apply must widen nothing).
//  3. Two networks mapped to two PMS interfaces must not leak into each other: room numbers are a PMS's
//     namespace.
//
// Every positive assertion has a negative counterpart, since a permissive evaluator would pass the positive alone.

import (
	"context"
	"fmt"
	"testing"
)

// Fixtures for a second client network and PMS interface: stays are seeded inside an opened 'stay' controlled
// operation, revisions are published via the pointer scd pins, and a healthy runtime row is required or the
// resolve path refuses early.

// clientNetwork is one seeded client network plus a device sitting on it.
type clientNetwork struct {
	id              string
	subnet, gateway string
	ip, mac         string
}

// addClientNetwork seeds one client network with its own subnet. `enabled` is false for the retired side of
// a replacement (applied replacements disable the original row); a disabled network resolves no devices.
func (f *authFixture) addClientNetwork(t *testing.T, label string, enabled bool) clientNetwork {
	t.Helper()
	n := claimFixtureNet(t, f.pool)
	cn := clientNetwork{
		subnet:  n.subnet,
		gateway: n.gateway,
		ip:      n.host(40),
		mac:     fmt.Sprintf("02:00:00:dd:%02x:%02x", (n.index>>8)&0xff, n.index&0xff),
	}
	// bridge_name and parent_interface must be unique, so derive both from a fresh uuid.
	if err := f.pool.QueryRow(context.Background(), `
		INSERT INTO public.guest_networks
		  (id,tenant_id,site_id,name,parent_interface,bridge_name,gateway_cidr,gateway_ip,subnet_cidr,enabled)
		SELECT gen_random_uuid(), $1, $2, $3,
		       'p'||substr(md5(g::text),1,12), 'b'||substr(md5(g::text),1,12),
		       ($4::text)::inet, ($5::text)::inet, ($6::text)::cidr, $7
		  FROM gen_random_uuid() g
		RETURNING id::text`,
		f.tenant, f.site, label, cn.gateway+"/24", cn.gateway, cn.subnet, enabled).Scan(&cn.id); err != nil {
		t.Fatalf("add client network %s: %v", label, err)
	}
	return cn
}

// addPMSInterface seeds a second ACTIVE PMS interface with a published revision (the pointer scd pins) and a
// healthy runtime row, and returns both ids.
func (f *authFixture) addPMSInterface(t *testing.T, label string) (ifaceID, revisionID string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin interface seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open controlled operation: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO iam_v2.pms_interfaces(id,tenant_id,site_id,connector_kind,display_label,lifecycle_state)
		VALUES (gen_random_uuid(),$1,$2,'protel-fias',$3,'ACTIVE') RETURNING id::text`,
		f.tenant, f.site, label).Scan(&ifaceID); err != nil {
		t.Fatalf("add pms interface %s: %v", label, err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO iam_v2.pms_interface_revisions
		  (id,tenant_id,site_id,pms_interface_id,revision_no,source_timezone,config)
		VALUES (gen_random_uuid(),$1,$2,$3,1,'UTC','{}'::jsonb) RETURNING id::text`,
		f.tenant, f.site, ifaceID).Scan(&revisionID); err != nil {
		t.Fatalf("add interface revision: %v", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE iam_v2.pms_interfaces SET current_revision_id=$2::uuid WHERE id=$1::uuid`,
		ifaceID, revisionID); err != nil {
		t.Fatalf("publish the interface revision: %v", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.pms_interface_runtime
		(tenant_id, site_id, pms_interface_id, runtime_generation, credential_mode, published_resync_generation,
		 pinned_revision_id, transport_status, sync_status, continuity_status, last_connected_at, last_heartbeat_at)
		VALUES ($1,$2,$3::uuid,1,'NONE',0,$4::uuid,'CONNECTED','IN_SYNC','CONTINUOUS',now(),now())`,
		f.tenant, f.site, ifaceID, revisionID); err != nil {
		t.Fatalf("seed pms interface runtime: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit interface seed: %v", err)
	}
	return ifaceID, revisionID
}

// mapClientNetworkToPMSInterface routes a network's sign-ins at one interface; it alone decides which interfaces are probed.
func (f *authFixture) mapClientNetworkToPMSInterface(t *testing.T, networkID, ifaceID string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id,is_default)
		VALUES ($1,$2,$3::uuid,$4::uuid,true)`, f.tenant, f.site, networkID, ifaceID); err != nil {
		t.Fatalf("map client network to interface: %v", err)
	}
}

// addStay seeds one IN_HOUSE stay with fresh occupancy evidence from that interface's own published revision;
// without it, or with another interface's revision, no Auth Context can issue.
func (f *authFixture) addStay(t *testing.T, ifaceID, revisionID, room, surnameNorm string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin stay seed: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT iam_v2.begin_controlled_operation('stay')`); err != nil {
		t.Fatalf("open controlled operation: %v", err)
	}
	var stayID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO iam_v2.stays
		  (id,tenant_id,site_id,pms_interface_id,external_reservation_id,external_stay_identity,
		   normalized_room_number,status,lifecycle_version,last_applied_event_version)
		VALUES (gen_random_uuid(),$1,$2,$3::uuid,$4,$5,$6,'IN_HOUSE',1,0) RETURNING id::text`,
		f.tenant, f.site, ifaceID,
		"RES-"+room+"-"+surnameNorm, "STAY-"+room+"-"+surnameNorm, room).Scan(&stayID); err != nil {
		t.Fatalf("add stay %s/%s: %v", room, surnameNorm, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO iam_v2.stay_guests(tenant_id,site_id,pms_interface_id,stay_id,last_name_norm,is_primary)
		VALUES ($1,$2,$3::uuid,$4::uuid,$5,true)`,
		f.tenant, f.site, ifaceID, stayID, surnameNorm); err != nil {
		t.Fatalf("add stay guest %s: %v", surnameNorm, err)
	}
	// The occupancy tuple is all-or-none.
	if _, err := tx.Exec(ctx, `
		UPDATE iam_v2.stays SET
		  occupancy_evidence_at = now() - interval '10 seconds',
		  occupancy_ingested_at = now() - interval '9 seconds',
		  occupancy_revision_id = $2::uuid,
		  occupancy_normalization_version = 1,
		  occupancy_clock_suspect = false,
		  occupancy_evidence_version = 1,
		  arrival = (now() - interval '2 days')::date, departure = (now() + interval '1 day')::date
		 WHERE id = $1::uuid`, stayID, revisionID); err != nil {
		t.Fatalf("seed occupancy evidence for %s: %v", stayID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit stay seed: %v", err)
	}
	return stayID
}

// resolveFrom is resolveBody for a device on a named network; the network is derived from the source address, never submitted.
func (f *authFixture) resolveFrom(cn clientNetwork, room, last, reqID string) map[string]any {
	return map[string]any{
		"room": room, "last_name": last, "reservation_number": "", "request_id": reqID,
		"device": map[string]string{"ip": cn.ip, "mac": cn.mac},
	}
}

func offeredRevisions(res phase3Response) map[string]bool {
	out := map[string]bool{}
	for _, o := range res.Offers {
		out[o.PackageRevisionID] = true
	}
	return out
}

// contextStayAndInterface reads what the Auth Context pinned; the response deliberately names neither.
func (f *authFixture) contextStayAndInterface(t *testing.T, contextID string) (stay, iface string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(),
		`SELECT stay_id::text, pms_interface_id::text FROM iam_v2.auth_contexts
		  WHERE id=$1::uuid AND tenant_id=$2 AND site_id=$3`,
		contextID, f.tenant, f.site).Scan(&stay, &iface); err != nil {
		t.Fatalf("read the auth context %s: %v", contextID, err)
	}
	return stay, iface
}

func siteNetworkRule(networkIDs ...string) []map[string]any {
	return []map[string]any{{
		"type":  "SITE_NETWORK",
		"value": map[string]any{"guest_network_ids": networkIDs},
	}}
}

// A package restricted to the guest's own client network is offered on the room path. A second package naming a
// different network must be absent: one assertion proves the id arrives, the other that it still refuses.
func TestIntegration_Phase3SiteNetwork_RoomSignInOffersThePackageScopedToTheDevicesNetwork(t *testing.T) {
	f := newAuthFixture(t)

	thisNetwork := f.addPackage(t, "ON_THIS_NETWORK", siteNetworkRule(f.network))
	someOtherNetwork := f.addPackage(t, "ON_ANOTHER_NETWORK", siteNetworkRule(mustUUID(t, f.pool)))

	_, res := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", mustUUID(t, f.pool)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("the stay did not verify: %+v", res)
	}
	offered := offeredRevisions(res)
	if !offered[thisNetwork] {
		t.Fatalf("a package restricted to the client network this device is ACTUALLY ON was not offered to a "+
			"verified room sign-in. That is the defect: the offer set was computed with an empty client "+
			"network id, so no SITE_NETWORK rule could ever match. Offers: %+v", res.Offers)
	}
	if offered[someOtherNetwork] {
		t.Fatalf("a package restricted to a DIFFERENT client network was offered. The id is reaching the "+
			"evaluator but no longer deciding anything, which would make the assertion above vacuous: %+v",
			res.Offers)
	}
	// The unrestricted package is the control for a fully-evaluated offer set.
	if !offered[f.pkgRev] {
		t.Fatalf("the unrestricted included package was not offered: %+v", res.Offers)
	}
}

// A rule naming a retired client network is satisfied by a device on its successor, only when the replacement
// took effect. Pinned revisions cannot be rewritten, so without lineage a cabling change would make unused
// printed vouchers unredeemable. Flipping the replacement to REVERTED must stop the match: reading lineage
// regardless of state would widen eligibility to a network that replaced nothing. The unrestricted package
// is asserted in both halves so the second half cannot pass because sign-in broke.
func TestIntegration_Phase3SiteNetwork_AReplacedClientNetworkKeepsItsLogicalIdentity(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	// The retired original is disabled, as an applied replacement leaves it.
	retired := f.addClientNetwork(t, "retired-untagged-port", false)
	// The successor is live and routed at the same PMS interface.
	successor := f.addClientNetwork(t, "guests-on-the-trunk", true)
	f.mapClientNetworkToPMSInterface(t, successor.id, f.iface)

	var replacement string
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO iam_v2.guest_network_replacements
		  (tenant_id,site_id,original_network_id,successor_network_id,state,
		   network_type,parent_interface,vlan_id,reason,created_by,applied_at,settled_at)
		VALUES ($1,$2,$3::uuid,$4::uuid,'CONFIRMED','vlan','ens192',140,
		        'moved the guest port onto a tagged trunk','integration-test',now(),now())
		RETURNING id::text`,
		f.tenant, f.site, retired.id, successor.id).Scan(&replacement); err != nil {
		t.Fatalf("stage the confirmed replacement: %v", err)
	}

	// A pinned revision's rule, written against the original network.
	namingRetired := f.addPackage(t, "ON_THE_RETIRED_NETWORK", siteNetworkRule(retired.id))

	_, res := post(t, f.p3.resolveHandler,
		f.resolveFrom(successor, "412", "Okonkwo", mustUUID(t, f.pool)))
	if res.Outcome != outcomeVerified {
		t.Fatalf("a guest on the successor network did not verify: %+v", res)
	}
	offered := offeredRevisions(res)
	if !offered[f.pkgRev] {
		t.Fatalf("setup: the unrestricted included package was not offered on the successor network: %+v", res.Offers)
	}
	if !offered[namingRetired] {
		t.Fatalf("a package whose pinned revision names the RETIRED client network was not offered to a device "+
			"on its successor. A cabling change must not narrow eligibility, and an unused printed voucher "+
			"pinned to such a revision would have become unredeemable: %+v", res.Offers)
	}

	// Negative: the apply did not stand, so nothing is inherited.
	if _, err := f.pool.Exec(ctx,
		`UPDATE iam_v2.guest_network_replacements SET state='REVERTED', settled_at=now() WHERE id=$1::uuid`,
		replacement); err != nil {
		t.Fatalf("revert the replacement: %v", err)
	}

	_, after := post(t, f.p3.resolveHandler,
		f.resolveFrom(successor, "412", "Okonkwo", mustUUID(t, f.pool)))
	if after.Outcome != outcomeVerified {
		t.Fatalf("the sign-in itself stopped working after the revert, so the assertion below would prove "+
			"nothing: %+v", after)
	}
	afterOffered := offeredRevisions(after)
	if !afterOffered[f.pkgRev] {
		t.Fatalf("the unrestricted included package vanished after the revert, so the offer path broke rather "+
			"than the lineage narrowing: %+v", after.Offers)
	}
	if afterOffered[namingRetired] {
		t.Fatalf("a REVERTED replacement still conferred the retired network's identity. Lineage must count "+
			"only replacements that actually took effect; a rolled-back request never happened on the wire "+
			"and must widen eligibility to nothing: %+v", after.Offers)
	}
}

// A device's client network decides which PMS namespace answers and which packages it sees. A stay on
// interface 2 must not verify from network 1 and vice versa, else a path probing both would pass.
func TestIntegration_Phase3SiteNetwork_TwoClientNetworksResolveAgainstTheirOwnPMSInterface(t *testing.T) {
	f := newAuthFixture(t)

	secondNet := f.addClientNetwork(t, "guests-building-two", true)
	secondIface, secondRev := f.addPMSInterface(t, "building two")
	f.mapClientNetworkToPMSInterface(t, secondNet.id, secondIface)
	secondStay := f.addStay(t, secondIface, secondRev, "501", "WHITFIELD")

	onlyNetworkOne := f.addPackage(t, "NETWORK_ONE_ONLY", siteNetworkRule(f.network))
	onlyNetworkTwo := f.addPackage(t, "NETWORK_TWO_ONLY", siteNetworkRule(secondNet.id))

	// Network one: interface one's stay and package.
	_, one := post(t, f.p3.resolveHandler,
		f.resolveBody("412", "Okonkwo", "", mustUUID(t, f.pool)))
	if one.Outcome != outcomeVerified {
		t.Fatalf("the stay on interface one did not verify from network one: %+v", one)
	}
	stay, iface := f.contextStayAndInterface(t, one.AuthContextID)
	if stay != f.stay || iface != f.iface {
		t.Fatalf("a sign-in from client network one pinned stay %s on interface %s, want %s on %s",
			stay, iface, f.stay, f.iface)
	}
	offered := offeredRevisions(one)
	if !offered[onlyNetworkOne] {
		t.Fatalf("network one was not offered its own package: %+v", one.Offers)
	}
	if offered[onlyNetworkTwo] {
		t.Fatalf("network one was offered the package restricted to network two: %+v", one.Offers)
	}

	// Network two: interface two's stay and package.
	_, two := post(t, f.p3.resolveHandler,
		f.resolveFrom(secondNet, "501", "Whitfield", mustUUID(t, f.pool)))
	if two.Outcome != outcomeVerified {
		t.Fatalf("the stay on interface two did not verify from network two: %+v", two)
	}
	stay, iface = f.contextStayAndInterface(t, two.AuthContextID)
	if stay != secondStay || iface != secondIface {
		t.Fatalf("a sign-in from client network two pinned stay %s on interface %s, want %s on %s",
			stay, iface, secondStay, secondIface)
	}
	offered = offeredRevisions(two)
	if !offered[onlyNetworkTwo] {
		t.Fatalf("network two was not offered its own package: %+v", two.Offers)
	}
	if offered[onlyNetworkOne] {
		t.Fatalf("network two was offered the package restricted to network one: %+v", two.Offers)
	}

	// Negatives: a submission valid on the other interface is an ordinary non-success here (never probed).
	if _, crossed := post(t, f.p3.resolveHandler,
		f.resolveBody("501", "Whitfield", "", mustUUID(t, f.pool))); crossed.Outcome == outcomeVerified {
		t.Fatalf("interface two's guest verified from client network one, which maps only to interface one: %+v",
			crossed)
	}
	if _, crossed := post(t, f.p3.resolveHandler,
		f.resolveFrom(secondNet, "412", "Okonkwo", mustUUID(t, f.pool))); crossed.Outcome == outcomeVerified {
		t.Fatalf("interface one's guest verified from client network two, which maps only to interface two: %+v",
			crossed)
	}
}

// A room number belongs to a PMS, not the property: the surname that opens room 707 on interface one must not
// open room 707 on interface two. Both guests verifying fits a broken site-wide search; only refusals pin the namespace.
func TestIntegration_Phase3SiteNetwork_TheSameRoomNumberOnTwoInterfacesDoesNotCollide(t *testing.T) {
	f := newAuthFixture(t)

	secondNet := f.addClientNetwork(t, "guests-building-two", true)
	secondIface, secondRev := f.addPMSInterface(t, "building two")
	f.mapClientNetworkToPMSInterface(t, secondNet.id, secondIface)

	// The same room number on each interface with different guests.
	const room = "707"
	stayOne := f.addStay(t, f.iface, f.revision, room, "ADEYEMI")
	stayTwo := f.addStay(t, secondIface, secondRev, room, "WHITFIELD")

	_, one := post(t, f.p3.resolveHandler,
		f.resolveBody(room, "Adeyemi", "", mustUUID(t, f.pool)))
	if one.Outcome != outcomeVerified {
		t.Fatalf("room %s on interface one did not verify from network one: %+v", room, one)
	}
	if stay, iface := f.contextStayAndInterface(t, one.AuthContextID); stay != stayOne || iface != f.iface {
		t.Fatalf("network one's sign-in to room %s pinned stay %s on interface %s, want %s on %s",
			room, stay, iface, stayOne, f.iface)
	}

	_, two := post(t, f.p3.resolveHandler,
		f.resolveFrom(secondNet, room, "Whitfield", mustUUID(t, f.pool)))
	if two.Outcome != outcomeVerified {
		t.Fatalf("room %s on interface two did not verify from network two: %+v", room, two)
	}
	if stay, iface := f.contextStayAndInterface(t, two.AuthContextID); stay != stayTwo || iface != secondIface {
		t.Fatalf("network two's sign-in to room %s pinned stay %s on interface %s, want %s on %s",
			room, stay, iface, stayTwo, secondIface)
	}

	// Negatives: the room exists on both interfaces, so the refusal is that this surname is not on this interface's room.
	if _, crossed := post(t, f.p3.resolveHandler,
		f.resolveFrom(secondNet, room, "Adeyemi", mustUUID(t, f.pool))); crossed.Outcome == outcomeVerified {
		t.Fatalf("interface one's guest verified against interface two's room %s. Room numbers are not unique "+
			"across interfaces and the namespaces must not merge: %+v", room, crossed)
	}
	if _, crossed := post(t, f.p3.resolveHandler,
		f.resolveBody(room, "Whitfield", "", mustUUID(t, f.pool))); crossed.Outcome == outcomeVerified {
		t.Fatalf("interface two's guest verified against interface one's room %s: %+v", room, crossed)
	}
}
