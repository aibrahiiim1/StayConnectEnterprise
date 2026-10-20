package ledger

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"filippo.io/age"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

const (
	tLedger = "11111111-1111-4111-8111-111111111111"
	tTenant = "22222222-2222-4222-8222-222222222222"
	tSite   = "33333333-3333-4333-8333-333333333333"
)

// mk builds a valid ledger of n entries (a genesis and n-1 erasures).
func mk(n int) []Entry {
	var out []Entry
	prev := GenesisPrev
	for i := 1; i <= n; i++ {
		kind := "erase"
		if i == 1 {
			kind = "genesis"
		}
		e := Entry{Ledger: tLedger, Seq: int64(i), Prev: prev, Kind: kind, Tenant: tTenant, Site: tSite,
			AtUS: 1_700_000_000_000_000 + int64(i), Body: fmt.Sprintf(`{"erasure_ref":"ER-%012X"}`, i)}
		e.Hash = e.ComputeHash()
		prev = e.Hash
		out = append(out, e)
	}
	return out
}

func enc(t *testing.T, e []Entry) []byte {
	t.Helper()
	b, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// ---- the chain -------------------------------------------------------------------------------------------------------

func TestParseChain_AGoodLedgerIsIntactAndKnowsItsHead(t *testing.T) {
	p := ParseChain(enc(t, mk(5)))
	if !p.Intact || p.Err != nil || p.Torn || len(p.Entries) != 5 {
		t.Fatalf("parse = %+v", p)
	}
	if s, h := p.Head(); s != 5 || h != mk(5)[4].Hash {
		t.Fatalf("head = %d %s", s, h)
	}
	if p.LedgerID() != tLedger {
		t.Fatal("ledger id")
	}
}

func TestParseChain_AnInterruptedWriteIsATornTailNotDamage(t *testing.T) {
	good := enc(t, mk(3))
	l, _ := mk(4)[3].Line()
	p := ParseChain(append(append([]byte{}, good...), l[:len(l)/2]...))
	if p.Err != nil || !p.Torn || p.Intact || len(p.Entries) != 3 || p.ValidBytes != len(good) {
		t.Fatalf("parse = %+v", p)
	}
	if !(Copy{Present: true, Parse: p}).Intact() {
		t.Fatal("a copy with a torn tail still counts as a valid chain")
	}
}

func TestParseChain_EveryKindOfDamageIsRefused(t *testing.T) {
	base := mk(4)
	mut := func(f func(e []Entry)) []byte {
		c := make([]Entry, len(base))
		copy(c, base)
		f(c)
		return enc(t, c)
	}
	cases := map[string][]byte{
		"a changed body": mut(func(e []Entry) { e[2].Body = `{"erasure_ref":"ER-FORGED"}` }),
		"a changed kind": mut(func(e []Entry) { e[2].Kind = "checkpoint" }),
		"a lost entry":   mut(func(e []Entry) { copy(e[1:], e[2:]); e[3] = Entry{} }),
		"reordered":      mut(func(e []Entry) { e[1], e[2] = e[2], e[1] }),
		"not json":       append(enc(t, base[:2]), []byte("garbage\n")...),
		"unknown field":  append(enc(t, base[:2]), []byte(`{"ledger":"x","seq":3,"extra":1}`+"\n")...),
		"another ledger": mut(func(e []Entry) {
			e[3].Ledger = "99999999-9999-4999-8999-999999999999"
			e[3].Hash = e[3].ComputeHash()
		}),
		"no genesis": enc(t, base[1:]),
	}
	for name, b := range cases {
		p := ParseChain(b)
		if p.Err == nil || p.Intact {
			t.Errorf("%s: accepted (%d entries)", name, len(p.Entries))
		}
		if !errors.Is(p.Err, ErrCorrupt) {
			t.Errorf("%s: error %v", name, p.Err)
		}
	}
	// a truncated tail is NOT detectable from the chain alone: that is exactly what witnesses are for
	if p := ParseChain(enc(t, base[:3])); p.Err != nil || len(p.Entries) != 3 {
		t.Fatal("a shorter valid prefix is a valid chain")
	}
}

func TestExtends_RefusesAnythingThatDoesNotContinue(t *testing.T) {
	l := mk(5)
	if err := Extends(3, l[2].Hash, l[3:]); err != nil {
		t.Fatalf("a real continuation: %v", err)
	}
	if err := Extends(3, l[2].Hash, l[4:]); err == nil {
		t.Error("a gap was accepted")
	}
	if err := Extends(2, l[1].Hash, l[3:]); err == nil {
		t.Error("a continuation of the wrong head was accepted")
	}
	bad := l[3]
	bad.Body = "{}"
	if err := Extends(3, l[2].Hash, []Entry{bad}); err == nil {
		t.Error("an entry with a wrong hash was accepted")
	}
}

// ---- which copy may a restore rely on ----------------------------------------------------------------------------------

func copyOf(name string, n int) Copy {
	if n == 0 {
		return Copy{Name: name}
	}
	b, _ := Encode(mk(n))
	return Copy{Name: name, Present: true, Parse: ParseChain(b)}
}

func wit(src string, n int) Witness {
	e := mk(max(n, 1))
	return Witness{Source: src, Ledger: tLedger, Seq: int64(n), Hash: e[max(n, 1)-1].Hash}
}

func TestAssess_Scenarios(t *testing.T) {
	damaged := Copy{Name: "local", Present: true, Parse: ParseChain(append(enc(t, mk(3)), []byte("x\n")...))}
	unreachable := func(name string) Copy { return Copy{Name: name, Reach: errors.New("down")} }
	forkA := mk(4)
	forkB := mk(4)
	forkB[3].Body = "{}"
	forkB[3].Hash = forkB[3].ComputeHash()
	cases := []struct {
		name         string
		copies       []Copy
		wit          []Witness
		wantBest     string
		wantComplete bool
		wantConflict bool
		wantHeal     string
	}{
		{"both intact and equal", []Copy{copyOf("local", 5), copyOf("external", 5)}, nil, "local", true, false, ""},
		{"local ahead of external", []Copy{copyOf("local", 6), copyOf("external", 5)}, nil, "local", true, false, "external"},
		{"external ahead of local", []Copy{copyOf("local", 4), copyOf("external", 6)}, nil, "external", true, false, "local"},
		{"local only, backup witness satisfied", []Copy{copyOf("local", 6), unreachable("external")}, []Witness{wit("the backup", 5)}, "local", true, false, "external"},
		{"external only, backup witness satisfied", []Copy{unreachable("local"), copyOf("external", 6)}, []Witness{wit("the backup", 6)}, "external", true, false, "local"},
		{"local only, no witness", []Copy{copyOf("local", 6), unreachable("external")}, nil, "", false, false, ""},
		{"local only, truncated: the backup reached further", []Copy{copyOf("local", 4), unreachable("external")}, []Witness{wit("the backup", 6)}, "", false, false, ""},
		{"external only, truncated: the live database reached further", []Copy{unreachable("local"), copyOf("external", 5)}, []Witness{wit("the backup", 3), wit("the live database", 7)}, "", false, false, ""},
		{"a witness the copy does not contain", []Copy{copyOf("local", 6), copyOf("external", 6)}, []Witness{{Source: "the backup", Ledger: tLedger, Seq: 3, Hash: strings.Repeat("a", 64)}}, "", false, true, ""},
		{"a witness from another ledger", []Copy{copyOf("local", 6), unreachable("external")}, []Witness{{Source: "the backup", Ledger: "other", Seq: 3}}, "", false, true, ""},
		{"copies fork", []Copy{{Name: "local", Present: true, Parse: ParseChain(enc(t, forkA))}, {Name: "external", Present: true, Parse: ParseChain(enc(t, forkB))}}, nil, "", false, true, ""},
		{"local damaged, external good", []Copy{damaged, copyOf("external", 6)}, []Witness{wit("the backup", 5)}, "external", true, false, "local"},
		{"both damaged", []Copy{damaged, {Name: "external", Present: true, Parse: ParseChain([]byte("junk\n"))}}, []Witness{wit("the backup", 2)}, "", false, false, ""},
		{"nothing at all", []Copy{copyOf("local", 0), unreachable("external")}, []Witness{wit("the backup", 2)}, "", false, false, ""},
		{"only the start of the ledger, nothing to apply", []Copy{copyOf("local", 1), unreachable("external")}, nil, "local", true, false, "external"},
		{"missing local is healed from external", []Copy{copyOf("local", 0), copyOf("external", 6)}, []Witness{wit("the backup", 4)}, "external", true, false, "local"},
	}
	for _, c := range cases {
		v := Assess(c.copies, c.wit)
		got := ""
		if v.Best != nil {
			got = v.Best.Name
		}
		if got != c.wantBest || v.Complete != c.wantComplete || v.Conflict != c.wantConflict {
			t.Errorf("%s: best=%q complete=%v conflict=%v (%v)", c.name, got, v.Complete, v.Conflict, v.Reasons)
		}
		if c.wantHeal != "" && !contains(v.Heal, c.wantHeal) {
			t.Errorf("%s: heal=%v, want %s", c.name, v.Heal, c.wantHeal)
		}
		if len(v.Reasons) == 0 {
			t.Errorf("%s: no reason given", c.name)
		}
		if c.wantComplete && v.Best == nil {
			t.Errorf("%s: complete without a copy", c.name)
		}
	}
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// ---- the store -----------------------------------------------------------------------------------------------------

// flaky wraps a Destination and fails or corrupts on demand.
type flaky struct {
	Destination
	failPut   atomic.Bool
	failGet   atomic.Bool
	corruptGt atomic.Bool
	puts      atomic.Int32
}

func (f *flaky) Put(ctx context.Context, n string, b []byte) error {
	f.puts.Add(1)
	if f.failPut.Load() {
		return errors.New("disk unplugged")
	}
	return f.Destination.Put(ctx, n, b)
}

func (f *flaky) Get(ctx context.Context, n string) ([]byte, error) {
	if f.failGet.Load() {
		return nil, errors.New("storage offline")
	}
	b, err := f.Destination.Get(ctx, n)
	if err == nil && f.corruptGt.Load() && n != ExternalHead && len(b) > 4 {
		b = append([]byte(nil), b...)
		b[3] ^= 0xff
	}
	return b, err
}

func newStore(t *testing.T) (*Store, *flaky, string, string) {
	t.Helper()
	root := t.TempDir()
	ext := filepath.Join(root, "external")
	if err := os.MkdirAll(ext, 0o700); err != nil {
		t.Fatal(err)
	}
	fl := &flaky{Destination: &DirDest{Root: ext}}
	local := filepath.Join(root, "local", "ledger.jsonl")
	return NewStore(Config{LocalPath: local, Dest: fl}), fl, local, ext
}

func TestCommit_BothCopiesHoldTheEntryBeforeItReturns(t *testing.T) {
	s, _, local, ext := newStore(t)
	all := mk(3)
	if err := s.Commit(context.Background(), all[:1]); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(context.Background(), all[1:]); err != nil {
		t.Fatal(err)
	}
	lp := ParseChain(mustRead(t, local))
	if !lp.Intact || len(lp.Entries) != 3 {
		t.Fatalf("local = %+v", lp)
	}
	ep := ParseChain(mustRead(t, filepath.Join(ext, ExternalObject)))
	if !ep.Intact || len(ep.Entries) != 3 {
		t.Fatalf("external = %+v", ep)
	}
	var h HeadInfo
	if err := json.Unmarshal(mustRead(t, filepath.Join(ext, ExternalHead)), &h); err != nil || h.Seq != 3 || h.Hash != all[2].Hash || h.Entries != 3 {
		t.Fatalf("head note = %+v %v", h, err)
	}
	if m, _ := os.Stat(local); m.Mode().Perm() != 0o600 && os.PathSeparator == '/' {
		t.Errorf("local mode %v", m.Mode())
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCommit_WhenTheSecondaryFailsNothingIsLeftOnTheAppliance(t *testing.T) {
	s, fl, local, _ := newStore(t)
	all := mk(4)
	if err := s.Commit(context.Background(), all[:2]); err != nil {
		t.Fatal(err)
	}
	before := mustRead(t, local)
	for name, set := range map[string]func(){
		"unwritable":        func() { fl.failPut.Store(true) },
		"unreadable":        func() { fl.failGet.Store(true) },
		"corrupted storage": func() { fl.corruptGt.Store(true) },
	} {
		set()
		err := s.Commit(context.Background(), all[2:3])
		var ee *ExternalError
		if !errors.As(err, &ee) {
			t.Fatalf("%s: err = %v, want an ExternalError", name, err)
		}
		if !bytes.Equal(mustRead(t, local), before) {
			t.Fatalf("%s: the appliance kept an entry that was not durable in both places", name)
		}
		fl.failPut.Store(false)
		fl.failGet.Store(false)
		fl.corruptGt.Store(false)
	}
	// storage back: the same entry now goes through
	if err := s.Commit(context.Background(), all[2:3]); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
	if n := len(ParseChain(mustRead(t, local)).Entries); n != 3 {
		t.Fatalf("entries = %d", n)
	}
}

func TestCommit_IsIdempotentAndRefusesAForkedEntry(t *testing.T) {
	s, _, _, _ := newStore(t)
	all := mk(3)
	ctx := context.Background()
	if err := s.Commit(ctx, all); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, all); err != nil {
		t.Fatalf("a retry of what is recorded: %v", err)
	}
	forged := all[2]
	forged.Body = "{}"
	forged.Hash = forged.ComputeHash()
	if err := s.Commit(ctx, []Entry{forged}); !errors.Is(err, ErrConflict) {
		t.Fatalf("a different entry for a recorded sequence number: %v", err)
	}
	gap := mk(6)[5:]
	if err := s.Commit(ctx, gap); !errors.Is(err, ErrConflict) {
		t.Fatalf("a gap: %v", err)
	}
}

func TestCommit_WithoutSecondaryStorageIsRefused(t *testing.T) {
	s := NewStore(Config{LocalPath: filepath.Join(t.TempDir(), "l.jsonl")})
	if err := s.Commit(context.Background(), mk(1)); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(s.LocalPath); err == nil {
		t.Fatal("something was written without the second copy")
	}
}

func TestCommit_ATornTailFromAnInterruptedWriteIsRepaired(t *testing.T) {
	s, _, local, _ := newStore(t)
	all := mk(4)
	ctx := context.Background()
	if err := s.Commit(ctx, all[:2]); err != nil {
		t.Fatal(err)
	}
	l, _ := all[2].Line()
	f, _ := os.OpenFile(local, os.O_APPEND|os.O_WRONLY, 0o600)
	f.Write(l[:len(l)/3])
	f.Close()
	if err := s.Commit(ctx, all[2:]); err != nil {
		t.Fatalf("commit after a torn write: %v", err)
	}
	p := ParseChain(mustRead(t, local))
	if !p.Intact || len(p.Entries) != 4 {
		t.Fatalf("local = intact:%v n:%d err:%v", p.Intact, len(p.Entries), p.Err)
	}
}

func TestCommit_ADamagedLocalCopyBlocksNewEntries(t *testing.T) {
	s, _, local, _ := newStore(t)
	all := mk(4)
	ctx := context.Background()
	if err := s.Commit(ctx, all[:3]); err != nil {
		t.Fatal(err)
	}
	b := mustRead(t, local)
	b[len(b)/2] ^= 0x55
	if err := os.WriteFile(local, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, all[3:]); !errors.Is(err, ErrLocalDamaged) {
		t.Fatalf("err = %v", err)
	}
}

func TestCommit_ConcurrentCallersSerialiseAndNeverInterleave(t *testing.T) {
	s, _, local, _ := newStore(t)
	all := mk(12)
	ctx := context.Background()
	if err := s.Commit(ctx, all[:1]); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var conflicts atomic.Int32
	for i := 1; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// every goroutine offers the whole remainder up to its own entry; exactly one chain results
			err := s.Commit(ctx, all[1:i+1])
			if errors.Is(err, ErrConflict) {
				conflicts.Add(1)
			} else if err != nil {
				t.Errorf("commit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	p := ParseChain(mustRead(t, local))
	if !p.Intact || len(p.Entries) != 12 {
		t.Fatalf("local = intact:%v n:%d err:%v (conflicts %d)", p.Intact, len(p.Entries), p.Err, conflicts.Load())
	}
}

func TestAbort_RemovesOnlyTheEntriesItWasGiven(t *testing.T) {
	s, _, local, ext := newStore(t)
	all := mk(4)
	ctx := context.Background()
	if err := s.Commit(ctx, all); err != nil {
		t.Fatal(err)
	}
	// not the tail: nothing happens
	if err := s.Abort(ctx, all[1:2]); err != nil {
		t.Fatal(err)
	}
	if n := len(ParseChain(mustRead(t, local)).Entries); n != 4 {
		t.Fatalf("an entry that is not the tail was removed: %d", n)
	}
	if err := s.Abort(ctx, all[3:]); err != nil {
		t.Fatal(err)
	}
	if n := len(ParseChain(mustRead(t, local)).Entries); n != 3 {
		t.Fatalf("the tail was not removed: %d", n)
	}
	if n := len(ParseChain(mustRead(t, filepath.Join(ext, ExternalObject))).Entries); n != 3 {
		t.Fatalf("the secondary was not realigned: %d", n)
	}
}

// ---- the encrypted external copy -------------------------------------------------------------------------------------

func TestExternalCopyIsEncryptedAndOnlyAKeyHolderCanReadItBack(t *testing.T) {
	s, _, _, ext := newStore(t)
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	s.Cipher = func(context.Context) (*Cipher, error) {
		return &Cipher{Recipients: []age.Recipient{id.Recipient()}, Fingerprint: "fp1"}, nil
	}
	all := mk(3)
	ctx := context.Background()
	if err := s.Commit(ctx, all); err != nil {
		t.Fatal(err)
	}
	obj := mustRead(t, filepath.Join(ext, ExternalObject+backupcrypt.Suffix))
	if bytes.Contains(obj, []byte("erasure_ref")) {
		t.Fatal("the external copy is readable without a key")
	}
	if _, err := os.Stat(filepath.Join(ext, ExternalObject)); err == nil {
		t.Fatal("a plaintext copy was left beside the encrypted one")
	}
	info, ok, found, err := s.ReadExternalHead(ctx)
	if err != nil || !ok || !found || info.Seq != 3 || !info.Encrypted || info.CipherFP != "fp1" {
		t.Fatalf("head = %+v ok=%v found=%v err=%v", info, ok, found, err)
	}
	// without a key it can be described but not verified
	if c := s.ReadExternal(ctx, nil); c.Reach == nil {
		t.Fatal("an encrypted copy was read without a key")
	}
	other, _ := age.GenerateX25519Identity()
	if c := s.ReadExternal(ctx, other); c.Reach == nil {
		t.Fatal("the wrong key opened the copy")
	}
	c := s.ReadExternal(ctx, id)
	if c.Reach != nil || !c.Intact() || len(c.Parse.Entries) != 3 {
		t.Fatalf("with the right key: %+v", c)
	}
	// a changed object is noticed by the note's hash even without the key
	obj[len(obj)-3] ^= 0xff
	if err := os.WriteFile(filepath.Join(ext, ExternalObject+backupcrypt.Suffix), obj, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, _, _ := s.ReadExternalHead(ctx); ok {
		t.Fatal("a tampered object still matched its note")
	}
	if c := s.ReadExternal(ctx, id); c.Intact() {
		t.Fatal("a tampered object was accepted")
	}
	// switching encryption off removes the stale encrypted object
	s.Cipher = nil
	if err := s.PushExternal(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ext, ExternalObject+backupcrypt.Suffix)); err == nil {
		t.Fatal("a stale encrypted copy was left")
	}
}

func TestRewriteLocalAndAppendLocalHealFromAVerifiedChain(t *testing.T) {
	s, _, local, _ := newStore(t)
	all := mk(5)
	if err := s.RewriteLocal(all[:3]); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendLocal(all[3:]); err != nil {
		t.Fatal(err)
	}
	if p := ParseChain(mustRead(t, local)); !p.Intact || len(p.Entries) != 5 {
		t.Fatalf("local: %+v", p)
	}
	bad := mk(3)
	bad[1].Body = "{}"
	if err := s.RewriteLocal(bad); !errors.Is(err, ErrConflict) {
		t.Fatalf("an invalid chain was written: %v", err)
	}
	if err := s.AppendLocal(mk(9)[7:]); !errors.Is(err, ErrConflict) {
		t.Fatalf("a gap was appended: %v", err)
	}
}

// ---- destinations ----------------------------------------------------------------------------------------------------

func TestDirDest_PutIsAtomicAndCheckRoundTrips(t *testing.T) {
	d := &DirDest{Root: t.TempDir()}
	ctx := context.Background()
	if err := d.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "x", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "x", []byte("two")); err != nil {
		t.Fatal(err)
	}
	if b, err := d.Get(ctx, "x"); err != nil || string(b) != "two" {
		t.Fatalf("get = %q %v", b, err)
	}
	if _, err := d.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	es, _ := os.ReadDir(d.Root)
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".partial") || e.Name() == "ledger-probe.tmp" {
			t.Errorf("left behind: %s", e.Name())
		}
	}
	for _, bad := range []string{"", "../x", "a/b", ".."} {
		if err := d.Put(ctx, bad, nil); err == nil {
			t.Errorf("object name %q accepted", bad)
		}
	}
	rel := &DirDest{Root: "relative/dir"}
	if rel.Check(ctx) == nil {
		t.Error("a relative folder was accepted")
	}
	called := false
	ind := &DirDest{Root: d.Root, Independent: func(string) error { called = true; return errors.New("that is the appliance's own disk") }}
	if err := ind.Check(ctx); err == nil || !called {
		t.Errorf("independence was not enforced: %v", err)
	}
}

func TestSigningKeyMatchesTheAWSPublishedVector(t *testing.T) {
	// From the AWS Signature Version 4 documentation ("Examples of how to derive a signing key").
	got := hex.EncodeToString(signingKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20120215", "us-east-1", "iam"))
	if got != "f4780e2d9f65fa895f9c67b32ce1baf0b0d8a43505a000a1a9e090d414db404d" {
		t.Fatalf("signing key = %s", got)
	}
}
