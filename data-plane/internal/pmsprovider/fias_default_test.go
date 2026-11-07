package pmsprovider

import "testing"

// A new Protel FIAS connection refreshes its full guest list every 60 minutes unless set otherwise (PO, 2026-11-07); the
// value stays a per-interface field.
func TestProtelFIAS_FullSyncDefaultsTo60Minutes(t *testing.T) {
	p, ok := Get(KindProtelFIAS)
	if !ok {
		t.Fatal("protel-fias is not in the catalogue")
	}
	f, ok := p.Field("complete_sync_ms")
	if !ok {
		t.Fatal("complete_sync_ms is not a Protel FIAS field")
	}
	if f.Default != int64(3600000) {
		t.Fatalf("default %v, want 3600000 (60 minutes)", f.Default)
	}
	if !f.Required || f.Min == nil || *f.Min != 1 || f.Max == nil || *f.Max != 86_400_000 {
		t.Fatalf("the interval must stay a required, bounded per-interface setting: %+v", f)
	}
}