package main

// THE APPLIER MUST NOT AUTHORIZE AN ADDRESS ON THE PRODUCER'S WORD ALONE.
//
// The producer retires an attachment when the device turns up somewhere newer. It cannot see the other case:
// the device that left and never came back. Durable state contains no evidence of an absence — but DHCP does,
// because DHCP is what assigned the address, and netd owns it.
//
// This is the case that actually happened. An iPhone authenticated on 192.168.77.139 and was switched off; the
// lease lapsed; Kea reissued .139 to a different device; and the appliance went on authorizing .139 and
// metering its traffic onto the absent guest's entitlement.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/shapeplan"
)

// fakeLeases is the DHCP evidence, staged. A nil error with no rows is a server that answered "nobody holds
// anything", which is a different fact from a server that could not be asked.
type fakeLeases struct {
	rows []KeaLease
	err  error
}

func (f fakeLeases) Leases() ([]KeaLease, error) { return f.rows, f.err }

// switchableLeases is the same evidence source with a failure that can be turned on and off between passes,
// so an outage and its recovery are one continuous scenario rather than two unrelated tests.
type switchableLeases struct {
	rows []KeaLease
	err  error
}

func (f *switchableLeases) Leases() ([]KeaLease, error) { return f.rows, f.err }

// lease is a lease somebody holds right NOW — stamped at this instant, because a lease's identity is only half
// of the evidence and the other half is that it has not lapsed.
func lease(ip, mac string) KeaLease {
	return KeaLease{IPAddress: ip, HWAddr: mac, ValidLft: 600, State: 0, CLTT: time.Now().Unix()}
}

func TestAddressOwner_LeasedToThisDeviceIsOwned(t *testing.T) {
	o := addressOwner{src: fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressOwned {
		t.Fatalf("ownership = %v, want owned", got)
	}
	// MAC formatting must not decide a security answer.
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:F9:7A:9B:09"); got != addressOwned {
		t.Fatalf("case-different MAC = %v, want owned", got)
	}
}

// THE EXPOSURE, IN ONE ASSERTION. The address is live, and it belongs to somebody else.
func TestAddressOwner_ReassignedToAnotherDeviceIsForeign(t *testing.T) {
	o := addressOwner{src: fakeLeases{rows: []KeaLease{lease("192.168.77.139", "d6:5a:1c:d2:12:d6")}}}
	if got := o.Owns(context.Background(), net.ParseIP("192.168.77.139"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("ownership = %v, want foreign — this is the case that put a stranger on a guest's account", got)
	}
}

// No live lease at all. Guests here are addressed by DHCP, so an address nobody holds is an address this
// session does not hold either — and an authorization for it protects nobody while waiting for whoever picks
// it up next.
func TestAddressOwner_NoLeaseIsForeign(t *testing.T) {
	o := addressOwner{src: fakeLeases{rows: []KeaLease{lease("10.0.0.9", "aa:bb:cc:dd:ee:ff")}}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("ownership = %v, want foreign", got)
	}
}

// An expired or released lease is history. Its former holder is not the current owner.
func TestAddressOwner_AnExpiredLeaseIsNotOwnership(t *testing.T) {
	expired := lease("10.0.0.5", "96:48:f9:7a:9b:09")
	expired.State = keaLeaseStateExpired
	o := addressOwner{src: fakeLeases{rows: []KeaLease{expired}}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("an expired lease read as %v, want foreign", got)
	}
}

// EVIDENCE OUTAGE IS NOT EVIDENCE. Kea being unreachable must never disconnect every guest on the property.
func TestAddressOwner_UnreadableEvidenceIsUnknown(t *testing.T) {
	o := addressOwner{src: fakeLeases{err: errors.New("control socket unavailable")}}
	if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != addressUnknown {
		t.Fatalf("ownership = %v, want unknown", got)
	}
	// ...and so is a plan that carries no MAC to check against, which is every plan built before this contract.
	o2 := addressOwner{src: fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}}
	if got := o2.Owns(context.Background(), net.ParseIP("10.0.0.5"), ""); got != addressUnknown {
		t.Fatalf("a session with no MAC read as %v, want unknown", got)
	}
}

// ---- the applier's behaviour on each answer ---------------------------------------------------------------

func ownershipWriter(t *testing.T, tc *fakeTC, g *fakeGate, src leaseSource) *phase3Shaping {
	t.Helper()
	p := liveWriter(tc)
	p.gate = g
	p.owner = addressOwner{src: src}
	return p
}

func sessionOn(ip, mac string) shapeplan.Session {
	return shapeplan.Session{SessionID: "s-1", DeviceID: "dev-1", IP: ip, MAC: mac, Bridge: "br-guest",
		EntitlementID: "ent-1", DownKbps: 9000, UpKbps: 4000, Entitled: true}
}

// A FOREIGN ADDRESS IS WITHDRAWN, NOT WAITED OUT. The bounded nft lease is the backstop for netd dying, not
// the plan: while netd is running the authorization goes immediately.
func TestApplier_ForeignAddressIsRevokedAndTornDown(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "d6:5a:1c:d2:12:d6")}})

	res, err := p.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 {
		t.Fatalf("a session on somebody else's address was shaped: %+v", res)
	}
	if g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("the address is still authorized after DHCP said it belongs to another device")
	}
	if fwd, _ := tc.SessionForwarding(context.Background(), "br-guest", net.ParseIP("10.0.0.5")); fwd {
		t.Fatal("the accountable class is still forwarding for an address this session does not own")
	}
}

// UNKNOWN WITHHOLDS THE RENEWAL AND DESTROYS NOTHING.
//
// Durable state alone must never renew an address indefinitely, so a pass that cannot verify ownership does
// not renew. It also does not tear anything down: the authorization already installed is a bounded lease, and
// letting it run out on its own is the transient tolerance a brief Kea outage needs. The account is untouched.
func TestApplier_UnknownOwnershipWithholdsRenewalWithoutEndingAnything(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	src := &switchableLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	p := ownershipWriter(t, tc, g, src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")

	// A healthy pass first, so there is a live lease to let run down.
	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}
	before := g.leaseOf("br-guest", "10.0.0.5")
	if before <= 0 {
		t.Fatal("setup: no lease was installed")
	}

	// Kea goes away. The pass must not renew, must not revoke, and must say so.
	src.err = errors.New("kea down")
	g.advance(30 * time.Second)
	res, err := p.submit(context.Background(), envelope(2, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 {
		t.Fatalf("a session was counted as shaped while its ownership was unverifiable: %+v", res)
	}
	if !g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("the guest was cut off the moment DHCP could not be asked — the bounded lease is the tolerance")
	}
	if after := g.leaseOf("br-guest", "10.0.0.5"); after >= before {
		t.Fatalf("the lease was renewed anyway (%s -> %s): durable state alone must not renew an address",
			before, after)
	}
	if p.unverifiedCount() != 1 {
		t.Fatalf("unverified sessions = %d, want 1 — the state must be visible, not merely absent",
			p.unverifiedCount())
	}
	var reported bool
	for _, msg := range res.Problems {
		if contains(msg, "NOT renewed") {
			reported = true
		}
	}
	if !reported {
		t.Fatalf("the withheld renewal was not reported: %+v", res.Problems)
	}
}

// ...AND EVIDENCE RETURNING BEFORE EXPIRY RESUMES NORMAL RENEWAL. The guest never notices the gap.
func TestApplier_OwnershipConfirmedBeforeExpiryResumesRenewal(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	src := &switchableLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}}
	p := ownershipWriter(t, tc, g, src)
	s := sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")

	if res, err := p.submit(context.Background(), envelope(1, s), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}
	src.err = errors.New("kea down")
	g.advance(30 * time.Second)
	if _, err := p.submit(context.Background(), envelope(2, s), time.Now()); err != nil {
		t.Fatal(err)
	}
	if p.unverifiedCount() != 1 {
		t.Fatalf("setup: the session was not marked unverified")
	}

	// Kea comes back, still naming this device, and the lease is refreshed to full length.
	src.err = nil
	g.advance(30 * time.Second)
	res, err := p.submit(context.Background(), envelope(3, s), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 1 {
		t.Fatalf("renewal did not resume once ownership was confirmed again: %+v", res)
	}
	if p.unverifiedCount() != 0 {
		t.Fatalf("the session is still marked unverified after confirmation")
	}
	if got := g.leaseOf("br-guest", "10.0.0.5"); got < 60*time.Second {
		t.Fatalf("lease = %s after a confirming pass, want a full-length renewal", got)
	}
}

// The ordinary case must be untouched: verified ownership shapes and authorizes exactly as before.
func TestApplier_OwnedAddressIsEnforcedNormally(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	p := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}})

	res, err := p.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 1 || res.Failed != 0 {
		t.Fatalf("a verified guest was not enforced: %+v", res)
	}
	if !g.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatal("a verified guest holds no authorization")
	}
}

// RESTART AND RECOVERY. A fresh applier — empty state directory, no memory — re-verifies ownership on its
// first pass rather than inheriting a decision it cannot see. A stale address must not survive a restart.
func TestApplier_ForeignAddressIsStillRefusedAfterRestart(t *testing.T) {
	tc, g := newFakeTC(), newFakeGate()
	first := ownershipWriter(t, tc, g, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "96:48:f9:7a:9b:09")}})
	if res, err := first.submit(context.Background(), envelope(1, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now()); err != nil || res.Shaped != 1 {
		t.Fatalf("setup: %+v %v", res, err)
	}

	// The device leaves; Kea reissues the address; netd restarts with no memory of any of it.
	tc2, g2 := newFakeTC(), newFakeGate()
	second := ownershipWriter(t, tc2, g2, fakeLeases{rows: []KeaLease{lease("10.0.0.5", "d6:5a:1c:d2:12:d6")}})
	res, err := second.submit(context.Background(), envelope(2, sessionOn("10.0.0.5", "96:48:f9:7a:9b:09")), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Shaped != 0 || g2.isAuthorized("br-guest", "10.0.0.5") {
		t.Fatalf("a restarted applier authorized a reassigned address: %+v", res)
	}
}

// A LAPSED LEASE THAT KEA STILL CARRIES IS NOT OWNERSHIP.
//
// Found on the PRE-LIVE appliance: memfile was holding ninety-two lease rows whose six hundred second lifetime
// had run out days earlier, every one of them still labelled state 0 because nothing had reclaimed the address
// yet. A check that reads only the state field calls all ninety-two of them owned, and the guest who walked out
// on Tuesday still owns their address on Sunday.
func TestOwnership_ALeaseThatHasLapsedIsNotCurrent(t *testing.T) {
	stale := KeaLease{IPAddress: "192.168.77.102", HWAddr: "96:48:f9:7a:9b:09", ValidLft: 600, State: 0,
		CLTT: time.Now().Add(-72 * time.Hour).Unix()}
	o := addressOwner{src: fakeLeases{rows: []KeaLease{stale}}}

	if got := o.Owns(context.Background(), net.ParseIP("192.168.77.102"), "96:48:f9:7a:9b:09"); got != addressForeign {
		t.Fatalf("a lease three days past its lifetime was read as %v, want foreign", got)
	}
	// The same row, renewed a moment ago, IS ownership — the rule is about currency, not about age of acquaintance.
	fresh := stale
	fresh.CLTT = time.Now().Unix()
	o2 := addressOwner{src: fakeLeases{rows: []KeaLease{fresh}}}
	if got := o2.Owns(context.Background(), net.ParseIP("192.168.77.102"), "96:48:f9:7a:9b:09"); got != addressOwned {
		t.Fatalf("a lease renewed a moment ago was read as %v, want owned", got)
	}
}

// The boundary is the lease's own expiry, evaluated against a clock the test controls rather than the wall.
func TestOwnership_CurrencyIsDecidedAtTheLeaseExpiry(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	l := KeaLease{IPAddress: "10.0.0.5", HWAddr: "96:48:f9:7a:9b:09", ValidLft: 600, State: 0, CLTT: base.Unix()}

	for _, tc := range []struct {
		at   time.Time
		want addressOwnership
		note string
	}{
		{at: base.Add(599 * time.Second), want: addressOwned, note: "one second before expiry"},
		{at: base.Add(601 * time.Second), want: addressForeign, note: "one second after expiry"},
	} {
		at := tc.at
		o := addressOwner{src: fakeLeases{rows: []KeaLease{l}}, now: func() time.Time { return at }}
		if got := o.Owns(context.Background(), net.ParseIP("10.0.0.5"), "96:48:f9:7a:9b:09"); got != tc.want {
			t.Fatalf("%s: got %v, want %v", tc.note, got, tc.want)
		}
	}
}
