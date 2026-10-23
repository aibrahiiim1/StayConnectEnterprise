//go:build kernelgate

// Package kerneltest runs the real nft and tc binaries against a real kernel, sending real packets between three
// disposable network namespaces. It touches nothing outside the namespaces the harness creates.
// Built behind the kernelgate tag and driven by scripts/ci/kernel-netns-suite.sh.
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

// The harness passes the topology via the environment; an unset variable fails the suite rather than running elsewhere.
var (
	nftWrapper = mustEnv("KG_NFT")
	tcWrapper  = mustEnv("KG_TC")
	ipWrapper  = mustEnv("KG_IP")
	guestNS    = mustEnv("KG_GUEST_NS")
	guestIP    = mustEnv("KG_GUEST_IP")
	wanIP      = mustEnv("KG_WAN_IP")
	guestIface = mustEnv("KG_GUEST_IF")
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

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v — %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out)
}

// reaches sends real packets from the guest namespace to the WAN address. It is the only honest test of "is this
// guest on the internet"; an element, a filter or a row are proxies that have each been wrong.
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

// The Phase-3 set is empty and concatenated (ifname, IPv4) with timeouts; without `flags timeout` the lease
// would be silently ignored.
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

// Set isolation: a legacy authorization is invisible to Phase-3 operations and a Phase-3 revocation cannot remove it.
func TestKernel_LegacyAndPhase3SetsAreIsolated(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	if err := c.Allow(ctx(t), guestIface, ip, 0); err != nil {
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

// With nothing running, the element goes away by itself.
func TestKernel_LeaseExpiresWithNothingRunning(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); !ok {
		t.Fatal("the lease did not take")
	}
	time.Sleep(3500 * time.Millisecond)
	if ok, _ := c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); ok {
		t.Fatal("the element outlived its lease: the kernel is not expiring it, so a dead daemon would leave the guest online")
	}
}

// A repeated `add element` does not refresh the lease, so renewal needs the real refresh below.
func TestKernel_RepeatedAddDoesNotRefreshTheLease(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, 20*time.Second); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
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

// The real refresh restarts the lease without removing the authorization: delete and add are one transaction.
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

func TestKernel_NoElementMeansNoInternet(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	_ = c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)
	_ = c.Deny(ctx(t), guestIface, ip)

	if reaches(t) {
		t.Fatal("an unauthorized guest reached the WAN: the forward chain is not gating on the authorization sets")
	}
}

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

// Removing the tc classification denies nothing: packets fall back to the bridge's default class. Only the nft
// element denies.
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

	if err := sh.RemoveClassification(ctx(t), guestIface, ip); err != nil {
		t.Fatalf("remove classification: %v", err)
	}
	if !reaches(t) {
		t.Fatal("removing the tc classification denied access — if that is genuinely true of this kernel, the " +
			"whole nft-first ordering argument needs revisiting")
	}
	t.Log("CONFIRMED: with the tc classification removed the guest is STILL ONLINE, now through the bridge's " +
		"default class and metered by nothing")

	if err := c.DenyIn(ctx(t), nft.Phase3AuthV4, guestIface, ip); err != nil {
		t.Fatal(err)
	}
	if reaches(t) {
		t.Fatal("revoking packet authorization did not deny the guest")
	}
}

// A prepared class carries nothing: no classification filter, so the guest's packets are not in it.
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

// A re-rate changes the class in place; a delete+add would restart the counters the accounting checkpoint uses.
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

func TestKernel_FoundationInstallPreservesAPopulatedLegacySet(t *testing.T) {
	c := nftClient()
	f := &nftfoundation.Foundation{NftPath: nftWrapper, Run: nftfoundation.ExecRunner}
	ip := mustIP(t, guestIP)

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

	// The harness already renders the Phase-3 set; remove the foundation first to test an install onto a ruleset lacking it.
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
	if !reaches(t) {
		t.Fatal("a legacy guest lost internet access across the Phase-3 foundation install")
	}

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

// A reboot loses nft state: the authorization sets come back empty (scd and netd re-establish them). The foundation
// must come back, re-running the install must be a no-op, and the Phase-3 set must be empty.
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

	// The reboot: the generated ruleset is re-applied in full (delete table + recreate); valid as nothing is preserved.
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
	rep, err := f.Install(ctx(t))
	if err != nil {
		t.Fatalf("post-reboot install: %v", err)
	}
	if rep.Outcome != "ALREADY_INSTALLED" || len(rep.Commands) != 0 {
		t.Fatalf("a post-reboot install was not a no-op: %+v", rep)
	}
}

// A lease clamped to a hard access boundary is safe only if the installed timeout is never later than requested;
// nft timeouts are whole seconds.
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

// A sub-second lease is refused before the kernel: it cannot be represented without overshooting, and
// `timeout 0s` means no timeout.
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

// A 1.9s boundary becomes a 1s lease and the guest is gone from the set before 1.9s: the kernel stops forwarding in time.
func TestKernel_ClampedLeaseExpiresBeforeItsBoundary(t *testing.T) {
	c := nftClient()
	ip := mustIP(t, guestIP)
	t.Cleanup(func() { _ = c.DenyIn(context.Background(), nft.Phase3AuthV4, guestIface, ip) })

	boundary := 1900 * time.Millisecond
	lease := time.Duration(int(boundary/time.Second)) * time.Second
	start := time.Now()
	if err := c.LeaseIn(ctx(t), nft.Phase3AuthV4, guestIface, ip, lease); err != nil {
		t.Fatal(err)
	}
	if !reaches(t) {
		t.Fatal("the leased guest could not reach the WAN, so this proves nothing about expiry")
	}
	time.Sleep(boundary - time.Since(start) - 100*time.Millisecond)
	ok, err := c.AuthorizedIn(ctx(t), nft.Phase3AuthV4, guestIface, ip)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("the authorization survived to %v, inside its %v boundary", time.Since(start), boundary)
	}
}
