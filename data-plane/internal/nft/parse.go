package nft

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// parseJSON decodes the output of `nft -j list set …`.
//
// Example:
//
//	{ "nftables": [
//	    { "metainfo": {...} },
//	    { "set": {
//	        "family":"inet","name":"auth_ipv4","table":"stayconnect",
//	        "elem": [
//	          { "elem": { "val":"10.10.0.100", "timeout":3600, "expires":3550 } }
//	        ]
//	    }}
//	]}
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
				// THE TWO SHAPES nft ACTUALLY EMITS.
				//
				// An element with stateful attributes (a timeout, an expiry, a counter) is wrapped:
				//   {"elem": {"val": {"concat": ["br-g20","10.20.0.100"]}, "timeout": 90, "expires": 47}}
				// An element with NONE — which is what a permanent authorization is — carries no wrapper and
				// no "val" at all:
				//   {"concat": ["br-g20","10.20.0.100"]}
				//
				// Reading only the first shape made every permanent concatenated element INVISIBLE to List and
				// therefore to Authorized and Deny: a legacy guest could not be enumerated, and a Deny for one
				// silently did nothing. The real-kernel suite is what surfaced it — the modelled tests all fed
				// the wrapped shape, because that is the shape the code already understood.
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
							s, _ := p.(string)
							if ip := net.ParseIP(s); ip != nil {
								el.IP = ip
							} else if s != "" {
								el.Iface = s
							}
						}
					}
				}
				if t, ok := inner["timeout"].(float64); ok {
					el.Timeout = time.Duration(t) * time.Second
				}
				// "expires" is the REMAINING lease. It is what a renewal decision must read: an element with a
				// 90s timeout says nothing about whether it is about to disappear.
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

// ParseSetJSON exposes the set-listing parser so other packages can read `nft -j list set` output without
// duplicating the shape of it. One parser means one place where a change in nft's JSON has to be handled.
func ParseSetJSON(raw []byte) ([]Element, error) { return parseJSON(raw) }
