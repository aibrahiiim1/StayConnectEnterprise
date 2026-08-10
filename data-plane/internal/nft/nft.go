// Package nft is a thin wrapper over the `nft` command for managing the
// stayconnect auth set. All operations are idempotent.
//
// Phase 19: the auth set may be either the legacy IP-only form
// (`type ipv4_addr`) or the multi-network concatenated form
// (`type ifname . ipv4_addr`). The wrapper probes the live set once and
// formats elements accordingly, so scd works across the netd cutover without
// a coordinated restart. Callers always pass the ingress interface; it is
// ignored on an IP-only set.
package nft

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"
)

const (
	Table  = "inet stayconnect"
	AuthV4 = "auth_ipv4"
	// Phase3AuthV4 is the SEPARATE packet-authorization set that Phase 3 owns.
	//
	// It is deliberately NOT auth_ipv4. Legacy scd authorizes guests by adding to auth_ipv4 and reconciles that
	// set from public.sessions; Phase 3 authorizes through netd from iam_v2.sessions. If both wrote one set, a
	// Phase-3 full-state reconciliation — which must remove entries no Phase-3 session claims — would delete
	// live legacy authorizations, cutting off guests that Phase 3 knows nothing about. Two structurally
	// distinct sets make that impossible by construction rather than by careful filtering: Phase-3
	// reconciliation enumerates only this set, and legacy code never names it.
	//
	// The set exists in the ruleset from the start, EMPTY, whether or not Phase 3 is enabled. An empty set
	// matches nothing, so a dark appliance behaves exactly as it did before. It is present-but-empty rather
	// than added at cutover because installing it later would mean regenerating the ruleset, and the generated
	// ruleset is applied as `delete table` + recreate — which would flush auth_ipv4 and drop every live legacy
	// guest. Shipping the set from the start is what makes Phase-3 activation a flag flip with no packet
	// interruption, and gives the cutover exactly one boundary.
	Phase3AuthV4 = "phase3_auth_ipv4"
)

type Client struct {
	NftPath string

	// exec runs one nft invocation. It is a field so the exact command strings this package builds can be
	// proven by contract tests without a kernel — which is the only honest way to test a packet-authorization
	// gate in CI.
	exec func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu sync.Mutex
	// concatBySet caches the probed key type PER SET. auth_ipv4 and phase3_auth_ipv4 are independent sets and
	// a single shared flag would let one set's probe decide the other's element format.
	concatBySet map[string]bool
}

func realExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func New() *Client {
	return &Client{NftPath: "/usr/sbin/nft", exec: realExec, concatBySet: map[string]bool{}}
}

// probeConcatIn detects a set's key type once per set (cached). Best-effort: on any error it assumes IP-only
// (the historical default).
func (c *Client) probeConcatIn(ctx context.Context, set string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.concatBySet == nil {
		c.concatBySet = map[string]bool{}
	}
	if v, ok := c.concatBySet[set]; ok {
		return v
	}
	concat := false
	out, err := c.execOrReal(ctx, c.NftPath, "list", "set", "inet", "stayconnect", set)
	if err == nil && containsConcatType(string(out)) {
		concat = true
	}
	c.concatBySet[set] = concat
	return concat
}

// execOrReal tolerates a zero-value Client (callers that built one with &Client{NftPath: ...}).
func (c *Client) execOrReal(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.exec != nil {
		return c.exec(ctx, name, args...)
	}
	return realExec(ctx, name, args...)
}

func (c *Client) probeConcat(ctx context.Context) bool { return c.probeConcatIn(ctx, AuthV4) }

func containsConcatType(s string) bool {
	// "type ifname . ipv4_addr"
	return indexOf(s, "ifname . ipv4_addr") >= 0 || indexOf(s, "ipv4_addr . ifname") >= 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Allow adds (iface, ip) to the LEGACY auth_ipv4 set. Zero timeout = no timeout.
// On an IP-only set, iface is ignored. Legacy scd's behaviour is unchanged.
func (c *Client) Allow(ctx context.Context, iface string, ip net.IP, ttl time.Duration) error {
	return c.AllowIn(ctx, AuthV4, iface, ip, ttl)
}

// Deny removes (iface, ip) from the LEGACY auth_ipv4 set (no-op if absent).
func (c *Client) Deny(ctx context.Context, iface string, ip net.IP) error {
	return c.DenyIn(ctx, AuthV4, iface, ip)
}

// AllowIn authorizes (iface, ip) for internet forwarding in the NAMED set.
//
// This is the real packet-authorization gate: the forward chain accepts a guest's traffic only when its
// (bridge, IP) is in an authorization set, and the captive-portal DNAT rules redirect it when it is not.
// Adding an element here is what actually gives a guest the internet.
//
// Legacy scd calls this through Allow with ttl=0 (no timeout), which is its historical behaviour and is not
// changed here. Phase 3 does NOT use it: see LeaseIn.
func (c *Client) AllowIn(ctx context.Context, set, iface string, ip net.IP, ttl time.Duration) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("only IPv4 supported: %s", ip)
	}
	elem := c.elementIn(ctx, set, iface, ip4, ttl)
	return c.run(ctx, "add", "element", "inet", "stayconnect", set, "{", elem, "}")
}

// ErrUnboundedLease is returned when a bounded authorization is asked to be permanent.
var ErrUnboundedLease = fmt.Errorf("nft: packet authorization must be a bounded lease (ttl > 0)")

// LeaseIn asserts a BOUNDED, RENEWABLE authorization for (iface, ip) in the named set.
//
// A permanent element is a promise no daemon can keep. If the process that maintains authorization dies, hangs
// or is partitioned, a non-expiring element keeps forwarding that guest's traffic forever — past their
// entitlement window, past checkout, past a revocation nobody was alive to apply. The kernel's own timeout is
// the only mechanism that fails closed without anything running, so Phase-3 authorization is always a lease
// and renewal is the ONLY thing that keeps a guest online.
//
// It is idempotent in the way that matters: calling it for an absent element creates the lease, and calling it
// for a present one REFRESHES the remaining time.
//
// Refreshing is deliberately not `add element` again. nftables does not restart an existing element's timer on
// a repeated add — the element is already in the set, so the add is a no-op and the original expiry stands. A
// renewal loop built on `add` would look healthy and still drop every guest at the first lease boundary. The
// refresh is therefore a delete+add issued as ONE nft command buffer, which nft commits as a single netlink
// transaction: either both take or neither does, so there is no instant in which the guest is unauthorized.
func (c *Client) LeaseIn(ctx context.Context, set, iface string, ip net.IP, ttl time.Duration) error {
	if ttl <= 0 {
		return ErrUnboundedLease
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("only IPv4 supported: %s", ip)
	}
	key := c.keyIn(ctx, set, iface, ip4)
	elem := fmt.Sprintf("%s timeout %ds", key, leaseSeconds(ttl))
	present, err := c.AuthorizedIn(ctx, set, iface, ip4)
	if err != nil {
		return err
	}
	if !present {
		return c.run(ctx, "add", "element", "inet", "stayconnect", set, "{", elem, "}")
	}
	return c.run(ctx, fmt.Sprintf("delete element inet stayconnect %s { %s } ; add element inet stayconnect %s { %s }",
		set, key, set, elem))
}

// leaseSeconds rounds a lease UP to whole seconds, because nft's element timeout granularity is seconds and
// rounding down would hand back a lease shorter than the caller asked for — including, for a sub-second
// remainder, a lease of zero, which nft reads as "no timeout": permanent access from a bound that was meant
// to be about to expire.
func leaseSeconds(ttl time.Duration) int {
	s := int(ttl / time.Second)
	if ttl%time.Second != 0 {
		s++
	}
	if s < 1 {
		s = 1
	}
	return s
}

// keyIn renders the element KEY (no timeout) for a set, in that set's own key format.
func (c *Client) keyIn(ctx context.Context, set, iface string, ip net.IP) string {
	if c.probeConcatIn(ctx, set) && iface != "" {
		return fmt.Sprintf("%q . %s", iface, ip.String())
	}
	return ip.String()
}

// DenyIn removes (iface, ip) from the NAMED set (no-op if absent), revoking internet forwarding.
func (c *Client) DenyIn(ctx context.Context, set, iface string, ip net.IP) error {
	ip4 := ip.To4()
	if ip4 == nil {
		ip4 = ip
	}
	// delete fails if absent → probe membership first.
	listed, err := c.ListIn(ctx, set)
	if err != nil {
		return err
	}
	concat := c.probeConcatIn(ctx, set)
	for _, e := range listed {
		if e.IP.Equal(ip4) && (!concat || iface == "" || e.Iface == iface) {
			key := ip4.String()
			if concat && (iface != "" || e.Iface != "") {
				useIf := iface
				if useIf == "" {
					useIf = e.Iface
				}
				key = fmt.Sprintf("%q . %s", useIf, ip4.String())
			}
			return c.run(ctx, "delete", "element", "inet", "stayconnect", set, "{", key, "}")
		}
	}
	return nil
}

// AuthorizedIn reports whether (iface, ip) is CURRENTLY authorized in the named set. It is the verification
// half of the gate: an Allow that returned nil but did not take is indistinguishable from success otherwise.
func (c *Client) AuthorizedIn(ctx context.Context, set, iface string, ip net.IP) (bool, error) {
	ip4 := ip.To4()
	if ip4 == nil {
		ip4 = ip
	}
	listed, err := c.ListIn(ctx, set)
	if err != nil {
		return false, err
	}
	concat := c.probeConcatIn(ctx, set)
	for _, e := range listed {
		if e.IP.Equal(ip4) && (!concat || iface == "" || e.Iface == "" || e.Iface == iface) {
			return true, nil
		}
	}
	return false, nil
}

func (c *Client) elementIn(ctx context.Context, set, iface string, ip net.IP, ttl time.Duration) string {
	base := c.keyIn(ctx, set, iface, ip)
	if ttl > 0 {
		return fmt.Sprintf("%s timeout %ds", base, leaseSeconds(ttl))
	}
	return base
}

type Element struct {
	Iface string
	IP    net.IP
	// Timeout is the lease the element was created with; Expires is how much of it is LEFT. Renewal decisions
	// need the remainder, not the original: an element created with a 90s lease 89 seconds ago is one second
	// from cutting its guest off, and only Expires says so.
	Timeout time.Duration
	Expires time.Duration
}

func (c *Client) List(ctx context.Context) ([]Element, error) { return c.ListIn(ctx, AuthV4) }

// ListIn enumerates the named authorization set. Phase-3 reconciliation uses it to find strays — entries the
// desired state does not claim — and it only ever names the Phase-3 set, so it can never enumerate (and
// therefore never remove) a legacy authorization.
func (c *Client) ListIn(ctx context.Context, set string) ([]Element, error) {
	out, err := c.execOrReal(ctx, c.NftPath, "-j", "list", "set", "inet", "stayconnect", set)
	if err != nil {
		return nil, fmt.Errorf("nft list %s: %w", set, err)
	}
	return parseJSON(out)
}

func (c *Client) run(ctx context.Context, args ...string) error {
	out, err := c.execOrReal(ctx, c.NftPath, args...)
	if err != nil {
		return fmt.Errorf("nft %v: %w — %s", args, err, string(out))
	}
	return nil
}
