package main

import (
	"reflect"
	"testing"
)

// THE GARDEN OPENS THE PORT A SIGN-IN NEEDS AND NOTHING ELSE (edge.go planGarden, internal/nft/garden.go).
// Measured on PRE-LIVE 2026-10-11 before this: every port to every garden address, a rule's Ports ignored,
// and DNS-over-HTTPS to the baseline resolvers reachable before sign-in.
func TestGardenPlanOpensExactlyTheNamedPorts(t *testing.T) {
	resolve := func(name string) []string {
		switch name {
		case "mail.google.com":
			return []string{"142.251.209.197"}
		case "accounts.google.com":
			return []string{"142.251.209.197", "142.251.209.198"}
		case "checkout.stripe.com":
			return []string{"54.187.1.1"}
		case "pay.example.com":
			return []string{"203.0.113.9"}
		case "unresolvable.example":
			return nil
		}
		return nil
	}
	plan := planGarden(gardenSources{
		Rules: []gardenRule{
			{Kind: "ip", Value: "198.51.100.7"},                           // every port, as written
			{Kind: "cidr", Value: "10.9.0.0/24", Ports: []int{8443}},      // only 8443
			{Kind: "domain", Value: "pay.example.com", Ports: []int{443}}, // only 443
			{Kind: "domain", Value: "unresolvable.example"},
			{Kind: "ip", Value: "not-an-address"},
			{Kind: "ip", Value: "203.0.113.5", Ports: []int{0, 70000, 80}}, // bad ports skipped, 80 kept
		},
		Payment: []string{"checkout.stripe.com"},
		Social:  []string{"accounts.google.com"},
		Mail:    []string{"mail.google.com", "accounts.google.com"},
	}, resolve)

	if want := []string{"198.51.100.7"}; !reflect.DeepEqual(plan.IP, want) {
		t.Fatalf("address-only set: got %v want %v", plan.IP, want)
	}
	wantTCP := []string{
		"1.1.1.1 . 53", "10.9.0.0/24 . 8443",
		"142.251.209.197 . 443", "142.251.209.197 . 5223", "142.251.209.197 . 5228", "142.251.209.197 . 993",
		"142.251.209.198 . 443", "142.251.209.198 . 5223", "142.251.209.198 . 5228", "142.251.209.198 . 993",
		"203.0.113.5 . 80", "203.0.113.9 . 443", "54.187.1.1 . 443", "8.8.4.4 . 53", "8.8.8.8 . 53",
	}
	if !reflect.DeepEqual(plan.TCP, wantTCP) {
		t.Fatalf("tcp set:\n got %v\nwant %v", plan.TCP, wantTCP)
	}
	// UDP: the baseline resolvers' 53 and an operator's ported rules (a rule names no protocol); never a mail
	// service, a payment page or an identity provider.
	wantUDP := []string{"1.1.1.1 . 53", "10.9.0.0/24 . 8443", "203.0.113.5 . 80", "203.0.113.9 . 443", "8.8.4.4 . 53", "8.8.8.8 . 53"}
	if !reflect.DeepEqual(plan.UDP, wantUDP) {
		t.Fatalf("udp set:\n got %v\nwant %v", plan.UDP, wantUDP)
	}
	for _, e := range append(append([]string{}, plan.IP...), plan.TCP...) {
		if e == "1.1.1.1" || e == "8.8.8.8" || e == "8.8.4.4" || e == "1.1.1.1 . 443" || e == "8.8.8.8 . 443" {
			t.Fatalf("a baseline resolver must be reachable on 53 only; found %q", e)
		}
	}
}

func TestGardenPlanWithNothingConfiguredIsTheResolversOn53Only(t *testing.T) {
	plan := planGarden(gardenSources{}, func(string) []string { return nil })
	if len(plan.IP) != 0 || len(plan.TCP) != 3 || len(plan.UDP) != 3 {
		t.Fatalf("empty sources: %+v", plan)
	}
}
