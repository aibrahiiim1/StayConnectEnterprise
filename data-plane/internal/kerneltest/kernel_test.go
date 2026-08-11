//go:build kernelgate

// Package kerneltest is the REAL-KERNEL contract suite for Phase-3 enforcement.
//
// Everything else that tests this subsystem is a model. The netd suites model nft and tc as Go interfaces and
// prove ordering; the nft and shape suites assert the command strings; the foundation suite applies commands
// to a modelled ruleset. All of that is worth having, and none of it can answer the questions that only the
// kernel decides:
//
//	does nftables actually accept a `ifname . ipv4_addr` element with a timeout?
//	does an element with a lease actually DISAPPEAR when the lease elapses, with nothing running?
//	does a repeated `add element` refresh that lease, or silently not?
//	does a guest with no authorization element actually fail to reach the internet?
//	and — the claim that mattered most and was wrong for the longest — does removing a tc classification
//	filter deny that guest anything at all?
//
// So this suite runs the REAL `nft` and `tc` binaries against a REAL kernel, and it sends REAL packets between
// three disposable network namespaces to see whether they arrive.
//
// It is not live evidence about an appliance. It contacts no appliance, no production database and no PMS; it
// mutates nothing outside the namespaces the harness created and destroys them afterwards. It is evidence
// about the KERNEL CONTRACT this design rests on, produced on a disposable machine.
//
// It is built behind the `kernelgate` tag and driven by scripts/ci/kernel-netns-suite.sh, which builds the
// topology, points the clients at namespace-scoped wrappers, and proves the host's own ruleset is untouched.
package kerneltest

import (
	"context"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/nft"
	"github.com/stayconnect/enterprise/data-plane/internal/nftfoundation"
	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// The harness passes the disposable topology in the environment. Nothing here has a default that could point
// at a real system: an unset variable fails the suite rather than silently running somewhere else.
var (
	nftWrapper = mustEnv("KG_NFT") // runs nft INSIDE the router namespace
	tcWrapper  = mustEnv("KG_TC")  // runs tc INSIDE the router namespace
	ipWrapper  = mustEnv("KG_IP")  // runs ip INSIDE the router namespace
	guestNS    = mustEnv("KG_GUEST_NS")
	guestIP    = mustEnv("KG_GUEST_IP")
	wanIP      = mustEnv("KG_WAN_IP")
	guestIface = mustEnv("KG_GUEST_IF") // the router-side interface guests arrive on
	ifbOK      = os.Getenv("KG_IFB") == "1"
)

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		panic("kerneltest: " + k + " is not set; this suite must only ever run against the disposable namespace harness")
	}
	return v
}

func ctx(t *testing.T) context.Context { return t.Context() }

func nftClient() *nft.Client {
	c := nft.New()
	c.NftPath = nftWrapper
	return c
}

func shapeClient() *shape.Client {
	c := shape.New()
	c.TCPath = tcWrapper
	c.IPPath = ipWrapper
	return c
}

// run executes a command in the ROUTER namespace via the same wrappers the clients use.
func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v — %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}

// reaches sends real packets from the guest namespace to the WAN address and reports whether they arrive.
//
// This is the only honest test of "is this guest on the internet". Every other formulation — an element is
// present, a filter is installed, a row says active — is a proxy for it, and each of those proxies has at some
// point been wrong in exactly the way that let traffic through.
func reaches(t *testing.T) bool {
	t.Helper()
	cmd := exec.Command("ip", "netns", "exec", guestNS, "ping", "-c", "2", "-W", "1", "-i", "0.2", wanIP)
	return cmd.Run() == nil
}

func mustIP(t *testing.T, s string) net.IP {
	t.Helper()
	ip := net.ParseIP(s)
	if ip == nil {
		t.Fatalf("unusable address %q", s)
	}
	return ip
}

// ---- 1. the set itself -------------------------------------------------------

// The Phase-3 set exists, is empty, and is a CONCATENATED (ifname, IPv4) set with timeouts enabled. Every
// other property in this file depends on all three, and a set declared without `flags timeout` would accept
// the element and silently ignore the lease.
func TestKernel_Phase3SetExistsEmptyAndSupportsLeases(t *testing.T) {
	out := run(t, nftWrapper, "list", "set", "inet", "stayconnect", nft.Phase3AuthV4)
	if !strings.Contains(out, "ifname . ipv4_addr") {
		t.Fatalf("the Phase-3 set is not the (ifname, ipv4) concatenation the design requires:\n%s", out)
	}
	if !strings.Contains(out, "flags timeout") {
		t.Fatalf("the Phase-3 set does not support element timeouts, so no lease can ever expire:\n%s", out)
	}
	els, err := nftClient().ListIn(ctx(t), nft.Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 0 {
		t.Fatalf("the Phase-3 set is not empty at the start of the suite: %v", els)
	}
}

// A real concatenated element goes in, is read back with its bridge and address intact, and comes out again.
func TestKernel_AuthorizeReadBackAndRevoke(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 60*time.Second); err != nil {
		t.Fatalf("lease: %v", err)
	}
	ok, err := c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)
	if err != nil || !ok {
		t.Fatalf("the element was not readable after being installed (ok=%v err=%v)", ok, err)
	}
	els, err := c.ListIn(ctx(t), nft.Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 1 || els[0].Iface != guestIface || els[0].IP.String() != guestIP {
		t.Fatalf("the element did not round-trip through the kernel: %+v", els)
	}
	if els[0].Expires <= 0 || els[0].Expires > 60*time.Second {
		t.Fatalf("the kernel reports a remaining lease of %v for a 60s lease", els[0].Expires)
	}

	if err := c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	ok, _ = c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)
	if ok {
		t.Fatal("the element survived its revocation")
	}
}

// SET ISOLATION, against the real kernel. A legacy authorization is invisible to every Phase-3 operation, and
// a Phase-3 revocation cannot remove it. This is the property that makes Phase-3 full-state reconciliation
// safe to run beside a live legacy pipeline.
func TestKernel_LegacyAndPhase3SetsAreIsolated(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	if err := c.Allow(ctx(t), guestIface, ip, 0); err != nil { // legacy: permanent, as scd installs it
		t.Fatalf("legacy allow: %v", err)
	}
	t.Cleanup(func() { _ = c.Deny(context.Background(), guestIface, ip) })

	els, err := c.ListIn(ctx(t), nft.Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 0 {
		t.Fatalf("a legacy authorization is visible to Phase-3 enumeration: %+v", els)
	}
	// A Phase-3 revocation names only the Phase-3 set, so it cannot reach the legacy element.
	if err := c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); err != nil {
		t.Fatalf("phase-3 revoke of an absent element should be a no-op: %v", err)
	}
	legacy, err := c.List(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 1 {
		t.Fatalf("a Phase-3 revocation removed a LEGACY authorization: %d left", len(legacy))
	}
}

// ---- 2. the lease, in the kernel ---------------------------------------------

// THE PROPERTY THE WHOLE LEASE DESIGN RESTS ON: with nothing running, the element goes away by itself.
func TestKernel_LeaseExpiresWithNothingRunning(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); !ok {
		t.Fatal("the lease did not take")
	}
	time.Sleep(3500 * time.Millisecond) // nothing renews it; nothing else runs at all
	if ok, _ := c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); ok {
		t.Fatal("the element outlived its lease: the kernel is not expiring it, so a dead daemon would leave the guest online")
	}
}

// A REPEATED `add element` DOES NOT REFRESH THE LEASE. This is the exact assumption that would have made the
// renewal mechanism a no-op — healthy-looking, and dropping every guest at the first lease boundary.
func TestKernel_RepeatedAddDoesNotRefreshTheLease(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	// The naive "renewal": add the same element again with a full lease.
	_ = c.AllowIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 20*time.Second)
	els, err := c.ListIn(ctx(t), nft.Phase3AuthV4)
	if err != nil || len(els) != 1 {
		t.Fatalf("list after a repeated add: %v %+v", err, els)
	}
	if els[0].Expires > 18*time.Second {
		t.Fatalf("a repeated add DID refresh the lease (%v left). If that is now true of this nftables "+
			"version, the renewal design should be revisited — but the code must not depend on it", els[0].Expires)
	}
}

// THE REAL REFRESH DOES restart the lease, and does it without ever removing the guest's authorization: the
// delete and the add are one transaction.
func TestKernel_LeaseInRefreshesAnExistingElement(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 20*time.Second); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	els, err := c.ListIn(ctx(t), nft.Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 1 {
		t.Fatalf("the refresh changed the element COUNT (%d); a duplicate element is a leak that grows with uptime", len(els))
	}
	if els[0].Expires <= 18*time.Second {
		t.Fatalf("the refresh did not restart the lease: %v left of 20s", els[0].Expires)
	}
}

// ---- 3. what actually decides internet access --------------------------------

// A guest with no authorization element does not reach the internet. Real packets, real forward chain.
func TestKernel_NoElementMeansNoInternet(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	_ = c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)
	_ = c.Deny(ctx(t), guestIface, ip)

	if reaches(t) {
		t.Fatal("an unauthorized guest reached the WAN: the forward chain is not gating on the authorization sets")
	}
}

// And an authorized one does — then stops, by itself, when the lease elapses.
func TestKernel_Phase3ElementGrantsInternetAndTheLeaseTakesItBack(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 4*time.Second); err != nil {
		t.Fatal(err)
	}
	if !reaches(t) {
		t.Fatal("an authorized guest could not reach the WAN")
	}
	time.Sleep(5500 * time.Millisecond)
	if reaches(t) {
		t.Fatal("the guest still reaches the WAN after their lease elapsed with nothing running")
	}
}

// THE CLAIM THAT WAS WRONG. Removing the tc classification does NOT deny a guest anything: their packets fall
// back to the bridge's default class and keep flowing. Any document that says otherwise is wrong, and this is
// the test that says so in packets.
func TestKernel_RemovingTCClassificationDoesNotDenyAccess(t *testing.T) {
	if !ifbOK {
		t.Skip("KG_IFB=0: the ifb module is unavailable on this runner, so the staged tc surface cannot be driven; " +
			"see LIMITATIONS in the evidence artifact")
	}
	c := nftClient()
	sh := shapeClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() {
		_ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip)
		_ = sh.AbortSession(context.Background(), guestIface, ip)
	})

	if err := sh.EnsureBridgeInfra(ctx(t), guestIface); err != nil {
		t.Fatalf("bridge infra: %v", err)
	}
	if err := sh.PrepareSession(ctx(t), guestIface, ip, 8000, 3000); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := sh.ActivateSession(ctx(t), guestIface, ip); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	if !reaches(t) {
		t.Fatal("an authorized, classified guest could not reach the WAN")
	}

	// Strip the classification only. Nothing about authorization changes.
	if err := sh.RemoveClassification(ctx(t), guestIface, ip); err != nil {
		t.Fatalf("remove classification: %v", err)
	}
	if !reaches(t) {
		t.Fatal("removing the tc classification denied access — if that is genuinely true of this kernel, the " +
			"whole nft-first ordering argument needs revisiting")
	}
	t.Log("CONFIRMED: with the tc classification removed the guest is STILL ONLINE, now through the bridge's " +
		"default class and metered by nothing")

	// The nft element is what denies. Same guest, same (absent) classification.
	if err := c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); err != nil {
		t.Fatal(err)
	}
	if reaches(t) {
		t.Fatal("revoking packet authorization did not deny the guest")
	}
}

// ---- 4. the staged tc surface, against real tc -------------------------------

// A PREPARED class exists and carries nothing: no classification filter, so the guest's packets are not in it.
// This is the accountable-before-forwarding window, in the kernel.
func TestKernel_PrepareInstallsAClassThatClassifiesNothing(t *testing.T) {
	if !ifbOK {
		t.Skip("KG_IFB=0: ifb unavailable; see LIMITATIONS in the evidence artifact")
	}
	sh := shapeClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = sh.AbortSession(context.Background(), guestIface, ip) })

	if err := sh.EnsureBridgeInfra(ctx(t), guestIface); err != nil {
		t.Fatalf("bridge infra: %v", err)
	}
	if err := sh.PrepareSession(ctx(t), guestIface, ip, 8000, 3000); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	minor, _ := shape.MinorForIP(ip)
	classes, err := sh.ReadClasses(ctx(t), guestIface)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := classes[minor]; !ok {
		t.Fatalf("prepare did not create the class: %v", classes)
	}
	fwd, err := sh.SessionForwarding(ctx(t), guestIface, ip)
	if err != nil {
		t.Fatal(err)
	}
	if fwd {
		t.Fatal("a PREPARED class is already classifying: the accountable-before-forwarding window does not exist")
	}
	if err := sh.ActivateSession(ctx(t), guestIface, ip); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if fwd, err = sh.SessionForwarding(ctx(t), guestIface, ip); err != nil || !fwd {
		t.Fatalf("activation did not install the classification (fwd=%v err=%v)", fwd, err)
	}
}

// A RE-RATE changes the class in place. The counters must survive it: a delete+add would restart the series
// and the accounting checkpoint would be describing a class that no longer exists.
func TestKernel_ReRatePreservesTheCounterSeries(t *testing.T) {
	if !ifbOK {
		t.Skip("KG_IFB=0: ifb unavailable; see LIMITATIONS in the evidence artifact")
	}
	c := nftClient()
	sh := shapeClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() {
		_ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip)
		_ = sh.AbortSession(context.Background(), guestIface, ip)
	})

	if err := sh.EnsureBridgeInfra(ctx(t), guestIface); err != nil {
		t.Fatal(err)
	}
	if err := sh.PrepareSession(ctx(t), guestIface, ip, 8000, 3000); err != nil {
		t.Fatal(err)
	}
	if err := sh.ActivateSession(ctx(t), guestIface, ip); err != nil {
		t.Fatal(err)
	}
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 60*time.Second); err != nil {
		t.Fatal(err)
	}
	minor, _ := shape.MinorForIP(ip)

	// Push real traffic so the class has counters worth preserving.
	for i := 0; i < 3; i++ {
		reaches(t)
	}
	before, err := sh.ReadClasses(ctx(t), guestIface)
	if err != nil {
		t.Fatal(err)
	}
	if before[minor].Bytes == 0 {
		t.Fatalf("the class counted nothing, so this test cannot prove the counters were preserved: %+v", before[minor])
	}

	if err := sh.ReRateSession(ctx(t), guestIface, ip, 4000, 1500); err != nil {
		t.Fatalf("re-rate: %v", err)
	}
	after, err := sh.ReadClasses(ctx(t), guestIface)
	if err != nil {
		t.Fatal(err)
	}
	if after[minor].Bytes < before[minor].Bytes {
		t.Fatalf("the re-rate RESET the counters (%d -> %d): the class was replaced, not changed, and the "+
			"accounting checkpoint now describes a series that no longer exists",
			before[minor].Bytes, after[minor].Bytes)
	}
	if !reaches(t) {
		t.Fatal("the guest lost access across a re-rate")
	}
}

// ---- 5. the surgical live-dark foundation, on a POPULATED legacy set ----------

// The whole point of the surgical install: real legacy guests are authorized, and after the install they are
// still exactly as authorized as they were.
func TestKernel_FoundationInstallPreservesAPopulatedLegacySet(t *testing.T) {
	c := nftClient()
	f := &nftfoundation.Foundation{NftPath: nftWrapper, Run: nftfoundation.ExecRunner}
	ip := mustIP(t, guestIP)

	// Two live legacy guests, one of them the one we can send packets as.
	if err := c.Allow(ctx(t), guestIface, ip, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.Allow(ctx(t), guestIface, mustIP(t, "10.77.0.9"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Deny(context.Background(), guestIface, ip)
		_ = c.Deny(context.Background(), guestIface, mustIP(t, "10.77.0.9"))
	})
	if !reaches(t) {
		t.Fatal("the legacy guest is not online before the install, so this proves nothing about preserving them")
	}

	// The suite's namespace already carries the Phase-3 set (the harness renders the current ruleset), so
	// remove the foundation first: what has to be proven is an install onto a ruleset that lacks it.
	if _, err := f.Rollback(ctx(t)); err != nil {
		t.Fatalf("preparing a pre-foundation ruleset: %v", err)
	}
	if !reaches(t) {
		t.Fatal("the legacy guest lost access when the Phase-3 foundation was removed")
	}

	rep, err := f.Install(ctx(t))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rep.Outcome != "INSTALLED" {
		t.Fatalf("outcome = %s", rep.Outcome)
	}
	if len(rep.LegacyBefore) != 2 || len(rep.LegacyAfter) != 2 {
		t.Fatalf("legacy parity was not proven by the install report: %+v", rep)
	}
	for i := range rep.LegacyBefore {
		if rep.LegacyBefore[i] != rep.LegacyAfter[i] {
			t.Fatalf("legacy element %d changed across the install: %s -> %s", i, rep.LegacyBefore[i], rep.LegacyAfter[i])
		}
	}
	// AND THE GUEST NEVER NOTICED.
	if !reaches(t) {
		t.Fatal("a legacy guest lost internet access across the Phase-3 foundation install")
	}

	// Idempotent.
	rep2, err := f.Install(ctx(t))
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if rep2.Outcome != "ALREADY_INSTALLED" || len(rep2.Commands) != 0 {
		t.Fatalf("a second install was not a no-op: %+v", rep2)
	}
	if !reaches(t) {
		t.Fatal("a legacy guest lost access across the second install")
	}

	// And the set it created authorizes nobody.
	els, err := c.ListIn(ctx(t), nft.Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 0 {
		t.Fatalf("the installed Phase-3 set is not empty: %+v", els)
	}
}

// Rollback removes only the foundation, and the legacy guests stay online through it.
func TestKernel_FoundationRollbackPreservesLegacyAccess(t *testing.T) {
	c := nftClient()
	f := &nftfoundation.Foundation{NftPath: nftWrapper, Run: nftfoundation.ExecRunner}
	ip := mustIP(t, guestIP)

	if _, err := f.Install(ctx(t)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := c.Allow(ctx(t), guestIface, ip, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Deny(context.Background(), guestIface, ip)
		_, _ = f.Install(context.Background())
	})
	if !reaches(t) {
		t.Fatal("the legacy guest is not online before the rollback")
	}

	rep, err := f.Rollback(ctx(t))
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rep.Outcome != "REMOVED" {
		t.Fatalf("outcome = %s", rep.Outcome)
	}
	if len(rep.LegacyAfter) != len(rep.LegacyBefore) {
		t.Fatalf("the rollback changed legacy authorization: %+v", rep)
	}
	if !reaches(t) {
		t.Fatal("a legacy guest lost internet access across the Phase-3 foundation rollback")
	}
	st, err := f.Inspect(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase3Set || st.ForwardRule {
		t.Fatalf("the foundation survived the rollback: %+v", st)
	}
}

// A REBOOT, and what it does and does not preserve.
//
// nftables state is not persistent: a reboot brings the appliance up with whatever ruleset the unit renders,
// and the authorization sets come back EMPTY. That is true of the legacy set as well, and it is not a defect —
// scd re-establishes legacy authorizations from `public.sessions` on its own reconciliation, exactly as it
// does today, and netd re-establishes Phase-3 leases from `iam_v2.sessions` when the flags are on.
//
// So the property a reboot has to have is not "the elements survive". It is:
//
//	the FOUNDATION comes back, so the cutover is still flag-only, and re-running the install afterwards is a
//	no-op rather than a second set of rules;
//	and the Phase-3 set comes back EMPTY, so a dark appliance authorizes nobody across a restart.
//
// This test models the reboot the way the appliance performs it — regenerating and re-applying the whole
// ruleset — and then asserts both.
func TestKernel_RebootRestoresTheFoundationEmptyAndKeepsInstallIdempotent(t *testing.T) {
	c := nftClient()
	f := &nftfoundation.Foundation{NftPath: nftWrapper, Run: nftfoundation.ExecRunner}
	ip := mustIP(t, guestIP)

	if _, err := f.Install(ctx(t)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := c.Allow(ctx(t), guestIface, ip, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 60*time.Second); err != nil {
		t.Fatal(err)
	}

	// THE REBOOT: the generated ruleset is re-applied in full. This is the `delete table` + recreate that the
	// surgical install exists to avoid doing casually — here it is legitimate, because the unit is restarting
	// and nothing is being preserved by anyone.
	rulesetPath := os.Getenv("KG_RULESET")
	if rulesetPath == "" {
		t.Skip("KG_RULESET not provided by the harness")
	}
	run(t, nftWrapper, "-f", rulesetPath)

	st, err := f.Inspect(ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !st.Phase3Set || !st.ForwardRule {
		t.Fatalf("the Phase-3 foundation did not come back after the reboot: %+v", st)
	}
	if !st.Phase3SetEmpty {
		t.Fatal("the Phase-3 set came back POPULATED after a reboot; a dark appliance must authorize nobody")
	}
	for _, r := range st.CaptiveRules {
		if !r.HasPhase3 {
			t.Fatalf("a captive rule came back without the Phase-3 exclusion: %s", r.Text)
		}
	}
	if len(st.LegacyElements) != 0 {
		t.Fatalf("legacy authorizations survived a full ruleset re-apply (%d); if that is true of this kernel "+
			"the runbook's description of what a reboot preserves is wrong", len(st.LegacyElements))
	}
	// And re-running the install after a reboot changes nothing.
	rep, err := f.Install(ctx(t))
	if err != nil {
		t.Fatalf("post-reboot install: %v", err)
	}
	if rep.Outcome != "ALREADY_INSTALLED" || len(rep.Commands) != 0 {
		t.Fatalf("a post-reboot install was not a no-op: %+v", rep)
	}
}

// ---- 6. the timeout REPRESENTATION, in the kernel ----------------------------

// THE QUANTIZATION PROOF. A lease clamped to a guest's hard access boundary is only safe if the timeout the
// kernel actually installs is never LATER than the lease that was asked for. nft's element timeout granularity
// is whole seconds, so this is the test that says what the kernel really does with the values this design
// produces — rather than what the Go code believes it does.
func TestKernel_InstalledTimeoutNeverExceedsTheRequestedLease(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	for _, ttl := range []time.Duration{90 * time.Second, 15 * time.Second, 1900 * time.Millisecond, time.Second} {
		_ = c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)
		if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, ttl); err != nil {
			t.Fatalf("%v: %v", ttl, err)
		}
		els, err := c.ListIn(ctx(t), nft.Phase3AuthV4)
		if err != nil || len(els) != 1 {
			t.Fatalf("%v: list returned %d element(s): %v", ttl, len(els), err)
		}
		if els[0].Timeout > ttl {
			t.Fatalf("a %v lease was installed with a %v kernel timeout — %v PAST the caller's bound; a "+
				"boundary-clamped lease would outlive the boundary", ttl, els[0].Timeout, els[0].Timeout-ttl)
		}
		if els[0].Timeout <= 0 {
			t.Fatalf("a %v lease produced a kernel timeout of %v; nft reads no timeout as PERMANENT",
				ttl, els[0].Timeout)
		}
	}
}

// A sub-second lease is REFUSED before it reaches the kernel. There is no representable form of it that does
// not overshoot, and `timeout 0s` — the other thing a naive conversion produces — is read by nft as no
// timeout at all.
func TestKernel_SubSecondLeaseIsRefusedAndInstallsNothing(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	_ = c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)

	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 200*time.Millisecond); err == nil {
		t.Fatal("a 200ms lease was accepted")
	}
	els, err := c.ListIn(ctx(t), nft.Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 0 {
		t.Fatalf("a refused lease installed %d element(s): %+v", len(els), els)
	}
}

// AND THE CLAMPED LEASE REALLY EXPIRES BEFORE THE BOUNDARY. A 1.9s boundary becomes a 1s lease; a guest
// leased that way is gone from the set well before 1.9 seconds have passed. This is the end-to-end form of
// the invariant: not "the number is smaller" but "the kernel stopped forwarding in time".
func TestKernel_ClampedLeaseExpiresBeforeItsBoundary(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	boundary := 1900 * time.Millisecond
	lease := time.Duration(int(boundary/time.Second)) * time.Second // what leaseFor would derive: 1s
	start := time.Now()
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, lease); err != nil {
		t.Fatal(err)
	}
	if !reaches(t) {
		t.Fatal("the leased guest could not reach the WAN, so this proves nothing about expiry")
	}
	// Sleep to just inside the boundary. The element must already be gone.
	time.Sleep(boundary - time.Since(start) - 100*time.Millisecond)
	ok, err := c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("the authorization survived to %v, inside its %v boundary", time.Since(start), boundary)
	}
}
