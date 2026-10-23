package ledger

import (
	"fmt"
	"sort"
	"strings"
)

type Copy struct {
	Name    string // "local" or "external"
	Present bool
	Reach   error // why the storage could not be asked (unreachable, no key, ...); nil when it answered
	Parse   *Parse
}

// Intact is true for a valid chain; an unfinished last line (an unacknowledged interrupted write) is ignored.
func (c Copy) Intact() bool { return c.Present && c.Parse != nil && c.Parse.Err == nil }

// Witness is an independent statement of how far the ledger had reached (the restored database, the live
// database, a backup's head). Hash may be "" when only the number is known.
type Witness struct {
	Source string
	Ledger string // "" when unknown
	Seq    int64
	Hash   string
}

type Verdict struct {
	Best *Copy
	// Complete is true only when Best is intact and reaches everything the available witnesses say was committed.
	Complete bool
	// Heal lists the copies that are absent, damaged or behind and must be rewritten from Best once Best is trusted.
	Heal    []string
	Reasons []string
	// Conflict is true when the evidence contradicts itself (forked copies, a witness hash the copy lacks); restore must not guess.
	Conflict bool
}

func (v *Verdict) say(f string, a ...any) { v.Reasons = append(v.Reasons, fmt.Sprintf(f, a...)) }

// Assess decides whether a restore may rely on the ledger. One trustworthy, complete copy suffices; an unreachable
// copy is simply not a witness.
//
//   - Both intact and one is the other's prefix (or they are equal): the longer one; each vouches for the other.
//   - Both intact but they disagree (a fork): conflict, nothing proceeds.
//   - One intact: it is complete only if at least one independent witness exists and every witness is satisfied. With no
//     witness at all, the only ledger that can be accepted is one holding nothing but its first entry (nothing to replay);
//     anything longer is "unwitnessed" and refused, because a copy that was silently cut short looks exactly like a good one.
//   - None intact: nothing is trusted. (A damaged copy's valid prefix is never used on its own.)
func Assess(copies []Copy, witnesses []Witness) Verdict {
	var v Verdict
	var intact []*Copy
	for i := range copies {
		c := &copies[i]
		switch {
		case c.Reach != nil:
			v.say("the %s copy could not be reached (%v)", c.Name, c.Reach)
		case !c.Present:
			v.say("the %s copy holds no ledger", c.Name)
			v.Heal = append(v.Heal, c.Name)
		case !c.Intact():
			v.say("the %s copy is damaged (%v)", c.Name, damageReason(c.Parse))
			v.Heal = append(v.Heal, c.Name)
		default:
			intact = append(intact, c)
		}
	}
	if len(intact) == 0 {
		v.say("no copy of the erasure ledger can be trusted")
		return v
	}
	// the same ledger?
	id := intact[0].Parse.LedgerID()
	for _, c := range intact[1:] {
		if c.Parse.LedgerID() != id {
			v.Conflict = true
			v.say("the %s and %s copies are different ledgers", intact[0].Name, c.Name)
			return v
		}
	}
	best := intact[0]
	for _, c := range intact[1:] {
		bs, bh := best.Parse.Head()
		cs, ch := c.Parse.Head()
		lo := min(bs, cs)
		if lo > 0 && best.Parse.Entries[lo-1].Hash != c.Parse.Entries[lo-1].Hash {
			v.Conflict = true
			v.say("the %s and %s copies disagree about entry %d: they have forked", best.Name, c.Name, lo)
			return v
		}
		_ = bh
		_ = ch
		if cs > bs {
			v.Heal = append(v.Heal, best.Name)
			best = c
		} else if cs < bs {
			v.Heal = append(v.Heal, c.Name)
		}
	}
	bs, _ := best.Parse.Head()
	satisfied := 0
	for _, w := range witnesses {
		if w.Seq <= 0 {
			continue
		}
		if w.Ledger != "" && w.Ledger != id {
			v.Conflict = true
			v.say("%s belongs to a different ledger than the %s copy", w.Source, best.Name)
			return v
		}
		if bs < w.Seq {
			v.say("the %s copy stops at entry %d but %s says the ledger had reached entry %d: it is missing the most recent erasures",
				best.Name, bs, w.Source, w.Seq)
			v.Best = nil
			return v
		}
		if w.Hash != "" && best.Parse.Entries[w.Seq-1].Hash != w.Hash {
			v.Conflict = true
			v.say("the %s copy does not contain the entry %d that %s recorded: it is not this appliance's ledger", best.Name, w.Seq, w.Source)
			return v
		}
		satisfied++
	}
	v.Best = best
	switch {
	case len(intact) > 1:
		v.Complete = true
		v.say("both copies are intact and agree up to entry %d", min(headSeq(intact[0]), headSeq(intact[1])))
	case satisfied > 0:
		v.Complete = true
		v.say("the %s copy is intact and reaches every independent record of how far the ledger had gone (%d checked)", best.Name, satisfied)
	case bs == 1:
		v.Complete = true
		v.say("the %s copy holds only the start of the ledger, so there is nothing to apply", best.Name)
	default:
		v.say("the %s copy is intact but nothing independent shows it is complete (no other copy, no database or backup that records the ledger): it cannot be used", best.Name)
		v.Best = nil
	}
	if v.Complete {
		for _, c := range copies {
			if c.Reach != nil {
				v.Heal = append(v.Heal, c.Name)
			}
		}
	}
	v.Heal = uniq(v.Heal)
	return v
}

func headSeq(c *Copy) int64 { s, _ := c.Parse.Head(); return s }

func damageReason(p *Parse) string {
	if p == nil {
		return "unreadable"
	}
	if p.Err != nil {
		return strings.TrimPrefix(p.Err.Error(), ErrCorrupt.Error()+": ")
	}
	if p.Torn {
		return "ends in an unfinished write"
	}
	return "unreadable"
}

func uniq(in []string) []string {
	sort.Strings(in)
	out := in[:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}
