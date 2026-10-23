package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// decodeJSON must use UseNumber: the commerce validators accept only json.Number/int/int64, so float64 grant
// values would reject every numeric override as invalid_grant_tier.
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
