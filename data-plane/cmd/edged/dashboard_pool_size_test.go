package main

// An inet host cast to cidr carries a /32, so network(start::cidr) = network(end::cidr) is true only for a
// single-address pool; every real range was excluded and COALESCE turned that into a confident zero ("no
// address range is configured") on a working network. The behaviour lives in Postgres and CI never runs the
// integration tag, so this asserts on the SQL text in the ordinary suite.

import (
	"os"
	"strings"
	"testing"
)

func TestThePoolSizeGuardAsksTheQuestionTheArithmeticAssumes(t *testing.T) {
	src, err := os.ReadFile("resources_dashboard.go")
	if err != nil {
		t.Fatal(err)
	}
	// Comments are stripped because they quote the broken predicate to explain it.
	s := stripComments(string(src))

	if strings.Contains(s, "network(p.start_ip::cidr) = network(p.end_ip::cidr)") {
		t.Error("the same-subnet guard compares /32 host networks again; every real pool is excluded and the " +
			"dashboard reports 'no address range is configured' for a network that is serving addresses")
	}

	// Fourth-octet arithmetic is only meaningful inside one /24, so the guard must ask exactly that.
	usesFourthOctet := strings.Contains(s, `split_part(host(p.end_ip),'.',4)`)
	if usesFourthOctet {
		if !strings.Contains(s, "set_masklen(p.start_ip::cidr, 24)") ||
			!strings.Contains(s, "set_masklen(p.end_ip::cidr, 24)") {
			t.Error("the pool size is computed from the fourth octet without a guard that start and end are in " +
				"the same /24; the count would be wrong for a range that spans one")
		}
	}

	if !strings.Contains(s, "p.end_ip >= p.start_ip") {
		t.Error("nothing rejects a pool whose end precedes its start; it would subtract from the total")
	}
}

// The size is the integer distance between the addresses, correct for any IPv4 range; a /24 guard would score
// pools crossing an octet boundary (e.g. on a /22) as zero. The Go twin ipv4RangeSize is tested in overview_calc_test.go.
func TestThePoolSizeCountsRangesThatCrossAnOctetBoundary(t *testing.T) {
	src, err := os.ReadFile("resources_dashboard.go")
	if err != nil {
		t.Fatal(err)
	}
	s := stripComments(string(src))
	if !strings.Contains(s, "sum((p.end_ip - p.start_ip) + 1)") {
		t.Error("the pool size is no longer the address distance plus one; ranges wider than a /24 would miscount")
	}
	if strings.Contains(s, "set_masklen(p.start_ip::cidr, 24)") {
		t.Error("the /24 guard is back; every pool that crosses an octet boundary would be scored as zero")
	}
}

func TestZeroMeansNoPoolRatherThanAPoolThatWasExcluded(t *testing.T) {
	// The COALESCE must stay: a network with genuinely no pool is 0, not NULL.
	src, _ := os.ReadFile("resources_dashboard.go")
	s := stripComments(string(src))
	if !strings.Contains(s, "FROM public.dhcp_pools p") {
		t.Fatal("the pool size is no longer read from public.dhcp_pools")
	}
	if !strings.Contains(s, "), 0)::bigint") {
		t.Error("the sum is no longer coalesced; a network with no pool would render as an absent value")
	}
}

// stripComments removes Go and SQL line comments so assertions about the query ignore prose about it.
func stripComments(s string) string {
	out := make([]string, 0, 512)
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "//") || strings.HasPrefix(t, "--") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
