package pmsd

import (
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// GUEST ATTRIBUTES FROM THE PROPERTY-MAPPED FIAS FIELDS.
//
// This property's Protel maps its travel agent (AG) to A0 and its VIP flag (VP) to A1, on GI and GC. They are
// what package rules for VIP guests and a travel agent's guests are judged against, so they must arrive -- but
// they are attributes, not identity, and an unusable one must never stop the feed.

func TestLinkRecordsAskForTravelAgentAndVIPOnArrivalAndChange(t *testing.T) {
	lrs := pms.BuildLRs()
	for _, want := range []string{"LR|RIGI|FLRNG#GNGFGAGDA0A1|", "LR|RIGC|FLRNG#GNGFGAGDA0A1|", "LR|RIGO|FLRNG#|"} {
		found := false
		for _, lr := range lrs {
			found = found || lr == want
		}
		if !found {
			t.Errorf("link records %v do not include %s", lrs, want)
		}
	}
}

func typed(t *testing.T, body string) typedDomainFields {
	t.Helper()
	pr, err := parseStrictRecord(body)
	if err != nil {
		t.Fatalf("parse %q: %v", body, err)
	}
	f, err := extractTypedDomainFields(pr)
	if err != nil {
		t.Fatalf("extract %q: %v", body, err)
	}
	return f
}

func TestGuestAttributes_ParsedFromA0AndA1(t *testing.T) {
	f := typed(t, "GI|RN1408|G#1|GNDoe|A0Sunny Tours |A1VP|")
	if f.TravelAgent == nil || *f.TravelAgent != "Sunny Tours" {
		t.Fatalf("travel agent = %v", f.TravelAgent)
	}
	if f.VIP == nil || !*f.VIP {
		t.Fatalf("vip = %v", f.VIP)
	}
}

func TestGuestAttributes_VIPFlagValues(t *testing.T) {
	for flag, want := range map[string]bool{
		"": false, "0": false, "N": false, "no": false, "False": false,
		"1": true, "Y": true, "VP": true, "V1": true, "VIP": true,
	} {
		f := typed(t, "GC|RN1|G#1|A1"+flag+"|")
		if f.VIP == nil || *f.VIP != want {
			t.Errorf("A1=%q: vip = %v, want %v", flag, f.VIP, want)
		}
	}
}

func TestGuestAttributes_AbsentIsNotStated(t *testing.T) {
	f := typed(t, "GI|RN1408|G#1|GNDoe|")
	if f.TravelAgent != nil || f.VIP != nil {
		t.Fatalf("absent fields must be nil (not stated), got %v %v", f.TravelAgent, f.VIP)
	}
	// Present but empty travel agent is a STATEMENT: no agent.
	f = typed(t, "GC|RN1408|G#1|A0|")
	if f.TravelAgent == nil || *f.TravelAgent != "" {
		t.Fatalf("stated empty travel agent = %v", f.TravelAgent)
	}
}

func TestGuestAttributes_UnusableValueIsDroppedNotAFault(t *testing.T) {
	// duplicated: ambiguous, so not stated -- and the record is still usable
	f := typed(t, "GI|RN1408|G#1|A0One|A0Two|A1Y|A1N|")
	if f.TravelAgent != nil || f.VIP != nil {
		t.Fatalf("duplicated attributes must be dropped, got %v %v", f.TravelAgent, f.VIP)
	}
	if f.Room != "1408" || f.Reservation != "1" {
		t.Fatalf("identity lost: %+v", f)
	}
	// overlong: never truncated, dropped
	f = typed(t, "GI|RN1408|G#1|A0"+strings.Repeat("x", maxTravelAgentLen+1)+"|")
	if f.TravelAgent != nil {
		t.Fatal("an overlong travel agent must be dropped, not truncated")
	}
}

func TestGuestAttributes_PayloadCarriesThemOnlyWhenStated(t *testing.T) {
	ta, vip := "Sunny Tours", true
	ev := Event{ReservationRef: "RES1", RoomNumber: "101", TravelAgent: &ta, VIP: &vip}
	got := string(eventPayloadJSON(ev))
	if !strings.Contains(got, `"travel_agent":"Sunny Tours"`) || !strings.Contains(got, `"vip":true`) {
		t.Fatalf("payload %s lacks the attributes", got)
	}
	no := false
	ev = Event{ReservationRef: "RES1", RoomNumber: "101", VIP: &no}
	if got = string(eventPayloadJSON(ev)); !strings.Contains(got, `"vip":false`) || strings.Contains(got, "travel_agent") {
		t.Fatalf("a stated non-VIP must persist as false and an unstated agent must be absent: %s", got)
	}
}

// THE LINK DESCRIPTION IS THE VERIFIED ONE. pmsd once sent "LD|..|pmsd|V#1|RT4|": no IF (Interface Family)
// field at all, although IF is what the PMS uses to activate an interface's functions. The record must be the
// handshake verified on this property's Protel for both the feed and posting.
func TestLinkDescriptionIsTheVerifiedHandshake(t *testing.T) {
	if got, want := pms.VerifiedLD("261001", "120000"), "LD|DA261001|TI120000|IFPB|V#1.13|RT4|"; got != want {
		t.Fatalf("LD = %q, want %q", got, want)
	}
}
