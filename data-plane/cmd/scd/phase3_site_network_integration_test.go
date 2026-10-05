//go:build integration

package main

// THE CLIENT NETWORK A DEVICE IS ACTUALLY ON MUST DECIDE THE SAME THINGS ON THE ROOM SIGN-IN PATH AS IT DOES
// EVERYWHERE ELSE.
//
// Three separate facts are held here, and all three were broken or absent in the same corner of the guest path.
//
//  1. THE OFFER SET DID NOT KNOW WHICH NETWORK THE GUEST WAS ON. offersFor was called with an empty client
//     network id on the PMS room sign-in path, while the voucher, client-account, OTP, social and open paths all
//     read the server-derived id back off the Auth Context. A SITE_NETWORK rule tests the subject's network id
//     against the ids the operator configured; an empty id matches none of them. So EVERY package carrying a
//     "this client network only" condition silently vanished from the offer set of EVERY room sign-in, and a
//     verified guest standing in a correctly configured room was told "verified, but no eligible package".
//
//  2. A REPLACED CLIENT NETWORK LOST ITS LOGICAL IDENTITY. A staged replacement (0105) gives the network a NEW
//     id for reasons — a port, a VLAN tag, a subnet — that have nothing to do with who may buy what, and a
//     package revision is immutable. Republishing the current revision forward cannot help a revision that is
//     PINNED and must stay pinned: an unused printed voucher redeems the revision it was issued against. So an
//     ordinary cabling change made valid unused codes unredeemable. The lineage view (0106) restores the
//     identity, and it must stay scoped to replacements that actually took effect — a reverted apply never
//     happened on the wire and must widen nothing.
//
//  3. TWO CLIENT NETWORKS MAPPED TO TWO PMS INTERFACES MUST NOT LEAK INTO EACH OTHER. Room numbers are a PMS's
//     namespace, not the property's: two interfaces can both have a room 707 belonging to different guests.
//     Everything that decides a sign-in — which interfaces are probed, which stay matches, which packages are
//     offered — has to stay inside the namespace the device's own network maps to.
//
// Every positive assertion below has a negative counterpart on purpose. "The package was offered" is satisfied
// by a permissive evaluator that offers everything, which is exactly the failure a test of this must not pass
// through; "and the package scoped to a DIFFERENT network was not offered" is what gives the first assertion
// its meaning.

import (
	"context"
	"fmt"
	"testing"
)

// ---- fixtures: a second client network, a second PMS interface, extra stays ----
//
// These build the shapes the fixture deliberately does not have — it has exactly one client network mapped to
// exactly one interface, which is the common property. Everything here is seeded the way real code must: stays
// inside a transaction that has opened the 'stay' controlled operation, the interface revision PUBLISHED through
// the pointer scd pins rather than by revision number, and a healthy runtime row, because a PMS that is telling
// the appliance nothing cannot vouch for anybody and the whole resolve path would refuse before it looked at a
// room.

// clientNetwork is one seeded client network plus a device sitting on it.
type clientNetwork struct {
	id              string
	subnet, gateway string
	ip, mac         string
}

// freeSiteNetworkOctet picks a third octet inside 10.78.0.0/16 that this database does not already use.
//
// It has to be a query, not a counter. The appliance resolves a device's client network by "which enabled
// subnet contains this address" (resolveNetwork), so two networks sharing a subnet make that answer arbitrary —
// and the symptom is not a clean failure but a resolution that lands in another run's tenant. 10.78 is used
// rather than the shared fixture generator's 10.77 so that allocating a network here can never collide with a
// subnet claimFreeFixtureOctet has handed to a fixture in this same run.
func (f *authFixture) freeSiteNetworkOctet(t *testing.T) int {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT DISTINCT split_part(host(network(subnet_cidr)), '.', 3)::int
		   FROM public.guest_networks WHERE subnet_cidr <<= '10.78.0.0/16'`)
	if err != nil {
		t.Fatalf("read the used 10.78 subnets: %v", err)
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err == nil {
			used[n] = true
		}
	}
	for n := 1; n < 250; n++ {
		if !used[n] {
			return n
		}
	}
	t.Skip("no free 10.78.x subnet remains in this database")
	return 0
}

// addClientNetwork seeds one client network on this site, with a subnet of its own and a device address inside
// it. `enabled` is false for the retired side of a replacement: an applied replacement disables the original
// row rather than deleting it, and a disabled network resolves no devices.
func (f *authFixture) addClientNetwork(t *testing.T, label string, enabled bool) clientNetwork {
	t.Helper()
	octet := f.freeSiteNetworkOctet(t)
	cn := clientNetwork{
		subnet:  fmt.Sprintf("10.78.%d.0/24", octet),
		gateway: fmt.Sprintf("10.78.%d.1", octet),
		ip:      fmt.Sprintf("10.78.%d.40", octet),
		mac:     fmt.Sprintf("02:00:00:dd:00:%02x", octet),
	}
	// bridge_name is globally unique and parent_interface is unique among enabled untagged networks, so both
	// are derived from a fresh uuid rather than from the label.
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

// addPMSInterface seeds a second ACTIVE PMS interface with a PUBLISHED revision and a healthy runtime row, and
// returns both ids. The revision pointer is what scd pins; a revision that exists without being published is
// correctly refused, which is the behaviour a Draft mid-configuration must get.
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

// mapClientNetworkToPMSInterface routes a client network's sign-ins at one interface. This mapping is the only
// thing that decides which interfaces a device's submission is probed against.
func (f *authFixture) mapClientNetworkToPMSInterface(t *testing.T, networkID, ifaceID string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO iam_v2.guest_network_pms_map(tenant_id,site_id,guest_network_id,pms_interface_id,is_default)
		VALUES ($1,$2,$3::uuid,$4::uuid,true)`, f.tenant, f.site, networkID, ifaceID); err != nil {
		t.Fatalf("map client network to interface: %v", err)
	}
}

// addStay seeds one IN_HOUSE stay on an interface, with the FRESH occupancy evidence produced by that
// interface's own published revision. The evidence is not decoration: a stay without it cannot issue an Auth
// Context at all, and evidence pinned to another interface's revision is refused, so this is also what keeps
// each interface's stays answerable only under their own configuration.
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
	// The occupancy tuple is all-or-none, so every column goes in together, exactly as the shared fixture does.
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

// resolveFrom is resolveBody for a device on a named client network rather than on the fixture's own one. The
// network is never submitted: the appliance derives it from this source address, which is the whole reason the
// id is trustworthy enough to decide eligibility with.
func (f *authFixture) resolveFrom(cn clientNetwork, room, last, reqID string) map[string]any {
	return map[string]any{
		"room": room, "last_name": last, "reservation_number": "", "request_id": reqID,
		"device": map[string]string{"ip": cn.ip, "mac": cn.mac},
	}
}

// offeredRevisions is the set of package revisions a resolve answered with.
func offeredRevisions(res phase3Response) map[string]bool {
	out := map[string]bool{}
	for _, o := range res.Offers {
		out[o.PackageRevisionID] = true
	}
	return out
}

// contextStayAndInterface reads what the Auth Context actually pinned. The response does not name the stay or
// the interface — deliberately, it discloses nothing — so the namespace assertions are made against the record
// the grant will later be checked against.
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

// siteNetworkRule is the SITE_NETWORK eligibility rule as an operator configures it: the client networks a
// package is restricted to, named by id.
func siteNetworkRule(networkIDs ...string) []map[string]any {
	return []map[string]any{{
		"type":  "SITE_NETWORK",
		"value": map[string]any{"guest_network_ids": networkIDs},
	}}
}

// ---- (a) the room sign-in path knows which client network the guest is on ----

// A PACKAGE RESTRICTED TO THE GUEST'S OWN CLIENT NETWORK IS OFFERED ON THE ROOM SIGN-IN PATH.
//
// This is the regression for the defect in the file header: offersFor was called with an empty client network
// id, so a SITE_NETWORK rule could never match on this path and every package carrying one disappeared from
// every room sign-in's offer set. The guest was verified and then told there was nothing they qualified for.
//
// The second half of the test is what makes the first half mean anything. "The package naming this network was
// offered" would also be true of an evaluator that ignored SITE_NETWORK altogether, or of one that treated a
// rule it could not answer as no constraint at all. So a second package, identical except that its rule names a
// DIFFERENT client network, must be absent from the same answer. One assertion proves the id arrives; the other
// proves it is still being used to refuse.
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
	// The unrestricted included package is the control: it proves this answer is a real, fully-evaluated offer
	// set rather than a coincidence of some rule type being skipped wholesale.
	if !offered[f.pkgRev] {
		t.Fatalf("the unrestricted included package was not offered: %+v", res.Offers)
	}
}

// ---- (b) a replaced client network keeps its logical identity ----

// A RULE NAMING A RETIRED CLIENT NETWORK IS SATISFIED BY A DEVICE ON ITS SUCCESSOR — AND ONLY WHEN THE
// REPLACEMENT ACTUALLY TOOK EFFECT.
//
// The hotel moved a port onto a tagged trunk. 0105 stages that as a replacement and the apply swaps the client
// network ROW, so the successor carries a new id, while every existing package revision still names the old one
// and cannot be rewritten: an unused printed voucher redeems the revision it was issued against, and a guest
// holding an entitlement keeps the revision they were granted on. Before 0106 the cabling change therefore
// narrowed the offer set of every pinned revision, and valid unused codes became unredeemable.
//
// The negative counterpart is the whole scope of the fix. Flipping the same replacement row to REVERTED means
// the apply never stood: the successor row is gone on a real appliance and the original is enabled again, so
// nothing was continued and the rule must stop matching. If lineage were read from any replacement row
// regardless of state, a cancelled or rolled-back request would permanently widen eligibility to a network that
// never replaced anything — which is the opposite error and worse, because it grants rather than withholds.
//
// The unrestricted included package is asserted in BOTH halves on purpose. Without it, the second half would
// pass just as well if the sign-in itself had stopped working for an unrelated reason.
func TestIntegration_Phase3SiteNetwork_AReplacedClientNetworkKeepsItsLogicalIdentity(t *testing.T) {
	f := newAuthFixture(t)
	ctx := context.Background()

	// The retired original is disabled, as an applied replacement leaves it: it resolves no devices any more.
	retired := f.addClientNetwork(t, "retired-untagged-port", false)
	// ...and the successor is live, carrying the guests, routed at the same PMS interface the replacement
	// carried forward.
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

	// A pinned revision's rule, written against the network the operator chose — before the cable moved.
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

	// THE NEGATIVE: the apply did not stand. Nothing was continued, so nothing is inherited.
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

// ---- (c) two client networks, two PMS interfaces ----

// A DEVICE'S CLIENT NETWORK DECIDES WHICH PMS NAMESPACE ANSWERS FOR IT, AND WHICH PACKAGES IT IS SHOWN.
//
// A property can run two PMS interfaces — two buildings, a hotel and a club — each with its own client network.
// The mapping from client network to interface is the only thing that says which mirrored roster a submission is
// compared against, and the resolve path probes exactly the interfaces that mapping names.
//
// Both directions are asserted, in both halves. A stay that exists only on interface 2 must not verify from
// network 1, and vice versa: without that, "resolved against interface 1" would be satisfied by a path that
// probed both and happened to find the right answer first. And each network is shown only the package scoped to
// it, which is the same property as (a) measured where getting it wrong would show one property's guests the
// other's offer.
func TestIntegration_Phase3SiteNetwork_TwoClientNetworksResolveAgainstTheirOwnPMSInterface(t *testing.T) {
	f := newAuthFixture(t)

	secondNet := f.addClientNetwork(t, "guests-building-two", true)
	secondIface, secondRev := f.addPMSInterface(t, "building two")
	f.mapClientNetworkToPMSInterface(t, secondNet.id, secondIface)
	secondStay := f.addStay(t, secondIface, secondRev, "501", "WHITFIELD")

	onlyNetworkOne := f.addPackage(t, "NETWORK_ONE_ONLY", siteNetworkRule(f.network))
	onlyNetworkTwo := f.addPackage(t, "NETWORK_TWO_ONLY", siteNetworkRule(secondNet.id))

	// Network one: interface one's stay, interface one's package.
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

	// Network two: interface two's stay, interface two's package.
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

	// THE NEGATIVES. Neither network can reach the other's roster at all: a submission that is perfectly valid
	// on the other interface is an ordinary non-success here, because the interface it belongs to was never
	// probed.
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

// ---- (d) the same room number on both interfaces ----

// A ROOM NUMBER BELONGS TO A PMS, NOT TO THE PROPERTY.
//
// Two interfaces can both hold a room 707, occupied by unrelated guests. Nothing in a room number, or in a
// surname, is unique across interfaces, so every step of the sign-in has to stay inside the namespace the
// device's own client network maps to. A lookup that searched the site rather than the interface would be
// ambiguous at best — and at worst would hand one property's guest the other's access, or refuse a legitimate
// guest as "ambiguous" because somebody in another building has their room number.
//
// The negatives are what make this a test of isolation rather than of two happy paths: the surname that opens
// room 707 on interface one must NOT open room 707 on interface two, and the reverse. Both guests verifying is
// consistent with a broken, site-wide search; only the refusals pin the namespace down.
func TestIntegration_Phase3SiteNetwork_TheSameRoomNumberOnTwoInterfacesDoesNotCollide(t *testing.T) {
	f := newAuthFixture(t)

	secondNet := f.addClientNetwork(t, "guests-building-two", true)
	secondIface, secondRev := f.addPMSInterface(t, "building two")
	f.mapClientNetworkToPMSInterface(t, secondNet.id, secondIface)

	// The SAME room number on each interface, with DIFFERENT guests. This is the configuration the isolation
	// has to survive; two different room numbers would prove nothing at all.
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

	// THE NEGATIVES. The room exists on both interfaces, so the refusal below is specifically that this
	// surname is not on THIS interface's room 707 — not that the room is unknown.
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
