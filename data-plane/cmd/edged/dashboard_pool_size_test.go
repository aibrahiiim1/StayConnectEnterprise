package main

// THE ADDRESS POOL THE DASHBOARD COULD NOT SEE.
//
// During the guest pilot the dashboard said, of a network Kea was actively serving 101 addresses from:
//
//     "No address range is configured, so this appliance cannot hand out addresses here."
//
// The row was there. The pool was there. The query excluded it, because its same-subnet guard read
//
//     network(p.start_ip::cidr) = network(p.end_ip::cidr)
//
// and an inet HOST cast to cidr carries a /32 — so that compares 192.168.77.100/32 with 192.168.77.200/32
// and is true only when a pool is a SINGLE address. Every range of more than one address failed it, the sum
// came back NULL, and COALESCE turned "I excluded everything" into a confident zero.
//
// That is worse than silence on an operator screen: it does not say "unknown", it says the appliance cannot
// hand out addresses, which during a pilot sends somebody to repair a working DHCP configuration.
//
// WHY THIS IS ASSERTED ON THE SQL TEXT. The behaviour lives in Postgres, and the repository's integration
// tests are compiled by CI but never run by it (the workflows vet `-tags integration`; nothing executes
// them). A test that only existed behind that tag would look like protection and provide none. This one
// runs in the ordinary suite and fails the build if the broken predicate returns. The behaviour itself
// was verified directly against the appliance's database, where the corrected guard returns 101 for the pool
// the old one scored 0, and still rejects a range spanning two /24s.

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
	// COMMENTS DESCRIBE THE DEFECT, SO THEY MUST NOT BE SEARCHED FOR IT. The first version of this test read
	// the whole file and failed on the corrected code, because the comment above the query quotes the broken
	// predicate in order to explain it. A check that cannot tell a fault from an account of a fault would
	// have forced the explanation to be deleted to make the build pass -- which is the opposite of the point.
	s := stripComments(string(src))

	// The /32 comparison is the defect itself. It can only ever pass for a one-address pool.
	if strings.Contains(s, "network(p.start_ip::cidr) = network(p.end_ip::cidr)") {
		t.Error("the same-subnet guard compares /32 host networks again; every real pool is excluded and the " +
			"dashboard reports 'no address range is configured' for a network that is serving addresses")
	}

	// The arithmetic subtracts the FOURTH OCTET, which is only meaningful inside one /24. The guard has to
	// ask exactly that, or it is either excluding valid pools or admitting ones it will miscount.
	usesFourthOctet := strings.Contains(s, `split_part(host(p.end_ip),'.',4)`)
	if usesFourthOctet {
		if !strings.Contains(s, "set_masklen(p.start_ip::cidr, 24)") ||
			!strings.Contains(s, "set_masklen(p.end_ip::cidr, 24)") {
			t.Error("the pool size is computed from the fourth octet without a guard that start and end are in " +
				"the same /24; the count would be wrong for a range that spans one")
		}
	}

	// A reversed range would produce a negative contribution and silently shrink the total.
	if !strings.Contains(s, "p.end_ip >= p.start_ip") {
		t.Error("nothing rejects a pool whose end precedes its start; it would subtract from the total")
	}
}

// THE /24 GUARD WAS THE SECOND FORM OF THE SAME DEFECT. Once the /32 comparison was fixed, the fourth-octet
// arithmetic still needed both ends in one /24, and every pool crossing an octet boundary (10.20.0.10 to
// 10.20.3.250 on a /22) was excluded and scored as zero. The size is now the integer distance between the two
// addresses, which is correct for any IPv4 range; the overview computes the same figure in Go (ipv4RangeSize)
// and that arithmetic is tested directly in overview_calc_test.go.
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
	// The COALESCE is correct and must stay — a network with genuinely no pool is 0, not NULL. What made the
	// original defect invisible was that the SAME zero also meant "the guard rejected every row". With the
	// guard fixed those two cases can no longer be confused, but the wording the operator reads depends on
	// this number being trustworthy, so the pairing is pinned here.
	src, _ := os.ReadFile("resources_dashboard.go")
	s := stripComments(string(src))
	if !strings.Contains(s, "FROM public.dhcp_pools p") {
		t.Fatal("the pool size is no longer read from public.dhcp_pools")
	}
	if !strings.Contains(s, "), 0)::bigint") {
		t.Error("the sum is no longer coalesced; a network with no pool would render as an absent value")
	}
}

// stripComments removes Go line comments and the SQL line comments inside the embedded query, so an
// assertion about the QUERY cannot be satisfied or broken by prose about the query.
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
