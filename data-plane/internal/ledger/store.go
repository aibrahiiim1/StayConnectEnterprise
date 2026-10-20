package ledger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"filippo.io/age"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

// Names of the objects kept at the external destination.
const (
	ExternalObject = "erasure-ledger.jsonl"
	ExternalHead   = "erasure-ledger.head.json"
)

var (
	// ErrNotConfigured: no secondary storage is set up. Erasure is refused until it is: the ledger needs two copies.
	ErrNotConfigured = errors.New("the secondary storage for the erasure ledger is not set up")
	// ErrConflict: what was offered does not continue the ledger as it stands.
	ErrConflict = errors.New("the entry does not continue the erasure ledger")
	// ErrLocalDamaged: the copy on the appliance is damaged and must be healed before anything is added.
	ErrLocalDamaged = errors.New("the erasure ledger on the appliance is damaged")
)

// ExternalError is a failure of the secondary storage (not of the appliance), so a caller can tell the administrator which
// of the two places to look at.
type ExternalError struct{ Err error }

func (e *ExternalError) Error() string { return "the secondary storage failed: " + e.Err.Error() }
func (e *ExternalError) Unwrap() error { return e.Err }

// LocalError is a failure of the appliance's own storage.
type LocalError struct{ Err error }

func (e *LocalError) Error() string {
	return "the ledger on the appliance could not be written: " + e.Err.Error()
}
func (e *LocalError) Unwrap() error { return e.Err }

// Cipher is who the external copy is encrypted to: the hotel's backup key holders (public keys only; the appliance cannot
// read the copy back, which is why recovery from it needs a key holder's key, exactly like a backup).
type Cipher struct {
	Recipients  []age.Recipient
	Fingerprint string
}

// HeadInfo is the small plain-text note kept next to the external copy. It lets the appliance see how far that copy has
// reached, and whether the object is still the one it wrote, WITHOUT being able to decrypt it. It carries no personal data.
type HeadInfo struct {
	Ledger       string `json:"ledger"`
	Seq          int64  `json:"seq"`
	Hash         string `json:"hash"`
	Entries      int    `json:"entries"`
	PlainSHA256  string `json:"plain_sha256"`
	Object       string `json:"object"`
	ObjectSHA256 string `json:"object_sha256"`
	Encrypted    bool   `json:"encrypted"`
	CipherFP     string `json:"recipients_fp,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

// Config wires a Store.
type Config struct {
	// LocalPath is the primary copy: a file on the appliance, mode 0600.
	LocalPath string
	// Dest is the secondary copy; nil when not set up.
	Dest Destination
	// Cipher returns the recipients for the external copy, or nil for none. Called on every push.
	Cipher func(ctx context.Context) (*Cipher, error)
	// Now is the clock, replaceable in tests.
	Now func() time.Time
}

// Store maintains the two copies. All of its methods are safe to call concurrently; they serialise on one lock, which is
// what makes "append, then replicate" one step.
type Store struct {
	Config
	mu sync.Mutex
}

// NewStore returns a Store for cfg.
func NewStore(cfg Config) *Store { return &Store{Config: cfg} }

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ReadLocal reads the primary copy as a Copy for Assess. A torn tail is reported but does not make it damaged.
func (s *Store) ReadLocal() Copy {
	b, err := os.ReadFile(s.LocalPath)
	if errors.Is(err, os.ErrNotExist) {
		return Copy{Name: "local"}
	}
	if err != nil {
		return Copy{Name: "local", Reach: err}
	}
	if len(b) == 0 {
		return Copy{Name: "local"}
	}
	return Copy{Name: "local", Present: true, Parse: ParseChain(b)}
}

// Commit makes entries durable in BOTH places, or in neither: it appends them to the primary copy (fsynced and read back),
// replaces the secondary copy (read back byte for byte), and only then returns nil. If the secondary storage fails, the
// primary copy is put back as it was. The caller reports an erasure as done only after Commit returned nil.
//
// Commit is idempotent: entries the primary copy already holds, with the same hashes, are skipped, so a retry after an
// interrupted call is safe.
func (s *Store) Commit(ctx context.Context, entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Dest == nil {
		return ErrNotConfigured
	}
	if len(entries) == 0 {
		return nil
	}
	cur := s.ReadLocal()
	if cur.Reach != nil {
		return &LocalError{cur.Reach}
	}
	var hs int64
	var hh string
	var size int64
	if cur.Present {
		if cur.Parse.Err != nil {
			return fmt.Errorf("%w: %v", ErrLocalDamaged, cur.Parse.Err)
		}
		hs, hh = cur.Parse.Head()
		size = int64(cur.Parse.ValidBytes)
		if cur.Parse.Torn {
			// An unfinished line is a write that never completed and was never acknowledged: cut it off.
			if err := os.Truncate(s.LocalPath, size); err != nil {
				return &LocalError{err}
			}
		}
	}
	var fresh []Entry
	for _, e := range entries {
		if e.Seq >= 1 && e.Seq <= hs {
			if cur.Parse.Entries[e.Seq-1].Hash != e.Hash {
				return fmt.Errorf("%w: entry %d is not the one already recorded", ErrConflict, e.Seq)
			}
			continue
		}
		fresh = append(fresh, e)
	}
	if len(fresh) > 0 {
		if hs > 0 && fresh[0].Ledger != cur.Parse.LedgerID() {
			return fmt.Errorf("%w: it belongs to another ledger", ErrConflict)
		}
		if err := Extends(hs, hh, fresh); err != nil {
			return fmt.Errorf("%w: %v", ErrConflict, err)
		}
		if err := s.appendLocal(fresh, size); err != nil {
			return &LocalError{err}
		}
	}
	// The secondary copy: always pushed, even when nothing was fresh, so a retry also repairs a copy that fell behind.
	if err := s.pushExternalLocked(ctx); err != nil {
		if len(fresh) > 0 {
			if terr := os.Truncate(s.LocalPath, size); terr != nil {
				return &ExternalError{fmt.Errorf("%w (and the ledger on the appliance could not be put back: %v)", err, terr)}
			}
		}
		return &ExternalError{err}
	}
	return nil
}

// Abort removes entries that Commit made durable but whose database transaction then failed, so the ledger never records
// an erasure that was not reported. It only removes the tail, and only if the tail is exactly those entries. (If the process
// dies before Abort runs, reconciliation adopts the entries instead: see Reconcile in the appliance.)
func (s *Store) Abort(ctx context.Context, entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.ReadLocal()
	if !cur.Present || cur.Parse.Err != nil || len(entries) == 0 {
		return nil
	}
	n := len(cur.Parse.Entries)
	k := len(entries)
	if k > n {
		return nil
	}
	for i := 0; i < k; i++ {
		if cur.Parse.Entries[n-k+i].Hash != entries[i].Hash {
			return nil // not ours: leave it
		}
	}
	keep, err := Encode(cur.Parse.Entries[:n-k])
	if err != nil {
		return err
	}
	if err := os.Truncate(s.LocalPath, int64(len(keep))); err != nil {
		return &LocalError{err}
	}
	if s.Dest != nil {
		_ = s.pushExternalLocked(ctx) // best effort: the next reconciliation repairs it
	}
	return nil
}

func (s *Store) appendLocal(fresh []Entry, size int64) error {
	if err := os.MkdirAll(filepath.Dir(s.LocalPath), 0o700); err != nil {
		return err
	}
	data, err := Encode(fresh)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.LocalPath, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	// Position explicitly (not O_APPEND) so the write lands exactly after the valid prefix.
	if _, err := f.WriteAt(data, size); err != nil {
		f.Close()
		_ = os.Truncate(s.LocalPath, size)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Truncate(s.LocalPath, size)
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	syncDir(filepath.Dir(s.LocalPath))
	// Read back: what is on the disk must be a valid chain reaching the last entry.
	back := s.ReadLocal()
	if !back.Present || back.Parse.Err != nil || back.Parse.Torn {
		_ = os.Truncate(s.LocalPath, size)
		return errors.New("what was written to the appliance's storage could not be read back")
	}
	if seq, hash := back.Parse.Head(); seq != fresh[len(fresh)-1].Seq || hash != fresh[len(fresh)-1].Hash {
		_ = os.Truncate(s.LocalPath, size)
		return errors.New("what was read back from the appliance's storage is not what was written")
	}
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// PushExternal replaces the secondary copy with the primary one, and proves it by reading it back.
func (s *Store) PushExternal(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Dest == nil {
		return ErrNotConfigured
	}
	return s.pushExternalLocked(ctx)
}

func (s *Store) pushExternalLocked(ctx context.Context) error {
	plain, err := os.ReadFile(s.LocalPath)
	if err != nil {
		return fmt.Errorf("the ledger on the appliance could not be read: %w", err)
	}
	p := ParseChain(plain)
	if p.Err != nil || len(p.Entries) == 0 {
		return fmt.Errorf("the ledger on the appliance is not a valid ledger, so it was not copied")
	}
	if p.Torn {
		plain = plain[:p.ValidBytes]
	}
	seq, hash := p.Head()
	obj := plain
	name := ExternalObject
	info := HeadInfo{Ledger: p.LedgerID(), Seq: seq, Hash: hash, Entries: len(p.Entries), PlainSHA256: sum(plain), UpdatedAt: s.now().UTC().Format(time.RFC3339)}
	if s.Cipher != nil {
		c, err := s.Cipher(ctx)
		if err != nil {
			return err
		}
		if c != nil && len(c.Recipients) > 0 {
			var buf bytes.Buffer
			w, err := backupcrypt.Encrypt(&buf, c.Recipients)
			if err != nil {
				return err
			}
			if _, err := w.Write(plain); err != nil {
				return err
			}
			if err := w.Close(); err != nil {
				return err
			}
			obj, name = buf.Bytes(), ExternalObject+backupcrypt.Suffix
			info.Encrypted, info.CipherFP = true, c.Fingerprint
		}
	}
	info.Object, info.ObjectSHA256 = name, sum(obj)
	if err := s.Dest.Put(ctx, name, obj); err != nil {
		return err
	}
	back, err := s.Dest.Get(ctx, name)
	if err != nil {
		return fmt.Errorf("what was written cannot be read back: %w", err)
	}
	if sum(back) != info.ObjectSHA256 {
		return errors.New("what was read back from the secondary storage is not what was written")
	}
	hb, _ := json.Marshal(info)
	if err := s.Dest.Put(ctx, ExternalHead, hb); err != nil {
		return err
	}
	if hback, err := s.Dest.Get(ctx, ExternalHead); err != nil || !bytes.Equal(hback, hb) {
		return errors.New("the note describing the secondary copy could not be read back")
	}
	// A copy kept under the other name (plain after encrypted, or the reverse) would be a second, stale truth.
	other := ExternalObject
	if name == ExternalObject {
		other = ExternalObject + backupcrypt.Suffix
	}
	_ = s.Dest.Remove(ctx, other)
	return nil
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ReadExternalHead returns the note kept with the secondary copy and whether the object it names still matches it.
// found is false when the storage holds no ledger at all.
func (s *Store) ReadExternalHead(ctx context.Context) (info *HeadInfo, objectOK bool, found bool, err error) {
	if s.Dest == nil {
		return nil, false, false, ErrNotConfigured
	}
	hb, err := s.Dest.Get(ctx, ExternalHead)
	if errors.Is(err, ErrNotFound) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, err
	}
	var h HeadInfo
	if err := json.Unmarshal(hb, &h); err != nil || h.Object == "" {
		return nil, false, true, errors.New("the note describing the secondary copy is unreadable")
	}
	obj, err := s.Dest.Get(ctx, h.Object)
	if errors.Is(err, ErrNotFound) {
		return &h, false, true, nil
	}
	if err != nil {
		return nil, false, true, err
	}
	return &h, sum(obj) == h.ObjectSHA256, true, nil
}

// ReadExternal reads and verifies the whole secondary copy. An encrypted copy needs identity (a key holder's key); without
// it the copy can only be described by its note, not verified, and the returned Copy says so in Reach.
func (s *Store) ReadExternal(ctx context.Context, identity age.Identity) Copy {
	c := Copy{Name: "external"}
	if s.Dest == nil {
		c.Reach = ErrNotConfigured
		return c
	}
	info, _, found, err := s.ReadExternalHead(ctx)
	if err != nil {
		c.Reach = err
		return c
	}
	if !found {
		return c
	}
	obj, err := s.Dest.Get(ctx, info.Object)
	if errors.Is(err, ErrNotFound) {
		return c
	}
	if err != nil {
		c.Reach = err
		return c
	}
	if sum(obj) != info.ObjectSHA256 {
		c.Present, c.Parse = true, &Parse{Err: fmt.Errorf("%w: the stored object is not the one that was written", ErrCorrupt)}
		return c
	}
	plain := obj
	if info.Encrypted {
		if identity == nil {
			c.Reach = errors.New("the secondary copy is encrypted: a backup key holder's key is needed to read it")
			return c
		}
		r, err := backupcrypt.Decrypt(bytes.NewReader(obj), identity)
		if err != nil {
			c.Reach = errors.New("that key does not open the secondary copy")
			return c
		}
		plain, err = io.ReadAll(io.LimitReader(r, maxObject))
		if err != nil {
			c.Reach = errors.New("the secondary copy could not be decrypted")
			return c
		}
	}
	c.Present, c.Parse = true, ParseChain(plain)
	if c.Parse.Err == nil && sum(plain) != info.PlainSHA256 && !c.Parse.Torn {
		c.Parse = &Parse{Err: fmt.Errorf("%w: the content is not what the note describes", ErrCorrupt)}
	}
	return c
}

// RewriteLocal replaces the primary copy with entries (a verified chain), atomically. Used to heal a missing or damaged
// primary copy from the database or from the secondary copy. It refuses a chain that is not valid.
func (s *Store) RewriteLocal(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(entries) > 0 {
		if err := Extends(0, "", entries); err != nil {
			return fmt.Errorf("%w: %v", ErrConflict, err)
		}
	}
	data, err := Encode(entries)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.LocalPath), 0o700); err != nil {
		return &LocalError{err}
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.LocalPath), filepath.Base(s.LocalPath)+".*.partial")
	if err != nil {
		return &LocalError{err}
	}
	tn := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		_ = os.Remove(tn)
		return &LocalError{err}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		_ = os.Remove(tn)
		return &LocalError{err}
	}
	tmp.Close()
	_ = os.Chmod(tn, 0o600)
	if err := os.Rename(tn, s.LocalPath); err != nil {
		_ = os.Remove(tn)
		return &LocalError{err}
	}
	syncDir(filepath.Dir(s.LocalPath))
	return nil
}

// AppendLocal adds entries (a valid continuation) to the primary copy WITHOUT touching the secondary copy: used when the
// database holds entries the file lacks. The caller pushes afterwards.
func (s *Store) AppendLocal(entries []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.ReadLocal()
	if cur.Reach != nil {
		return &LocalError{cur.Reach}
	}
	var hs int64
	var hh string
	var size int64
	if cur.Present {
		if cur.Parse.Err != nil {
			return fmt.Errorf("%w: %v", ErrLocalDamaged, cur.Parse.Err)
		}
		hs, hh = cur.Parse.Head()
		size = int64(cur.Parse.ValidBytes)
	}
	if len(entries) == 0 {
		return nil
	}
	if err := Extends(hs, hh, entries); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	if err := s.appendLocal(entries, size); err != nil {
		return &LocalError{err}
	}
	return nil
}
