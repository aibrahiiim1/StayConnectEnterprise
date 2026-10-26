package main

import (
	"testing"
	"time"
)

func TestAProposalAuthorisesOnlyItsOwnReviewedChangeByItsOwnOperatorOnceAndBriefly(t *testing.T) {
	p := newProposalStore()
	now := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	ids := []string{"b", "a"}
	pr := p.mint("n1", "op1", "SHARED_STRICT", ids, "state-1", now)
	if pr == nil || len(pr.ID) != 32 {
		t.Fatalf("no unguessable id: %+v", pr)
	}
	if other := p.mint("n1", "op1", "SHARED_STRICT", ids, "state-1", now); other.ID == pr.ID {
		t.Fatal("two proposals share an id")
	}
	cases := []struct {
		name, id, network, operator, mode, state string
		ids                                      []string
		at                                       time.Time
		want                                     string
	}{
		{"the reviewed change", pr.ID, "n1", "op1", "SHARED_STRICT", "state-1", []string{"a", "b"}, now.Add(time.Minute), propOK},
		{"no id", "", "n1", "op1", "SHARED_STRICT", "state-1", ids, now, propUnknown},
		{"an invented id", "0000", "n1", "op1", "SHARED_STRICT", "state-1", ids, now, propUnknown},
		{"another network", pr.ID, "n2", "op1", "SHARED_STRICT", "state-1", ids, now, propUnknown},
		{"another operator", pr.ID, "n1", "op2", "SHARED_STRICT", "state-1", ids, now, propOperator},
		{"no operator", pr.ID, "n1", "", "SHARED_STRICT", "state-1", ids, now, propOperator},
		{"another mode", pr.ID, "n1", "op1", "DEDICATED", "state-1", ids, now, propMismatch},
		{"other interfaces", pr.ID, "n1", "op1", "SHARED_STRICT", "state-1", []string{"a", "c"}, now, propMismatch},
		{"fewer interfaces", pr.ID, "n1", "op1", "SHARED_STRICT", "state-1", []string{"a"}, now, propMismatch},
		{"the state changed underneath it", pr.ID, "n1", "op1", "SHARED_STRICT", "state-2", ids, now, propObsolete},
		{"after it expired", pr.ID, "n1", "op1", "SHARED_STRICT", "state-1", ids, now.Add(proposalTTL + time.Second), propObsolete},
	}
	for _, c := range cases {
		if got := p.check(c.id, c.network, c.operator, c.mode, c.ids, c.state, c.at); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	p.applied("n1")
	if got := p.check(pr.ID, "n1", "op1", "SHARED_STRICT", ids, "state-1", now); got != propUnknown {
		t.Errorf("a used proposal is %q, want it gone", got)
	}
}

func TestAnyChangeToANetworkRetiresEveryOutstandingApprovalOfThatNetworkOnly(t *testing.T) {
	p := newProposalStore()
	now := time.Now()
	one := p.mint("n1", "op", "NONE", nil, "s", now)
	two := p.mint("n1", "op", "DEDICATED", []string{"a"}, "s", now)
	other := p.mint("n2", "op", "NONE", nil, "s", now)
	p.applied("n1")
	for _, x := range []*routingProposal{one, two} {
		if got := p.check(x.ID, "n1", "op", x.Mode, x.IDs, "s", now); got == propOK {
			t.Fatalf("an approval survived a change to its network")
		}
	}
	if got := p.check(other.ID, "n2", "op", "NONE", nil, "s", now); got != propOK {
		t.Fatalf("a change to one network retired another network's approval: %q", got)
	}
	late := p.mint("n1", "op", "NONE", nil, "s", now)
	if late.Version != 1 {
		t.Fatalf("the change counter did not advance: %d", late.Version)
	}
}

func TestTheStoreIsBounded(t *testing.T) {
	p := newProposalStore()
	now := time.Now()
	for i := 0; i < proposalsMax+50; i++ {
		p.mint("n", "op", "NONE", nil, "s", now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(p.items) > proposalsMax {
		t.Fatalf("%d proposals held", len(p.items))
	}
}
