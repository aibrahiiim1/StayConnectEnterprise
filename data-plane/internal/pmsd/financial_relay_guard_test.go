package pmsd

import "testing"

// Audit P7 L14: at the bound the oldest carried P# is forgotten first; every more recent one is still refused.
func TestRelay_CarriedSetEvictsOldestFirst(t *testing.T) {
	r := NewFinancialRelay(true, nil, nil)
	for pn := int64(1); pn <= maxCarriedPerLink+10; pn++ {
		if r.markCarried("i1", pn) {
			t.Fatalf("P# %d reported as already carried", pn)
		}
	}
	for pn := int64(11); pn <= maxCarriedPerLink+10; pn++ {
		if !r.wasCarried("i1", pn) {
			t.Fatalf("recent P# %d was evicted", pn)
		}
	}
	for pn := int64(1); pn <= 10; pn++ {
		if r.wasCarried("i1", pn) {
			t.Fatalf("oldest P# %d should have been evicted", pn)
		}
	}
	if !r.markCarried("i1", maxCarriedPerLink+10) {
		t.Fatal("a repeat of a recent P# must be reported as carried")
	}
	if r.wasCarried("i2", 11) {
		t.Fatal("carried sets are per interface")
	}
}
