package shape

// EnsureBridgeInfra builds everything the enforcement plane needs, idempotently, on every submit pass, so
// a boot with zero tc state is rebuilt by the first pass with no priming unit. These cases pin that at the
// command level.

import (
	"context"
	"strings"
	"testing"
)

// With nothing existing (no IFB, root qdisc, ingress or redirect), one call must create all of it.
func TestFromZero_EnsureBridgeInfraBuildsEverythingNeeded(t *testing.T) {
	c, rec := newTestClient()
	if err := c.EnsureBridgeInfra(context.Background(), "br-g-00d1fa1a"); err != nil {
		t.Fatalf("building infrastructure from zero state failed: %v", err)
	}

	for _, want := range []string{
		"link add ifb-g-00d1fa1a type ifb",
		"link set ifb-g-00d1fa1a up",
		"qdisc add dev ifb-g-00d1fa1a root handle 1: htb",
		"qdisc add dev br-g-00d1fa1a root handle 1: htb",
		"qdisc add dev br-g-00d1fa1a handle ffff: ingress",
		"mirred egress redirect dev ifb-g-00d1fa1a",
	} {
		if !rec.has(want) {
			t.Fatalf("from zero state, netd never issued %q\nissued:\n  %s", want, strings.Join(rec.cmds, "\n  "))
		}
	}
}

// It must never delete a root qdisc: that takes every live per-session and SHARED class with it. Only the
// ingress qdisc (whose filters netd owns) and per-class leaves are deleted.
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

// The second call must not re-issue link/qdisc creation: acctd submits about once a second, and churning
// kernel state each pass would reset counters.
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

// The bridge name is a parameter (learned per session from sessions.ingress_interface), never a constant.
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
