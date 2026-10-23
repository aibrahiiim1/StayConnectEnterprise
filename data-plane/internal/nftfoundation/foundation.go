// Package nftfoundation installs the Phase-3 packet-authorization foundation into a live nftables ruleset without
// disturbing the guests on it: the phase3_auth_ipv4 set, the forward rule accepting it, and the Phase-3 exclusion on
// the captive DNAT rules.
//
// Regenerating the ruleset (`delete table` + recreate) would recreate the authorization sets empty and drop every
// live legacy guest at once, so the install is surgical: one nft transaction that adds a set and a rule and rewrites
// the captive rules in place. Legacy elements are snapshotted before and re-read after; if they differ it rolls back
// and reports failure. It authorizes nobody: phase3_auth_ipv4 is created empty and matches nothing, so a dark
// appliance forwards exactly what it did before.
package nftfoundation

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nft"
)

const (
	legacySet   = "auth_ipv4"
	phase3Set   = "phase3_auth_ipv4"
	forwardChn  = "forward"
	captiveChn  = "prerouting_nat"
	phase3Cmt   = "phase-3 authorized guests"
	legacyExcl  = "ip saddr != @" + legacySet
	phase3Excl  = "iifname . ip saddr != @" + phase3Set
	phase3Match = "@" + phase3Set
)

// Runner executes one nft invocation; injectable so tests can assert the exact command buffers and point at a
// disposable namespace.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

type Foundation struct {
	NftPath string
	Run     Runner
}

// ExecRunner is the real runner; exported so a namespace test harness can point a Foundation at a namespace-scoped
// nft wrapper.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func New() *Foundation {
	return &Foundation{NftPath: "/usr/sbin/nft", Run: ExecRunner}
}

func (f *Foundation) nft(ctx context.Context, args ...string) (string, error) {
	out, err := f.Run(ctx, f.NftPath, args...)
	if err != nil {
		return string(out), fmt.Errorf("nft %s: %w — %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

type State struct {
	TablePresent   bool
	LegacySet      bool
	Phase3Set      bool
	Phase3SetEmpty bool
	ForwardRule    bool
	// CaptiveRules is every captive DNAT rule that excludes the legacy set, with whether it also excludes the Phase-3
	// one; a partially converted chain is possible after an interrupted attempt.
	CaptiveRules   []CaptiveRule
	LegacyElements []string
	// ForwardAcceptHandle is the handle of the legacy authenticated-guests accept rule, the anchor the Phase-3 rule is
	// added after.
	ForwardAcceptHandle string
	// WANInterface is read back from that rule rather than configured: a second source for the WAN name can be wrong.
	WANInterface string

	// RenderMarker is whether the live table carries netd's render fingerprint; any structural change made here must
	// invalidate it or netd would skip reconciliation believing the structure is current.
	RenderMarker bool
}

type CaptiveRule struct {
	Handle    string
	Text      string
	HasPhase3 bool
}

var handleRe = regexp.MustCompile(`\s*#\s+handle\s+(\d+)\s*$`)

// Inspect reads the live ruleset; read-only and safe on an appliance serving guests.
func (f *Foundation) Inspect(ctx context.Context) (State, error) {
	var st State
	out, err := f.nft(ctx, "-a", "list", "table", "inet", "stayconnect")
	if err != nil {
		// No table: not a StayConnect edge, or not rendered yet. The foundation must not create one: it would not be the
		// generated table and the next render would delete it.
		return st, fmt.Errorf("phase-3 foundation: the stayconnect nftables table is not present; refusing to create it: %w", err)
	}
	st.TablePresent = true
	chain := ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "set "):
			name := strings.Fields(line)[1]
			if name == legacySet {
				st.LegacySet = true
			}
			if name == phase3Set {
				st.Phase3Set = true
			}
			if name == netcfg.RenderMarkerSet {
				st.RenderMarker = true
			}
		case strings.HasPrefix(line, "chain "):
			chain = strings.Fields(line)[1]
		}
		handle := ""
		if m := handleRe.FindStringSubmatch(raw); m != nil {
			handle = m[1]
		}
		body := handleRe.ReplaceAllString(strings.TrimSpace(raw), "")
		switch chain {
		case forwardChn:
			if strings.Contains(body, phase3Match) && strings.Contains(body, "accept") {
				st.ForwardRule = true
			}
			if strings.Contains(body, "@"+legacySet) && strings.Contains(body, "accept") && !strings.Contains(body, phase3Match) {
				st.ForwardAcceptHandle = handle
				st.WANInterface = firstQuoted(body)
			}
		case captiveChn:
			if strings.Contains(body, legacyExcl) && strings.Contains(body, "dnat") {
				st.CaptiveRules = append(st.CaptiveRules, CaptiveRule{
					Handle: handle, Text: body, HasPhase3: strings.Contains(body, phase3Match)})
			}
		}
	}
	if st.Phase3Set {
		elems, err := f.elements(ctx, phase3Set)
		if err != nil {
			return st, err
		}
		st.Phase3SetEmpty = len(elems) == 0
	}
	if st.LegacySet {
		if st.LegacyElements, err = f.elements(ctx, legacySet); err != nil {
			return st, err
		}
	}
	return st, nil
}

// firstQuoted pulls the first quoted token out of a rule (the WAN interface for the authenticated-guests accept).
func firstQuoted(s string) string {
	i := strings.Index(s, `"`)
	if i < 0 {
		return ""
	}
	j := strings.Index(s[i+1:], `"`)
	if j < 0 {
		return ""
	}
	return s[i+1 : i+1+j]
}

// elements returns a stable, comparable rendering of a set's membership ("iface|ip", sorted), omitting the timeout: a
// legacy element's remaining lease ticks down between reads, and what must be identical is who is authorized.
func (f *Foundation) elements(ctx context.Context, set string) ([]string, error) {
	out, err := f.Run(ctx, f.NftPath, "-j", "list", "set", "inet", "stayconnect", set)
	if err != nil {
		return nil, fmt.Errorf("nft -j list set %s: %w — %s", set, err, strings.TrimSpace(string(out)))
	}
	els, err := nft.ParseSetJSON(out)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(els))
	for _, e := range els {
		ip := ""
		if e.IP != nil {
			ip = e.IP.String()
		}
		keys = append(keys, e.Iface+"|"+ip)
	}
	sort.Strings(keys)
	return keys, nil
}

type Report struct {
	Outcome string // INSTALLED | ALREADY_INSTALLED | REMOVED | ALREADY_ABSENT
	// LegacyBefore/LegacyAfter are the legacy authorization elements on either side of the mutation, reported in full
	// because the runbook's parity proof is the operator re-checking them.
	LegacyBefore []string
	LegacyAfter  []string
	Commands     []string
	Rolled       bool
}

// Install adds the Phase-3 foundation surgically and idempotently as ONE nft command buffer (a single netlink
// transaction), so there is no interval in which the set exists but the captive rules do not.
func (f *Foundation) Install(ctx context.Context) (Report, error) {
	var rep Report
	st, err := f.Inspect(ctx)
	if err != nil {
		return rep, err
	}
	if !st.LegacySet {
		return rep, fmt.Errorf("phase-3 foundation: the legacy %s set is absent; this ruleset is not one this operation understands", legacySet)
	}
	rep.LegacyBefore = st.LegacyElements

	cmds := f.installCommands(st)
	if len(cmds) == 0 {
		rep.Outcome = "ALREADY_INSTALLED"
		rep.LegacyAfter = st.LegacyElements
		return rep, nil
	}
	rep.Commands = cmds
	if _, err := f.nft(ctx, strings.Join(cmds, " ; ")); err != nil {
		return rep, fmt.Errorf("phase-3 foundation: install transaction refused (nothing was applied): %w", err)
	}

	after, verr := f.verifyInstalled(ctx, rep.LegacyBefore)
	rep.LegacyAfter = after
	if verr != nil {
		// Fail closed: the ruleset is in a state this operation cannot vouch for, so it is put back.
		if _, rerr := f.Rollback(ctx); rerr != nil {
			return rep, fmt.Errorf("phase-3 foundation: verification failed (%v) AND rollback failed (%v) — the ruleset needs manual inspection", verr, rerr)
		}
		rep.Rolled = true
		return rep, fmt.Errorf("phase-3 foundation: verification failed, rolled back: %w", verr)
	}
	rep.Outcome = "INSTALLED"
	return rep, nil
}

// installCommands derives exactly the missing pieces; an already-complete ruleset yields none, so a second run is a no-op.
func (f *Foundation) installCommands(st State) []string {
	var cmds []string
	if !st.Phase3Set {
		cmds = append(cmds, fmt.Sprintf(
			`add set inet stayconnect %s { type ifname . ipv4_addr ; flags timeout ; comment "Phase-3 authorized guests (netd-owned): (ingress bridge, IP)" ; }`,
			phase3Set))
	}
	if !st.ForwardRule && st.WANInterface != "" {
		rule := fmt.Sprintf(`oifname "%s" iifname . ip saddr @%s accept comment "%s"`, st.WANInterface, phase3Set, phase3Cmt)
		if st.ForwardAcceptHandle != "" {
			// `add ... handle N` places the new rule immediately after rule N, beside the legacy one as the generated ruleset renders it.
			cmds = append(cmds, fmt.Sprintf("add rule inet stayconnect %s handle %s %s", forwardChn, st.ForwardAcceptHandle, rule))
		} else {
			cmds = append(cmds, fmt.Sprintf("add rule inet stayconnect %s %s", forwardChn, rule))
		}
	}
	for _, r := range st.CaptiveRules {
		if r.HasPhase3 || r.Handle == "" {
			continue
		}
		cmds = append(cmds, fmt.Sprintf("replace rule inet stayconnect %s handle %s %s", captiveChn, r.Handle, addPhase3Exclusion(r.Text)))
	}
	if len(cmds) > 0 {
		cmds = append(cmds, invalidateMarker(st)...)
	}
	return cmds
}

// invalidateMarker returns the command that removes netd's render fingerprint, or nothing if the table carries none.
// Any structural change here must drop it: netd does nothing when the fingerprint matches, so a marker claiming the
// structure is current after this tool changed it (e.g. rollback removing phase3_auth_ipv4) would make netd skip the
// reconciliation that restores it.
func invalidateMarker(st State) []string {
	if !st.RenderMarker {
		return nil
	}
	return []string{fmt.Sprintf("delete set inet stayconnect %s", netcfg.RenderMarkerSet)}
}

// addPhase3Exclusion inserts the Phase-3 exclusion immediately after the legacy one, as the generated form reads.
func addPhase3Exclusion(rule string) string {
	i := strings.Index(rule, legacyExcl)
	if i < 0 {
		return rule
	}
	end := i + len(legacyExcl)
	return rule[:end] + " " + phase3Excl + rule[end:]
}

func removePhase3Exclusion(rule string) string {
	return strings.ReplaceAll(strings.ReplaceAll(rule, " "+phase3Excl, ""), phase3Excl+" ", "")
}

func (f *Foundation) verifyInstalled(ctx context.Context, before []string) ([]string, error) {
	st, err := f.Inspect(ctx)
	if err != nil {
		return nil, err
	}
	if !sameElements(before, st.LegacyElements) {
		return st.LegacyElements, fmt.Errorf("legacy authorization parity broken: %d element(s) before, %d after", len(before), len(st.LegacyElements))
	}
	if !st.Phase3Set {
		return st.LegacyElements, fmt.Errorf("%s was not created", phase3Set)
	}
	if !st.Phase3SetEmpty {
		// A non-empty set after an install that authorizes nobody means something else is writing it, so the dark
		// guarantee no longer holds.
		return st.LegacyElements, fmt.Errorf("%s is not empty after installation", phase3Set)
	}
	if !st.ForwardRule {
		return st.LegacyElements, fmt.Errorf("the phase-3 forward accept rule was not installed")
	}
	for _, r := range st.CaptiveRules {
		if !r.HasPhase3 {
			return st.LegacyElements, fmt.Errorf("captive rule handle %s still lacks the phase-3 exclusion", r.Handle)
		}
	}
	return st.LegacyElements, nil
}

// Rollback removes only the Phase-3 foundation, never the legacy set, its elements or any rule that does not
// mention Phase 3, and verifies legacy parity afterwards as Install does.
func (f *Foundation) Rollback(ctx context.Context) (Report, error) {
	var rep Report
	st, err := f.Inspect(ctx)
	if err != nil {
		return rep, err
	}
	rep.LegacyBefore = st.LegacyElements

	var cmds []string
	for _, r := range st.CaptiveRules {
		if !r.HasPhase3 || r.Handle == "" {
			continue
		}
		cmds = append(cmds, fmt.Sprintf("replace rule inet stayconnect %s handle %s %s", captiveChn, r.Handle, removePhase3Exclusion(r.Text)))
	}
	if h := f.phase3ForwardHandle(ctx); h != "" {
		cmds = append(cmds, fmt.Sprintf("delete rule inet stayconnect %s handle %s", forwardChn, h))
	}
	if st.Phase3Set {
		// The set is deleted last within the buffer; nft resolves the batch before committing, so the rules referencing it
		// go in the same transaction.
		cmds = append(cmds, fmt.Sprintf("delete set inet stayconnect %s", phase3Set))
	}
	if len(cmds) > 0 {
		cmds = append(cmds, invalidateMarker(st)...)
	}
	if len(cmds) == 0 {
		rep.Outcome = "ALREADY_ABSENT"
		rep.LegacyAfter = st.LegacyElements
		return rep, nil
	}
	rep.Commands = cmds
	if _, err := f.nft(ctx, strings.Join(cmds, " ; ")); err != nil {
		return rep, fmt.Errorf("phase-3 foundation: rollback transaction refused (nothing was applied): %w", err)
	}
	post, err := f.Inspect(ctx)
	if err != nil {
		return rep, err
	}
	rep.LegacyAfter = post.LegacyElements
	if !sameElements(rep.LegacyBefore, post.LegacyElements) {
		return rep, fmt.Errorf("phase-3 foundation: rollback changed legacy authorization (%d before, %d after)",
			len(rep.LegacyBefore), len(post.LegacyElements))
	}
	if post.Phase3Set || post.ForwardRule {
		return rep, fmt.Errorf("phase-3 foundation: rollback did not remove the foundation")
	}
	rep.Outcome = "REMOVED"
	return rep, nil
}

func (f *Foundation) phase3ForwardHandle(ctx context.Context) string {
	out, err := f.nft(ctx, "-a", "list", "chain", "inet", "stayconnect", forwardChn)
	if err != nil {
		return ""
	}
	for _, raw := range strings.Split(out, "\n") {
		if !strings.Contains(raw, phase3Match) {
			continue
		}
		if m := handleRe.FindStringSubmatch(raw); m != nil {
			return m[1]
		}
	}
	return ""
}

func sameElements(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
