package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A grant tier published over HTTP must reach the domain as an INTEGER.
//
// This is the regression for a defect that shipped invisibly: decodeJSON did not call UseNumber, so numbers
// inside a map[string]any body arrived as float64, while the commerce validators accept only
// json.Number/int/int64 (they reject anything containing "." or an exponent, so a bandwidth cap can never
// arrive as 2047.9999999). The result was that EVERY numeric grant override sent to
// POST /edge/v1/commercial-packages was rejected as "invalid_grant_tier" -- publishing a package with a
// bandwidth or quota override was impossible through the API.
//
// The existing commerce tests could not catch it: they construct json.Number values in Go and call the admin
// layer directly, so they exercised a type the HTTP surface was incapable of producing. This test therefore
// asserts at the decoder, which is where the two halves actually meet.
func TestDecodeJSONPreservesIntegersInFreeFormMaps(t *testing.T) {
	body := `{"grant_tiers":[{"order":10,"grant":{"down_kbps":2048,"up_kbps":1024}}]}`
	var dst struct {
		GrantTiers []struct {
			Order int            `json:"order"`
			Grant map[string]any `json:"grant"`
		} `json:"grant_tiers"`
	}
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	if err := decodeJSON(req, &dst); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(dst.GrantTiers) != 1 {
		t.Fatalf("want 1 tier, got %d", len(dst.GrantTiers))
	}
	for _, key := range []string{"down_kbps", "up_kbps"} {
		v, ok := dst.GrantTiers[0].Grant[key]
		if !ok {
			t.Fatalf("%s missing from decoded grant", key)
		}
		n, ok := v.(json.Number)
		if !ok {
			// The exact failure that shipped: float64 here means the domain rejects the tier.
			t.Fatalf("%s decoded as %T, not json.Number -- the commerce validators reject it and the package cannot be published over HTTP", key, v)
		}
		if strings.ContainsAny(n.String(), ".eE") {
			t.Fatalf("%s decoded as %q, which the validators reject as non-integral", key, n.String())
		}
		if _, err := n.Int64(); err != nil {
			t.Fatalf("%s is not an integer: %v", key, err)
		}
	}
}
