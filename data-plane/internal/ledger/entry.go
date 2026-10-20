// Package ledger is the GDPR erasure ledger: the append-only, hash-chained record of every erasure and of how every
// data-protection request was closed, kept in TWO places outside the database's own tables (a local file on the appliance and
// an independently stored external copy) so that a database restored from an older backup can be brought back to the state
// the hotel had already promised the guest -- erased, and closed.
//
// The ledger holds no guest data. An entry names the kind and the id of a record that was anonymised, the reference given to the
// guest, when, why not, and the counts; never a name, an address or a device address.
//
// WHAT MAKES A COPY TRUSTWORTHY. A file existing proves nothing. A copy is intact only if every entry parses, the sequence has no
// gap, every entry names the hash of the one before it, and every hash is the hash of the entry itself (a chain, so one
// altered, lost or reordered entry breaks everything after it). A copy is COMPLETE only if, besides being intact, it reaches
// every independent witness that is available: the head of the database being restored, the head of the live database, the head
// recorded with every backup, and the other copy. What cannot be proved is said so (assess.go); it is never assumed.
package ledger

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// GenesisPrev is the "previous hash" of the first entry.
var GenesisPrev = strings.Repeat("0", 64)

const hashDomain = "stayconnect-ledger-v1"

// Entry is one line of the ledger. Body is the exact text the database stored (a JSON document); it is carried verbatim and
// never re-serialised, so the hash that covers it can be recomputed by the database and by this package alike.
type Entry struct {
	Ledger string `json:"ledger"`
	Seq    int64  `json:"seq"`
	Prev   string `json:"prev"`
	Kind   string `json:"kind"`
	Tenant string `json:"tenant"`
	Site   string `json:"site"`
	AtUS   int64  `json:"at_us"`
	Body   string `json:"body"`
	Hash   string `json:"hash"`
}

// ComputeHash is the hash of everything in the entry except the hash. The database computes the same value
// (iam_v2.ledger_hash, migration 0148); the two must agree byte for byte.
func (e Entry) ComputeHash() string {
	s := strings.Join([]string{hashDomain, e.Ledger, strconv.FormatInt(e.Seq, 10), e.Prev, e.Kind, e.Tenant, e.Site,
		strconv.FormatInt(e.AtUS, 10), e.Body}, "\x1f")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Line is the entry as it is written: one JSON object and a newline. A line is complete only when its newline is there.
func (e Entry) Line() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Parse is what could be read of a ledger file.
type Parse struct {
	// Entries is the longest valid prefix.
	Entries []Entry
	// Intact is true when the whole input is that prefix: no damaged line, no torn tail.
	Intact bool
	// Torn is true when the input ended in a fragment without its newline (a write that was interrupted); the fragment is
	// ignored, and the prefix before it is still a good ledger.
	Torn bool
	// Err says why the prefix ended, when it did not end at the end of the input.
	Err error
	// ValidBytes is the length of the valid prefix, for truncating a torn file back to a good state.
	ValidBytes int
}

// Head is the last valid entry, or zero values for an empty ledger.
func (p *Parse) Head() (int64, string) {
	if len(p.Entries) == 0 {
		return 0, ""
	}
	h := p.Entries[len(p.Entries)-1]
	return h.Seq, h.Hash
}

// LedgerID is the identity of the ledger the entries belong to ("" when empty).
func (p *Parse) LedgerID() string {
	if len(p.Entries) == 0 {
		return ""
	}
	return p.Entries[0].Ledger
}

// ErrCorrupt describes a damaged ledger.
var ErrCorrupt = errors.New("ledger is damaged")

// ParseChain reads a ledger file. It never trusts the file: every line is parsed strictly and checked against the chain.
func ParseChain(data []byte) *Parse {
	p := &Parse{Intact: true}
	rest := data
	off := 0
	var prev *Entry
	lineNo := 0
	for len(rest) > 0 {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			p.Torn, p.Intact = true, false
			return p
		}
		line := rest[:i]
		lineNo++
		var e Entry
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil || dec.More() {
			p.Intact, p.Err = false, fmt.Errorf("%w: line %d is not a ledger entry", ErrCorrupt, lineNo)
			return p
		}
		if err := checkLink(prev, &e); err != nil {
			p.Intact, p.Err = false, fmt.Errorf("%w: line %d: %v", ErrCorrupt, lineNo, err)
			return p
		}
		p.Entries = append(p.Entries, e)
		prev = &p.Entries[len(p.Entries)-1]
		off += i + 1
		p.ValidBytes = off
		rest = rest[i+1:]
	}
	return p
}

func checkLink(prev, e *Entry) error {
	if e.Seq < 1 || e.Hash == "" || e.Ledger == "" || e.Kind == "" {
		return errors.New("an entry is incomplete")
	}
	if prev == nil {
		if e.Seq != 1 || e.Prev != GenesisPrev {
			return errors.New("the first entry is not the start of a ledger")
		}
	} else {
		if e.Seq != prev.Seq+1 {
			return fmt.Errorf("the sequence jumps from %d to %d", prev.Seq, e.Seq)
		}
		if e.Prev != prev.Hash {
			return errors.New("an entry does not follow the one before it")
		}
		if e.Ledger != prev.Ledger {
			return errors.New("an entry belongs to another ledger")
		}
	}
	if e.ComputeHash() != e.Hash {
		return errors.New("an entry was changed after it was written")
	}
	return nil
}

// Encode writes entries as a ledger file's content.
func Encode(entries []Entry) ([]byte, error) {
	var buf bytes.Buffer
	for _, e := range entries {
		l, err := e.Line()
		if err != nil {
			return nil, err
		}
		buf.Write(l)
	}
	return buf.Bytes(), nil
}

// Extends reports whether next is a valid continuation of the ledger ending at head (seq, hash).
func Extends(headSeq int64, headHash string, next []Entry) error {
	var prev *Entry
	if headSeq > 0 {
		prev = &Entry{Seq: headSeq, Hash: headHash}
		if len(next) > 0 {
			prev.Ledger = next[0].Ledger
		}
	}
	for i := range next {
		if err := checkLink(prev, &next[i]); err != nil {
			return err
		}
		prev = &next[i]
	}
	return nil
}
