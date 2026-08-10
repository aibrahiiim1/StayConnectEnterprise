package main

// THE WRITE-AHEAD ACTIVATION JOURNAL.
//
// A guest is admitted to the internet on a short provisional lease BEFORE their Session can be proven durably
// active. That interval is bounded — but the bound only exists if something durable knows the attempt started.
//
// Recording it at the END of reconciliation, with the rest of the class inventory, is too late. Between the
// nft element going in and that save landing there is a real window, and a crash inside it leaves an
// authorized guest with no durable record that any grace was ever spent. The next process starts, finds a
// perfectly valid older state file — or no file at all, which looks exactly like a clean first run — and
// awards a brand-new grace. Repeat the crash and the guest holds provisional access indefinitely, with every
// individual bound in the code still correct.
//
// So the record is WRITE-AHEAD: the attempt and its deadline are fsynced to disk, and the directory entry
// fsynced too, BEFORE the first provisional authorization is installed. If that write cannot be proven
// durable, the guest is not authorized at all. The ordering is the guarantee:
//
//	durable attempt record  →  provisional nft authorization  →  ... → proven ACTIVE → record cleared
//
// Two asymmetries are deliberate:
//
//	A failed WRITE denies access. There is nothing to recover, because nothing was granted.
//	A failed CLEAR leaves the record behind. A stale record is harmless to a session that can be proven
//	active (the proof clears it again next pass) and protective for one that cannot, so failing to remove it
//	is always the safe direction.
//
// The journal is a separate file from the class inventory on purpose. The inventory is written once per
// reconciliation and describes what is installed; this is written at an exact instant and describes what is
// OWED. Sharing a file would mean either fsyncing the whole inventory mid-pass or leaving this record as late
// as the inventory — which is the defect.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
)

// activationAttempt is one unproven activation, and the durable bound on it.
type activationAttempt struct {
	// Key is classKey(bridge, sessionID) — stable across restarts and reboots.
	Key       string `json:"key"`
	SessionID string `json:"session_id"`
	// BootID identifies the boot whose monotonic timeline the two readings below belong to. Across a boot
	// change they are not old — they are meaningless, and the code must not pretend otherwise.
	BootID string `json:"boot_id"`
	// BeganBootMs / DeadlineBootMs are BOOT-RELATIVE monotonic milliseconds (see phase3_securitytime.go).
	// They are the authority for whether the grace is spent.
	BeganBootMs    int64 `json:"began_boot_ms"`
	DeadlineBootMs int64 `json:"deadline_boot_ms"`
	// Strikes counts attempts that exhausted the grace; it drives the backoff and only ever grows.
	Strikes int `json:"strikes"`
	// BackoffUntilBootMs is when re-admission may next be attempted, again boot-relative.
	BackoffUntilBootMs int64 `json:"backoff_until_boot_ms,omitempty"`
	// BeganWall is AUDIT ONLY — a human-readable timestamp for an operator reading the file. Nothing decides
	// anything from it, because the wall clock is exactly what this design refuses to trust.
	BeganWall string `json:"began_wall_audit_only,omitempty"`
}

// journalState is the on-disk form.
type journalState struct {
	TenantID    string              `json:"tenant_id"`
	SiteID      string              `json:"site_id"`
	ApplianceID string              `json:"appliance_id"`
	Attempts    []activationAttempt `json:"attempts"`
}

// activationJournal persists attempts with an explicit durability boundary.
type activationJournal struct {
	mu   sync.Mutex
	path string
	// writeErr lets a test inject a durable-write failure at the exact seam that matters. Production leaves
	// it nil; the field exists because "what happens when the disk is full" must be a test, not a hope.
	writeErr error
	// syncErr injects an fsync failure specifically, which is the failure that makes a write look successful
	// and still not be on the platter.
	syncErr error
}

// journalUnreadable distinguishes "this appliance has never recorded an attempt" from "the record exists and
// cannot be trusted". They demand opposite behaviour, and conflating them is how a corrupt file becomes a
// fresh grace period.
type journalLoad struct {
	Attempts   []activationAttempt
	Unreadable bool
}

func (j *activationJournal) load() journalLoad {
	if j == nil || j.path == "" {
		return journalLoad{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := os.ReadFile(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			return journalLoad{} // never written: no attempt has ever been begun
		}
		return journalLoad{Unreadable: true}
	}
	var st journalState
	if json.Unmarshal(raw, &st) != nil {
		return journalLoad{Unreadable: true}
	}
	return journalLoad{Attempts: st.Attempts}
}

// save writes the whole attempt set and returns only after the bytes AND the directory entry are fsynced.
//
// The caller treats a non-nil error as "the bound is not durable", which means no guest is authorized. That is
// why every failure mode here is returned rather than logged: a write this function could not prove is a write
// the caller must not act on.
func (j *activationJournal) save(tenant, site, appliance string, attempts []activationAttempt) error {
	if j == nil || j.path == "" {
		return nil // no journal configured (unit tests that drive the kernel halves directly)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.writeErr != nil {
		return j.writeErr
	}
	sort.Slice(attempts, func(a, b int) bool { return attempts[a].Key < attempts[b].Key })
	raw, err := json.Marshal(journalState{
		TenantID: tenant, SiteID: site, ApplianceID: appliance, Attempts: attempts})
	if err != nil {
		return err
	}
	dir := filepath.Dir(j.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if j.syncErr != nil {
		_ = f.Close()
		return j.syncErr
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, j.path); err != nil {
		return err
	}
	// The directory entry too: without this fsync the rename itself can be lost to a power cut, and the unit
	// comes back with no record of an attempt it had already authorized a guest for.
	//
	// On LINUX — the only platform this daemon runs on in production — a failure here is fatal to the write,
	// because a record that may not survive a power cut is not a record the caller may act on. On other
	// platforms (Windows, used for development) the OS does not permit syncing a directory handle at all;
	// that is a platform limitation rather than an I/O error, and it is named explicitly here rather than
	// swallowed everywhere, so the strict path is the one that actually ships.
	d, err := os.Open(dir)
	if err != nil {
		if runtime.GOOS == "linux" {
			return fmt.Errorf("journal directory could not be opened for sync: %w", err)
		}
		return nil
	}
	if err := d.Sync(); err != nil {
		_ = d.Close()
		if runtime.GOOS == "linux" {
			return fmt.Errorf("journal directory sync failed: %w", err)
		}
		return nil
	}
	return d.Close()
}

// ---- the in-memory view the reconciliation works against --------------------

// quarantineState is one class's activation-uncertainty record, in SECURITY time.
//
// Every field here is boot-relative monotonic milliseconds. There is deliberately no wall-clock field: the
// only wall-clock value in this subsystem is BeganWall in the durable record, and it is labelled audit-only
// so that a future reader cannot mistake it for something the code consults.
type quarantineState struct {
	// BootID is the boot these readings belong to. When it differs from the current boot the readings are not
	// comparable, and the session is handled by the cross-boot path rather than by arithmetic.
	BootID string
	// BeganBootMs is when this attempt first failed to prove ACTIVE. Zero means it has not failed yet.
	BeganBootMs int64
	// DeadlineBootMs is when the grace for the current attempt expires.
	DeadlineBootMs int64
	// BackoffUntilBootMs is when re-admission may next be attempted.
	BackoffUntilBootMs int64
	Strikes            int
	// BeganWall is carried through so the durable record keeps its audit timestamp across a rewrite.
	BeganWall string
}

// crossBoot reports that this record belongs to a previous boot, so its monotonic readings cannot be compared
// with the current clock at all.
func (q *quarantineState) crossBoot(currentBootID string) bool {
	return q.BootID != "" && currentBootID != "" && q.BootID != currentBootID
}

// journalAttempts renders the in-memory records for persistence. Caller holds p.mu.
func (p *phase3Shaping) journalAttempts() []activationAttempt {
	out := make([]activationAttempt, 0, len(p.quarantined))
	for k, q := range p.quarantined {
		if q == nil {
			continue
		}
		out = append(out, activationAttempt{
			Key: k, SessionID: p.sessionForKey(k), BootID: q.BootID,
			BeganBootMs: q.BeganBootMs, DeadlineBootMs: q.DeadlineBootMs,
			Strikes: q.Strikes, BackoffUntilBootMs: q.BackoffUntilBootMs,
			BeganWall: q.BeganWall,
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

// sessionForKey recovers the session id a class key belongs to, for the durable record's audit field.
func (p *phase3Shaping) sessionForKey(key string) string {
	if c, ok := p.classes[key]; ok {
		return c.SessionID
	}
	for i := 0; i < len(key); i++ {
		if key[i] == '|' {
			return key[i+1:]
		}
	}
	return ""
}

// persistAttempts writes the current attempt set through the durability boundary. Caller holds p.mu.
func (p *phase3Shaping) persistAttempts() error {
	if p.journal == nil {
		return nil
	}
	return p.journal.save(p.mode.TenantID, p.mode.SiteID, p.mode.ApplianceID, p.journalAttempts())
}

// beginAttempt is the WRITE-AHEAD BOUNDARY. Caller holds p.mu.
//
// It records that an unproven activation is about to be granted provisional internet access, and the latest
// monotonic instant at which that access must end, and it returns only once both are fsynced to disk. A
// non-empty return means the bound is NOT durable, and the caller must not authorize the guest.
//
// It is idempotent for an attempt already in progress: a retry of the same admission must continue the same
// countdown, not restart it. That is the property a crash loop would otherwise exploit.
func (p *phase3Shaping) beginAttempt(ctx context.Context, key, sessionID string, nowBoot int64) string {
	if p.journal == nil {
		return "" // no journal configured (unit tests that drive the kernel halves directly)
	}
	if p.unprovenUnknown {
		// The journal exists and cannot be parsed, so THIS session's history is unknown — it may have been
		// failing for hours. Writing a fresh record would erase that unknown and award a new grace, which is
		// how corrupting one file becomes unbounded provisional access.
		//
		// One thing still resolves it honestly: durable state proving the Session is already active. Such a
		// guest needs no grace at all, so they are admitted; everyone else is refused until an operator
		// resolves the file, which is the right ceremony for destroyed security state.
		if p.enforcement != nil {
			if active, err := p.enforcement.Confirm(ctx, sessionID); err == nil && active {
				return ""
			}
		}
		return "the durable activation journal is unreadable, so no grace period can be granted; not authorizing"
	}
	q := p.quarantineFor(key)
	if q.BeganBootMs != 0 && !q.crossBoot(p.currentBootID()) {
		return "" // already begun on this boot's timeline; the existing deadline stands
	}
	q.BootID = p.currentBootID()
	q.BeganBootMs = nowBoot
	q.DeadlineBootMs = nowBoot + phase3ActivationGrace.Milliseconds()
	q.BeganWall = p.wallStamp()
	if err := p.journal.save(p.mode.TenantID, p.mode.SiteID, p.mode.ApplianceID, p.journalAttempts()); err != nil {
		// Roll the in-memory record back so a later pass does not believe a bound exists that was never
		// written. Nothing was authorized, so there is nothing else to undo.
		delete(p.quarantined, key)
		return "the activation bound could not be made durable (" + err.Error() + "); not authorizing"
	}
	return ""
}

// restoreAttempts rebuilds the in-memory activation records from the durable journal.
//
// It is deliberately unconditional — no scope check, no boot check, no kernel check. Those checks exist to
// decide whether a CLASS may be carried forward, and a class whose generation can no longer be trusted is
// correctly dropped. An unproven activation is a different kind of fact: it says a guest has already been
// given provisional access on somebody's clock, and no amount of doubt about the kernel makes that untrue.
// Dropping it would award a fresh grace, which is the opposite of the safe direction.
//
// An UNREADABLE journal is not an empty one. If the file exists and cannot be parsed, every session's history
// is unknown, and an activation that cannot be proven is then treated as having already spent its grace.
func (p *phase3Shaping) restoreAttempts(l journalLoad) {
	p.quarantined = map[string]*quarantineState{}
	p.unprovenUnknown = l.Unreadable
	for _, a := range l.Attempts {
		if a.Key == "" {
			continue
		}
		p.quarantined[a.Key] = &quarantineState{
			BootID: a.BootID, BeganBootMs: a.BeganBootMs, DeadlineBootMs: a.DeadlineBootMs,
			BackoffUntilBootMs: a.BackoffUntilBootMs, Strikes: a.Strikes, BeganWall: a.BeganWall,
		}
	}
}
