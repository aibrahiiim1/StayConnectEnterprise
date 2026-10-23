package nftfoundation

// Installing the phase-3 foundation on a populated live ruleset must not disturb a single legacy guest.
// The nft binary is faked here; kernel behaviour is covered by internal/kerneltest.

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRuleset struct {
	sets     map[string][]string
	forward  []rule
	captive  []rule
	nextH    int
	cmds     []string
	failNext error
	noTable  bool
}

type rule struct {
	handle string
	text   string
}

func newFakeRuleset() *fakeRuleset {
	f := &fakeRuleset{sets: map[string][]string{}, nextH: 10}
	f.sets[legacySet] = []string{"br-guest|10.20.0.14", "br-guest|10.20.0.7", "br-lan|10.0.0.31"}
	f.forward = []rule{
		{f.h(), `ct state established,related accept`},
		{f.h(), `oifname "ens160" iifname . ip saddr @auth_ipv4 accept comment "authenticated guests"`},
		{f.h(), `iifname @guest_interfaces oifname "ens160" ip daddr @walled_garden_ip accept`},
	}
	f.captive = []rule{
		{f.h(), `iifname "br-guest" iifname . ip saddr != @auth_ipv4 ip daddr != @walled_garden_ip tcp dport 80 dnat ip to 10.20.0.1:8080`},
		{f.h(), `iifname "br-guest" iifname . ip saddr != @auth_ipv4 ip daddr != @walled_garden_ip tcp dport 443 dnat ip to 10.20.0.1:8443`},
	}
	return f
}

func (f *fakeRuleset) h() string {
	f.nextH++
	return itoa(f.nextH)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (f *fakeRuleset) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	f.cmds = append(f.cmds, joined)

	if f.noTable {
		return []byte("Error: No such file or directory"), errors.New("exit status 1")
	}
	switch {
	case strings.HasPrefix(joined, "-a list table"):
		return []byte(f.render()), nil
	case strings.HasPrefix(joined, "-a list chain"):
		return []byte(f.renderChain("forward", f.forward)), nil
	case strings.HasPrefix(joined, "-j list set"):
		set := args[len(args)-1]
		if _, ok := f.sets[set]; !ok {
			return []byte(""), errors.New("no such set")
		}
		return []byte(f.renderSetJSON(set)), nil
	}
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return []byte("Error: transaction refused"), err
	}
	return nil, f.apply(joined)
}

func (f *fakeRuleset) apply(buf string) error {
	type change func()
	var staged []change
	for _, cmd := range splitCommands(buf) {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		switch {
		case strings.HasPrefix(cmd, "add set inet stayconnect "+phase3Set):
			staged = append(staged, func() { f.sets[phase3Set] = nil })
		case strings.HasPrefix(cmd, "delete set inet stayconnect "):
			name := strings.TrimSpace(strings.TrimPrefix(cmd, "delete set inet stayconnect "))
			if _, ok := f.sets[name]; !ok {
				// real nft aborts the whole transaction when asked to delete a set that is not there
				return errors.New("nft: No such file or directory: delete set " + name)
			}
			staged = append(staged, func() { delete(f.sets, name) })
		case strings.HasPrefix(cmd, "add rule inet stayconnect forward"):
			body := cmd
			if i := strings.Index(cmd, " handle "); i >= 0 {
				// `add ... handle N <rule>`: insert after N
				rest := cmd[i+len(" handle "):]
				sp := strings.Index(rest, " ")
				anchor, text := rest[:sp], rest[sp+1:]
				staged = append(staged, func() { f.insertAfter(anchor, text) })
				continue
			}
			text := strings.TrimPrefix(body, "add rule inet stayconnect forward ")
			staged = append(staged, func() { f.forward = append(f.forward, rule{f.h(), text}) })
		case strings.HasPrefix(cmd, "delete rule inet stayconnect forward handle "):
			anchor := strings.TrimPrefix(cmd, "delete rule inet stayconnect forward handle ")
			staged = append(staged, func() { f.deleteForward(anchor) })
		case strings.HasPrefix(cmd, "replace rule inet stayconnect "+captiveChn+" handle "):
			rest := strings.TrimPrefix(cmd, "replace rule inet stayconnect "+captiveChn+" handle ")
			sp := strings.Index(rest, " ")
			anchor, text := rest[:sp], rest[sp+1:]
			staged = append(staged, func() { f.replaceCaptive(anchor, text) })
		default:
			return errors.New("nft: unknown command: " + cmd)
		}
	}
	for _, c := range staged {
		c()
	}
	return nil
}

func splitCommands(buf string) []string {
	var out []string
	depth, start := 0, 0
	for i, r := range buf {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		case ';':
			if depth == 0 {
				out = append(out, buf[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, buf[start:])
	return out
}

func (f *fakeRuleset) insertAfter(anchor, text string) {
	out := make([]rule, 0, len(f.forward)+1)
	for _, r := range f.forward {
		out = append(out, r)
		if r.handle == anchor {
			out = append(out, rule{f.h(), text})
		}
	}
	f.forward = out
}

func (f *fakeRuleset) deleteForward(anchor string) {
	out := f.forward[:0]
	for _, r := range f.forward {
		if r.handle != anchor {
			out = append(out, r)
		}
	}
	f.forward = out
}

func (f *fakeRuleset) replaceCaptive(anchor, text string) {
	for i := range f.captive {
		if f.captive[i].handle == anchor {
			f.captive[i].text = text
		}
	}
}

func (f *fakeRuleset) render() string {
	var b strings.Builder
	b.WriteString("table inet stayconnect {\n")
	for _, name := range []string{legacySet, phase3Set, "sc_render_fp"} {
		if _, ok := f.sets[name]; ok {
			b.WriteString("\tset " + name + " {\n\t\ttype ifname . ipv4_addr\n\t}\n")
		}
	}
	b.WriteString(f.renderChain("forward", f.forward))
	b.WriteString(f.renderChain(captiveChn, f.captive))
	b.WriteString("}\n")
	return b.String()
}

func (f *fakeRuleset) renderChain(name string, rules []rule) string {
	var b strings.Builder
	b.WriteString("\tchain " + name + " {\n")
	for _, r := range rules {
		b.WriteString("\t\t" + r.text + " # handle " + r.handle + "\n")
	}
	b.WriteString("\t}\n")
	return b.String()
}

func (f *fakeRuleset) renderSetJSON(set string) string {
	var elems []string
	for _, k := range f.sets[set] {
		i := strings.Index(k, "|")
		elems = append(elems, `{"elem":{"val":{"concat":["`+k[:i]+`","`+k[i+1:]+`"]},"timeout":0}}`)
	}
	return `{"nftables":[{"set":{"name":"` + set + `","elem":[` + strings.Join(elems, ",") + `]}}]}`
}

func newTestFoundation() (*Foundation, *fakeRuleset) {
	rs := newFakeRuleset()
	return &Foundation{NftPath: "nft", Run: rs.run}, rs
}

func TestInstallPreservesEveryLegacyAuthorization(t *testing.T) {
	f, rs := newTestFoundation()
	before := append([]string(nil), rs.sets[legacySet]...)

	rep, err := f.Install(context.Background())
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rep.Outcome != "INSTALLED" {
		t.Fatalf("outcome = %s", rep.Outcome)
	}
	if len(rs.sets[legacySet]) != len(before) {
		t.Fatalf("legacy authorizations changed: %v -> %v", before, rs.sets[legacySet])
	}
	for i := range before {
		if rs.sets[legacySet][i] != before[i] {
			t.Fatalf("legacy element %d changed: %s -> %s", i, before[i], rs.sets[legacySet][i])
		}
	}
	if len(rep.LegacyBefore) != 3 || len(rep.LegacyAfter) != 3 {
		t.Fatalf("the report does not record legacy parity: %+v", rep)
	}
}

// Nothing is flushed: a `delete table` or `flush` would take every legacy guest offline.
func TestInstallNeverFlushesOrRecreatesTheTable(t *testing.T) {
	f, rs := newTestFoundation()
	if _, err := f.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, c := range rs.cmds {
		for _, forbidden := range []string{"flush", "delete table", "add table", "delete set " + legacySet, "flush set"} {
			if strings.Contains(c, forbidden) {
				t.Fatalf("the install issued %q, which would disturb live guests: %s", forbidden, c)
			}
		}
	}
}

// The phase-3 set arrives empty, so installing while dark authorizes nobody.
func TestInstallLeavesThePhase3SetEmpty(t *testing.T) {
	f, rs := newTestFoundation()
	if _, err := f.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	elems, ok := rs.sets[phase3Set]
	if !ok {
		t.Fatal("the Phase-3 set was not created")
	}
	if len(elems) != 0 {
		t.Fatalf("the Phase-3 set was created with %d element(s); a dark appliance must authorize nobody", len(elems))
	}
}

// A missing captive exclusion leaves the guest with internet but still seeing the portal.
func TestInstallAddsTheForwardRuleAndBothCaptiveExclusions(t *testing.T) {
	f, rs := newTestFoundation()
	if _, err := f.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	found := false
	for _, r := range rs.forward {
		if strings.Contains(r.text, "@"+phase3Set) && strings.Contains(r.text, "accept") {
			found = true
			if !strings.Contains(r.text, `oifname "ens160"`) {
				t.Fatalf("the forward rule does not name the WAN interface read from the live ruleset: %s", r.text)
			}
		}
	}
	if !found {
		t.Fatalf("no phase-3 forward accept rule: %+v", rs.forward)
	}
	for _, r := range rs.captive {
		if !strings.Contains(r.text, "!= @"+phase3Set) {
			t.Fatalf("a captive rule still lacks the Phase-3 exclusion: %s", r.text)
		}
		if !strings.Contains(r.text, "!= @"+legacySet) {
			t.Fatalf("the legacy exclusion was lost from a captive rule: %s", r.text)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	f, rs := newTestFoundation()
	if _, err := f.Install(context.Background()); err != nil {
		t.Fatalf("first install: %v", err)
	}
	forwardCount := len(rs.forward)
	captive := []string{rs.captive[0].text, rs.captive[1].text}

	rep, err := f.Install(context.Background())
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if rep.Outcome != "ALREADY_INSTALLED" {
		t.Fatalf("outcome = %s, want ALREADY_INSTALLED", rep.Outcome)
	}
	if len(rep.Commands) != 0 {
		t.Fatalf("a second install issued mutations: %v", rep.Commands)
	}
	if len(rs.forward) != forwardCount {
		t.Fatalf("a second install added a duplicate forward rule (%d -> %d)", forwardCount, len(rs.forward))
	}
	for i, want := range captive {
		if rs.captive[i].text != want {
			t.Fatalf("a second install rewrote a captive rule:\n  %s\n  %s", want, rs.captive[i].text)
		}
	}
}

func TestRollbackRemovesOnlyThePhase3Foundation(t *testing.T) {
	f, rs := newTestFoundation()
	if _, err := f.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	before := append([]string(nil), rs.sets[legacySet]...)

	rep, err := f.Rollback(context.Background())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rep.Outcome != "REMOVED" {
		t.Fatalf("outcome = %s", rep.Outcome)
	}
	if _, ok := rs.sets[phase3Set]; ok {
		t.Fatal("the Phase-3 set survived the rollback")
	}
	for _, r := range rs.forward {
		if strings.Contains(r.text, "@"+phase3Set) {
			t.Fatalf("a phase-3 forward rule survived the rollback: %s", r.text)
		}
	}
	for _, r := range rs.captive {
		if strings.Contains(r.text, phase3Set) {
			t.Fatalf("a captive rule still mentions Phase 3 after the rollback: %s", r.text)
		}
		if !strings.Contains(r.text, "!= @"+legacySet) {
			t.Fatalf("the rollback damaged the legacy exclusion: %s", r.text)
		}
	}
	if len(rs.sets[legacySet]) != len(before) {
		t.Fatalf("the rollback changed legacy authorization: %v -> %v", before, rs.sets[legacySet])
	}
	for _, c := range rs.cmds {
		if strings.Contains(c, "delete set inet stayconnect "+legacySet) || strings.Contains(c, "flush") {
			t.Fatalf("the rollback issued a command that touches legacy state: %s", c)
		}
	}
}

func TestRollbackOnACleanRulesetIsANoOp(t *testing.T) {
	f, rs := newTestFoundation()
	rep, err := f.Rollback(context.Background())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rep.Outcome != "ALREADY_ABSENT" {
		t.Fatalf("outcome = %s, want ALREADY_ABSENT", rep.Outcome)
	}
	if len(rs.sets[legacySet]) != 3 {
		t.Fatal("a no-op rollback changed legacy authorization")
	}
}

func TestARefusedTransactionLeavesTheRulesetUntouched(t *testing.T) {
	f, rs := newTestFoundation()
	rs.failNext = errors.New("exit status 1")

	if _, err := f.Install(context.Background()); err == nil {
		t.Fatal("a refused transaction was reported as a successful install")
	}
	if _, ok := rs.sets[phase3Set]; ok {
		t.Fatal("the set exists after a refused transaction")
	}
	if len(rs.sets[legacySet]) != 3 {
		t.Fatal("legacy authorization changed on a refused transaction")
	}
}

func TestVerificationFailureRollsItselfBack(t *testing.T) {
	f, rs := newTestFoundation()
	orig := rs.run
	f.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		out, err := orig(ctx, name, args...)
		if _, ok := rs.sets[phase3Set]; ok && rs.sets[phase3Set] == nil {
			rs.sets[phase3Set] = []string{"br-guest|10.20.0.99"}
		}
		return out, err
	}
	if _, err := f.Install(context.Background()); err == nil {
		t.Fatal("an install whose verification failed reported success")
	}
	if len(rs.sets[legacySet]) != 3 {
		t.Fatal("legacy authorization was damaged by the failed install")
	}
}

// A ruleset with no stayconnect table is refused: a conjured table would be deleted by the next render.
func TestInstallRefusesWhenTheTableIsAbsent(t *testing.T) {
	f, rs := newTestFoundation()
	rs.noTable = true
	if _, err := f.Install(context.Background()); err == nil {
		t.Fatal("the foundation was installed into a ruleset that has no stayconnect table")
	}
}

func TestInspectMutatesNothing(t *testing.T) {
	f, rs := newTestFoundation()
	if _, err := f.Inspect(context.Background()); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	for _, c := range rs.cmds {
		if !strings.Contains(c, "list") {
			t.Fatalf("inspect issued a non-listing command: %s", c)
		}
	}
}

// netd does nothing when the live fingerprint matches its render, so this tool must invalidate the marker after any
// structural change; otherwise rollback on a converged appliance is never repaired.

func markedRuleset() *fakeRuleset {
	f := newFakeRuleset()
	f.sets["sc_render_fp"] = nil
	return f
}

func TestRollbackInvalidatesTheRenderMarker(t *testing.T) {
	f := newFakeRuleset()
	fo := &Foundation{NftPath: "nft", Run: f.run}
	if _, err := fo.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	f.sets["sc_render_fp"] = nil
	f.cmds = nil

	rep, err := fo.Rollback(context.Background())
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rep.Outcome != "REMOVED" {
		t.Fatalf("outcome = %q", rep.Outcome)
	}
	joined := strings.Join(f.cmds, " ; ")
	if !strings.Contains(joined, "delete set inet stayconnect sc_render_fp") {
		t.Fatalf("rollback changed the structure but left the render marker intact: %s", joined)
	}
	if _, ok := f.sets["sc_render_fp"]; ok {
		t.Fatal("the marker set still exists after a structural rollback")
	}
	if len(f.sets[legacySet]) != 3 {
		t.Fatalf("legacy authorization changed: %v", f.sets[legacySet])
	}
}

func TestInstallInvalidatesTheRenderMarkerWhenItChangesStructure(t *testing.T) {
	f := markedRuleset()
	fo := &Foundation{NftPath: "nft", Run: f.run}

	if _, err := fo.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	joined := strings.Join(f.cmds, " ; ")
	if !strings.Contains(joined, "delete set inet stayconnect sc_render_fp") {
		t.Fatalf("install changed the structure but left the render marker intact: %s", joined)
	}
}

// A no-op install leaves the marker untouched; dropping it would force a pointless full-table replacement.
func TestAlreadyInstalledLeavesTheRenderMarkerAlone(t *testing.T) {
	f := markedRuleset()
	fo := &Foundation{NftPath: "nft", Run: f.run}
	if _, err := fo.Install(context.Background()); err != nil {
		t.Fatalf("first install: %v", err)
	}
	f.sets["sc_render_fp"] = nil
	f.cmds = nil

	rep, err := fo.Install(context.Background())
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if rep.Outcome != "ALREADY_INSTALLED" {
		t.Fatalf("outcome = %q, want ALREADY_INSTALLED", rep.Outcome)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "delete set") {
			t.Fatalf("a no-op install issued a structural command: %s", c)
		}
	}
	if _, ok := f.sets["sc_render_fp"]; !ok {
		t.Fatal("a no-op install invalidated a truthful marker")
	}
}

// An unmarked table has no marker to invalidate, and no delete may be emitted for a set that is absent (it aborts the
// transaction).
func TestUnmarkedRulesetIssuesNoMarkerDelete(t *testing.T) {
	f := newFakeRuleset()
	fo := &Foundation{NftPath: "nft", Run: f.run}
	if _, err := fo.Install(context.Background()); err != nil {
		t.Fatalf("install: %v", err)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "sc_render_fp") {
			t.Fatalf("issued a marker command against a table with no marker: %s", c)
		}
	}
}
