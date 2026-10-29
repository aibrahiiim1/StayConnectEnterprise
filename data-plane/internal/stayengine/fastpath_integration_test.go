//go:build integration

package stayengine

import (
	"context"
	"testing"
	"time"
)

// Audit AD-37 step 2: a record that changes nothing is applied by re-stamping the occupancy evidence and pinning lineage,
// not by rewriting the stay. A record that changes anything takes the full path.

func lineage(t *testing.T, s scope, res string) (eventID string, evidenceAt *time.Time, guests int) {
	t.Helper()
	p := pool(t)
	defer p.Close()
	if err := p.QueryRow(context.Background(), `SELECT COALESCE(last_applied_event_id::text,''), occupancy_evidence_at,
		(SELECT count(*) FROM iam_v2.stay_guests g WHERE g.stay_id = stays.id)::int
		FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id=$2`, s.iface, res).Scan(&eventID, &evidenceAt, &guests); err != nil {
		t.Fatal(err)
	}
	return
}

func TestIntegration_AD37_AnUnchangedRecordRestampsEvidenceAndPinsLineage(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	seedPinnedRevision(t, p, s)
	pr := NewProcessor(p)
	res := "R-AD37"
	body := pay(res, "1408", "Example", "Guest", "F1", "260101", "260105")

	t0 := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	insertLiveReceivedAt(t, p, s, "ad37-gi", "GI", body, t0, false)
	process(t, pr, s)
	_, ev0, g0 := lineage(t, s, res)
	if ev0 == nil || g0 != 1 {
		t.Fatalf("setup: evidence %v guests %d", ev0, g0)
	}

	before := FastPathApplied()
	t1 := t0.Add(2 * time.Hour)
	insertLiveReceivedAt(t, p, s, "ad37-gc-same", "GC", body, t1, false)
	process(t, pr, s)
	if FastPathApplied() != before+1 {
		t.Fatalf("an unchanged record must take the cheap path (%d -> %d)", before, FastPathApplied())
	}
	id1, ev1, g1 := lineage(t, s, res)
	if status, _ := eventOutcome(t, p, s, "ad37-gc-same"); status != "APPLIED" {
		t.Fatalf("the unchanged record is APPLIED (not skipped), got %s", status)
	}
	var eventID string
	_ = p.QueryRow(context.Background(), `SELECT id::text FROM iam_v2.stay_events WHERE pms_interface_id=$1 AND external_event_identity='ad37-gc-same'`, s.iface).Scan(&eventID)
	if id1 != eventID {
		t.Fatalf("lineage points at the event that confirmed the stay: %s vs %s", id1, eventID)
	}
	if ev1 == nil || !ev1.Equal(t1) || g1 != 1 {
		t.Fatalf("the evidence is re-stamped to the new confirmation (freshness) and the occupants are untouched: %v guests=%d", ev1, g1)
	}
}

func TestIntegration_AD37_AChangedRecordTakesTheFullPath(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	seedPinnedRevision(t, p, s)
	pr := NewProcessor(p)
	res := "R-AD37B"
	base := time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second)
	insertLiveReceivedAt(t, p, s, "b-gi", "GI", pay(res, "1408", "Example", "Guest", "F1", "260101", "260105"), base, false)
	process(t, pr, s)

	cases := []struct{ id, body, what string }{
		{"b-name", pay(res, "1408", "Renamed", "Guest", "F1", "260101", "260105"), "a changed surname"},
		{"b-date", pay(res, "1408", "Renamed", "Guest", "F1", "260101", "260107"), "a changed departure"},
		{"b-sharer", `{"reservation":"` + res + `","room":"1408","arrival_raw":"260101","departure_raw":"260107","sharers":[` +
			`{"external_guest_id":"G1","first_name":"Guest","last_name":"Renamed","is_primary":true},` +
			`{"external_guest_id":"G2","first_name":"Second","last_name":"Occupant","is_primary":false}]}`, "new occupants (the first record had no external ids, so they are added beside the old primary)"},
	}
	for i, c := range cases {
		before := FastPathApplied()
		insertLiveReceivedAt(t, p, s, c.id, "GC", c.body, base.Add(time.Duration(i+1)*time.Minute), false)
		process(t, pr, s)
		if FastPathApplied() != before {
			t.Fatalf("%s must not take the cheap path", c.what)
		}
	}
	var last, dep string
	var n int
	if err := p.QueryRow(context.Background(), `SELECT (SELECT last_name_norm FROM iam_v2.stay_guests g WHERE g.stay_id=s.id AND is_primary), s.departure::text,
		(SELECT count(*) FROM iam_v2.stay_guests g WHERE g.stay_id=s.id)::int
		FROM iam_v2.stays s WHERE s.pms_interface_id=$1 AND s.external_reservation_id=$2`, s.iface, res).Scan(&last, &dep, &n); err != nil {
		t.Fatal(err)
	}
	if last != "renamed" && last != "RENAMED" || dep != "2026-01-07" || n != 3 {
		t.Fatalf("the changes were applied in full: surname %q departure %s occupants %d", last, dep, n)
	}
	// Once those are the mirrored state, repeating the same sharer list is unchanged and cheap.
	before := FastPathApplied()
	insertLiveReceivedAt(t, p, s, "b-same", "GC", cases[2].body, base.Add(10*time.Minute), false)
	process(t, pr, s)
	if FastPathApplied() != before+1 {
		t.Fatal("a repeated identical sharer list takes the cheap path")
	}
}
