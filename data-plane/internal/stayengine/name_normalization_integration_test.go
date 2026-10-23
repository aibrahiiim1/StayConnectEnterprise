//go:build integration

package stayengine

// Never insert a `_norm` value directly here: everything goes in as a PMS payload through the real
// Processor, so the stored form is checked against the guest-side normalizer.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/namenorm"
)

// probeMatches copies the predicate of the resolver's probeInterface (cmd/scd/phase3_auth.go; a main
// package, so not importable) and reports how many stays the typed room and surname would find.
func probeMatches(t *testing.T, p *pgxpool.Pool, s scope, typedRoom, typedLast string) int {
	t.Helper()
	var n int
	err := p.QueryRow(context.Background(), `
		SELECT count(*) FROM iam_v2.stays s
		 WHERE s.tenant_id=$1 AND s.site_id=$2 AND s.pms_interface_id=$3
		   AND s.status IN ('IN_HOUSE','POST_STAY_ACTIVE')
		   AND s.normalized_room_number = $4
		   AND EXISTS (SELECT 1 FROM iam_v2.stay_guests g
		                WHERE g.stay_id = s.id AND g.last_name_norm = $5)`,
		s.tenant, s.site, s.iface,
		namenorm.Room(typedRoom), namenorm.Name(typedLast)).Scan(&n)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	return n
}

func guestRow(t *testing.T, p *pgxpool.Pool, s scope, res string) (first, last, display string, rows int) {
	t.Helper()
	ctx := context.Background()
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.stay_guests g
		JOIN iam_v2.stays st ON st.id=g.stay_id
		WHERE st.pms_interface_id=$1 AND st.external_reservation_id=$2`, s.iface, res).Scan(&rows); err != nil {
		t.Fatalf("count guests: %v", err)
	}
	_ = p.QueryRow(ctx, `SELECT COALESCE(g.first_name_norm,''), COALESCE(g.last_name_norm,''), COALESCE(g.display_name,'')
		FROM iam_v2.stay_guests g JOIN iam_v2.stays st ON st.id=g.stay_id
		WHERE st.pms_interface_id=$1 AND st.external_reservation_id=$2 AND g.is_primary`,
		s.iface, res).Scan(&first, &last, &display)
	return
}

func TestIntegration_MixedCaseGuestIngestsCanonicalAndAuthenticates(t *testing.T) {
	p := pool(t)
	s := seed(t, p)
	pr := NewProcessor(p)

	insertLive(t, p, s, "E-GI-MIX", "GI", pay("RES-MIX", " 512 ", "  Okafor ", "Chinua", "F-MIX", "260101", "260105"))
	process(t, pr, s)

	first, last, display, rows := guestRow(t, p, s, "RES-MIX")
	if rows != 1 {
		t.Fatalf("expected exactly one guest row, got %d", rows)
	}
	if last != "OKAFOR" {
		t.Fatalf("last_name_norm = %q, want canonical %q", last, "OKAFOR")
	}
	if first != "CHINUA" {
		t.Fatalf("first_name_norm = %q, want canonical %q", first, "CHINUA")
	}
	// display_name keeps the PMS spelling (and padding); trimming it is a product decision, not auth.
	if display == namenorm.Name(display) && display != "" {
		t.Fatalf("display_name = %q looks canonicalized; it must preserve the PMS spelling, not be normalized", display)
	}
	if !strings.Contains(display, "Okafor") || !strings.Contains(display, "Chinua") {
		t.Fatalf("display_name = %q lost the PMS spelling of the guest's name", display)
	}
	if _, _, room := stayState(t, p, s, "RES-MIX"); room != "512" {
		t.Fatalf("normalized_room_number = %q, want %q", room, "512")
	}

	for _, typed := range []string{"OKAFOR", "okafor", "OkAfOr", "  okafor  ", " Okafor "} {
		if n := probeMatches(t, p, s, "512", typed); n != 1 {
			t.Fatalf("surname %q typed at the portal matched %d stays, want 1", typed, n)
		}
	}
	for _, typedRoom := range []string{"512", " 512 "} {
		if n := probeMatches(t, p, s, typedRoom, "okafor"); n != 1 {
			t.Fatalf("room %q matched %d stays, want 1", typedRoom, n)
		}
	}
}

func TestIntegration_WrongCredentialsStillDoNotMatch(t *testing.T) {
	p := pool(t)
	s := seed(t, p)
	pr := NewProcessor(p)
	insertLive(t, p, s, "E-GI-WRONG", "GI", pay("RES-WRONG", "601", "Okafor", "Chinua", "F-W", "260101", "260105"))
	process(t, pr, s)

	for _, c := range []struct{ room, last, why string }{
		{"601", "Okafur", "a different surname"},
		{"601", "Okafo", "a prefix of the surname"},
		{"602", "Okafor", "the right surname in the wrong room"},
		{"601", "", "an empty surname"},
	} {
		if n := probeMatches(t, p, s, c.room, c.last); n != 0 {
			t.Fatalf("%s matched %d stays, want 0 -- normalization must not widen who authenticates", c.why, n)
		}
	}
}

func TestIntegration_NormalizationDoesNotCrossScopes(t *testing.T) {
	p := pool(t)
	a := seed(t, p)
	b := seed(t, p)
	pr := NewProcessor(p)

	insertLive(t, p, a, "E-GI-A", "GI", pay("RES-A", "700", "Okafor", "Chinua", "F-A", "260101", "260105"))
	process(t, pr, a)

	for _, typed := range []string{"OKAFOR", "okafor"} {
		if n := probeMatches(t, p, b, "700", typed); n != 0 {
			t.Fatalf("scope B matched %d of scope A's stays for %q -- confinement broken", n, typed)
		}
	}
	if n := probeMatches(t, p, a, "700", "okafor"); n != 1 {
		t.Fatalf("scope A should match its own stay, got %d", n)
	}
}

// The sharer lookup finds an occupant by name when the PMS sends no guest id, so it must normalize
// like the writes or a re-sent occupant is inserted twice.
func TestIntegration_MixedCaseSharerUpdatesInsteadOfDuplicating(t *testing.T) {
	p := pool(t)
	s := seed(t, p)
	pr := NewProcessor(p)
	ctx := context.Background()

	sharer := func(last, first string) string {
		return fmt.Sprintf(
			`{"reservation":"RES-SH","room":"801","last_name":"Primary","first_name":"Pat","folio":"F-SH",`+
				`"arrival_raw":"260101","departure_raw":"260105",`+
				`"sharers":[{"last_name":%q,"first_name":%q,"is_primary":false}]}`, last, first)
	}

	insertLive(t, p, s, "E-SH-1", "GI", sharer("Adeyemi", "Bola"))
	process(t, pr, s)

	insertLive(t, p, s, "E-SH-2", "GC", sharer("ADEYEMI", "Bola"))
	process(t, pr, s)

	var occupants int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM iam_v2.stay_guests g
		JOIN iam_v2.stays st ON st.id=g.stay_id
		WHERE st.pms_interface_id=$1 AND st.external_reservation_id='RES-SH' AND NOT g.is_primary`,
		s.iface).Scan(&occupants); err != nil {
		t.Fatalf("count sharers: %v", err)
	}
	if occupants != 1 {
		t.Fatalf("got %d sharer rows, want 1 -- a re-sent occupant must update, never duplicate", occupants)
	}

	var sharerLast string
	_ = p.QueryRow(ctx, `SELECT COALESCE(g.last_name_norm,'') FROM iam_v2.stay_guests g
		JOIN iam_v2.stays st ON st.id=g.stay_id
		WHERE st.pms_interface_id=$1 AND st.external_reservation_id='RES-SH' AND NOT g.is_primary`,
		s.iface).Scan(&sharerLast)
	if sharerLast != "ADEYEMI" {
		t.Fatalf("sharer last_name_norm = %q, want canonical %q", sharerLast, "ADEYEMI")
	}
	if n := probeMatches(t, p, s, "801", "adeyemi"); n != 1 {
		t.Fatalf("sharer matched %d stays, want 1", n)
	}
}

func TestIntegration_RoomMoveUsesTheSameRoomContract(t *testing.T) {
	p := pool(t)
	s := seed(t, p)
	pr := NewProcessor(p)

	insertLive(t, p, s, "E-RM-1", "GI", pay("RES-RM", "a12", "Okafor", "Chinua", "F-RM", "260101", "260105"))
	process(t, pr, s)
	if _, _, room := stayState(t, p, s, "RES-RM"); room != "A12" {
		t.Fatalf("created room = %q, want canonical %q", room, "A12")
	}
	if n := probeMatches(t, p, s, "a12", "okafor"); n != 1 {
		t.Fatalf("alphanumeric room typed lower-case matched %d, want 1", n)
	}

	// A move is a GC with a different room; the engine derives OpRoomMove (resolve.go), there is no "RM" type.
	insertLive(t, p, s, "E-RM-2", "GC", pay("RES-RM", " b7 ", "Okafor", "Chinua", "F-RM", "260101", "260105"))
	process(t, pr, s)
	if _, _, room := stayState(t, p, s, "RES-RM"); room != "B7" {
		t.Fatalf("moved room = %q, want canonical %q", room, "B7")
	}
	if n := probeMatches(t, p, s, "B7", "OKAFOR"); n != 1 {
		t.Fatalf("after the move the guest matched %d stays, want 1", n)
	}
	if n := probeMatches(t, p, s, "A12", "OKAFOR"); n != 0 {
		t.Fatalf("the old room still matched %d stays, want 0", n)
	}
}

// Sign-in must keep resolving from the published mirror while the PMS transport is deliberately offline.
func TestIntegration_AuthenticatesFromMirrorWhilePMSTransportOffline(t *testing.T) {
	p := pool(t)
	s := seed(t, p)
	pr := NewProcessor(p)
	ctx := context.Background()

	insertLive(t, p, s, "E-OFF", "GI", pay("RES-OFF", "902", "Okafor", "Chinua", "F-OFF", "260101", "260105"))
	process(t, pr, s)

	if _, err := p.Exec(ctx, `UPDATE iam_v2.pms_interface_runtime
		SET transport_status='DISCONNECTED', transport_error_code='DIAL_FAILED',
		    disconnected_since=now(), last_connected_at=now() - interval '6 hours',
		    last_complete_sync_at=now() - interval '6 hours'
		WHERE tenant_id=$1 AND site_id=$2 AND pms_interface_id=$3`, s.tenant, s.site, s.iface); err != nil {
		t.Fatalf("mark transport offline: %v", err)
	}

	var status string
	_ = p.QueryRow(ctx, `SELECT transport_status FROM iam_v2.pms_interface_runtime
		WHERE pms_interface_id=$1`, s.iface).Scan(&status)
	if status != "DISCONNECTED" {
		t.Fatalf("fixture did not take the transport offline: %q", status)
	}

	if n := probeMatches(t, p, s, "902", "okafor"); n != 1 {
		t.Fatalf("guest matched %d stays with the PMS offline, want 1 -- "+
			"a published mirror must keep serving sign-in while the transport is intentionally down", n)
	}
}
