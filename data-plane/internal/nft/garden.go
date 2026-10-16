package nft

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// THE WALLED GARDEN IS THREE SETS, NOT ONE.
//
// walled_garden_ip opened every port to every address in it, and that was the whole garden: the "Ports" an
// operator wrote on a rule were stored and ignored, the three baseline resolvers were reachable on 443 (DNS
// over HTTPS, and whatever else those addresses serve), and every email service or identity provider added
// for a sign-in opened its addresses to any protocol. Measured on PRE-LIVE 2026-10-11 from a device that had
// not signed in: port 80, UDP 443 and any TCP port to a Google address, and a page from a rule's address on a
// port the rule did not name.
//
//	walled_garden_ip   address only -- every port (an operator rule written without ports; unchanged meaning)
//	walled_garden_tcp  address . TCP port
//	walled_garden_udp  address . UDP port
//
// What this cannot do, and is not claimed to: a NAME behind a shared front end resolves to addresses that
// serve many names, and the kernel sees only the address. Isolation by host name needs the TLS server name,
// which is a different mechanism and a separate decision.
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

// GardenList returns the current elements of walled_garden_ip. Elements may
// be single addresses or CIDR ranges (the set has flags interval).
func (c *Client) GardenList(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, c.NftPath, "-j", "list", "set", "inet", "stayconnect", GardenV4).Output()
	if err != nil {
		return nil, fmt.Errorf("nft list garden: %w", err)
	}
	return parseGardenJSON(out)
}

// GardenSync replaces the contents of all three sets in ONE nft -f transaction (flush + add per set), so the
// garden is never momentarily empty for the forward chain and the three sets can never describe two moments.
// A set the live table does not declare (an appliance whose netd has not been re-rendered yet) is skipped on
// a second attempt rather than failing the whole sync, so the address-only set keeps reconciling.
func (c *Client) GardenSync(ctx context.Context, plan GardenPlan) error {
	script := gardenScript(plan, map[string]bool{GardenV4: true, GardenTCP: true, GardenUDP: true})
	if err := c.runScript(ctx, script); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "No such file or directory") && !strings.Contains(err.Error(), "does not exist") {
		return fmt.Errorf("nft garden sync: %w", err)
	}
	// Something named in the script is missing: find out which sets exist and sync the ones that do.
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

// gardenScript is the transaction text, deterministic for a given plan (tests compare it).
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

// parseGardenJSON decodes `nft -j list set` output into element strings,
// handling plain addresses, prefixes and auto-merged ranges.
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

// ValidGardenElem reports whether s is an IPv4 address or CIDR usable as a
// walled-garden element.
//
// THE ENFORCEMENT FLOOR (audit N-7). Whatever a rule says, the garden never opens 0.0.0.0, loopback, multicast or
// anything wider than a /8: one stored 0.0.0.0/0 rule used to open the whole internet before sign-in. The
// hotel's own, narrower limit is enforced where rules are written (edged, migration 0125); this is the line no
// stored row can cross.
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

// GardenFloorPrefix is the widest network the garden will ever install.
const GardenFloorPrefix = 8

func gardenAddrUsable(ip net.IP) bool {
	return !ip.IsUnspecified() && !ip.IsLoopback() && !ip.IsMulticast() && !ip.Equal(net.IPv4bcast)
}

// GardenTargetRefusal explains why an operator-supplied IP or CIDR may not be a walled-garden target, or "" when
// it may. widest is the hotel's setting (the smallest prefix length allowed). Beyond the enforcement floor it
// refuses the addresses a garden entry is meaningless for: private, shared (CGNAT), link-local and reserved
// ranges -- a sign-in or payment service is a public address, and a private one would open the hotel's own
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
			"public sign-in and payment services through it, and a private entry would open the hotel's own networks"
	}
	return ""
}

func inCIDR(ip net.IP, cidr string) bool {
	_, n, _ := net.ParseCIDR(cidr)
	return n.Contains(ip)
}

// GardenPortElem formats one `address . port` element for the TCP/UDP sets, or "" when either part is invalid.
func GardenPortElem(addr string, port int) string {
	if !ValidGardenElem(addr) || port < 1 || port > 65535 {
		return ""
	}
	return fmt.Sprintf("%s . %d", addr, port)
}
