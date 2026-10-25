package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nftconverge"
)

// fakeKernel records the commands netd executes: the production invariant is about execution (a steady-state
// restart issues no nft mutation and a converge never drops authorization), which only a recording fake can tell.
type fakeKernel struct {
	t           *testing.T
	tableExists bool
	fp          string              // marker fingerprint; "" means a table with no marker
	sets        map[string][]string // set name -> element keys, "iface|ip" or "ip"
	timeouts    map[string]int      // element key -> remaining seconds
	cmds        []string
	failNftF    bool
	loaded      []string // the fingerprint of every ruleset loaded, in order

	// Read-failure seams: ways nft can fail that do not mean "the object is absent".
	failListTables   bool
	garbleListTables bool
	failListTable    bool
	failReadSet      string
	garbleReadSet    string
}

func newFakeKernel(t *testing.T) *fakeKernel {
	return &fakeKernel{t: t, sets: map[string][]string{}, timeouts: map[string]int{}}
}

// legacyJulyTable seeds a pre-Phase-3 table: auth_ipv4 populated, no marker, no phase3_auth_ipv4.
func (k *fakeKernel) legacyJulyTable(guests ...string) {
	k.tableExists = true
	k.fp = ""
	k.sets["auth_ipv4"] = append([]string{}, guests...)
	for _, g := range guests {
		k.timeouts[g] = 3600
	}
	delete(k.sets, "phase3_auth_ipv4")
}

func (k *fakeKernel) run(_ context.Context, name string, args ...string) error {
	k.cmds = append(k.cmds, name+" "+strings.Join(args, " "))
	if name != "nft" {
		return nil
	}
	if len(args) == 2 && args[0] == "-f" {
		if k.failNftF {
			return fmt.Errorf("simulated nft failure")
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		k.loadScript(string(raw))
	}
	return nil
}

// loadScript models `nft -f`: the render begins with `delete table`, so only what the script re-adds survives.
func (k *fakeKernel) loadScript(script string) {
	defer func() { k.loaded = append(k.loaded, k.fp) }()
	if strings.Contains(script, "delete table inet stayconnect") {
		k.sets = map[string][]string{}
		k.timeouts = map[string]int{}
	}
	k.tableExists = true
	k.fp = ""
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "set ") && strings.HasSuffix(line, "{"):
			name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "set "), "{"))
			if _, ok := k.sets[name]; !ok {
				k.sets[name] = nil
			}
		case strings.HasPrefix(line, "comment \""):
			if fp := netcfg.FingerprintFromSetComment(line); fp != "" {
				k.fp = fp
			}
		case strings.HasPrefix(line, "add element inet stayconnect "):
			rest := strings.TrimPrefix(line, "add element inet stayconnect ")
			name, body, ok := strings.Cut(rest, " {")
			if !ok {
				continue
			}
			body = strings.TrimSuffix(strings.TrimSpace(body), "}")
			key, ttl := parseElemBody(body)
			k.sets[name] = append(k.sets[name], key)
			k.timeouts[key] = ttl
		}
	}
}

func parseElemBody(body string) (string, int) {
	body = strings.TrimSpace(body)
	ttl := 0
	if i := strings.Index(body, " timeout "); i >= 0 {
		fmt.Sscanf(strings.TrimSpace(body[i+len(" timeout "):]), "%ds", &ttl)
		body = strings.TrimSpace(body[:i])
	}
	if iface, ip, ok := strings.Cut(body, " . "); ok {
		return strings.Trim(iface, `"`) + "|" + strings.TrimSpace(ip), ttl
	}
	return body, ttl
}

func (k *fakeKernel) output(_ context.Context, name string, args ...string) ([]byte, error) {
	k.cmds = append(k.cmds, name+" "+strings.Join(args, " "))
	joined := strings.Join(args, " ")

	// Failure injection: each nft failure that is not "the object is absent" must abort the converge, not read as "empty".
	if k.failListTables {
		if strings.HasPrefix(joined, "-j list tables") {
			return nil, fmt.Errorf("nft: netlink: Operation not permitted")
		}
	}
	if k.garbleListTables && strings.HasPrefix(joined, "-j list tables") {
		return []byte("{not json"), nil
	}
	if strings.HasPrefix(joined, "-j list tables") {
		// `list tables` works whether or not our table exists, so it is the authority on absence.
		var objs []any
		if k.tableExists {
			objs = append(objs, map[string]any{"table": map[string]any{"family": "inet", "name": "stayconnect"}})
		}
		objs = append(objs, map[string]any{"table": map[string]any{"family": "inet", "name": "someone-else"}})
		return json.Marshal(map[string]any{"nftables": objs})
	}

	if !k.tableExists {
		return nil, fmt.Errorf("No such file or directory")
	}
	if k.failListTable && strings.HasPrefix(joined, "-j list table inet stayconnect") {
		return nil, fmt.Errorf("nft: netlink: Interrupted system call")
	}
	if k.failReadSet != "" && strings.HasSuffix(joined, k.failReadSet) && strings.Contains(joined, "list set") {
		return nil, fmt.Errorf("nft: netlink: Device or resource busy")
	}
	if k.garbleReadSet != "" && strings.HasSuffix(joined, k.garbleReadSet) && strings.Contains(joined, "list set") {
		return []byte("{ truncated json"), nil
	}

	switch {
	case strings.HasPrefix(joined, "-j list table inet stayconnect"):
		var objs []map[string]any
		if k.fp != "" {
			objs = append(objs, map[string]any{"set": map[string]any{
				"name": netcfg.RenderMarkerSet, "comment": "netd-render-fp=" + k.fp}})
		}
		for name := range k.sets {
			// Real nft lists each set once; re-emitting the marker would hide whether the reader handles the real listing.
			if name == netcfg.RenderMarkerSet {
				continue
			}
			objs = append(objs, map[string]any{"set": map[string]any{"name": name, "comment": ""}})
		}
		if len(objs) == 0 {
			objs = append(objs, map[string]any{"table": map[string]any{"name": "stayconnect"}})
		}
		return json.Marshal(map[string]any{"nftables": objs})
	case strings.HasPrefix(joined, "-j list set inet stayconnect "):
		set := args[len(args)-1]
		els, ok := k.sets[set]
		if !ok {
			return nil, fmt.Errorf("No such file or directory")
		}
		var out []any
		for _, e := range els {
			var val any
			if iface, ip, isConcat := strings.Cut(e, "|"); isConcat {
				val = map[string]any{"concat": []any{iface, ip}}
			} else {
				val = e
			}
			// timeouts[e] < 0 is a permanent element (no timeout or expires), like legacy scd authorizations.
			if k.timeouts[e] < 0 {
				out = append(out, map[string]any{"elem": map[string]any{"val": val}})
				continue
			}
			out = append(out, map[string]any{"elem": map[string]any{
				"val": val, "timeout": float64(3600), "expires": float64(k.timeouts[e])}})
		}
		return json.Marshal(map[string]any{"nftables": []any{
			map[string]any{"set": map[string]any{"name": set, "elem": out}}}})
	}
	return nil, fmt.Errorf("unexpected: %s", joined)
}

func (k *fakeKernel) mutations() []string {
	var out []string
	for _, c := range k.cmds {
		if strings.HasPrefix(c, "nft -f") {
			out = append(out, c)
		}
	}
	return out
}

func (k *fakeKernel) reset() { k.cmds = nil }

func (k *fakeKernel) has(set, key string) bool {
	for _, e := range k.sets[set] {
		if e == key {
			return true
		}
	}
	return false
}

func testIntent() []netcfg.GuestNetwork {
	return []netcfg.GuestNetwork{{
		Name: "guest90", Enabled: true, NetworkType: "vlan", ParentInterface: "ens192", VLANID: 90,
		BridgeName: "br-g90", GatewayIP: "10.20.0.1", SubnetCIDR: "10.20.0.0/22", PrefixLen: 22,
		DHCPMode: "managed", CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
	}}
}

func testTopo() netcfg.Topology {
	return netcfg.Topology{WANInterface: "ens160", MgmtInterface: "ens160", MgmtAddr: "192.0.2.23"}
}

func newTestApplier(t *testing.T, k *fakeKernel) *applier {
	t.Helper()
	return &applier{
		topo:         testTopo(),
		generatedDir: t.TempDir(),
		runFn:        k.run,
		outFn:        k.output,
		// Stands in for /sys/class/net, whose read is fail-closed in production; cases that care override it.
		liveBridgesFn: func() map[string]bool { return map[string]bool{} },
		// The post-reconcile health checks read the machine; cases that care override this.
		healthFn: func(context.Context, string, []netcfg.GuestNetwork) []healthResult { return nil },
	}
}

func converge(t *testing.T, a *applier, trigger string) nftConvergeOutcome {
	t.Helper()
	res, err := a.ensureNftStructure(context.Background(), testIntent(), trigger)
	if err != nil {
		t.Fatalf("%s: %v", trigger, err)
	}
	return res
}

// Upgrade from the pre-Phase-3 structure (no phase3_auth_ipv4, no marker): one reconciliation converges it
// without deauthorizing current guests.
func TestReconcile_UpgradeFromPrePhase3Table(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.55", "br-g90|10.20.0.56")
	a := newTestApplier(t, k)

	res := converge(t, a, "boot_reconcile")

	if !res.Changed {
		t.Fatal("a pre-Phase-3 table was left as-is; this is the Increment-9 blocker")
	}
	if _, ok := k.sets["phase3_auth_ipv4"]; !ok {
		t.Fatal("phase3_auth_ipv4 is still missing after reconciliation")
	}
	if k.fp != netcfg.RenderFingerprint(testIntent(), testTopo()) {
		t.Fatalf("live fingerprint %q does not match the current render", k.fp)
	}
	for _, g := range []string{"br-g90|10.20.0.55", "br-g90|10.20.0.56"} {
		if !k.has("auth_ipv4", g) {
			t.Fatalf("converging deauthorized live guest %s", g)
		}
	}
	if res.Carried != 2 {
		t.Fatalf("carried %d elements, want 2", res.Carried)
	}
}

func TestReconcile_Phase3SetExistsAndIsEmptyWhileDark(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	converge(t, a, "fresh")

	els, ok := k.sets["phase3_auth_ipv4"]
	if !ok {
		t.Fatal("phase3_auth_ipv4 absent after deployment")
	}
	if len(els) != 0 {
		t.Fatalf("phase3_auth_ipv4 authorizes %d element(s) while Phase 3 is dark; must be empty", len(els))
	}
}

// Once converged, a restart must issue no nft mutation. Asserted on the command log, not the ruleset: re-applying
// the same render yields an identical structure while destroying every live authorization.
func TestReconcile_SteadyStateRestartIssuesNoMutation(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	converge(t, a, "first")
	k.sets["auth_ipv4"] = []string{"br-g90|10.20.0.77"}
	k.timeouts["br-g90|10.20.0.77"] = 1800

	k.reset()
	res := converge(t, a, "restart")

	if res.Changed {
		t.Fatal("a steady-state restart rewrote the ruleset")
	}
	if m := k.mutations(); len(m) != 0 {
		t.Fatalf("a steady-state restart executed %d nft mutation(s): %v", len(m), m)
	}
	if !k.has("auth_ipv4", "br-g90|10.20.0.77") {
		t.Fatal("a steady-state restart dropped a live guest's authorization")
	}
}

func TestReconcile_RepeatedRestartsAreIdempotent(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	first := converge(t, a, "boot")
	if !first.Changed {
		t.Fatal("the first reconciliation on an empty kernel changed nothing")
	}
	for i := 0; i < 5; i++ {
		k.reset()
		res := converge(t, a, fmt.Sprintf("restart-%d", i))
		if res.Changed || len(k.mutations()) != 0 {
			t.Fatalf("restart %d was not idempotent: changed=%v cmds=%v", i, res.Changed, k.mutations())
		}
	}
}

// A reboot clears the ruleset (or leaves only the static conf); reconstruction rebuilds it and then settles.
func TestReconcile_RebootReconstructionThenSettles(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	converge(t, a, "install")

	for cycle := 0; cycle < 3; cycle++ {
		k.tableExists, k.fp, k.sets, k.timeouts = false, "", map[string][]string{}, map[string]int{}
		k.reset()
		res := converge(t, a, "boot_reconcile")
		if !res.Changed {
			t.Fatalf("cycle %d: reboot did not reconstruct the ruleset", cycle)
		}
		if _, ok := k.sets["phase3_auth_ipv4"]; !ok {
			t.Fatalf("cycle %d: phase3_auth_ipv4 missing after reboot", cycle)
		}
		k.reset()
		if res := converge(t, a, "settle"); res.Changed || len(k.mutations()) != 0 {
			t.Fatalf("cycle %d: reconciliation did not settle after reboot", cycle)
		}
	}
}

func TestReconcile_FreshInstallProducesCurrentFormat(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	res := converge(t, a, "fresh-install")

	if res.TableWas {
		t.Fatal("the fresh-install case was not actually starting from no table")
	}
	if k.fp != netcfg.RenderFingerprint(testIntent(), testTopo()) {
		t.Fatal("a fresh installation did not land on the current render")
	}
	for _, s := range []string{"auth_ipv4", "phase3_auth_ipv4", netcfg.RenderMarkerSet} {
		if _, ok := k.sets[s]; !ok {
			t.Fatalf("fresh installation is missing %s", s)
		}
	}
	k.reset()
	if r := converge(t, a, "verify"); r.Changed {
		t.Fatal("a fresh installation required a second convergence")
	}
}

// An obsolete stored bundle (no phase3 set) must not influence the structure.
func TestReconcile_ObsoleteStoredBundleIsNotConsulted(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable()
	a := newTestApplier(t, k)

	stale := "#!/usr/sbin/nft -f\ntable inet stayconnect\ndelete table inet stayconnect\n\ntable inet stayconnect {\n\tset auth_ipv4 {\n\t\ttype ifname . ipv4_addr\n\t}\n}\n"
	staleFile := a.generatedDir + "/revision-000056-stayconnect.nft"
	if err := os.WriteFile(staleFile, []byte(stale), 0o640); err != nil {
		t.Fatal(err)
	}

	converge(t, a, "boot_reconcile")

	for _, c := range k.cmds {
		if strings.Contains(c, "revision-000056") {
			t.Fatalf("reconciliation read the obsolete stored bundle: %s", c)
		}
	}
	if _, ok := k.sets["phase3_auth_ipv4"]; !ok {
		t.Fatal("the obsolete bundle still determined the live structure")
	}
}

func TestReconcile_UnreadableMarkerNeverCountsAsAMatch(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	converge(t, a, "install")

	// Corrupt the marker like a truncated or hand-edited comment.
	for _, bad := range []string{"", "netd-render-fp=", "netd-render-fp=zzzz", "netd-render-fp=" + strings.Repeat("a", 31)} {
		k.fp = netcfg.FingerprintFromSetComment(bad)
		k.reset()
		res := converge(t, a, "after-corruption")
		if !res.Changed {
			t.Fatalf("an unreadable marker (%q) was treated as a match", bad)
		}
	}
}

func TestReconcile_FingerprintTracksIntentAndTopology(t *testing.T) {
	base := netcfg.RenderFingerprint(testIntent(), testTopo())

	other := testIntent()
	other[0].SubnetCIDR = "10.30.0.0/22"
	if netcfg.RenderFingerprint(other, testTopo()) == base {
		t.Fatal("changing the subnet did not change the fingerprint")
	}
	topo2 := testTopo()
	topo2.WANInterface = "eth9"
	if netcfg.RenderFingerprint(testIntent(), topo2) == base {
		t.Fatal("changing the WAN interface did not change the fingerprint")
	}
	if netcfg.RenderFingerprint(testIntent(), testTopo()) != base {
		t.Fatal("the fingerprint is not deterministic for identical input")
	}
}

func TestReconcile_ApplyFailurePropagates(t *testing.T) {
	k := newFakeKernel(t)
	k.failNftF = true
	a := newTestApplier(t, k)
	if _, err := a.ensureNftStructure(context.Background(), testIntent(), "boot"); err == nil {
		t.Fatal("a failed nft load was reported as a successful reconciliation")
	}
}

// An unreadable live state is not an empty one: `nft list set` exits non-zero for a missing set, a missing
// binary, denied permission or a busy socket alike, and reading all as "nothing to preserve" before a
// `delete table` would deauthorize a whole property. Absence is decided by the table listing, and anything
// listed must be readable or the converge is abandoned before any mutation.
func TestReconcile_AnUnreadableLiveStateAbortsBeforeAnyMutation(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(k *fakeKernel)
	}{
		{"nft itself is unusable", func(k *fakeKernel) { k.failListTables = true }},
		{"table enumeration is unparseable", func(k *fakeKernel) { k.garbleListTables = true }},
		{"the table exists but cannot be read", func(k *fakeKernel) { k.failListTable = true }},
		{"a listed auth set cannot be read", func(k *fakeKernel) { k.failReadSet = "auth_ipv4" }},
		{"a listed auth set returns unparseable output", func(k *fakeKernel) { k.garbleReadSet = "auth_ipv4" }},
		{"the PHASE-3 set cannot be read", func(k *fakeKernel) { k.failReadSet = "phase3_auth_ipv4" }},
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(tc.name, " ", "_"), func(t *testing.T) {
			k := newFakeKernel(t)
			k.legacyJulyTable("br-g90|10.20.0.55", "br-g90|10.20.0.56")
			k.sets["phase3_auth_ipv4"] = []string{}
			before := len(k.sets["auth_ipv4"])
			a := newTestApplier(t, k)
			tc.break_(k)

			_, err := a.ensureNftStructure(context.Background(), testIntent(), "boot_reconcile")
			if err == nil {
				t.Fatal("converged from a live state it could not read")
			}
			if !errors.Is(err, nftconverge.ErrLiveStateUntrusted) {
				t.Fatalf("error does not identify itself as an untrusted live state: %v", err)
			}
			if m := k.mutations(); len(m) != 0 {
				t.Fatalf("the live ruleset was mutated after failing to read it: %v", m)
			}
			if len(k.sets["auth_ipv4"]) != before {
				t.Fatal("live authorization changed despite the refusal")
			}
			if k.fp != "" {
				t.Fatal("the marker was written for a converge that never happened")
			}
		})
	}
}

// A set the enumeration does not list is genuinely absent and must not block the converge (pre-Phase-3
// appliances have no phase3_auth_ipv4).
func TestReconcile_AGenuinelyAbsentSetIsEmptyNotAnError(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.55")
	if _, ok := k.sets["phase3_auth_ipv4"]; ok {
		t.Fatal("precondition: the phase-3 set should not be present")
	}
	a := newTestApplier(t, k)

	res, err := a.ensureNftStructure(context.Background(), testIntent(), "boot_reconcile")
	if err != nil {
		t.Fatalf("a genuinely absent set blocked the converge: %v", err)
	}
	if !res.Changed {
		t.Fatal("the converge did not happen")
	}
	if res.Carried != 1 {
		t.Fatalf("carried %d; the one legacy element should have been carried", res.Carried)
	}
	for _, c := range k.cmds {
		if strings.Contains(c, "list set inet stayconnect phase3_auth_ipv4") {
			t.Fatal("the absent set was probed by reading it; absence must come from the table listing")
		}
	}
}

func TestReconcile_ReadFailureIsReportedNotSwallowed(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.55")
	a := newTestApplier(t, k)
	k.failReadSet = "auth_ipv4"

	_, err := a.ensureNftStructure(context.Background(), testIntent(), "boot")
	if err == nil || !strings.Contains(err.Error(), "auth_ipv4") {
		t.Fatalf("the refusal does not name the set that could not be read: %v", err)
	}

	k.failReadSet = ""
	res, err := a.ensureNftStructure(context.Background(), testIntent(), "boot-retry")
	if err != nil || !res.Changed || res.Carried != 1 {
		t.Fatalf("recovery converge failed: changed=%v carried=%d err=%v", res.Changed, res.Carried, err)
	}
}

func TestReconcile_CarryOverUsesRemainingLeaseNotOriginal(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.60")
	k.timeouts["br-g90|10.20.0.60"] = 7 // seconds left of a 3600s lease
	a := newTestApplier(t, k)

	converge(t, a, "boot")

	if got := k.timeouts["br-g90|10.20.0.60"]; got != 7 {
		t.Fatalf("carried the element with %ds; the remaining lease was 7s — an authorization was extended", got)
	}
}

// Legacy scd authorizations are permanent elements (no timeout) and must survive a converge.
func TestReconcile_PermanentLegacyElementsSurviveConverge(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-lan|10.10.0.42")
	k.timeouts["br-lan|10.10.0.42"] = -1 // permanent
	a := newTestApplier(t, k)

	res := converge(t, a, "boot")

	if res.Carried != 1 {
		t.Fatalf("carried %d elements; a permanent legacy authorization was dropped", res.Carried)
	}
	if !k.has("auth_ipv4", "br-lan|10.10.0.42") {
		t.Fatal("a permanent legacy guest lost authorization across the converge")
	}
	if k.timeouts["br-lan|10.10.0.42"] != 0 {
		t.Fatal("a permanent element was given a lease it never had")
	}
}

func TestReconcile_ExpiredElementsAreNotResurrected(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.61")
	k.timeouts["br-g90|10.20.0.61"] = 0
	a := newTestApplier(t, k)

	res := converge(t, a, "boot")

	if res.Carried != 0 {
		t.Fatalf("carried %d expired element(s)", res.Carried)
	}
	if k.has("auth_ipv4", "br-g90|10.20.0.61") {
		t.Fatal("an expired authorization was re-created by the converge")
	}
}

// An older table holding the retired verify_ipv4 set is upgraded by removing it, carrying nothing of it into
// another set, and keeping every signed-in guest online.
func TestReconcile_UpgradeDropsAPopulatedVerificationSetAndCarriesNothingOfIt(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.70")
	k.sets["verify_ipv4"] = []string{"br-g90|10.20.0.71", "br-g90|10.20.0.72"}
	k.timeouts["br-g90|10.20.0.71"], k.timeouts["br-g90|10.20.0.72"] = 300, 120
	a := newTestApplier(t, k)

	res := converge(t, a, "boot_reconcile")

	if !res.Changed {
		t.Fatal("a table carrying the retired verification set was left as-is")
	}
	if _, ok := k.sets["verify_ipv4"]; ok {
		t.Fatal("verify_ipv4 survived the converge")
	}
	if res.Carried != 1 || !k.has("auth_ipv4", "br-g90|10.20.0.70") {
		t.Fatalf("carried %d; want exactly the one signed-in guest, still authorized", res.Carried)
	}
	for set := range k.sets {
		for _, ip := range []string{"10.20.0.71", "10.20.0.72"} {
			if k.has(set, "br-g90|"+ip) || k.has(set, ip) {
				t.Fatalf("a device that only held a verification window is in %s after the converge", set)
			}
		}
	}
	for _, c := range k.cmds {
		if strings.Contains(c, "verify_") {
			t.Fatalf("the converge touched the retired set: %s", c)
		}
	}
	muts := k.mutations()
	if len(muts) != 1 {
		t.Fatalf("want one atomic nft -f, got %v", muts)
	}
	raw, err := os.ReadFile(strings.TrimPrefix(muts[0], "nft -f "))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "delete table inet stayconnect") {
		t.Fatal("the applied script does not replace the table, so the old set would linger")
	}
	if strings.Contains(string(raw), "verify_") || strings.Contains(string(raw), "10.20.0.71") || strings.Contains(string(raw), "10.20.0.72") {
		t.Fatalf("the applied script still names the verification set or its devices:\n%s", raw)
	}
}
