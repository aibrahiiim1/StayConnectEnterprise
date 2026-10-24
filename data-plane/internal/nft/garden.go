package nft

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// The walled garden is three sets: walled_garden_ip (address only, every port, for operator rules written without
// ports) and walled_garden_tcp / walled_garden_udp (address . port). A single address-only set ignored rule Ports,
// left the baseline resolvers reachable on 443 (DNS over HTTPS) and opened every added service to any protocol.
// Not claimed: a name behind a shared front end resolves to addresses that serve many names and the kernel sees only
// the address; host-name isolation needs the TLS server name, a separate decision.
const (
	GardenV4  = "walled_garden_ip"
	GardenTCP = "walled_garden_tcp"
	GardenUDP = "walled_garden_udp"
)

// GardenPlan is the desired contents of the three sets. IP holds addresses or CIDRs; TCP and UDP hold
// "address . port" pairs (ValidGardenElem on the address part, 1..65535 on the port).
type GardenPlan struct {
	IP  []string
	TCP []string
	UDP []string
}

// GardenList returns the current elements of walled_garden_ip (addresses or CIDR ranges).
func (c *Client) GardenList(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, c.NftPath, "-j", "list", "set", "inet", "stayconnect", GardenV4).Output()
	if err != nil {
		return nil, fmt.Errorf("nft list garden: %w", err)
	}
	return parseGardenJSON(out)
}

// GardenSync replaces all three sets in one nft -f transaction so the garden is never momentarily empty. A set the
// live table does not declare (netd not yet re-rendered) is skipped on a second attempt so the address-only set
// keeps reconciling.
func (c *Client) GardenSync(ctx context.Context, plan GardenPlan) error {
	script := gardenScript(plan, map[string]bool{GardenV4: true, GardenTCP: true, GardenUDP: true})
	if err := c.runScript(ctx, script); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "No such file or directory") && !strings.Contains(err.Error(), "does not exist") {
		return fmt.Errorf("nft garden sync: %w", err)
	}
	have := map[string]bool{}
	for _, s := range []string{GardenV4, GardenTCP, GardenUDP} {
		if _, err := exec.CommandContext(ctx, c.NftPath, "list", "set", "inet", "stayconnect", s).Output(); err == nil {
			have[s] = true
		}
	}
	if len(have) == 0 {
		return fmt.Errorf("nft garden sync: no walled-garden set exists in table inet stayconnect")
	}
	if err := c.runScript(ctx, gardenScript(plan, have)); err != nil {
		return fmt.Errorf("nft garden sync: %w", err)
	}
	return nil
}

func (c *Client) runScript(ctx context.Context, script string) error {
	cmd := exec.CommandContext(ctx, c.NftPath, "-f", "-")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w — %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gardenScript(plan GardenPlan, have map[string]bool) string {
	var b strings.Builder
	add := func(set string, elems []string) {
		if !have[set] {
			return
		}
		b.WriteString("flush set inet stayconnect " + set + "\n")
		if len(elems) > 0 {
			b.WriteString("add element inet stayconnect " + set + " { " + strings.Join(elems, ", ") + " }\n")
		}
	}
	add(GardenV4, plan.IP)
	add(GardenTCP, plan.TCP)
	add(GardenUDP, plan.UDP)
	return b.String()
}

// parseGardenJSON decodes `nft -j list set` output into element strings (addresses, prefixes, merged ranges).
func parseGardenJSON(raw []byte) ([]string, error) {
	var doc struct {
		Nftables []map[string]json.RawMessage `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	var out []string
	for _, obj := range doc.Nftables {
		setRaw, ok := obj["set"]
		if !ok {
			continue
		}
		var set struct {
			Elem []json.RawMessage `json:"elem"`
		}
		if err := json.Unmarshal(setRaw, &set); err != nil {
			continue
		}
		for _, e := range set.Elem {
			// element forms: "1.1.1.1" | {"prefix":{"addr":"8.8.8.0","len":24}}
			// | {"range":["a","b"]}
			var s string
			if json.Unmarshal(e, &s) == nil {
				out = append(out, s)
				continue
			}
			var pfx struct {
				Prefix struct {
					Addr string `json:"addr"`
					Len  int    `json:"len"`
				} `json:"prefix"`
				Range []string `json:"range"`
			}
			if json.Unmarshal(e, &pfx) == nil {
				if pfx.Prefix.Addr != "" {
					out = append(out, fmt.Sprintf("%s/%d", pfx.Prefix.Addr, pfx.Prefix.Len))
				} else if len(pfx.Range) == 2 {
					out = append(out, pfx.Range[0]+"-"+pfx.Range[1])
				}
			}
		}
	}
	return out, nil
}

// ValidGardenElem reports whether s is an IPv4 address or CIDR usable as a walled-garden element. Enforcement floor:
// whatever a rule says, the garden never opens 0.0.0.0, loopback, multicast or anything wider than a /8 (a stored
// 0.0.0.0/0 rule once opened the internet before sign-in). The hotel's narrower limit is enforced where rules are
// written (edged).
func ValidGardenElem(s string) bool {
	if ip := net.ParseIP(s); ip != nil {
		v4 := ip.To4()
		return v4 != nil && gardenAddrUsable(v4)
	}
	if _, n, err := net.ParseCIDR(s); err == nil {
		ones, bits := n.Mask.Size()
		return n.IP.To4() != nil && bits == 32 && ones >= GardenFloorPrefix && gardenAddrUsable(n.IP.To4())
	}
	return false
}

const GardenFloorPrefix = 8

func gardenAddrUsable(ip net.IP) bool {
	return !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() && !ip.Equal(net.IPv4bcast)
}

// GardenTargetRefusal explains why an IP or CIDR may not be a walled-garden target, or "" when it may. widest is the
// hotel's setting (smallest allowed prefix length). Beyond the floor it refuses private, shared (CGNAT), link-local
// and reserved ranges: sign-in and payment services are public, and a private entry would open the hotel's own
// networks to clients that have not signed in.
func GardenTargetRefusal(value string, widest int) string {
	var ip net.IP
	ones := 32
	if p := net.ParseIP(value); p != nil {
		ip = p.To4()
	} else if _, n, err := net.ParseCIDR(value); err == nil {
		ip = n.IP.To4()
		ones, _ = n.Mask.Size()
		if ip != nil && !n.IP.Equal(net.ParseIP(strings.SplitN(value, "/", 2)[0]).To4()) {
			return "the address is not the start of the network; write it as " + n.String()
		}
	}
	if ip == nil {
		return "not an IPv4 address or network"
	}
	if ones < widest || ones < GardenFloorPrefix {
		return fmt.Sprintf("/%d is wider than this site allows (/%d at most): a walled-garden entry should name a "+
			"sign-in or payment service, not open a whole network before sign-in", ones, widest)
	}
	switch {
	case ip.IsUnspecified(), ip.IsLoopback(), ip.IsMulticast(), ip.Equal(net.IPv4bcast):
		return "this address cannot be reached through the walled garden"
	case ip.IsPrivate(), ip.IsLinkLocalUnicast(), inCIDR(ip, "100.64.0.0/10"), inCIDR(ip, "240.0.0.0/4"), inCIDR(ip, "0.0.0.0/8"):
		return "private, shared, link-local and reserved addresses are not walled-garden targets: clients reach " +
			"public sign-in and payment services through it, and a private entry would open the site's own networks"
	}
	return ""
}

func inCIDR(ip net.IP, cidr string) bool {
	_, n, _ := net.ParseCIDR(cidr)
	return n.Contains(ip)
}

func GardenPortElem(addr string, port int) string {
	if !ValidGardenElem(addr) || port < 1 || port > 65535 {
		return ""
	}
	return fmt.Sprintf("%s . %d", addr, port)
}
