package shape

// NETD REBUILDS THE GUEST SHAPING INFRASTRUCTURE FROM NOTHING. NO PRIMING UNIT IS INVOLVED.
//
// An oneshot unit, stayconnect-tc-setup, used to prime HTB roots at boot for a shaping model scd owned. It was
// retired because it had become three kinds of wrong at once: it primed ens160 (the WAN/management interface)
// and br-lan (a bridge that does not exist under the dynamic guest-network architecture), it described
// per-session classids as `1:<last octet>` when the current scheme is MinorForIP in 0x1000-0x1fff plus SHARED
// groups in 0x2000-0x2fff, and its prime() opened with `tc qdisc del dev <IF> root` — which, pointed at a real
// guest bridge, would delete the netd-owned root and every live class beneath it.
//
// What replaced it is not a different unit. It is this: EnsureBridgeInfra builds everything the enforcement
// plane needs, idempotently, on every submit pass — so a boot with ZERO tc state is fully reconstructed by the
// first pass, with nothing scheduled and nothing to fail 203/EXEC.
//
// These cases pin that contract at the command level, which is where the ownership boundary actually lives.

import (
	"context"
	"strings"
	"testing"
)

// FROM ZERO. Nothing exists: no IFB, no root qdisc, no ingress, no redirect. One call must create all of it.
func TestFromZero_EnsureBridgeInfraBuildsEverythingNeeded(t *testing.T) {
	c, rec := newTestClient()
	if err := c.EnsureBridgeInfra(context.Background(), "br-g-00d1fa1a"); err != nil {
		t.Fatalf("building infrastructure from zero state failed: %v", err)
	}

	for _, want := range []string{
		// the IFB device that makes upload measurable, and its own root
		"link add ifb-g-00d1fa1a type ifb",
		"link set ifb-g-00d1fa1a up",
		"qdisc add dev ifb-g-00d1fa1a root handle 1: htb",
		// the bridge egress root that download classes hang off — THE thing tc-setup used to claim to prime
		"qdisc add dev br-g-00d1fa1a root handle 1: htb",
		// and the ingress redirect that feeds the IFB
		"qdisc add dev br-g-00d1fa1a handle ffff: ingress",
		"mirred egress redirect dev ifb-g-00d1fa1a",
	} {
		if !rec.has(want) {
			t.Fatalf("from zero state, netd never issued %q\nissued:\n  %s", want, strings.Join(rec.cmds, "\n  "))
		}
	}
}

// AND IT NEVER DELETES A ROOT QDISC. This is the exact destructive act the retired script opened with, and the
// reason it could not be left installed: a root deletion takes every live per-session and SHARED class with
// it. netd deletes only the ingress qdisc (whose filters it fully owns and rebuilds) and per-class leaves.
func TestFromZero_InfrastructureSetupNeverDeletesARootQdisc(t *testing.T) {
	c, rec := newTestClient()
	if err := c.EnsureBridgeInfra(context.Background(), "br-g-00d1fa1a"); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range rec.cmds {
		if !strings.Contains(cmd, "qdisc del") {
			continue
		}
		// The one legitimate deletion: the ingress qdisc, so exactly one redirect filter exists afterwards.
		if strings.Contains(cmd, "ingress") {
			continue
		}
		if strings.Contains(cmd, "root") {
			t.Fatalf("infrastructure setup deleted a ROOT qdisc, which would destroy live classes: %q", cmd)
		}
	}
}

// IDEMPOTENT. The second call must not re-issue link/qdisc creation: acctd submits about once a second, and a
// setup path that churned kernel state on every pass would reset counters continuously.
func TestFromZero_SecondCallIsAQuietNoOp(t *testing.T) {
	c, rec := newTestClient()
	ctx := context.Background()
	if err := c.EnsureBridgeInfra(ctx, "br-g-00d1fa1a"); err != nil {
		t.Fatal(err)
	}
	first := len(rec.cmds)
	if err := c.EnsureBridgeInfra(ctx, "br-g-00d1fa1a"); err != nil {
		t.Fatal(err)
	}
	if len(rec.cmds) != first {
		t.Fatalf("the second call issued %d further command(s); infrastructure setup must be a no-op once ready:\n  %s",
			len(rec.cmds)-first, strings.Join(rec.cmds[first:], "\n  "))
	}
}

// THE BRIDGE NAME IS A PARAMETER, NOT A CONSTANT. The retired script hardcoded ens160 and br-lan; the current
// plane learns the interface per session from sessions.ingress_interface, which is why it survived a bridge
// being renamed from br-lan to a per-network br-g-<id> without anyone editing a script.
func TestFromZero_NoInterfaceNameIsHardcoded(t *testing.T) {
	c, rec := newTestClient()
	if err := c.EnsureBridgeInfra(context.Background(), "br-somewhere-else"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rec.cmds, "\n")
	for _, legacy := range []string{"ens160", "br-lan", "ens192"} {
		if strings.Contains(joined, legacy) {
			t.Fatalf("infrastructure setup named the legacy interface %q; topology must come from the caller", legacy)
		}
	}
	if !strings.Contains(joined, "br-somewhere-else") {
		t.Fatal("the bridge the caller asked for was never used")
	}
}
