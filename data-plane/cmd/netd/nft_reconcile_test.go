package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// ---- a simulated kernel ------------------------------------------------------------------------------------
//
// The point of these tests is not "did the right ruleset text get produced" — the renderer's own tests cover
// that. It is "WHAT DID netd EXECUTE", because the production invariant is about execution: a restart of a
// correct appliance must issue no nft mutation at all, and a converge must not drop authorization on the way
// through. Only a fake that records commands can tell those apart.

type fakeKernel struct {
	t           *testing.T
	tableExists bool
	fp          string              // marker fingerprint; "" means a table with no marker (pre-fingerprint)
	sets        map[string][]string // set name -> element keys, as "iface|ip" or "ip"
	timeouts    map[string]int      // element key -> remaining seconds
	cmds        []string            // every command executed, in order
	failNftF    bool
}

func newFakeKernel(t *testing.T) *fakeKernel {
	return &fakeKernel{t: t, sets: map[string][]string{}, timeouts: map[string]int{}}
}

// legacyJulyTable seeds the exact condition the pilot appliance was in: a stayconnect table that predates the
// Phase-3 renderer — auth_ipv4 present and populated, no marker, and no phase3_auth_ipv4 at all.
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

// loadScript models `nft -f`: the render begins with `delete table`, so the table and every set in it are
// replaced wholesale, and only what the script re-adds survives.
func (k *fakeKernel) loadScript(script string) {
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
	if !k.tableExists {
		return nil, fmt.Errorf("No such file or directory")
	}
	switch {
	case strings.HasPrefix(joined, "-j list table inet stayconnect"):
		var objs []map[string]any
		if k.fp != "" {
			objs = append(objs, map[string]any{"set": map[string]any{
				"name": netcfg.RenderMarkerSet, "comment": "netd-render-fp=" + k.fp}})
		}
		for name := range k.sets {
			objs = append(objs, map[string]any{"set": map[string]any{"name": name, "comment": ""}})
		}
		if len(objs) == 0 {
			// a table with no sets at all still exists
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
			// timeouts[e] < 0 models a PERMANENT element: nft emits neither timeout nor expires for one,
			// which is exactly the shape legacy scd authorizations have.
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

// ---- harness -----------------------------------------------------------------------------------------------

func testIntent() []netcfg.GuestNetwork {
	return []netcfg.GuestNetwork{{
		Name: "guest90", Enabled: true, NetworkType: "vlan", ParentInterface: "ens192", VLANID: 90,
		BridgeName: "br-g90", GatewayIP: "10.20.0.1", SubnetCIDR: "10.20.0.0/22", PrefixLen: 22,
		DHCPMode: "managed", CaptiveEnabled: true, InternetEnabled: true, NATEnabled: true,
	}}
}

func testTopo() netcfg.Topology {
	return netcfg.Topology{WANInterface: "ens160", MgmtInterface: "ens160", MgmtAddr: "172.21.60.23"}
}

func newTestApplier(t *testing.T, k *fakeKernel) *applier {
	t.Helper()
	return &applier{
		topo:         testTopo(),
		generatedDir: t.TempDir(),
		runFn:        k.run,
		outFn:        k.output,
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

// ---- 1. UPGRADE FROM A PRE-PHASE-3 STORED BUNDLE -------------------------------------------------------------

// This is Live Increment 9 reproduced. The live table is the July structure: no phase3_auth_ipv4, no marker.
// One reconciliation must converge it — and must not deauthorize the guests who are on the box.
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
	// The two legacy guests must still be authorized.
	for _, g := range []string{"br-g90|10.20.0.55", "br-g90|10.20.0.56"} {
		if !k.has("auth_ipv4", g) {
			t.Fatalf("converging deauthorized live guest %s", g)
		}
	}
	if res.Carried != 2 {
		t.Fatalf("carried %d elements, want 2", res.Carried)
	}
}

// ---- 2. PHASE 3 IS PRESENT BUT EMPTY WHILE DARK ---------------------------------------------------------------

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

// ---- 3. STEADY-STATE RESTART EXECUTES NOTHING -----------------------------------------------------------------

// THE PRODUCTION INVARIANT. Once converged, a restart must not issue a single nft mutation. This is asserted on
// the command log, not on the resulting ruleset: re-applying the same render would leave an identical structure
// while destroying every live guest's authorization, and a ruleset-only assertion could not tell the difference.
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

// ---- 4. REPEATED RESTART / REBOOT IS IDEMPOTENT ---------------------------------------------------------------

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

// A reboot clears the kernel ruleset entirely (or leaves only the static /etc/nftables.conf). Reconstruction
// must rebuild the current structure, and doing it repeatedly must settle.
func TestReconcile_RebootReconstructionThenSettles(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	converge(t, a, "install")

	for cycle := 0; cycle < 3; cycle++ {
		// reboot: kernel ruleset gone
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

// ---- 5. FRESH INSTALLATION IS ALREADY CURRENT-FORMAT -----------------------------------------------------------

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
	// and it needs no second pass
	k.reset()
	if r := converge(t, a, "verify"); r.Changed {
		t.Fatal("a fresh installation required a second convergence")
	}
}

// ---- 6. THE APPLIANCE NO LONGER DEPENDS ON THE STORED BUNDLE ---------------------------------------------------

// An obsolete bundle on disk must not be able to influence the structure. Here the stored file is the July
// render (no phase3 set); reconciliation must ignore it completely and still converge.
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

// ---- 7. FINGERPRINT SEMANTICS ---------------------------------------------------------------------------------

func TestReconcile_UnreadableMarkerNeverCountsAsAMatch(t *testing.T) {
	k := newFakeKernel(t)
	a := newTestApplier(t, k)
	converge(t, a, "install")

	// Corrupt the marker the way a truncated or hand-edited comment would.
	for _, bad := range []string{"", "netd-render-fp=", "netd-render-fp=zzzz", "netd-render-fp=" + strings.Repeat("a", 31)} {
		k.fp = netcfg.FingerprintFromSetComment(bad) // "" for all of these
		k.reset()
		res := converge(t, a, "after-corruption")
		if !res.Changed {
			t.Fatalf("an unreadable marker (%q) was treated as a match", bad)
		}
	}
}

// A change in intent must change the fingerprint, or an upgrade would go undetected.
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

// ---- 8. FAILURE IS NOT REPORTED AS SUCCESS ---------------------------------------------------------------------

func TestReconcile_ApplyFailurePropagates(t *testing.T) {
	k := newFakeKernel(t)
	k.failNftF = true
	a := newTestApplier(t, k)
	if _, err := a.ensureNftStructure(context.Background(), testIntent(), "boot"); err == nil {
		t.Fatal("a failed nft load was reported as a successful reconciliation")
	}
}

// If a set that holds live authorization cannot be read, converging would silently deauthorize it. Refusing is
// the only safe answer, and the live ruleset must be left untouched.
func TestReconcile_UnreadableAuthSetRefusesToConverge(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.55")
	a := newTestApplier(t, k)
	a.outFn = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "list set inet stayconnect auth_ipv4") {
			return []byte("{ this is not json"), nil
		}
		return k.output(ctx, name, args...)
	}
	if _, err := a.ensureNftStructure(context.Background(), testIntent(), "boot"); err == nil {
		t.Fatal("converged despite being unable to read a live authorization set")
	}
	if len(k.mutations()) != 0 {
		t.Fatal("the live ruleset was mutated after refusing to converge")
	}
}

// ---- 9. CARRY-OVER USES THE REMAINING LEASE ---------------------------------------------------------------------

func TestReconcile_CarryOverUsesRemainingLeaseNotOriginal(t *testing.T) {
	k := newFakeKernel(t)
	k.legacyJulyTable("br-g90|10.20.0.60")
	k.timeouts["br-g90|10.20.0.60"] = 7 // 7 seconds LEFT of a 3600s lease
	a := newTestApplier(t, k)

	converge(t, a, "boot")

	if got := k.timeouts["br-g90|10.20.0.60"]; got != 7 {
		t.Fatalf("carried the element with %ds; the remaining lease was 7s — an authorization was extended", got)
	}
}

// LEGACY scd AUTHORIZATIONS ARE PERMANENT ELEMENTS — no timeout, no expiry. They must survive a converge, or
// the upgrade that installs the Phase-3 structure would knock every legacy guest offline.
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
