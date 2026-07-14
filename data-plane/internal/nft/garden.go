package nft

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"strings"
)

const GardenV4 = "walled_garden_ip"

// GardenList returns the current elements of walled_garden_ip. Elements may
// be single addresses or CIDR ranges (the set has flags interval).
func (c *Client) GardenList(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, c.NftPath, "-j", "list", "set", "inet", "stayconnect", GardenV4).Output()
	if err != nil {
		return nil, fmt.Errorf("nft list garden: %w", err)
	}
	return parseGardenJSON(out)
}

// GardenSync replaces the set contents with want (addresses or CIDRs).
// Flush+add in one nft -f transaction so the walled garden is never
// momentarily empty for the forward chain.
func (c *Client) GardenSync(ctx context.Context, want []string) error {
	var b strings.Builder
	b.WriteString("flush set inet stayconnect " + GardenV4 + "\n")
	if len(want) > 0 {
		b.WriteString("add element inet stayconnect " + GardenV4 + " { " + strings.Join(want, ", ") + " }\n")
	}
	cmd := exec.CommandContext(ctx, c.NftPath, "-f", "-")
	cmd.Stdin = strings.NewReader(b.String())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft garden sync: %w — %s", err, string(out))
	}
	return nil
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
func ValidGardenElem(s string) bool {
	if ip := net.ParseIP(s); ip != nil {
		return ip.To4() != nil
	}
	if _, n, err := net.ParseCIDR(s); err == nil {
		return n.IP.To4() != nil
	}
	return false
}
