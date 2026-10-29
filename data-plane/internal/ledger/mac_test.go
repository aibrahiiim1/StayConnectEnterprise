package ledger

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

var tKey = MACKey([]byte("0123456789abcdef0123456789abcdef"))

// macChain builds a chain whose first `legacy` entries carry no MAC and the rest do.
func macChain(n, legacy int) []Entry {
	es := mk(n)
	for i := range es {
		if i >= legacy {
			es[i].Mac = tKey.EntryMAC(es[i].Hash)
		}
	}
	return es
}

// seal appends a MACed seal entry that names the last legacy entry.
func seal(es []Entry, through int, head string) []Entry {
	prev := es[len(es)-1]
	body, _ := json.Marshal(map[string]any{"legacy_through": through, "legacy_head": head})
	s := Entry{Ledger: tLedger, Seq: prev.Seq + 1, Prev: prev.Hash, Kind: KindSeal, Tenant: tTenant, Site: tSite, AtUS: 5, Body: string(body)}
	s.Hash = s.ComputeHash()
	s.Mac = tKey.EntryMAC(s.Hash)
	return append(es, s)
}

// AD-3: the vectors the database computes (iam_v2.ledger_mac_of / ledger_receipt_of) are HMAC-SHA256 over domain US hash.
func TestMACIsDomainSeparated(t *testing.T) {
	h := mk(2)[1].Hash
	if tKey.EntryMAC(h) == tKey.Receipt(h) {
		t.Fatal("an entry MAC must never be usable as a durability receipt")
	}
	if tKey.EntryMAC(h) != tKey.EntryMAC(h) || len(tKey.EntryMAC(h)) != 64 {
		t.Fatal("the MAC is not stable hex")
	}
	other := MACKey([]byte("ffffffffffffffffffffffffffffffff"))
	if other.EntryMAC(h) == tKey.EntryMAC(h) {
		t.Fatal("another key made the same MAC")
	}
}

// The same values iam_v2.ledger_mac_of / ledger_receipt_of return for this key (checked against PostgreSQL pgcrypto when the
// migration was written): the two implementations must agree byte for byte or no entry would ever verify.
func TestMACMatchesTheDatabaseVector(t *testing.T) {
	if got := tKey.EntryMAC("abc"); got != "7febd33c0dbc8d4f7183d7e43b2cb966f9573e4cfc447c333f9773f8486b68bf" {
		t.Fatalf("entry MAC = %s", got)
	}
	if got := tKey.Receipt("abc"); got != "7aaedfdb7109065bf130828f54debcc8ee66fb850a0f3c328283396224a4543e" {
		t.Fatalf("receipt = %s", got)
	}
	if tKey.Hex() != "3031323334353637383961626364656630313233343536373839616263646566" {
		t.Fatal("hex form changed")
	}
}

func TestAuthenticate(t *testing.T) {
	// Fully MACed: fine.
	if err := tKey.Authenticate(macChain(4, 0)); err != nil {
		t.Fatalf("a fully authenticated chain was refused: %v", err)
	}
	// Legacy entries with no seal: refused (never applied on ambiguity).
	if err := tKey.Authenticate(macChain(4, 2)); !errors.Is(err, ErrUnauthentic) {
		t.Fatalf("unsealed legacy entries were accepted: %v", err)
	}
	// Legacy entries covered by a valid seal: fine.
	es := macChain(2, 2)
	es = seal(es, 2, es[1].Hash)
	if err := tKey.Authenticate(es); err != nil {
		t.Fatalf("a sealed legacy ledger was refused: %v", err)
	}
	if n := tKey.Unsealed(es); n != 0 {
		t.Fatalf("unsealed = %d, want 0", n)
	}
	// A seal naming the wrong head covers nothing.
	bad := macChain(2, 2)
	bad = seal(bad, 2, "deadbeef")
	if err := tKey.Authenticate(bad); !errors.Is(err, ErrUnauthentic) {
		t.Fatalf("a seal that names the wrong hash was honoured: %v", err)
	}
	// A seal whose own MAC is forged covers nothing.
	forged := macChain(2, 2)
	forged = seal(forged, 2, forged[1].Hash)
	forged[2].Mac = "aa" + forged[2].Mac[2:]
	if err := tKey.Authenticate(forged); !errors.Is(err, ErrUnauthentic) {
		t.Fatalf("a seal with a forged MAC was honoured: %v", err)
	}
	// An entry appended by someone without the key, after a good ledger: refused.
	good := macChain(3, 0)
	extra := Entry{Ledger: tLedger, Seq: 4, Prev: good[2].Hash, Kind: "erase", Tenant: tTenant, Site: tSite, AtUS: 9, Body: `{"erasure_ref":"ER-FORGED"}`}
	extra.Hash = extra.ComputeHash()
	if err := tKey.Authenticate(append(good, extra)); !errors.Is(err, ErrUnauthentic) {
		t.Fatalf("a forged appended entry was accepted: %v", err)
	}
	extra.Mac = other()
	if err := tKey.Authenticate(append(good, extra)); !errors.Is(err, ErrUnauthentic) {
		t.Fatalf("an entry MACed with another key was accepted: %v", err)
	}
	// A nil key disables the check (an appliance whose database predates the key).
	if err := MACKey(nil).Authenticate(append(good, extra)); err != nil {
		t.Fatalf("a nil key must not check: %v", err)
	}
}

func other() string { return MACKey([]byte("ffffffffffffffffffffffffffffffff")).EntryMAC("x") }

func TestCommitRefusesAnUnauthenticEntryWhenAKeyIsSet(t *testing.T) {
	s, _, local, _ := newStore(t)
	s.MACKey = func() MACKey { return tKey }
	good := macChain(2, 0)
	if err := s.Commit(context.Background(), good); err != nil {
		t.Fatalf("authentic entries refused: %v", err)
	}
	extra := Entry{Ledger: tLedger, Seq: 3, Prev: good[1].Hash, Kind: "erase", Tenant: tTenant, Site: tSite, AtUS: 9, Body: `{}`}
	extra.Hash = extra.ComputeHash()
	if err := s.Commit(context.Background(), []Entry{extra}); !errors.Is(err, ErrUnauthentic) {
		t.Fatalf("an unauthentic entry was committed: %v", err)
	}
	if p := ParseChain(mustRead(t, local)); len(p.Entries) != 2 {
		t.Fatalf("the unauthentic entry reached the primary copy (%d entries)", len(p.Entries))
	}
}

func TestHeadNoteIsAuthenticated(t *testing.T) {
	s, _, _, ext := newStore(t)
	s.MACKey = func() MACKey { return tKey }
	if err := s.Commit(context.Background(), macChain(2, 0)); err != nil {
		t.Fatal(err)
	}
	info, ok, found, err := s.ReadExternalHead(context.Background())
	if err != nil || !found || !ok || info.Mac == "" {
		t.Fatalf("head note = %+v ok=%v found=%v err=%v", info, ok, found, err)
	}
	// Someone with write access to the storage rewrites the note to claim a longer ledger.
	forged := *info
	forged.Seq, forged.Entries = 99, 99
	b, _ := json.Marshal(forged)
	if err := os.WriteFile(filepath.Join(ext, ExternalHead), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.ReadExternalHead(context.Background()); !errors.Is(err, ErrUnauthentic) {
		t.Fatalf("a forged head note was believed: %v", err)
	}
	c := s.ReadExternal(context.Background(), nil)
	if c.Reach == nil {
		t.Fatal("a copy under a forged note must not be treated as readable")
	}
	_ = strconv.Itoa
}
