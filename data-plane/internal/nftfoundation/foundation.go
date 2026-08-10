// Package nftfoundation installs the Phase-3 packet-authorization foundation into a LIVE nftables ruleset
// WITHOUT disturbing the guests currently on it.
//
// WHY THIS EXISTS.
//
// The Phase-3 design needs three things present in `table inet stayconnect` before a flag-only cutover is
// possible: the `phase3_auth_ipv4` set, the forward rule that accepts traffic matching it, and the Phase-3
// exclusion on the captive-portal DNAT rules (without which an authorized Phase-3 guest would have internet
// and still see the login page on every request).
//
// The obvious way to get them there is to regenerate and re-apply the whole ruleset. That is exactly what
// must not happen. The generated ruleset is applied as `delete table inet stayconnect` followed by a full
// recreate — an atomic swap of the whole table — and the authorization set is part of the table. Recreating
// it means recreating it EMPTY, so every live legacy guest loses their authorization element in the same
// instant and drops off the internet until scd's own reconciliation notices and re-adds them. On a property
// with a few hundred guests that is a visible, simultaneous outage caused by a change that was supposed to do
// nothing at all.
//
// So the foundation is installed SURGICALLY: one nft transaction that adds a set, adds one rule, and rewrites
// the captive rules in place. Nothing is flushed, nothing is recreated, and the legacy authorization elements
// are never touched. The operation snapshots those elements before it runs and re-reads them afterwards, and
// if they are not exactly as they were it rolls itself back and reports failure rather than leaving a ruleset
// nobody can vouch for.
//
// WHAT IT IS NOT.
//
// It authorizes nobody. `phase3_auth_ipv4` is created EMPTY and stays empty until netd — with Phase-3 flags
// ON — leases an element into it. An empty set matches nothing, so the forward rule can never match and the
// DNAT exclusion can never exclude. A dark appliance with the foundation installed forwards precisely what it
// forwarded before, which is the property that makes this safe to do ahead of the cutover.
package nftfoundation

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strings"

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

// Runner executes one nft invocation. It is injectable so the exact command buffers this package builds can be
// asserted without a kernel, and so the real-kernel suite can point it at a disposable namespace.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Foundation performs and reverses the surgical installation.
type Foundation struct {
	NftPath string
	Run     Runner
}

// ExecRunner is the real runner: it executes the named binary. It is exported so a disposable-namespace test
// harness can build a Foundation that points at a namespace-scoped nft wrapper without reimplementing this.
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

// State is what the live ruleset currently looks like, as far as Phase 3 is concerned.
type State struct {
	TablePresent bool
	LegacySet    bool
	Phase3Set    bool
	// Phase3SetEmpty is only meaningful when Phase3Set is true.
	Phase3SetEmpty bool
	ForwardRule    bool
	// CaptiveRules is every captive DNAT rule that excludes the legacy set, with whether it also excludes the
	// Phase-3 one. A partially-converted chain (some rules excluded, some not) is a real possibility after an
	// interrupted attempt, and reporting the split is how an operator sees it.
	CaptiveRules []CaptiveRule
	// LegacyElements is the authorization state that must survive the operation unchanged.
	LegacyElements []string
	// ForwardAcceptHandle is the handle of the legacy authenticated-guests accept rule, used as the anchor the
	// Phase-3 rule is added after so the installed ruleset reads like a generated one.
	ForwardAcceptHandle string
	// WANInterface is read back out of that rule rather than configured here: the foundation must match the
	// ruleset it finds, and a second source for the WAN name is a second thing that can be wrong.
	WANInterface string
}

// CaptiveRule is one captive-portal DNAT rule in the live chain.
type CaptiveRule struct {
	Handle    string
	Text      string
	HasPhase3 bool
}

var handleRe = regexp.MustCompile(`\s*#\s+handle\s+(\d+)\s*$`)

// Inspect reads the live ruleset. It is read-only and safe to run at any time, including on an appliance
// serving guests.
func (f *Foundation) Inspect(ctx context.Context) (State, error) {
	var st State
	out, err := f.nft(ctx, "-a", "list", "table", "inet", "stayconnect")
	if err != nil {
		// No table means this is not a StayConnect edge, or the edge has not rendered its ruleset yet. Either
		// way the foundation must NOT create one: a table conjured here would not be the generated one, and
		// the next legitimate render would delete it along with anything installed into it.
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

// firstQuoted pulls the first "quoted" token out of a rule, which for the authenticated-guests accept rule is
// the WAN interface name.
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

// elements returns a stable, comparable rendering of a set's membership: "iface|ip", sorted.
//
// It deliberately omits the timeout. A legacy element's remaining lease ticks down between the before and
// after reads, and comparing it would make an operation that changed nothing look like it changed everything.
// What must be identical is WHO IS AUTHORIZED, and that is exactly what this returns.
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

// Report is the outcome of an Install or Rollback, in the terms an operator has to record.
type Report struct {
	Outcome string // INSTALLED | ALREADY_INSTALLED | REMOVED | ALREADY_ABSENT
	// LegacyBefore/LegacyAfter are the legacy authorization elements on either side of the mutation. They are
	// reported in full — not merely compared — because the runbook's legacy-parity proof is the operator
	// writing down what was there before and confirming it afterwards, and a boolean cannot be re-checked.
	LegacyBefore []string
	LegacyAfter  []string
	Commands     []string
	Rolled       bool
}

// Install adds the Phase-3 foundation surgically and idempotently.
//
// The whole mutation is ONE nft command buffer, which nft commits as a single netlink transaction: every part
// takes or none does. There is therefore no interval in which the set exists but the captive rules do not, or
// in which half the captive chain excludes Phase 3 and half does not.
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
		// FAIL CLOSED. The ruleset is in a state this operation cannot vouch for, so it is put back rather
		// than left for someone to interpret later.
		if _, rerr := f.Rollback(ctx); rerr != nil {
			return rep, fmt.Errorf("phase-3 foundation: verification failed (%v) AND rollback failed (%v) — the ruleset needs manual inspection", verr, rerr)
		}
		rep.Rolled = true
		return rep, fmt.Errorf("phase-3 foundation: verification failed, rolled back: %w", verr)
	}
	rep.Outcome = "INSTALLED"
	return rep, nil
}

// installCommands derives exactly the missing pieces. An already-complete ruleset yields none, which is what
// makes a second run a genuine no-op rather than a duplicate rule.
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
			// `add ... handle N` places the new rule immediately AFTER rule N, so the Phase-3 accept sits
			// beside the legacy one exactly as the generated ruleset renders it.
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
	return cmds
}

// addPhase3Exclusion inserts the Phase-3 exclusion immediately after the legacy one, so the rewritten rule
// reads identically to the generated form and the two exclusions stay adjacent for anyone reading the chain.
func addPhase3Exclusion(rule string) string {
	i := strings.Index(rule, legacyExcl)
	if i < 0 {
		return rule
	}
	end := i + len(legacyExcl)
	return rule[:end] + " " + phase3Excl + rule[end:]
}

// removePhase3Exclusion is its exact inverse.
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
		// A non-empty set after an install that authorizes nobody means something else is writing it, and the
		// dark guarantee ("this changes nothing for any guest") no longer holds.
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

// Rollback removes ONLY the Phase-3 foundation.
//
// It never touches the legacy set, its elements, or any rule that does not mention Phase 3, and it verifies
// legacy parity afterwards for the same reason Install does: the point of the operation is that the guests on
// the appliance cannot tell it happened.
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
		// The set is deleted LAST within the buffer; nft resolves the whole batch before committing, so the
		// rules referencing it are gone in the same transaction.
		cmds = append(cmds, fmt.Sprintf("delete set inet stayconnect %s", phase3Set))
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
