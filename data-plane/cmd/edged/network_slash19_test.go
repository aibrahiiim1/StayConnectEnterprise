package main

// /19 client networks at the edged API: the width rule is the same one netd applies, so a subnet that cannot be applied is refused when it
// is entered; and a switched-on network is never widened in place.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestAPIClientSubnetWidthEveryPrefix(t *testing.T) {
	s := &server{}
	for p := 8; p <= 32; p++ {
		bad := s.clientSubnetRefusal(context.Background(), fmt.Sprintf("10.40.0.0/%d", p))
		switch {
		case p < 19:
			if bad == nil || bad.code != "subnet_too_large" {
				t.Errorf("/%d: want subnet_too_large, got %+v", p, bad)
			}
		case p > 30:
			if bad == nil || bad.code != "subnet_too_small" {
				t.Errorf("/%d: want subnet_too_small, got %+v", p, bad)
			}
		default:
			if bad != nil {
				t.Errorf("/%d refused: %+v", p, bad)
			}
		}
	}
	if bad := s.clientSubnetRefusal(context.Background(), ""); bad != nil {
		t.Errorf("an empty subnet (not supplied) must not be refused here: %+v", bad)
	}
	if bad := s.clientSubnetRefusal(context.Background(), "banana"); bad == nil || bad.code != "bad_subnet" {
		t.Errorf("garbage: %+v", bad)
	}
	msg := s.clientSubnetRefusal(context.Background(), "10.40.0.0/18").msg
	if !strings.Contains(msg, "/19") || !strings.Contains(msg, "8190") {
		t.Errorf("the refusal must name the limit: %s", msg)
	}
}

// In-place widening decision: REFUSED for a network that is switched on; a disabled network, a narrowing, and an unchanged subnet are not touched.
func TestWideningALiveNetworkInPlaceIsRefused(t *testing.T) {
	why := widenRefusal("10.20.0.0/20", true, "10.20.0.0/19")
	if why == "" {
		t.Fatal("widening a live /20 to /19 in place was allowed")
	}
	for _, want := range []string{"cannot be made wider in place", "10.20.0.0/20", "10.20.0.0/19", "new client network"} {
		if !strings.Contains(why, want) {
			t.Errorf("refusal lacks %q: %s", want, why)
		}
	}
	// a /24 to a /23 or a /20 to /19 whose base moves are widenings as well
	if widenRefusal("10.20.16.0/20", true, "10.20.0.0/19") == "" || widenRefusal("10.20.0.0/24", true, "10.20.0.0/23") == "" {
		t.Error("another widening was allowed")
	}
	for name, got := range map[string]string{
		"switched off":        widenRefusal("10.20.0.0/20", false, "10.20.0.0/19"),
		"same subnet":         widenRefusal("10.20.0.0/20", true, "10.20.0.0/20"),
		"not supplied":        widenRefusal("10.20.0.0/20", true, ""),
		"narrower":            widenRefusal("10.20.0.0/19", true, "10.20.0.0/20"),
		"same width, new net": widenRefusal("10.20.0.0/20", true, "10.30.0.0/20"),
		"unparseable":         widenRefusal("garbage", true, "10.20.0.0/19"),
	} {
		if got != "" {
			t.Errorf("%s was refused: %s", name, got)
		}
	}
}
