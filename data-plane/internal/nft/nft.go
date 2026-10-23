// Package nft is a thin wrapper over the `nft` command for managing the stayconnect auth sets; all operations are
// idempotent. An auth set may be IP-only (`type ipv4_addr`) or concatenated (`type ifname . ipv4_addr`); the wrapper
// probes the live set once and formats elements accordingly, so scd works across the netd cutover. The ingress
// interface is ignored on an IP-only set.
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
	// Phase3AuthV4 is the separate packet-authorization set Phase 3 owns, deliberately not auth_ipv4: a Phase-3
	// full-state reconciliation would otherwise delete live legacy authorizations. It exists in the ruleset from the
	// start, empty (it matches nothing), because adding it later means regenerating the ruleset by `delete table` +
	// recreate, which would drop every live legacy guest.
	Phase3AuthV4 = "phase3_auth_ipv4"
)

type Client struct {
	NftPath string

	// exec runs one nft invocation; a field so tests can prove the exact command strings without a kernel.
	exec func(ctx context.Context, name string, args ...string) ([]byte, error)

	mu sync.Mutex
	// concatBySet caches the probed key type per set: auth_ipv4 and phase3_auth_ipv4 are independent, so one shared flag
	// would let one probe decide the other's element format.
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

func (c *Client) execOrReal(ctx context.Context, name string, args ...string) ([]byte, error) {
	if c.exec != nil {
		return c.exec(ctx, name, args...)
	}
	return realExec(ctx, name, args...)
}

func (c *Client) probeConcat(ctx context.Context) bool { return c.probeConcatIn(ctx, AuthV4) }

func containsConcatType(s string) bool {
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

// Allow adds (iface, ip) to the legacy auth_ipv4 set; zero timeout means none.
func (c *Client) Allow(ctx context.Context, iface string, ip net.IP, ttl time.Duration) error {
	return c.AllowIn(ctx, AuthV4, iface, ip, ttl)
}

func (c *Client) Deny(ctx context.Context, iface string, ip net.IP) error {
	return c.DenyIn(ctx, AuthV4, iface, ip)
}

// AllowIn authorizes (iface, ip) for internet forwarding in the named set: the forward chain accepts a guest only
// when its (bridge, IP) is in an authorization set. Legacy scd reaches it through Allow with ttl=0; Phase 3 uses LeaseIn.
func (c *Client) AllowIn(ctx context.Context, set, iface string, ip net.IP, ttl time.Duration) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("only IPv4 supported: %s", ip)
	}
	elem := c.elementIn(ctx, set, iface, ip4, ttl)
	return c.run(ctx, "add", "element", "inet", "stayconnect", set, "{", elem, "}")
}

var ErrUnboundedLease = fmt.Errorf("nft: packet authorization must be a bounded lease (ttl > 0)")

// LeaseIn asserts a bounded, renewable authorization in the named set. A permanent element is a promise no daemon
// can keep: if the maintaining process dies, a non-expiring element forwards traffic past entitlement and checkout.
// The kernel timeout fails closed with nothing running, so renewal is the only thing keeping a guest online.
// Refreshing is a delete+add in ONE nft command buffer (a single netlink transaction, so the guest is never
// unauthorized), not a repeated `add element`: nftables does not restart an existing element's timer on a repeated
// add, so such a loop would look healthy yet drop every guest at the first lease boundary.
func (c *Client) LeaseIn(ctx context.Context, set, iface string, ip net.IP, ttl time.Duration) error {
	if ttl <= 0 {
		return ErrUnboundedLease
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("only IPv4 supported: %s", ip)
	}
	secs, ok := leaseSeconds(ttl)
	if !ok {
		return ErrLeaseTooShort
	}
	key := c.keyIn(ctx, set, iface, ip4)
	elem := fmt.Sprintf("%s timeout %ds", key, secs)
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

// ErrLeaseTooShort is returned when a lease is shorter than the kernel can represent. nft timeouts are whole seconds,
// so a 200ms lease could only be installed as 1s, overshooting a guest's hard access boundary; it is refused rather
// than rounded up.
var ErrLeaseTooShort = fmt.Errorf("nft: lease is shorter than the kernel's one-second timeout granularity")

// leaseSeconds converts a lease to whole seconds without ever rounding up: the installed kernel expiry must never be
// later than requested. Below one second there is no representable lease (`timeout 0s` means no timeout in nft and
// would make an expiring authorization permanent), and the second return value says so.
func leaseSeconds(ttl time.Duration) (int, bool) {
	s := int(ttl / time.Second)
	if s < 1 {
		return 0, false
	}
	return s, true
}

func (c *Client) keyIn(ctx context.Context, set, iface string, ip net.IP) string {
	if c.probeConcatIn(ctx, set) && iface != "" {
		return fmt.Sprintf("%q . %s", iface, ip.String())
	}
	return ip.String()
}

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

// AuthorizedIn reports whether (iface, ip) is currently authorized in the named set; the verification half of the
// gate, since an Allow that returned nil but did not take looks like success.
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
	if secs, ok := leaseSeconds(ttl); ok {
		return fmt.Sprintf("%s timeout %ds", base, secs)
	}
	// ttl <= 0 or a sub-second ttl: AllowIn is the legacy surface and keeps its "no timeout" behaviour; the bounded
	// surface is LeaseIn, which refuses an unrepresentable lease.
	return base
}

type Element struct {
	Iface string
	IP    net.IP
	// Port is the service part of an `ipv4_addr . inet_service` element (port-restricted garden sets); 0 otherwise.
	Port int
	// Timeout is the lease the element was created with; Expires is how much is left, which is what renewal decisions need.
	Timeout time.Duration
	Expires time.Duration
}

func (c *Client) List(ctx context.Context) ([]Element, error) { return c.ListIn(ctx, AuthV4) }

// ListIn enumerates the named authorization set. Phase-3 reconciliation names only the Phase-3 set, so it can never
// enumerate (or remove) a legacy authorization.
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
