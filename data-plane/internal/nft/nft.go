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
)

type Client struct {
	NftPath string

	mu     sync.Mutex
	probed bool
	concat bool // true when auth_ipv4 is `ifname . ipv4_addr`
}

func New() *Client {
	return &Client{NftPath: "/usr/sbin/nft"}
}

// probeConcat detects the auth_ipv4 key type once (cached). Best-effort: on any
// error it assumes IP-only (the historical default).
func (c *Client) probeConcat(ctx context.Context) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.probed {
		return c.concat
	}
	c.probed = true
	out, err := exec.CommandContext(ctx, c.NftPath, "list", "set", "inet", "stayconnect", AuthV4).Output()
	if err == nil && containsConcatType(string(out)) {
		c.concat = true
	}
	return c.concat
}

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

// Allow adds (iface, ip) to auth_ipv4 with a timeout. Zero timeout = no timeout.
// On an IP-only set, iface is ignored.
func (c *Client) Allow(ctx context.Context, iface string, ip net.IP, ttl time.Duration) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("only IPv4 supported: %s", ip)
	}
	elem := c.element(ctx, iface, ip4, ttl)
	return c.run(ctx, "add", "element", "inet", "stayconnect", AuthV4, "{", elem, "}")
}

// Deny removes (iface, ip) from auth_ipv4 (no-op if absent).
func (c *Client) Deny(ctx context.Context, iface string, ip net.IP) error {
	ip4 := ip.To4()
	if ip4 == nil {
		ip4 = ip
	}
	// delete fails if absent → probe membership first.
	listed, err := c.List(ctx)
	if err != nil {
		return err
	}
	concat := c.probeConcat(ctx)
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
			return c.run(ctx, "delete", "element", "inet", "stayconnect", AuthV4, "{", key, "}")
		}
	}
	return nil
}

func (c *Client) element(ctx context.Context, iface string, ip net.IP, ttl time.Duration) string {
	base := ip.String()
	if c.probeConcat(ctx) && iface != "" {
		base = fmt.Sprintf("%q . %s", iface, ip.String())
	}
	if ttl > 0 {
		return fmt.Sprintf("%s timeout %ds", base, int(ttl.Seconds()))
	}
	return base
}

type Element struct {
	Iface   string
	IP      net.IP
	Timeout time.Duration
}

func (c *Client) List(ctx context.Context) ([]Element, error) {
	out, err := exec.CommandContext(ctx, c.NftPath, "-j", "list", "set", "inet", "stayconnect", AuthV4).Output()
	if err != nil {
		return nil, fmt.Errorf("nft list: %w", err)
	}
	return parseJSON(out)
}

func (c *Client) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, c.NftPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft %v: %w — %s", args, err, string(out))
	}
	return nil
}
