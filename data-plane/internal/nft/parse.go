package nft

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// parseJSON decodes the output of `nft -j list set`.
func parseJSON(raw []byte) ([]Element, error) {
	var root struct {
		Nftables []map[string]any `json:"nftables"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	var out []Element
	for _, obj := range root.Nftables {
		setObj, ok := obj["set"].(map[string]any)
		if !ok {
			continue
		}
		elems, ok := setObj["elem"].([]any)
		if !ok {
			continue
		}
		for _, e := range elems {
			switch v := e.(type) {
			case string:
				if ip := net.ParseIP(v); ip != nil {
					out = append(out, Element{IP: ip})
				}
			case map[string]any:
				inner, ok := v["elem"].(map[string]any)
				if !ok {
					inner = v
				}
				el := Element{}
				// The two shapes nft emits: an element with stateful attributes is wrapped
				// ({"elem": {"val": {"concat": [...]}, "timeout": 90, "expires": 47}}); one with none (a permanent authorization) is
				// bare ({"concat": [...]}). Reading only the first shape made permanent concatenated elements invisible to List,
				// Authorized and Deny.
				value, hasVal := inner["val"]
				if !hasVal {
					value = inner
				}
				switch val := value.(type) {
				case string:
					el.IP = net.ParseIP(val)
				case map[string]any:
					if parts, ok := val["concat"].([]any); ok {
						for _, p := range parts {
							// A port-restricted garden element is `address . service` with the service as a JSON number; without it a carry-over
							// would re-add a bare address and nft would refuse the replace.
							if n, ok := p.(float64); ok && n > 0 && n <= 65535 {
								el.Port = int(n)
								continue
							}
							s, _ := p.(string)
							if ip := net.ParseIP(s); ip != nil {
								el.IP = ip
							} else if hw, err := net.ParseMAC(s); err == nil && len(hw) == 6 {
								el.MAC = hw.String()
							} else if s != "" {
								el.Iface = s
							}
						}
					}
				}
				if t, ok := inner["timeout"].(float64); ok {
					el.Timeout = time.Duration(t) * time.Second
				}
				// "expires" is the remaining lease, which renewal decisions must read.
				if x, ok := inner["expires"].(float64); ok {
					el.Expires = time.Duration(x) * time.Second
				}
				if el.IP != nil {
					out = append(out, el)
				}
			}
		}
	}
	return out, nil
}

// ParseSetJSON exposes the set-listing parser so other packages share one parser of nft's JSON.
func ParseSetJSON(raw []byte) ([]Element, error) { return parseJSON(raw) }
