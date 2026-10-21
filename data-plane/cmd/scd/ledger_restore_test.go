package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
)

func chainOf(id string, n int) []ledger.Entry {
	var out []ledger.Entry
	prev := ledger.GenesisPrev
	for i := 1; i <= n; i++ {
		e := ledger.Entry{Ledger: id, Seq: int64(i), Prev: prev, Kind: "checkpoint", Tenant: "t", Site: "s", AtUS: int64(i), Body: fmt.Sprintf(`{"n":%d}`, i)}
		e.Hash = e.ComputeHash()
		prev = e.Hash
		out = append(out, e)
	}
	return out
}

func copyOfChain(name string, es []ledger.Entry) ledger.Copy {
	b, _ := ledger.Encode(es)
	return ledger.Copy{Name: name, Present: true, Parse: ledger.ParseChain(b)}
}

// A replacement appliance's brand-new ledger says nothing about erasures: it must not be read as a contradiction of the old one.
func TestSettleFreshLedgers(t *testing.T) {
	old, fresh := chainOf("11111111-1111-4111-8111-111111111111", 6), chainOf("22222222-2222-4222-8222-222222222222", 1)
	copies := []ledger.Copy{copyOfChain("local", fresh), copyOfChain("external", old)}
	wit := []ledger.Witness{
		{Source: "the live database", Ledger: fresh[0].Ledger, Seq: 1, Hash: fresh[0].Hash},
		{Source: "this backup", Ledger: old[0].Ledger, Seq: 4, Hash: old[3].Hash},
	}
	// without settling, the two ledgers contradict each other
	if v := ledger.Assess(copies, wit); !v.Conflict {
		t.Fatalf("setup: %+v", v)
	}
	c2, w2 := settleFreshLedgers(copies, wit)
	if c2[0].Present || !c2[1].Present {
		t.Errorf("the fresh local ledger was not set aside: %+v", c2[0])
	}
	if len(w2) != 1 || w2[0].Source != "this backup" {
		t.Errorf("the fresh witness was not dropped: %+v", w2)
	}
	v := ledger.Assess(c2, w2)
	if v.Best == nil || v.Best.Name != "external" || !v.Complete || v.Conflict {
		t.Fatalf("verdict = %+v", v)
	}
	if !strings.Contains(strings.Join(v.Reasons, ";"), "reaches every independent record") {
		t.Errorf("reasons = %v", v.Reasons)
	}
}

// A LONGER different ledger is a real contradiction and is never set aside: only a first entry can be "fresh".
func TestSettleFreshLedgersKeepsARealContradiction(t *testing.T) {
	a, b := chainOf("11111111-1111-4111-8111-111111111111", 6), chainOf("22222222-2222-4222-8222-222222222222", 3)
	copies := []ledger.Copy{copyOfChain("local", b), copyOfChain("external", a)}
	c2, _ := settleFreshLedgers(copies, nil)
	if !c2[0].Present {
		t.Fatal("a three-entry ledger was set aside as fresh")
	}
	if v := ledger.Assess(c2, nil); !v.Conflict {
		t.Fatalf("a real contradiction was settled: %+v", v)
	}
}

func TestSettleFreshLedgersDoesNothingWhenNoLedgerIsLonger(t *testing.T) {
	c := []ledger.Copy{copyOfChain("local", chainOf("11111111-1111-4111-8111-111111111111", 1)), {Name: "external"}}
	c2, w := settleFreshLedgers(c, []ledger.Witness{{Source: "x", Ledger: "other", Seq: 1}})
	if !c2[0].Present || len(w) != 1 {
		t.Fatalf("settled with nothing to settle against: %+v %+v", c2, w)
	}
}
