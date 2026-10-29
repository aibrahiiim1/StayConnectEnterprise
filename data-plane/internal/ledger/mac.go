package ledger

// Authentication of the ledger (audit AD-3, migration 0220). The hash chain proves an entry was not changed; it cannot prove
// who wrote it, so anyone able to write the secondary storage could append a well-formed entry that a later restore would
// obey. Every entry is therefore also MACed with a per-appliance key that lives in the appliance database (and, sealed to the
// backup key holders, in the escrow scd writes). Entries written before the key existed carry no MAC; they are accepted only
// when a MACed "seal" entry names the last of them and its hash. The values here are byte-for-byte what the database computes
// (iam_v2.ledger_mac_of / ledger_receipt_of).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	macDomain     = "stayconnect-ledger-mac-v1"
	receiptDomain = "stayconnect-ledger-durable-v1"
	headDomain    = "stayconnect-ledger-head-v1"

	// KindSeal is the entry that makes the unauthenticated entries before it usable.
	KindSeal = "seal"
)

// ErrUnauthentic is returned for an entry (or note) that was not made with this appliance's key.
var ErrUnauthentic = errors.New("the ledger entry was not written by this appliance")

// MACKey is the 32-byte ledger key. A nil key disables authentication (an appliance whose database predates the key).
type MACKey []byte

// ParseMACKey reads the key as the database hands it out (64 hex digits).
func ParseMACKey(hexKey string) (MACKey, error) {
	b, err := hex.DecodeString(hexKey)
	if err != nil || len(b) != 32 {
		return nil, errors.New("the ledger key is not 32 bytes in hex")
	}
	return MACKey(b), nil
}

func (k MACKey) Hex() string { return hex.EncodeToString(k) }

func (k MACKey) mac(domain, msg string) string {
	h := hmac.New(sha256.New, k)
	h.Write([]byte(domain + "\x1f" + msg))
	return hex.EncodeToString(h.Sum(nil))
}

// EntryMAC authenticates an entry by its hash (which already covers every other field).
func (k MACKey) EntryMAC(hash string) string { return k.mac(macDomain, hash) }

// Receipt is what scd gives edged once an entry is durable in both copies; the database accepts a confirmation only with it.
func (k MACKey) Receipt(hash string) string { return k.mac(receiptDomain, hash) }

func eq(a, b string) bool { return hmac.Equal([]byte(a), []byte(b)) }

// EntryOK reports whether e carries a MAC that this key makes.
func (k MACKey) EntryOK(e Entry) bool { return e.Mac != "" && e.Hash != "" && eq(e.Mac, k.EntryMAC(e.Hash)) }

// HeadMAC authenticates the note kept beside the secondary copy.
func (k MACKey) HeadMAC(h HeadInfo) string {
	return k.mac(headDomain, fmt.Sprintf("%s\x1f%d\x1f%s\x1f%d\x1f%s\x1f%s\x1f%s", h.Ledger, h.Seq, h.Hash, h.Entries,
		h.PlainSHA256, h.Object, h.ObjectSHA256))
}

type sealBody struct {
	LegacyThrough int64  `json:"legacy_through"`
	LegacyHead    string `json:"legacy_head"`
}

// SealedThrough returns the highest sequence number covered by a valid seal among entries (entries start at seq 1 and are a
// valid chain): the seal's MAC must check and the entry it names must carry the hash it names. 0 when nothing is sealed.
func (k MACKey) SealedThrough(entries []Entry) int64 {
	var through int64
	for _, e := range entries {
		if e.Kind != KindSeal || !k.EntryOK(e) {
			continue
		}
		var b sealBody
		if json.Unmarshal([]byte(e.Body), &b) != nil || b.LegacyThrough < 1 || b.LegacyThrough > int64(len(entries)) {
			continue
		}
		at := entries[b.LegacyThrough-1]
		if at.Seq != b.LegacyThrough || !eq(at.Hash, b.LegacyHead) || e.Seq <= b.LegacyThrough {
			continue
		}
		if b.LegacyThrough > through {
			through = b.LegacyThrough
		}
	}
	return through
}

// Authenticate checks a whole chain: every entry has a MAC that checks, or is covered by a valid seal. The first entry that
// is neither is reported; a MAC that is present and wrong is a forgery and is never treated as a legacy entry.
func (k MACKey) Authenticate(entries []Entry) error { return k.AuthenticateAfter(entries, 0) }

// AuthenticateAfter checks only the entries after sequence number `after` (those already in the database being restored are
// that database's own history), but still uses the whole chain to find the seals.
func (k MACKey) AuthenticateAfter(entries []Entry, after int64) error {
	if len(k) == 0 {
		return nil
	}
	sealed := k.SealedThrough(entries)
	for _, e := range entries {
		if e.Seq <= after {
			continue
		}
		switch {
		case e.Mac != "":
			if !k.EntryOK(e) {
				return fmt.Errorf("%w: entry %d has an authentication code that does not check", ErrUnauthentic, e.Seq)
			}
		case e.Seq <= sealed:
		default:
			return fmt.Errorf("%w: entry %d has no authentication code and no seal covers it", ErrUnauthentic, e.Seq)
		}
	}
	return nil
}

// CheckPresent verifies the MAC of every entry that carries one. An entry with none is not an error here (it may be a legacy
// entry a seal covers; Authenticate decides that).
func (k MACKey) CheckPresent(entries []Entry) error {
	if len(k) == 0 {
		return nil
	}
	for _, e := range entries {
		if e.Mac != "" && !k.EntryOK(e) {
			return fmt.Errorf("%w: entry %d has an authentication code that does not check", ErrUnauthentic, e.Seq)
		}
	}
	return nil
}

// Unsealed counts the entries that have no MAC and no seal covers.
func (k MACKey) Unsealed(entries []Entry) int {
	sealed := k.SealedThrough(entries)
	n := 0
	for _, e := range entries {
		if e.Mac == "" && e.Seq > sealed {
			n++
		}
	}
	return n
}
