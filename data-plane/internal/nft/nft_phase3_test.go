package nft

// Contract tests for the packet-authorization commands: no nft binary runs, they prove the exact command strings (a
// fake kernel cannot catch a command naming the wrong set).

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
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

// A Phase-3 authorization names the Phase-3 set, never the legacy one; otherwise reconciliation could delete legacy guests.
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

// Revoking something absent is a no-op, not an error: cleanup runs on paths where the element may never have been added.
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

// Membership is read from the named set.
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
	off, err := c.AuthorizedIn(context.Background(), Phase3AuthV4, "br-guest", net.ParseIP("10.0.0.9"))
	if err != nil {
		t.Fatal(err)
	}
	if off {
		t.Fatal("an absent address read as authorized")
	}
}

// ListIn enumerates only the named set, so Phase-3 stray removal cannot reach a legacy authorization.
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

// The concat probe is per set: one shared flag would build malformed elements for whichever set is probed second.
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

// A Phase-3 lease is always installed with a timeout; an element with none survives every process that maintains it.
func TestPhase3LeaseAlwaysCarriesATimeout(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"

	if err := c.LeaseIn(context.Background(), Phase3AuthV4, "br-guest", gIP, 90*time.Second); err != nil {
		t.Fatal(err)
	}
	if !rr.has(`add element inet stayconnect phase3_auth_ipv4 { "br-guest" . 10.0.0.5 timeout 90s }`) {
		t.Fatalf("the lease was not installed with its timeout: %v", rr.all())
	}
}

// A zero or negative lease is refused: nft reads "no timeout" as permanent.
func TestPhase3LeaseRefusesAnUnboundedAuthorization(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"

	for _, ttl := range []time.Duration{0, -time.Second} {
		if err := c.LeaseIn(context.Background(), Phase3AuthV4, "br-guest", gIP, ttl); err == nil {
			t.Fatalf("a ttl of %v was accepted; that installs a PERMANENT authorization", ttl)
		}
	}
	if len(rr.all()) != 0 {
		t.Fatalf("a refused lease still issued commands: %v", rr.all())
	}
}

// A sub-second lease is refused, not rounded up: nft timeouts are whole seconds, so 300ms would install as 1s and
// exceed a hard access boundary; `timeout 0s` means no timeout.
func TestPhase3LeaseRefusesAnUnrepresentableSubSecondLease(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"

	for _, ttl := range []time.Duration{999 * time.Millisecond, 300 * time.Millisecond, time.Millisecond} {
		err := c.LeaseIn(context.Background(), Phase3AuthV4, "br-guest", gIP, ttl)
		if err == nil {
			t.Fatalf("a %v lease was accepted; the only representable form overshoots the caller's bound", ttl)
		}
		if !errors.Is(err, ErrLeaseTooShort) {
			t.Fatalf("a %v lease failed for the wrong reason: %v", ttl, err)
		}
	}
	if len(rr.all()) != 0 {
		t.Fatalf("a refused lease still issued commands: %v", rr.all())
	}
}

// A lease is never rounded up: the installed timeout must never be later than requested, at any timestamp precision.
func TestPhase3LeaseNeverInstallsATimeoutLongerThanRequested(t *testing.T) {
	cases := []struct {
		ttl  time.Duration
		want string
	}{
		{90 * time.Second, "timeout 90s"},
		{15 * time.Second, "timeout 15s"},
		{1900 * time.Millisecond, "timeout 1s"},
		{1100 * time.Millisecond, "timeout 1s"},
		{time.Second, "timeout 1s"},
		{59900 * time.Millisecond, "timeout 59s"},
	}
	for _, tc := range cases {
		c, rr := testClient()
		rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"
		if err := c.LeaseIn(context.Background(), Phase3AuthV4, "br-guest", gIP, tc.ttl); err != nil {
			t.Fatalf("%v: %v", tc.ttl, err)
		}
		if !rr.has(tc.want) {
			t.Fatalf("a %v lease was not installed as %q: %v", tc.ttl, tc.want, rr.all())
		}
		// and the installed seconds must never exceed the requested duration
		var secs int
		for _, cmd := range rr.all() {
			if i := strings.Index(cmd, "timeout "); i >= 0 {
				fmt.Sscanf(cmd[i:], "timeout %ds", &secs)
			}
		}
		if time.Duration(secs)*time.Second > tc.ttl {
			t.Fatalf("a %v lease installed timeout %ds — %v PAST the caller's bound", tc.ttl, secs,
				time.Duration(secs)*time.Second-tc.ttl)
		}
	}
}

// Renewal is not a repeated add: nftables does not restart an existing element's timer, so the refresh is a
// delete+add in one command buffer (a single transaction).
func TestPhase3LeaseRenewalIsAnAtomicDeleteAndAdd(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"
	rr.listJSON[Phase3AuthV4] = `{"nftables":[{"set":{"name":"phase3_auth_ipv4","elem":[
		{"elem":{"val":{"concat":["br-guest","10.0.0.5"]},"timeout":90,"expires":12}}]}}]}`

	if err := c.LeaseIn(context.Background(), Phase3AuthV4, "br-guest", gIP, 90*time.Second); err != nil {
		t.Fatal(err)
	}
	var refresh string
	for _, cmd := range rr.all() {
		if strings.Contains(cmd, "delete element") {
			refresh = cmd
		}
	}
	if refresh == "" {
		t.Fatalf("renewing an existing element issued no delete: %v", rr.all())
	}
	if !strings.Contains(refresh, "delete element inet stayconnect phase3_auth_ipv4") ||
		!strings.Contains(refresh, "add element inet stayconnect phase3_auth_ipv4") {
		t.Fatalf("the refresh is not a delete+add: %q", refresh)
	}
	if strings.Count(refresh, ";") != 1 {
		t.Fatalf("the refresh was not issued as ONE command buffer, so the guest is unauthorized between the "+
			"two commands: %q", refresh)
	}
	if !strings.Contains(refresh, "timeout 90s") {
		t.Fatalf("the refreshed element carries no new lease: %q", refresh)
	}
	if strings.Contains(refresh, " "+AuthV4+" ") {
		t.Fatalf("a Phase-3 renewal named the LEGACY set: %q", refresh)
	}
}

// The remaining lease is what a renewal decision reads; the original timeout says nothing about imminent expiry.
func TestPhase3ListReportsRemainingLease(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[Phase3AuthV4] = "set phase3_auth_ipv4 { type ifname . ipv4_addr }"
	rr.listJSON[Phase3AuthV4] = `{"nftables":[{"set":{"name":"phase3_auth_ipv4","elem":[
		{"elem":{"val":{"concat":["br-guest","10.0.0.5"]},"timeout":90,"expires":7}}]}}]}`

	els, err := c.ListIn(context.Background(), Phase3AuthV4)
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 1 {
		t.Fatalf("want one element, got %d", len(els))
	}
	if els[0].Timeout != 90*time.Second {
		t.Fatalf("timeout = %v, want 90s", els[0].Timeout)
	}
	if els[0].Expires != 7*time.Second {
		t.Fatalf("expires = %v, want 7s — a renewal decision made on the timeout instead of the remainder "+
			"would never refresh anything", els[0].Expires)
	}
}

// Legacy behaviour is untouched: scd still installs permanent elements in auth_ipv4 through Allow.
func TestLegacyAllowIsUnchangedByTheLeaseWork(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[AuthV4] = "set auth_ipv4 { type ifname . ipv4_addr }"

	if err := c.Allow(context.Background(), "br-lan", gIP, 0); err != nil {
		t.Fatal(err)
	}
	if !rr.has("add element inet stayconnect "+AuthV4) || rr.has("timeout") {
		t.Fatalf("legacy authorization changed shape: %v", rr.all())
	}
	for _, cmd := range rr.all() {
		if strings.Contains(cmd, Phase3AuthV4) {
			t.Fatalf("a legacy authorization named the Phase-3 set: %s", cmd)
		}
	}
}

// A permanent concatenated element carries no `val` wrapper in nft's JSON (it has no stateful attributes). Reading
// only the wrapped shape made every permanent legacy authorization invisible to List, Authorized and Deny.
func TestListReadsPermanentConcatElementsWithNoValWrapper(t *testing.T) {
	c, rr := testClient()
	rr.typeOut[AuthV4] = "set auth_ipv4 { type ifname . ipv4_addr }"
	rr.listJSON[AuthV4] = `{"nftables":[{"set":{"name":"auth_ipv4","elem":[
		{"concat":["br-guest","10.20.0.14"]},
		{"elem":{"val":{"concat":["br-guest","10.20.0.7"]},"timeout":3600,"expires":1200}}]}}]}`

	els, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(els) != 2 {
		t.Fatalf("read %d of 2 elements; a permanent element with no `val` wrapper was dropped: %+v", len(els), els)
	}
	byIP := map[string]Element{}
	for _, e := range els {
		byIP[e.IP.String()] = e
	}
	perm, ok := byIP["10.20.0.14"]
	if !ok || perm.Iface != "br-guest" {
		t.Fatalf("the permanent element did not round-trip: %+v", byIP)
	}
	if perm.Timeout != 0 || perm.Expires != 0 {
		t.Fatalf("a permanent element reported a lease: %+v", perm)
	}
	if ok, err := c.AuthorizedIn(context.Background(), AuthV4, "br-guest", net.ParseIP("10.20.0.14")); err != nil || !ok {
		t.Fatalf("a permanent element is not visible to Authorized (ok=%v err=%v)", ok, err)
	}
}
