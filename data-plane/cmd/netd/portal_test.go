package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// Modelled portal interface: applies exactly the `ip` commands netd issues, so tests read back what netd executed and what the interface carries.

type fakePortalNet struct {
	exists bool
	up     bool
	addrs  []string
	cmds   []string
	fail   string
}

func (p *fakePortalNet) run(_ context.Context, name string, args ...string) error {
	c := name + " " + strings.Join(args, " ")
	p.cmds = append(p.cmds, c)
	if p.fail != "" && strings.HasPrefix(c, p.fail) {
		return errors.New("simulated failure: " + c)
	}
	switch {
	case c == "ip link add ogportal0 type dummy":
		p.exists, p.up, p.addrs = true, false, nil
	case c == "ip link del ogportal0":
		p.exists, p.up, p.addrs = false, false, nil
	case c == "ip link set ogportal0 up":
		p.up = true
	case strings.HasPrefix(c, "ip addr replace ") && strings.HasSuffix(c, " dev ogportal0"):
		p.addrs = append(p.addrs, args[2])
	case strings.HasPrefix(c, "ip addr del ") && strings.HasSuffix(c, " dev ogportal0"):
		var kept []string
		for _, a := range p.addrs {
			if a != args[2] {
				kept = append(kept, a)
			}
		}
		p.addrs = kept
	}
	return nil
}

func (p *fakePortalNet) link(name string) (bool, bool) {
	if name != netcfg.PortalIface {
		return false, false
	}
	return p.exists, p.up
}

func (p *fakePortalNet) mutations() []string {
	var out []string
	for _, c := range p.cmds {
		if strings.HasPrefix(c, "ip ") {
			out = append(out, c)
		}
	}
	return out
}

func portalApplier(t *testing.T, k *fakeKernel, pn *fakePortalNet) *applier {
	t.Helper()
	a := newTestApplier(t, k)
	a.runFn = func(ctx context.Context, name string, args ...string) error {
		if name == "ip" && strings.Contains(strings.Join(args, " "), netcfg.PortalIface) {
			return pn.run(ctx, name, args...)
		}
		c := name + " " + strings.Join(args, " ")
		pn.cmds = append(pn.cmds, c)
		if pn.fail != "" && strings.HasPrefix(c, pn.fail) {
			return errors.New("simulated failure: " + c)
		}
		return k.run(ctx, name, args...)
	}
	a.portalLinkFn = pn.link
	a.addrsFn = func(name string) []string {
		if name == netcfg.PortalIface {
			return append([]string(nil), pn.addrs...)
		}
		return nil
	}
	a.hostAddrsFn = func(context.Context) ([]netcfg.HostAddr, error) {
		return []netcfg.HostAddr{{Iface: "ens160", CIDR: "192.168.60.25/24"}, {Iface: "docker0", CIDR: "172.17.0.1/16"}}, nil
	}
	return a
}

func TestPortalIface_CreatedAddressedAndUp(t *testing.T) {
	pn := &fakePortalNet{}
	a := portalApplier(t, newFakeKernel(t), pn)
	changed, err := a.ensurePortalIface(context.Background(), "10.255.255.1")
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := []string{
		"ip link add ogportal0 type dummy",
		"ip addr replace 10.255.255.1/32 dev ogportal0",
		"ip link set ogportal0 up",
	}
	if strings.Join(pn.mutations(), "|") != strings.Join(want, "|") {
		t.Fatalf("executed %v, want %v", pn.mutations(), want)
	}
	if ok, detail := a.portalIfaceHealthy("10.255.255.1"); !ok {
		t.Fatalf("health check disagrees with the interface: %s", detail)
	}
}

func TestPortalIface_SteadyStateExecutesNothing(t *testing.T) {
	// The 15 s refresh must issue no command on a correct appliance.
	pn := &fakePortalNet{exists: true, up: true, addrs: []string{"10.255.255.1/32"}}
	a := portalApplier(t, newFakeKernel(t), pn)
	changed, err := a.ensurePortalIface(context.Background(), "10.255.255.1")
	if err != nil || changed || len(pn.mutations()) != 0 {
		t.Fatalf("steady state changed=%v err=%v executed %v", changed, err, pn.mutations())
	}
}

func TestPortalIface_ChangedAddressReplacesTheOldOne(t *testing.T) {
	pn := &fakePortalNet{exists: true, up: true, addrs: []string{"10.255.255.1/32"}}
	a := portalApplier(t, newFakeKernel(t), pn)
	if _, err := a.ensurePortalIface(context.Background(), "10.254.254.1"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(pn.addrs, ",") != "10.254.254.1/32" {
		t.Fatalf("the interface carries %v; the old address must be gone and only the new one present", pn.addrs)
	}
	// The old address goes first: never both at once.
	m := pn.mutations()
	if len(m) != 2 || !strings.HasPrefix(m[0], "ip addr del 10.255.255.1/32") || !strings.HasPrefix(m[1], "ip addr replace 10.254.254.1/32") {
		t.Fatalf("executed %v", m)
	}
}

func TestPortalIface_NoAddressDeletesTheInterface(t *testing.T) {
	pn := &fakePortalNet{exists: true, up: true, addrs: []string{"10.255.255.1/32"}}
	a := portalApplier(t, newFakeKernel(t), pn)
	if _, err := a.ensurePortalIface(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if pn.exists {
		t.Fatal("the portal interface survived its address being removed")
	}
	pn.cmds = nil
	if changed, _ := a.ensurePortalIface(context.Background(), ""); changed || len(pn.mutations()) != 0 {
		t.Fatalf("removing an absent interface executed %v", pn.mutations())
	}
}

func TestPortalIface_ADownInterfaceIsBroughtUp(t *testing.T) {
	pn := &fakePortalNet{exists: true, up: false, addrs: []string{"10.255.255.1/32"}}
	a := portalApplier(t, newFakeKernel(t), pn)
	if _, err := a.ensurePortalIface(context.Background(), "10.255.255.1"); err != nil {
		t.Fatal(err)
	}
	if m := pn.mutations(); len(m) != 1 || m[0] != "ip link set ogportal0 up" {
		t.Fatalf("executed %v", m)
	}
}

func TestPortalIface_DryRunTouchesNothingAndFailuresAreReported(t *testing.T) {
	pn := &fakePortalNet{}
	a := portalApplier(t, newFakeKernel(t), pn)
	a.dryRun = true
	if changed, err := a.ensurePortalIface(context.Background(), "10.255.255.1"); changed || err != nil || len(pn.cmds) != 0 {
		t.Fatalf("a dry run executed %v", pn.cmds)
	}
	a.dryRun = false
	pn.fail = "ip addr replace"
	if _, err := a.ensurePortalIface(context.Background(), "10.255.255.1"); err == nil {
		t.Fatal("a failed address assignment was reported as success")
	}
}

func TestParseHostAddrs(t *testing.T) {
	raw := `[{"ifindex":1,"ifname":"lo","addr_info":[{"family":"inet","local":"127.0.0.1","prefixlen":8}]},
	{"ifindex":2,"ifname":"ens160","addr_info":[{"family":"inet","local":"192.168.60.25","prefixlen":24}]},
	{"ifindex":3,"ifname":"docker0","addr_info":[{"family":"inet","local":"172.17.0.1","prefixlen":16}]},
	{"ifindex":4,"ifname":"ens192","addr_info":[]},
	{"ifindex":5,"ifname":"ogportal0","addr_info":[{"family":"inet","local":"10.255.255.1","prefixlen":32}]}]`
	got, err := parseHostAddrs([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, h := range got {
		s = append(s, h.Iface+"="+h.CIDR)
	}
	want := "lo=127.0.0.1/8 ens160=192.168.60.25/24 docker0=172.17.0.1/16 ogportal0=10.255.255.1/32"
	if strings.Join(s, " ") != want {
		t.Fatalf("got %v", s)
	}
	if _, err := parseHostAddrs([]byte("{oops")); err == nil {
		t.Fatal("unparseable output was accepted")
	}
}

func portalValidateCall(t *testing.T, a *applier, body string) (int, map[string]any) {
	t.Helper()
	s := &server{ap: a}
	w := httptest.NewRecorder()
	s.portalIPValidate(w, httptest.NewRequest(http.MethodPost, "/v1/portal-ip/validate", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestPortalIPValidateEndpoint(t *testing.T) {
	a := portalApplier(t, newFakeKernel(t), &fakePortalNet{})
	a.intentFn = func(context.Context) ([]netcfg.GuestNetwork, error) { return testIntent(), nil }

	cases := []struct {
		body string
		ok   bool
		code string
	}{
		{`{"portal_ip":"10.255.255.1"}`, true, ""},
		{`{"portal_ip":"10.20.1.1"}`, false, "portal_ip_overlap"},
		{`{"portal_ip":"172.17.9.9"}`, false, "portal_ip_overlap"},
		{`{"portal_ip":"192.168.60.77"}`, false, "portal_ip_overlap"},
		{`{"portal_ip":"203.0.113.5"}`, false, "portal_ip_not_private"},
		{`{"portal_ip":""}`, false, "required"},
	}
	for _, tc := range cases {
		code, out := portalValidateCall(t, a, tc.body)
		if code != 200 {
			t.Fatalf("%s: HTTP %d %v", tc.body, code, out)
		}
		if out["ok"] != tc.ok {
			t.Fatalf("%s: ok=%v want %v (%v)", tc.body, out["ok"], tc.ok, out)
		}
		issues, _ := out["issues"].([]any)
		if tc.ok && len(issues) != 0 {
			t.Fatalf("%s: issues on a valid address: %v", tc.body, issues)
		}
		if !tc.ok {
			found := false
			for _, i := range issues {
				if m, _ := i.(map[string]any); m["code"] == tc.code && m["field"] == "portal_ip" {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: want %s, got %v", tc.body, tc.code, issues)
			}
		}
	}
	if code, _ := portalValidateCall(t, a, "{not json"); code != 400 {
		t.Fatalf("a malformed body got HTTP %d", code)
	}
	// Unreadable host addresses are an error, never a pass.
	a.hostAddrsFn = func(context.Context) ([]netcfg.HostAddr, error) { return nil, errors.New("ip: netlink busy") }
	if code, _ := portalValidateCall(t, a, `{"portal_ip":"10.255.255.1"}`); code != 500 {
		t.Fatalf("an unknowable overlap answered HTTP %d", code)
	}
}

func TestGenerateRecordsThePortalAddressInTheBundle(t *testing.T) {
	a := portalApplier(t, newFakeKernel(t), &fakePortalNet{})
	a.setInForce(testIntent(), "10.255.255.1")
	bundle := filepath.Join(t.TempDir(), "revision-000042")
	if err := a.generate(bundle, testIntent()); err != nil {
		t.Fatal(err)
	}
	if vip, recorded := readPortalBundle(bundle); !recorded || vip != "10.255.255.1" {
		t.Fatalf("bundle records %q (recorded=%v)", vip, recorded)
	}
	nft, _ := os.ReadFile(filepath.Join(bundle, "stayconnect.nft"))
	if !strings.Contains(string(nft), "dnat ip to 10.255.255.1:8343") {
		t.Fatal("the bundle's ruleset does not carry the portal address it records")
	}
	a.setInForce(testIntent(), "")
	b2 := filepath.Join(t.TempDir(), "revision-000043")
	if err := a.generate(b2, testIntent()); err != nil {
		t.Fatal(err)
	}
	if vip, recorded := readPortalBundle(b2); !recorded || vip != "" {
		t.Fatalf("a revision with no portal address recorded %q (recorded=%v)", vip, recorded)
	}
	if _, recorded := readPortalBundle(t.TempDir()); recorded {
		t.Fatal("a bundle with no record was read as recording one")
	}
}

func TestRollbackRestoresThePreviousRevisionsPortalAddress(t *testing.T) {
	rev := &fakeRevisions{prev: confirmedIntent()}
	a, k := rollbackHarness(t, rev)
	pn := &fakePortalNet{exists: true, up: true, addrs: []string{"10.254.254.1/32"}}
	b := portalApplier(t, k, pn)
	a.runFn, a.portalLinkFn, a.addrsFn = b.runFn, b.portalLinkFn, b.addrsFn
	a.setInForce(draftIntent(), "10.254.254.1")
	if err := os.WriteFile(filepath.Join(rev.bundle_, netcfg.PortalVIPBundleFile), []byte("10.255.255.1\n"), 0o640); err != nil {
		t.Fatal(err)
	}

	a.rollback(context.Background(), "failed-rev", "confirmation window elapsed")

	if strings.Join(pn.addrs, ",") != "10.255.255.1/32" {
		t.Fatalf("after rollback the portal interface carries %v; want the previous revision's 10.255.255.1/32", pn.addrs)
	}
	if _, vip, _ := a.inForce(); vip != "10.255.255.1" {
		t.Fatalf("in-force portal address %q", vip)
	}
	topo := a.topo
	topo.PortalVIP = "10.255.255.1"
	if want := netcfg.RenderFingerprint(confirmedIntent(), topo); k.fp != want {
		t.Fatalf("the restored ruleset is not the previous revision with its portal address")
	}
}

func TestRollbackToABundleThatPredatesThePortalAddressKeepsTheConfirmedOne(t *testing.T) {
	rev := &fakeRevisions{prev: confirmedIntent()}
	a, k := rollbackHarness(t, rev)
	pn := &fakePortalNet{exists: true, up: true, addrs: []string{"10.254.254.1/32"}}
	b := portalApplier(t, k, pn)
	a.runFn, a.portalLinkFn, a.addrsFn = b.runFn, b.portalLinkFn, b.addrsFn
	a.setActiveVIP("10.255.255.1")
	a.setInForce(draftIntent(), "10.254.254.1")

	a.rollback(context.Background(), "failed-rev", "health check failed")

	if strings.Join(pn.addrs, ",") != "10.255.255.1/32" {
		t.Fatalf("the portal interface carries %v; want the confirmed revision's address", pn.addrs)
	}
}

func TestAFactoryCleanRollbackRemovesThePortalInterface(t *testing.T) {
	k := newFakeKernel(t)
	pn := &fakePortalNet{exists: true, up: true, addrs: []string{"10.255.255.1/32"}}
	a := portalApplier(t, k, pn)
	a.prevBundleFn = func(context.Context, string) (string, error) { return "", nil }
	a.markRolledFn = func(context.Context, string, string) error { return nil }
	a.revStateFn = func(context.Context, string) (string, error) { return "pending_confirmation", nil }
	a.eventFn = func(context.Context, string, string, bool, map[string]any) {}
	dir := t.TempDir()
	a.netplanFile, a.unboundFrag, a.keaConfFile = dir+"/np.yaml", dir+"/g.conf", dir+"/kea.conf"
	a.setInForce(testIntent(), "10.255.255.1")

	a.rollback(context.Background(), "first-rev", "confirmation window elapsed")

	if pn.exists {
		t.Fatal("a factory-clean rollback left the portal interface in place")
	}
	if _, _, known := a.inForce(); known {
		t.Fatal("a factory-clean rollback left an intent in force")
	}
}

func TestBootReconcileUsesTheRevisionsRecordNotTheSetting(t *testing.T) {
	k := newFakeKernel(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, netcfg.PortalVIPBundleFile), []byte("10.255.255.1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	rev := &fakeRevisions{id: "rev-confirmed", intent: confirmedIntent(), bundle: dir}
	pn := &fakePortalNet{}
	a := newLifecycleApplier(t, k, rev)
	b := portalApplier(t, k, pn)
	a.runFn, a.portalLinkFn, a.addrsFn, a.hostAddrsFn = b.runFn, b.portalLinkFn, b.addrsFn, b.hostAddrsFn
	a.portalIPFn = func(context.Context) (string, error) { return "10.254.254.1", nil }

	a.ReconcileActiveOnBoot(context.Background())

	if strings.Join(pn.addrs, ",") != "10.255.255.1/32" || !pn.up {
		t.Fatalf("boot put %v (up=%v) on the portal interface; want the confirmed revision's 10.255.255.1/32", pn.addrs, pn.up)
	}
	topo := a.topo
	topo.PortalVIP = "10.255.255.1"
	if k.fp != netcfg.RenderFingerprint(confirmedIntent(), topo) {
		t.Fatal("the boot ruleset does not name the confirmed revision's portal address")
	}
}

func TestBootReconcileAdoptsAValidSettingOnceForAPreFeatureRevision(t *testing.T) {
	k := newFakeKernel(t)
	dir := t.TempDir()
	rev := &fakeRevisions{id: "rev-confirmed", intent: confirmedIntent(), bundle: dir}
	pn := &fakePortalNet{}
	a := newLifecycleApplier(t, k, rev)
	b := portalApplier(t, k, pn)
	a.runFn, a.portalLinkFn, a.addrsFn, a.hostAddrsFn = b.runFn, b.portalLinkFn, b.addrsFn, b.hostAddrsFn
	a.portalIPFn = func(context.Context) (string, error) { return "10.255.255.1", nil }

	a.ReconcileActiveOnBoot(context.Background())

	if strings.Join(pn.addrs, ",") != "10.255.255.1/32" {
		t.Fatalf("a valid configured address was not adopted: %v", pn.addrs)
	}
	if vip, recorded := readPortalBundle(dir); !recorded || vip != "10.255.255.1" {
		t.Fatalf("the adoption was not recorded in the active bundle (%q, %v)", vip, recorded)
	}
}

func TestBootReconcileAdoptsNothingInvalid(t *testing.T) {
	k := newFakeKernel(t)
	dir := t.TempDir()
	rev := &fakeRevisions{id: "rev-confirmed", intent: confirmedIntent(), bundle: dir}
	pn := &fakePortalNet{}
	a := newLifecycleApplier(t, k, rev)
	b := portalApplier(t, k, pn)
	a.runFn, a.portalLinkFn, a.addrsFn, a.hostAddrsFn = b.runFn, b.portalLinkFn, b.addrsFn, b.hostAddrsFn
	a.portalIPFn = func(context.Context) (string, error) { return "10.20.0.77", nil }

	a.ReconcileActiveOnBoot(context.Background())

	if pn.exists {
		t.Fatal("an overlapping address was adopted on an unattended restart")
	}
	if _, recorded := readPortalBundle(dir); recorded {
		t.Fatal("a refused adoption was recorded")
	}
}

type fakeKea struct {
	running  map[string]any
	tested   int
	set      int
	failTest bool
	failRead bool
	lastSet  map[string]any
	setErr   error // makes ConfigSet fail
}

func (f *fakeKea) RunningDhcp4() (map[string]any, error) {
	if f.failRead {
		return nil, errors.New("kea dial: no such file")
	}
	// Kea answers generic JSON, so round-trip for a like-for-like comparison.
	raw, _ := json.Marshal(f.running)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

func (f *fakeKea) ConfigTest(map[string]any) error {
	f.tested++
	if f.failTest {
		return errors.New("kea config-test failed")
	}
	return nil
}

func (f *fakeKea) ConfigSet(d map[string]any) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.set++
	f.lastSet = d
	f.running = d
	return nil
}

func localIntent() []netcfg.GuestNetwork {
	n := testIntent()
	n[0].DHCPMode = netcfg.DHCPLocal
	n[0].Pools = []netcfg.Pool{{StartIP: "10.20.0.100", EndIP: "10.20.3.200"}}
	return n
}

func refreshApplier(t *testing.T, tls netcfg.PortalTLS) (*applier, *fakeKea, *fakePortalNet) {
	t.Helper()
	pn := &fakePortalNet{exists: true, up: true, addrs: []string{"10.255.255.1/32"}}
	a := portalApplier(t, newFakeKernel(t), pn)
	a.topo.PortalHTTPPort = 8380
	fk := &fakeKea{}
	a.keaRTSeam = fk
	a.portalTLSFn = func(context.Context) (netcfg.PortalTLS, error) { return tls, nil }
	a.setInForce(localIntent(), "10.255.255.1")
	fk.running = a.renderKea(localIntent())
	return a, fk, pn
}

func opt114Of(d map[string]any) string { return captiveOptions(d)["10.20.0.0/22"] }

func TestRefresh_HTTPSBecomingActiveSwitchesOption114WithoutARevision(t *testing.T) {
	a, fk, pn := refreshApplier(t, netcfg.PortalTLS{HTTPSActive: true, CanonicalHostname: "k3j9x0ab.gw.onegate.work"})
	// On the HTTP fallback option 114 is omitted (a plain-HTTP value silenced the phones' probes, 2026-11-01).
	if got := opt114Of(fk.running); got != "" {
		t.Fatalf("precondition: Kea runs %q", got)
	}

	a.portalRefresh(context.Background())

	if fk.tested != 1 || fk.set != 1 {
		t.Fatalf("config-test=%d config-set=%d; want one of each", fk.tested, fk.set)
	}
	if got := opt114Of(fk.running); got != "https://k3j9x0ab.gw.onegate.work/captive-portal/api" {
		t.Fatalf("Kea now runs %q", got)
	}
	want := a.renderKea(localIntent())
	if !jsonEqual(fk.lastSet, want) {
		t.Fatal("the refresh pushed something other than what an apply of the in-force intent renders")
	}
	for _, c := range pn.cmds {
		if strings.HasPrefix(c, "nft") || strings.HasPrefix(c, "netplan") {
			t.Fatalf("the runtime refresh touched %s", c)
		}
	}

	a.portalRefresh(context.Background())
	if fk.set != 1 {
		t.Fatalf("a refresh with nothing to change pushed again (config-set=%d)", fk.set)
	}
}

func TestRefresh_FallbackWithdrawsOption114(t *testing.T) {
	a, fk, _ := refreshApplier(t, netcfg.PortalTLS{HTTPSActive: true, CanonicalHostname: "k3j9x0ab.gw.onegate.work"})
	a.portalRefresh(context.Background())
	a.portalTLSFn = func(context.Context) (netcfg.PortalTLS, error) {
		return netcfg.PortalTLS{OneGateHostname: "k3j9x0ab.gw.onegate.work"}, nil
	}
	a.portalRefresh(context.Background())
	if got := opt114Of(fk.running); got != "" {
		t.Fatalf("after the fallback Kea runs %q; the HTTP fallback carries no option 114", got)
	}
}

func TestRefresh_StandsAsideAndFailsSafe(t *testing.T) {
	active := netcfg.PortalTLS{HTTPSActive: true, CanonicalHostname: "k3j9x0ab.gw.onegate.work"}

	t.Run("an apply in progress", func(t *testing.T) {
		a, fk, _ := refreshApplier(t, active)
		a.busy.Add(1)
		a.portalRefresh(context.Background())
		if fk.set != 0 || fk.tested != 0 {
			t.Fatal("the refresh pushed Kea while an apply was in progress")
		}
	})
	t.Run("config-test refuses", func(t *testing.T) {
		a, fk, _ := refreshApplier(t, active)
		fk.failTest = true
		a.portalRefresh(context.Background())
		if fk.set != 0 {
			t.Fatal("a configuration Kea refused in config-test was set anyway")
		}
	})
	t.Run("Kea not running", func(t *testing.T) {
		a, fk, _ := refreshApplier(t, active)
		fk.failRead = true
		a.portalRefresh(context.Background())
		if fk.set != 0 {
			t.Fatal("pushed to a Kea whose running configuration could not be read")
		}
	})
	t.Run("the HTTPS state is unreadable", func(t *testing.T) {
		a, fk, _ := refreshApplier(t, active)
		a.portalRefresh(context.Background())
		a.portalTLSFn = func(context.Context) (netcfg.PortalTLS, error) { return netcfg.PortalTLS{}, errors.New("db down") }
		a.portalRefresh(context.Background())
		if got := opt114Of(fk.running); got != "https://k3j9x0ab.gw.onegate.work/captive-portal/api" {
			t.Fatalf("a transient read failure flipped every device's option 114 to %q", got)
		}
	})
	t.Run("nothing in force yet", func(t *testing.T) {
		a, fk, pn := refreshApplier(t, active)
		a.clearInForce()
		pn.cmds = nil
		a.portalRefresh(context.Background())
		if fk.set != 0 || len(pn.cmds) != 0 {
			t.Fatalf("the refresh acted with nothing in force: kea sets=%d cmds=%v", fk.set, pn.cmds)
		}
	})
}

func TestRefresh_ReassertsADeletedPortalInterface(t *testing.T) {
	a, _, pn := refreshApplier(t, netcfg.PortalTLS{})
	pn.exists, pn.up, pn.addrs = false, false, nil
	a.portalRefresh(context.Background())
	if !pn.exists || !pn.up || strings.Join(pn.addrs, ",") != "10.255.255.1/32" {
		t.Fatalf("the portal interface was not re-asserted: exists=%v up=%v addrs=%v", pn.exists, pn.up, pn.addrs)
	}
}

func TestRefresh_WritesThePortalNamesFragmentAndReloadsOnlyOnChange(t *testing.T) {
	a, _, pn := refreshApplier(t, netcfg.PortalTLS{OneGateHostname: "k3j9x0ab.gw.onegate.work"})
	a.unboundPortalFrag = filepath.Join(t.TempDir(), "stayconnect-portal.conf")

	a.portalRefresh(context.Background())

	got, err := os.ReadFile(a.unboundPortalFrag)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `local-data: "k3j9x0ab.gw.onegate.work. IN A 10.255.255.1"`) {
		t.Fatalf("fragment:\n%s", got)
	}
	joined := strings.Join(pn.cmds, "|")
	if !strings.Contains(joined, "unbound-checkconf") || !strings.Contains(joined, "unbound-control reload") {
		t.Fatalf("the changed fragment was not validated and reloaded: %v", pn.cmds)
	}

	pn.cmds = nil
	a.portalRefresh(context.Background())
	for _, c := range pn.cmds {
		if strings.HasPrefix(c, "unbound") || strings.Contains(c, "restart unbound") {
			t.Fatalf("an unchanged fragment reloaded the resolver: %s", c)
		}
	}

	a.portalTLSFn = func(context.Context) (netcfg.PortalTLS, error) {
		return netcfg.PortalTLS{OneGateHostname: "k3j9x0ab.gw.onegate.work", CustomDomain: "wifi.nilehotel.com"}, nil
	}
	a.portalRefresh(context.Background())
	got, _ = os.ReadFile(a.unboundPortalFrag)
	if !strings.Contains(string(got), `"wifi.nilehotel.com. IN A 10.255.255.1"`) {
		t.Fatalf("the verified hotel domain is not answered:\n%s", got)
	}
}

func TestRefresh_ARefusedFragmentIsPutBack(t *testing.T) {
	a, _, pn := refreshApplier(t, netcfg.PortalTLS{OneGateHostname: "k3j9x0ab.gw.onegate.work"})
	a.unboundPortalFrag = filepath.Join(t.TempDir(), "stayconnect-portal.conf")
	prev := []byte("# previous good fragment\n")
	if err := os.WriteFile(a.unboundPortalFrag, prev, 0o644); err != nil {
		t.Fatal(err)
	}
	pn.fail = "unbound-checkconf"
	a.portalRefresh(context.Background())
	got, _ := os.ReadFile(a.unboundPortalFrag)
	if string(got) != string(prev) {
		t.Fatalf("a fragment the resolver refused was left in place:\n%s", got)
	}
	for _, c := range pn.cmds {
		if strings.HasPrefix(c, "unbound-control") {
			t.Fatal("the resolver was reloaded with a refused fragment")
		}
	}
}

func TestCaptiveOptionsReadsKeasOwnAnswer(t *testing.T) {
	running := map[string]any{"subnet4": []any{
		map[string]any{"subnet": "10.20.0.0/22", "option-data": []any{
			map[string]any{"code": float64(3), "name": "routers", "data": "10.20.0.1", "always-send": false},
			map[string]any{"code": float64(114), "data": "http://10.20.0.1:8380/", "space": "dhcp4"},
		}},
		map[string]any{"subnet": "10.30.0.0/24", "option-data": []any{}},
	}}
	got := captiveOptions(running)
	if got["10.20.0.0/22"] != "http://10.20.0.1:8380/" || got["10.30.0.0/24"] != "" || len(got) != 2 {
		t.Fatalf("got %v", got)
	}
}

func jsonEqual(a, b map[string]any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}
