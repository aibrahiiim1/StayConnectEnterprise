// Package ledger is the GDPR erasure ledger: an append-only, hash-chained record of every erasure and closed
// data-protection request, kept in two places outside the database (a local file and an external copy) so a
// restored database can be brought back to what the guest was promised.
//
// It holds no guest data: only record kinds and ids, the guest reference, timing and counts. A copy is intact only
// if every entry parses, the sequence has no gap and each hash chains; it is complete only if it also reaches every
// available independent witness (assess.go). What cannot be proved is never assumed.
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

var GenesisPrev = strings.Repeat("0", 64)

const hashDomain = "stayconnect-ledger-v1"

// Entry is one line of the ledger. Body is the exact stored JSON, carried verbatim so the database and this package
// compute the same hash.
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

// ComputeHash covers everything except the hash; the database computes the same value (iam_v2.ledger_hash) and the
// two must agree byte for byte.
func (e Entry) ComputeHash() string {
	s := strings.Join([]string{hashDomain, e.Ledger, strconv.FormatInt(e.Seq, 10), e.Prev, e.Kind, e.Tenant, e.Site,
		strconv.FormatInt(e.AtUS, 10), e.Body}, "\x1f")
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// Line is one JSON object and a newline; a line is complete only when its newline is there.
func (e Entry) Line() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

type Parse struct {
	Entries []Entry
	Intact  bool
	// Torn is true when the input ended in a fragment without a newline (an interrupted write); it is ignored.
	Torn bool
	Err  error
	// ValidBytes is the length of the valid prefix, for truncating a torn file back to a good state.
	ValidBytes int
}

func (p *Parse) Head() (int64, string) {
	if len(p.Entries) == 0 {
		return 0, ""
	}
	h := p.Entries[len(p.Entries)-1]
	return h.Seq, h.Hash
}

func (p *Parse) LedgerID() string {
	if len(p.Entries) == 0 {
		return ""
	}
	return p.Entries[0].Ledger
}

var ErrCorrupt = errors.New("ledger is damaged")

// ParseChain reads a ledger file, trusting nothing: every line is parsed strictly and checked against the chain.
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
