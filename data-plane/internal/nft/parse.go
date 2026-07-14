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
				// "val" may be a plain IP string, or a concatenation
				// {"concat": ["br-g20", "10.20.0.100"]}.
				switch val := inner["val"].(type) {
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
				if el.IP != nil {
					out = append(out, el)
				}
			}
		}
	}
	return out, nil
}
