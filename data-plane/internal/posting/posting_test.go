package posting

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// ---------------------------------------------------------------- flags

func TestConfig_DefaultIsDarkAndClosed(t *testing.T) {
	c := DefaultConfig()
	if c.PostingOn() || c.OutboxOn() || c.ReviewOn() || c.TransmitOn() {
		t.Fatalf("the zero config must have every phase-4 surface OFF: %s", c.SafeFlagSummary())
	}
	if !c.Dark() {
		t.Fatal("the zero config must be DARK")
	}
}

func TestConfig_ChildWithoutMasterFailsClosed(t *testing.T) {
	env := map[string]string{EnvPhase4Posting: "true"}
	if _, err := LoadConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("a child flag ON while the master is OFF must be a startup error")
	}
}

func TestConfig_TransmitWithoutOutboxFailsClosed(t *testing.T) {
	env := map[string]string{EnvPhase4Master: "true", EnvPhase4Transmit: "true"}
	if _, err := LoadConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("transmit ON while the outbox lane is OFF must be a startup error")
	}
}

func TestConfig_MalformedBooleanIsAStartupError(t *testing.T) {
	env := map[string]string{EnvPhase4Master: "yes-please"}
	if _, err := LoadConfigFromEnv(func(k string) string { return env[k] }); err == nil {
		t.Fatal("a malformed financial flag must fail closed, never default")
	}
}

func TestConfig_TransmitNeedsAllThreeFlags(t *testing.T) {
	// Each of the three, alone or in pairs, must leave the process DARK.
	for _, c := range []Config{
		{MasterEnabled: true},
		{MasterEnabled: true, OutboxEnabled: true},
		{MasterEnabled: true, TransmitEnabled: true},
		{OutboxEnabled: true, TransmitEnabled: true},
	} {
		if !c.Dark() {
			t.Fatalf("expected DARK for %s", c.SafeFlagSummary())
		}
	}
	full := Config{MasterEnabled: true, OutboxEnabled: true, TransmitEnabled: true}
	if full.Dark() {
		t.Fatal("all three ON must not be DARK")
	}
}

// ---------------------------------------------------------------- PS golden wire

// The exact bytes. If this test changes, the wire changed, and the wire is the contract.
func TestBuildPS_GoldenWire(t *testing.T) {
	got, err := BuildPS(PSRequest{RN: "1421", GNumber: "5", AmountMinor: 1000, PostingCode: "WIFI", PNumber: 7})
	if err != nil {
		t.Fatalf("BuildPS: %v", err)
	}
	const want = "PS|RN1421|G#5|TA1000|PTD|SOWIFI|CTWIFI|P#7|WSSTAYCONNECT|"
	if got != want {
		t.Fatalf("PS wire mismatch:\n got %q\nwant %q", got, want)
	}
}

func TestBuildPS_FieldOrderIsFixed(t *testing.T) {
	body, err := BuildPS(PSRequest{RN: "9", GNumber: "3", AmountMinor: 1, PostingCode: "X", PNumber: 1})
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"RN", "G#", "TA", "PT", "SO", "CT", "P#", "WS"}
	at := -1
	for _, id := range order {
		i := strings.Index(body, "|"+id)
		if i <= at {
			t.Fatalf("field %s is out of contract order in %q", id, body)
		}
		at = i
	}
}

func TestBuildPS_NoCurrencyOnTheWire(t *testing.T) {
	body, err := BuildPS(PSRequest{RN: "1", GNumber: "1", AmountMinor: 250, PostingCode: "WIFI", PNumber: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"USD", "EUR", "GBP", "|CU", "|CY"} {
		if strings.Contains(body, banned) {
			t.Fatalf("a currency reached the FIAS wire: %q in %q", banned, body)
		}
	}
	if !strings.Contains(body, "|TA250|") {
		t.Fatalf("TA must be the integer minor-unit amount: %q", body)
	}
}

func TestBuildPS_AmountIsIntegerMinorUnits(t *testing.T) {
	body, _ := BuildPS(PSRequest{RN: "1", GNumber: "1", AmountMinor: 100, PostingCode: "W", PNumber: 1})
	if strings.Contains(body, ".") || strings.Contains(body, ",") {
		t.Fatalf("TA must carry no decimal or thousands separator: %q", body)
	}
}

func TestBuildPS_RefusesUntransmissibleFields(t *testing.T) {
	base := PSRequest{RN: "101", GNumber: "5", AmountMinor: 100, PostingCode: "WIFI", PNumber: 1}
	cases := []struct {
		name string
		mut  func(*PSRequest)
		code Code
	}{
		{"empty RN", func(r *PSRequest) { r.RN = "" }, ErrWireFieldInvalid},
		{"RN with a field delimiter", func(r *PSRequest) { r.RN = "10|1" }, ErrWireFieldInvalid},
		{"RN with a control byte", func(r *PSRequest) { r.RN = "10\x02" }, ErrWireFieldInvalid},
		{"over-long RN", func(r *PSRequest) { r.RN = strings.Repeat("9", maxWireField+1) }, ErrWireFieldInvalid},
		{"empty G#", func(r *PSRequest) { r.GNumber = "" }, ErrWireFieldInvalid},
		{"G# with a delimiter", func(r *PSRequest) { r.GNumber = "a|b" }, ErrWireFieldInvalid},
		{"zero amount", func(r *PSRequest) { r.AmountMinor = 0 }, ErrAmountInvalid},
		{"negative amount", func(r *PSRequest) { r.AmountMinor = -1 }, ErrAmountInvalid},
		{"empty CT", func(r *PSRequest) { r.PostingCode = "  " }, ErrWireFieldInvalid},
		{"CT over the contract bound of 20", func(r *PSRequest) { r.PostingCode = strings.Repeat("x", 21) }, ErrWireFieldInvalid},
		{"no P#", func(r *PSRequest) { r.PNumber = 0 }, ErrWireFieldInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mut(&r)
			body, err := BuildPS(r)
			if err == nil {
				t.Fatalf("expected refusal, built %q", body)
			}
			if CodeOf(err) != tc.code {
				t.Fatalf("expected %s, got %s", tc.code, CodeOf(err))
			}
			if body != "" {
				t.Fatalf("a refused PS must produce no bytes, got %q", body)
			}
		})
	}
}

// ---------------------------------------------------------------- PA correlation

func TestParsePA_CorrelatesByPNumber(t *testing.T) {
	pa, err := ParsePA("PA|P#7|ASOK|")
	if err != nil {
		t.Fatal(err)
	}
	if pa.PNumber != 7 || pa.AS != "OK" || !pa.Posted() {
		t.Fatalf("unexpected parse: %+v", pa)
	}
}

func TestParsePA_AcceptsExactlyTheApprovedCatalog(t *testing.T) {
	for _, as := range []string{"OK", "NG", "NA", "NP", "NR", "RY", "UR"} {
		if _, err := ParsePA("PA|P#1|AS" + as + "|"); err != nil {
			t.Fatalf("approved status %s was refused: %v", as, err)
		}
	}
	for _, as := range []string{"ZZ", "ok", "OKAY", "", "YES", "ACCEPTED"} {
		if _, err := ParsePA("PA|P#1|AS" + as + "|"); err == nil {
			t.Fatalf("status %q is outside the catalog and must be refused", as)
		}
	}
}

func TestParsePA_OnlyOKMeansPosted(t *testing.T) {
	for _, as := range []string{"NG", "NA", "NP", "NR", "RY", "UR"} {
		pa, err := ParsePA("PA|P#1|AS" + as + "|")
		if err != nil {
			t.Fatal(err)
		}
		if pa.Posted() {
			t.Fatalf("AS %s must not be read as posted", as)
		}
	}
}

func TestParsePA_RefusesAmbiguousOrUnmatchable(t *testing.T) {
	cases := map[string]Code{
		"PA|ASOK|":              ErrPACorrelation,   // no P# at all
		"PA|P#|ASOK|":           ErrPACorrelation,   // empty P#
		"PA|P#abc|ASOK|":        ErrPACorrelation,   // unparseable P#
		"PA|P#0|ASOK|":          ErrPACorrelation,   // no P# is ever zero
		"PA|P#1|":               ErrPAStatusUnknown, // no AS
		"PA|P#1|P#2|ASOK|":      ErrPAAmbiguous,     // two different P#
		"PA|P#1|ASOK|ASNG|":     ErrPAAmbiguous,     // two different AS
		"GI|P#1|ASOK|":          ErrPACorrelation,   // not a PA at all
		"PA|RN101|P#1|ASBOGUS|": ErrPAStatusUnknown,
	}
	for body, want := range cases {
		if _, err := ParsePA(body); err == nil {
			t.Fatalf("%q must be refused", body)
		} else if CodeOf(err) != want {
			t.Fatalf("%q: expected %s, got %s", body, want, CodeOf(err))
		}
	}
}

// A PA that carries a room number matching a DIFFERENT posting must never be correlated by that room
// number. Two guests in one room is the ordinary case this protects.
func TestParsePA_NeverUsesRoomNumberAsIdentity(t *testing.T) {
	pa, err := ParsePA("PA|RN101|P#42|ASOK|")
	if err != nil {
		t.Fatal(err)
	}
	if pa.PNumber != 42 {
		t.Fatalf("correlation must use P#, got %d", pa.PNumber)
	}
	// Same room, two different postings: they must correlate to different attempts, never to each other.
	other, err := ParsePA("PA|RN101|P#43|ASNG|")
	if err != nil {
		t.Fatal(err)
	}
	if other.PNumber == pa.PNumber {
		t.Fatal("two answers sharing a room number must not correlate to the same posting")
	}
	// and a PA with a room number but NO P# is unmatchable, not "matched by room"
	if _, err := ParsePA("PA|RN101|ASOK|"); err == nil {
		t.Fatal("a PA carrying only a room number must be refused as uncorrelatable")
	}
}

// ---------------------------------------------------------------- DARK transport

// recordingTransport is the "even if the worker is running" stand-in: it would happily accept a send.
type recordingTransport struct{ sends []string }

func (r *recordingTransport) SendPS(_ context.Context, _ string, pn int64, body string) (*PA, error) {
	r.sends = append(r.sends, body)
	return &PA{PNumber: pn, AS: "OK"}, nil
}

func TestDarkGuard_RefusesEveryTransmissionWhileDark(t *testing.T) {
	inner := &recordingTransport{}
	g := NewDarkGuard(DefaultConfig(), inner)
	body, _ := BuildPS(PSRequest{RN: "101", GNumber: "5", AmountMinor: 100, PostingCode: "WIFI", PNumber: 9})

	pa, err := g.SendPS(context.Background(), "iface", 9, body)
	if err == nil {
		t.Fatal("a DARK transport must refuse")
	}
	if pa != nil {
		t.Fatal("a refused transmission must return no answer")
	}
	if CodeOf(err) != ErrDarkNoEgress {
		t.Fatalf("expected dark_no_egress, got %s", CodeOf(err))
	}
	// A DARK refusal is PROOF nothing was sent, so it must classify as not-transmitted and never as UNKNOWN.
	if !NotTransmitted(err) {
		t.Fatal("a DARK refusal must classify as NOT transmitted")
	}
	if len(inner.sends) != 0 {
		t.Fatalf("the inner transport was reached %d times while DARK", len(inner.sends))
	}
	if g.Refusals() != 1 {
		t.Fatalf("expected 1 recorded refusal, got %d", g.Refusals())
	}
	// and the thing it refused really was a complete financial record
	if !strings.HasPrefix(g.LastRefusedBody(), "PS|") {
		t.Fatalf("the guard should have refused a real PS, got %q", g.LastRefusedBody())
	}
}

func TestDarkGuard_NilInnerIsStillSafe(t *testing.T) {
	g := NewDarkGuard(DefaultConfig(), nil)
	if _, err := g.SendPS(context.Background(), "iface", 1, "PS|RN1|G#1|TA1|PTD|SOWIFI|CTW|P#1|WSSTAYCONNECT|"); err == nil {
		t.Fatal("a DARK guard with no transport must still refuse rather than panic")
	}
}

func TestDarkGuard_RefusesNonPSEvenWhenEnabled(t *testing.T) {
	on := Config{MasterEnabled: true, OutboxEnabled: true, TransmitEnabled: true}
	g := NewDarkGuard(on, &recordingTransport{})
	for _, body := range []string{"LA|", "DR|", "GI|RN101|", ""} {
		if _, err := g.SendPS(context.Background(), "iface", 9, body); err == nil {
			t.Fatalf("only a PS may pass the financial transport; %q was accepted", body)
		}
	}
}

func TestDarkError_CarriesBothClassifications(t *testing.T) {
	err := refusedDark("test")
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrDarkNoEgress {
		t.Fatal("a dark refusal must remain a typed posting error")
	}
	if !errors.Is(err, ErrNotTransmitted) {
		t.Fatal("a dark refusal must also be ErrNotTransmitted")
	}
}

// ---------------------------------------------------------------- the gate, on synthetic snapshots

func exp(v int16) *int16 { return &v }

func goodPair() (Pinned, Snapshot) {
	p := Pinned{
		TenantID: "t", SiteID: "s", PMSInterfaceID: "i",
		PostingInterfaceRevisionID: "rev", StayID: "stay", FolioID: "folio",
		PurchaseID: "pur", SettlementID: "set", PackageRevisionID: "pkg", SettlementMappingID: "map",
		AmountMinor: 100, Currency: "USD", CurrencyExponent: 2,
		RN: "101", GNumber: "5", PostingCode: "WIFI", IdempotencyKey: "k1",
	}
	s := Snapshot{
		InterfaceLifecycleState: "ACTIVE", InterfaceCurrentRevision: "rev",
		FolioIdentityStrategy: "GLOBALLY_UNIQUE", InterfaceCurrency: "USD", InterfaceExponent: exp(2),
		PurchaseCurrency: "USD", PurchaseExponent: exp(2), PurchaseState: "GRANTED",
		PackageCurrency: "USD", PackageExponent: exp(2),
		StayStatus: "IN_HOUSE", StayPostingAllowed: true, StayLifecycleVersion: 1,
		StayRoomNumber: "101", FolioExternalID: "5",
		ConnectorKind: "protel-fias", FreshnessBlock: "",
	}
	return p, s
}

func TestGate_AcceptsAFullyOnboardedInScopeCharge(t *testing.T) {
	p, s := goodPair()
	if err := (Gate{}).Check(p, s); err != nil {
		t.Fatalf("a complete, in-scope, onboarded charge must pass: %v", err)
	}
}

func TestGate_FailClosedMatrix(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Pinned, *Snapshot)
		code Code
	}{
		{"folio strategy UNSET", func(_ *Pinned, s *Snapshot) { s.FolioIdentityStrategy = "UNSET" }, ErrFolioStrategyUnset},
		{"folio strategy absent", func(_ *Pinned, s *Snapshot) { s.FolioIdentityStrategy = "" }, ErrFolioStrategyUnset},
		{"interface not financially onboarded", func(_ *Pinned, s *Snapshot) { s.InterfaceCurrency = ""; s.InterfaceExponent = nil }, ErrInterfaceNoCurrency},
		{"missing RN", func(p *Pinned, s *Snapshot) { p.RN = ""; s.StayRoomNumber = "" }, ErrRNMissing},
		{"blank RN", func(p *Pinned, s *Snapshot) { p.RN = "   "; s.StayRoomNumber = "   " }, ErrRNMissing},
		{"missing G#", func(p *Pinned, s *Snapshot) { p.GNumber = ""; s.FolioExternalID = "" }, ErrGNumberMissing},
		{"RN not wire safe", func(p *Pinned, s *Snapshot) { p.RN = "1|1"; s.StayRoomNumber = "1|1" }, ErrRNNotWireSafe},
		{"G# not wire safe", func(p *Pinned, s *Snapshot) { p.GNumber = "a\x01"; s.FolioExternalID = "a\x01" }, ErrGNumberNotWireSafe},
		{"RN disagrees with the pinned stay", func(p *Pinned, _ *Snapshot) { p.RN = "999" }, ErrEvidenceStale},
		{"G# disagrees with the pinned folio", func(p *Pinned, _ *Snapshot) { p.GNumber = "999" }, ErrEvidenceStale},
		{"stay not IN_HOUSE", func(_ *Pinned, s *Snapshot) { s.StayStatus = "CHECKED_OUT"; s.StayPostingAllowed = false }, ErrStayNotInHouse},
		{"posting not allowed", func(_ *Pinned, s *Snapshot) { s.StayPostingAllowed = false }, ErrPostingNotAllowed},
		{"stale stay lifecycle", func(p *Pinned, _ *Snapshot) { v := 9; p.ExpectStayLifecycleVersion = &v }, ErrEvidenceStale},
		{"stale purchase state", func(p *Pinned, _ *Snapshot) { p.ExpectPurchaseState = "PENDING" }, ErrEvidenceStale},
		{"superseded interface revision", func(_ *Pinned, s *Snapshot) { s.InterfaceCurrentRevision = "rev2" }, ErrEvidenceStale},
		{"retired settlement mapping", func(_ *Pinned, s *Snapshot) { s.SettlementMappingRetired = true }, ErrEvidenceStale},
		{"posting currency mismatch", func(p *Pinned, _ *Snapshot) { p.Currency = "EUR" }, ErrCurrencyMismatch},
		{"posting exponent mismatch", func(p *Pinned, _ *Snapshot) { p.CurrencyExponent = 3 }, ErrExponentMismatch},
		{"purchase currency mismatch", func(_ *Pinned, s *Snapshot) { s.PurchaseCurrency = "EUR" }, ErrCurrencyMismatch},
		{"purchase exponent mismatch", func(_ *Pinned, s *Snapshot) { s.PurchaseExponent = exp(3) }, ErrExponentMismatch},
		{"package currency mismatch", func(_ *Pinned, s *Snapshot) { s.PackageCurrency = "EUR" }, ErrCurrencyMismatch},
		{"package exponent mismatch", func(_ *Pinned, s *Snapshot) { s.PackageExponent = exp(0) }, ErrExponentMismatch},
		{"package currency absent", func(_ *Pinned, s *Snapshot) { s.PackageCurrency = ""; s.PackageExponent = nil }, ErrCurrencyMismatch},
		{"zero amount", func(p *Pinned, _ *Snapshot) { p.AmountMinor = 0 }, ErrAmountInvalid},
		{"negative amount", func(p *Pinned, _ *Snapshot) { p.AmountMinor = -100 }, ErrAmountInvalid},
		{"interface decommissioned", func(_ *Pinned, s *Snapshot) { s.InterfaceLifecycleState = "DECOMMISSIONED" }, ErrInterfaceDecomm},
		{"interface draining refuses NEW work", func(_ *Pinned, s *Snapshot) { s.InterfaceLifecycleState = "DRAINING" }, ErrInterfaceInactive},
		{"transport axis down", func(_ *Pinned, s *Snapshot) { s.FreshnessBlock = "TRANSPORT_DISCONNECTED" }, ErrInterfaceNotFresh},
		{"heartbeat axis stale", func(_ *Pinned, s *Snapshot) { s.FreshnessBlock = "TRANSPORT_HEARTBEAT_STALE" }, ErrInterfaceNotFresh},
		{"continuity axis broken", func(_ *Pinned, s *Snapshot) { s.FreshnessBlock = "CONTINUITY_GAP_DETECTED" }, ErrInterfaceNotFresh},
		{"sync axis out of sync", func(_ *Pinned, s *Snapshot) { s.FreshnessBlock = "SYNC_RESYNC_REQUIRED" }, ErrInterfaceNotFresh},
		{"pin coherence axis broken", func(_ *Pinned, s *Snapshot) { s.FreshnessBlock = "PIN_RESYNC_IN_FLIGHT" }, ErrInterfaceNotFresh},
		{"no runtime state at all", func(_ *Pinned, s *Snapshot) { s.FreshnessBlock = "RUNTIME_UNKNOWN" }, ErrInterfaceNotFresh},
		{"posting code not wire safe", func(p *Pinned, _ *Snapshot) { p.PostingCode = "WI|FI" }, ErrWireFieldInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, s := goodPair()
			tc.mut(&p, &s)
			err := (Gate{}).Check(p, s)
			if err == nil {
				t.Fatal("the gate must refuse")
			}
			if CodeOf(err) != tc.code {
				t.Fatalf("expected %s, got %s (%v)", tc.code, CodeOf(err), err)
			}
		})
	}
}

// The gate must never accept an FX conversion, no matter how "obviously" convertible the pair looks.
func TestGate_NeverConvertsBetweenCurrencies(t *testing.T) {
	for _, pair := range [][2]string{{"USD", "EUR"}, {"EUR", "USD"}, {"USD", "usd"}, {"GBP", "USD"}} {
		p, s := goodPair()
		p.Currency = pair[0]
		s.InterfaceCurrency, s.PurchaseCurrency, s.PackageCurrency = pair[1], pair[1], pair[1]
		if err := (Gate{}).Check(p, s); err == nil {
			t.Fatalf("%s posted against a %s interface must be refused", pair[0], pair[1])
		}
	}
}

// ---------------------------------------------------------------- hardening corrections

// Contract section 10: AUTH_DISABLED closes guest AUTHENTICATION, not the folio, and DRAINING must still
// drain. The first version of this gate refused both, which would have stranded authorized money.
func TestGate_LifecycleMatrixMatchesTheContract(t *testing.T) {
	cases := []struct {
		state             string
		createOK, drainOK bool
	}{
		{"ACTIVE", true, true},
		{"AUTH_DISABLED", true, true},
		{"DRAINING", false, true},
		{"DECOMMISSIONED", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			p, s := goodPair()
			s.InterfaceLifecycleState = tc.state
			if got := (Gate{}).CheckFor(PurposeCreate, p, s) == nil; got != tc.createOK {
				t.Fatalf("%s create: expected allowed=%v", tc.state, tc.createOK)
			}
			if got := (Gate{}).CheckFor(PurposeExecute, p, s) == nil; got != tc.drainOK {
				t.Fatalf("%s drain: expected allowed=%v", tc.state, tc.drainOK)
			}
		})
	}
}

// Contract section 9a fixes this posting path at exponent 2, and the Gate-3A evidence is a USD 1.00 debit.
func TestGate_FIASPathIsExponentTwoOnly(t *testing.T) {
	for _, exp := range []int16{0, 1, 3, 4} {
		p, s := goodPair()
		p.CurrencyExponent = exp
		s.InterfaceExponent, s.PurchaseExponent, s.PackageExponent = &exp, &exp, &exp
		err := (Gate{}).Check(p, s)
		if CodeOf(err) != ErrFIASExponent {
			t.Fatalf("exponent %d on protel-fias must be refused as unsupported, got %v", exp, err)
		}
	}
	// a non-FIAS connector is not constrained by the FIAS wire
	p, s := goodPair()
	three := int16(3)
	p.CurrencyExponent, s.ConnectorKind = 3, "some-other-connector"
	s.InterfaceExponent, s.PurchaseExponent, s.PackageExponent = &three, &three, &three
	if err := (Gate{}).Check(p, s); err != nil {
		t.Fatalf("a non-FIAS connector must not inherit the FIAS wire bound: %v", err)
	}
}

func TestBuildPS_CTIsBoundedAtTwenty(t *testing.T) {
	base := PSRequest{RN: "101", GNumber: "5", AmountMinor: 100, PNumber: 1}
	base.PostingCode = strings.Repeat("W", 20)
	if _, err := BuildPS(base); err != nil {
		t.Fatalf("a 20-character CT is within the contract bound: %v", err)
	}
	base.PostingCode = strings.Repeat("W", 21)
	if _, err := BuildPS(base); err == nil {
		t.Fatal("a 21-character CT exceeds the contract bound and must be refused")
	}
}

// wrongPNumberTransport answers with a P# that belongs to a different attempt. It is the shape of the
// defect that matters: everything about the answer looks valid except who it is for.
type wrongPNumberTransport struct{ answered int }

func (w *wrongPNumberTransport) SendPS(_ context.Context, _ string, pn int64, _ string) (*PA, error) {
	w.answered++
	return &PA{PNumber: pn + 1000, AS: "OK"}, nil
}

func TestDarkGuard_RefusesAPAForADifferentPNumber(t *testing.T) {
	on := Config{MasterEnabled: true, OutboxEnabled: true, TransmitEnabled: true}
	g := NewDarkGuard(on, &wrongPNumberTransport{})
	body, _ := BuildPS(PSRequest{RN: "101", GNumber: "5", AmountMinor: 100, PostingCode: "WIFI", PNumber: 7})
	pa, err := g.SendPS(context.Background(), "iface", 7, body)
	if err == nil || pa != nil {
		t.Fatal("a PA carrying another P# must never be returned as this attempt's answer")
	}
	if CodeOf(err) != ErrPAWrongPNumber {
		t.Fatalf("expected pa_wrong_p_number, got %s", CodeOf(err))
	}
	// and it must NOT be classified as not-transmitted: the PS really did go out
	if NotTransmitted(err) {
		t.Fatal("a mis-correlated answer follows a real transmission; it is not a not-sent failure")
	}
}

func TestDarkGuard_RefusesABodyThatDoesNotCarryTheAllocatedPNumber(t *testing.T) {
	on := Config{MasterEnabled: true, OutboxEnabled: true, TransmitEnabled: true}
	inner := &recordingTransport{}
	g := NewDarkGuard(on, inner)
	body, _ := BuildPS(PSRequest{RN: "101", GNumber: "5", AmountMinor: 100, PostingCode: "WIFI", PNumber: 7})
	if _, err := g.SendPS(context.Background(), "iface", 8, body); err == nil {
		t.Fatal("a PS body whose P# is not the allocated one must be refused before transmission")
	}
	if len(inner.sends) != 0 {
		t.Fatal("the inner transport must not be reached when the record and the allocation disagree")
	}
}
