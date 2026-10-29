package nft

// The MAC-bound Phase-3 set (AD-23 a): `type ifname . ipv4_addr . ether_addr`. The device address is part of the key, so a
// device that sets an authorized guest's IP by hand does not match. The functions mirror LeaseIn / DenyIn for the
// three-part key; the (bridge, IP) -> MAC relation is one-to-one, so installing a MAC for an address first removes any
// other MAC the address had (a reassigned lease must not leave the previous holder's element behind).

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

func macElemKey(iface string, ip net.IP, mac string) string {
	return fmt.Sprintf("%q . %s . %s", iface, ip.String(), mac)
}

// NormalizeMAC returns the lower-case colon form of a 6-octet hardware address, or "" when it is anything else.
func NormalizeMAC(s string) string {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return ""
	}
	return hw.String()
}

// LeaseMAC asserts a bounded authorization for (iface, ip, mac), replacing any other MAC the address held, in ONE nft
// transaction (the guest is never unauthorized in between).
func (c *Client) LeaseMAC(ctx context.Context, iface string, ip net.IP, mac string, ttl time.Duration) error {
	if ttl <= 0 {
		return ErrUnboundedLease
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("only IPv4 supported: %s", ip)
	}
	mac = NormalizeMAC(mac)
	if mac == "" {
		return fmt.Errorf("nft: a MAC-bound authorization needs a 6-octet hardware address")
	}
	secs, ok := leaseSeconds(ttl)
	if !ok {
		return ErrLeaseTooShort
	}
	listed, err := c.ListIn(ctx, Phase3MACV4)
	if err != nil {
		return err
	}
	var cmds []string
	for _, e := range listed {
		if e.IP.Equal(ip4) && e.Iface == iface {
			cmds = append(cmds, fmt.Sprintf("delete element inet stayconnect %s { %s }", Phase3MACV4, macElemKey(iface, ip4, e.MAC)))
		}
	}
	cmds = append(cmds, fmt.Sprintf("add element inet stayconnect %s { %s timeout %ds }", Phase3MACV4, macElemKey(iface, ip4, mac), secs))
	return c.run(ctx, strings.Join(cmds, " ; "))
}

// DenyMAC removes every MAC-bound authorization of (iface, ip); absent is not an error.
func (c *Client) DenyMAC(ctx context.Context, iface string, ip net.IP) error {
	ip4 := ip.To4()
	if ip4 == nil {
		return nil
	}
	listed, err := c.ListIn(ctx, Phase3MACV4)
	if err != nil {
		return err
	}
	var cmds []string
	for _, e := range listed {
		if e.IP.Equal(ip4) && (iface == "" || e.Iface == iface) && e.MAC != "" {
			cmds = append(cmds, fmt.Sprintf("delete element inet stayconnect %s { %s }", Phase3MACV4, macElemKey(e.Iface, ip4, e.MAC)))
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return c.run(ctx, strings.Join(cmds, " ; "))
}
