package nft

// CONTRACT TESTS for the packet-authorization commands.
//
// The orchestration tests in cmd/netd prove ordering with a fake kernel. These prove the other half: that the
// exact `nft` command strings this package builds are the ones intended — that a Phase-3 authorization names
// the Phase-3 set and never auth_ipv4, that a revoke deletes the right element, and that membership is read
// from the right set. A fake kernel cannot catch a command that names the wrong set; this can.
//
// These are COMMAND-STRING tests, not live evidence: no nft binary runs.

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

type recordingNft struct {
	mu   sync.Mutex
	cmds []string
	// listJSON is what `-j list set` returns, per set name.
	listJSON map[string]string
	// typeOut is what the plain `list set` (type probe) returns, per set name.
	typeOut map[string]string
}

func newRecordingNft() *recordingNft {
	return &recordingNft{listJSON: map[string]string{}, typeOut: map[string]string{}}
}

func (r *recordingNft) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, line)
	set := ""
	for _, a := range args {
		if a == AuthV4 || a == Phase3AuthV4 {
			set = a
		}
	}
	if len(args) > 0 && args[0] == "-j" {
		if j, ok := r.listJSON[set]; ok {
			return []byte(j), nil
		}
		return []byte(`{"nftables":[]}`), nil
	}
	if len(args) > 0 && args[0] == "list" {
		return []byte(r.typeOut[set]), nil
	}
	return nil, nil
}

func (r *recordingNft) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.cmds...)
}

func (r *recordingNft) has(sub string) bool {
	for _, c := range r.all() {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func testClient() (*Client, *recordingNft) {
	rr := newRecordingNft()
	return &Client{NftPath: "nft", exec: rr.run, concatBySet: map[string]bool{}}, rr
}

var gIP = net.ParseIP("10.0.0.5")

// A Phase-3 authorization names the PHASE-3 set — never the legacy one. This is the single most important
// property in the two-set design: if it named auth_ipv4, Phase-3 reconciliation could delete legacy guests.
func TestPhase3AllowNamesOnlyThePhase3Set(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"

	if err := c.AllowIn(context.Background(), Phase3AuthV4, "br-guest", gIP, 0); err != nil {
		t.Fatal(err)
	}
	if !rr.has("add element inet stayconnect " + Phase3AuthV4) {
		t.Fatalf("the authorization did not target the Phase-3 set: %v", rr.all())
	}
	for _, cmd := range rr.all() {
		if strings.Contains(cmd, " "+AuthV4) {
			t.Fatalf("a Phase-3 authorization named the LEGACY set: %s", cmd)
		}
	}
	// concatenated key, as the rendered set declares it
	if !rr.has(`"br-guest" . 10.0.0.5`) {
		t.Fatalf("the element is not the (bridge, ip) concatenation: %v", rr.all())
	}
}

// The legacy wrappers still target auth_ipv4, so scd's behaviour is unchanged by the Phase-3 work.
func TestLegacyAllowStillNamesTheLegacySet(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[AuthV4] = "set auth_ipv4 { type ifname . ipv4_addr }"

	if err := c.Allow(context.Background(), "br-lan", gIP, 0); err != nil {
		t.Fatal(err)
	}
	if !rr.has("add element inet stayconnect " + AuthV4) {
		t.Fatalf("legacy Allow no longer targets auth_ipv4: %v", rr.all())
	}
	for _, cmd := range rr.all() {
		if strings.Contains(cmd, Phase3AuthV4) {
			t.Fatalf("legacy Allow touched the Phase-3 set: %s", cmd)
		}
	}
}

// A revoke deletes the element from the Phase-3 set, and only after finding it there.
func TestPhase3DenyDeletesFromThePhase3Set(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"
	rr.listJSON[Phase3AuthV4] = `{"nftables":[{"set":{"name":"phase3_auth_ipv4","elem":[
		{"elem":{"val":{"concat":["br-guest","10.0.0.5"]}}}]}}]}`

	if err := c.DenyIn(context.Background(), Phase3AuthV4, "br-guest", gIP); err != nil {
		t.Fatal(err)
	}
	if !rr.has("delete element inet stayconnect " + Phase3AuthV4) {
		t.Fatalf("no delete against the Phase-3 set: %v", rr.all())
	}
	for _, cmd := range rr.all() {
		if strings.Contains(cmd, " "+AuthV4) {
			t.Fatalf("a Phase-3 revoke named the LEGACY set: %s", cmd)
		}
	}
}

// Revoking something that is not there is a no-op, not an error: fail-closed cleanup runs on paths where the
// element may never have been added, and it must not report a false failure.
func TestPhase3DenyAbsentElementIsNoOp(t *testing.T) {
	c, rr := testClient()
	rr.listJSON[Phase3AuthV4] = `{"nftables":[{"set":{"name":"phase3_auth_ipv4","elem":[]}}]}`
	if err := c.DenyIn(context.Background(), Phase3AuthV4, "br-guest", gIP); err != nil {
		t.Fatalf("revoking an absent element reported an error: %v", err)
	}
	if rr.has("delete element") {
		t.Fatal("a delete was issued for an element that was not present")
	}
}

// Membership is read from the named set — the verification half of the gate.
func TestAuthorizedInReadsTheNamedSet(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"
	rr.listJSON[Phase3AuthV4] = `{"nftables":[{"set":{"name":"phase3_auth_ipv4","elem":[
		{"elem":{"val":{"concat":["br-guest","10.0.0.5"]}}}]}}]}`

	on, err := c.AuthorizedIn(context.Background(), Phase3AuthV4, "br-guest", gIP)
	if err != nil {
		t.Fatal(err)
	}
	if !on {
		t.Fatal("an installed authorization was not detected")
	}
	// and an address that is not in the set reads as unauthorized
	off, err := c.AuthorizedIn(context.Background(), Phase3AuthV4, "br-guest", net.ParseIP("10.0.0.9"))
	if err != nil {
		t.Fatal(err)
	}
	if off {
		t.Fatal("an absent address read as authorized")
	}
}

// ListIn enumerates only the named set, which is what makes Phase-3 stray removal unable to reach a legacy
// authorization even in principle.
func TestListInEnumeratesOnlyTheNamedSet(t *testing.T) {
	c, rr := testClient()
	rr.listJSON[Phase3AuthV4] = `{"nftables":[{"set":{"name":"phase3_auth_ipv4","elem":[
		{"elem":{"val":{"concat":["br-guest","10.0.0.5"]}}}]}}]}`
	rr.listJSON[AuthV4] = `{"nftables":[{"set":{"name":"auth_ipv4","elem":[
		{"elem":{"val":{"concat":["br-lan","10.0.0.200"]}}}]}}]}`

	els, err := c.ListIn(context.Background(), Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 1 || els[0].IP.String() != "10.0.0.5" {
		t.Fatalf("the Phase-3 listing is wrong: %+v", els)
	}
	for _, e := range els {
		if e.IP.String() == "10.0.0.200" {
			t.Fatal("the Phase-3 listing returned a LEGACY authorization")
		}
	}
}

// The concat probe is PER SET. One shared flag would let one set's key type decide the other's element
// format, which would silently build malformed elements for whichever set was probed second.
func TestConcatProbeIsPerSet(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[AuthV4] = "set auth_ipv4 { type ipv4_addr }"                       // IP-only (older ruleset)
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }" // concatenated

	if err := c.Allow(context.Background(), "br-lan", gIP, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.AllowIn(context.Background(), Phase3AuthV4, "br-guest", gIP, 0); err != nil {
		t.Fatal(err)
	}
	var legacyCmd, phase3Cmd string
	for _, cmd := range rr.all() {
		if strings.Contains(cmd, "add element") && strings.Contains(cmd, AuthV4) {
			legacyCmd = cmd
		}
		if strings.Contains(cmd, "add element") && strings.Contains(cmd, Phase3AuthV4) {
			phase3Cmd = cmd
		}
	}
	if strings.Contains(legacyCmd, `"br-lan" .`) {
		t.Fatalf("an IP-only legacy set got a concatenated element: %s", legacyCmd)
	}
	if !strings.Contains(phase3Cmd, `"br-guest" .`) {
		t.Fatalf("a concatenated Phase-3 set got a bare-IP element: %s", phase3Cmd)
	}
}

// A TTL becomes a per-element timeout, so an authorization can expire in the kernel as well as in the DB.
func TestAllowInCarriesTheTimeout(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"
	if err := c.AllowIn(context.Background(), Phase3AuthV4, "br-guest", gIP, 90e9); err != nil {
		t.Fatal(err)
	}
	if !rr.has("timeout 90s") {
		t.Fatalf("the element carries no timeout: %v", rr.all())
	}
}
