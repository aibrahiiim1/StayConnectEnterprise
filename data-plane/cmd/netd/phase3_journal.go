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
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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

// load reads the journal for ONE appliance scope. The scope is a parameter, not a field, because the only
// scope that may ever be read is the one currently derived from enrollment and the signed assignment — the
// same value save() writes. Passing it in makes it impossible to read the file without stating whose it must
// be.
func (j *activationJournal) load(tenant, site, appliance string) journalLoad {
	if j == nil || j.path == "" {
		return journalLoad{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := os.ReadFile(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			// THE ONLY legitimate clean first run: the file truly does not exist. Every other outcome below
			// is UNKNOWN, because an appliance that cannot read its own security history does not have one.
			return journalLoad{}
		}
		return journalLoad{Unreadable: true}
	}
	var st journalState
	if json.Unmarshal(raw, &st) != nil {
		return journalLoad{Unreadable: true}
	}
	// THE ENVELOPE MUST BE OURS.
	//
	// A journal carrying another Tenant, Site or Appliance is not this appliance's security history, and it
	// is not an empty one either — it says nothing at all about the sessions here. Reading it as "no attempts
	// outstanding" would award every guest a fresh grace on the strength of a file that describes a different
	// property. It reaches this appliance by the ordinary means: a restored image, a cloned VM, a copied
	// /var/lib directory, a re-homed unit after a tenancy change.
	//
	// It is classified UNKNOWN and never silently re-scoped: rewriting the envelope to match would be
	// adopting another appliance's history as our own, which is worse than having none.
	if err := validateScope(st, tenant, site, appliance); err != nil {
		slog.Warn("phase3: the activation journal does not belong to this appliance; treating it as unknown",
			"err", err)
		return journalLoad{Unreadable: true}
	}
	// SYNTAX IS NOT TRUST. This file is security authority now, and a document that parses is not the same as
	// a document that means something. Every field below can be edited into a shape that is valid JSON and
	// silently widens the grace — a deadline far in the future, a negative start, an absent boot identity
	// beside boot-relative readings, two records fighting over one key. Any of those is treated exactly like
	// an unreadable file: UNKNOWN, and therefore fail-closed.
	//
	// The one thing NOT done here is normalisation. Repairing an incoherent record into a plausible one is how
	// a corrupted security bound becomes a fresh grace period that looks entirely ordinary afterwards.
	if err := validateAttempts(st.Attempts); err != nil {
		slog.Warn("phase3: the activation journal parsed but is not coherent; treating it as unknown",
			"err", err)
		return journalLoad{Unreadable: true}
	}
	return journalLoad{Attempts: st.Attempts}
}

// validateScope proves the journal envelope is this appliance's own, under the assignment it is running.
//
// Exact equality, and every value must be present on both sides: a journal with an empty Tenant is not
// "unscoped and therefore harmless", it is a document whose ownership cannot be established.
func validateScope(st journalState, tenant, site, appliance string) error {
	want := map[string]string{"tenant": tenant, "site": site, "appliance": appliance}
	got := map[string]string{"tenant": st.TenantID, "site": st.SiteID, "appliance": st.ApplianceID}
	for _, field := range []string{"tenant", "site", "appliance"} {
		w, g := strings.TrimSpace(want[field]), strings.TrimSpace(got[field])
		if w == "" {
			return fmt.Errorf("this appliance has no assigned %s id, so no journal can be proven to belong to it", field)
		}
		if g == "" {
			return fmt.Errorf("the journal carries no %s id", field)
		}
		if g != w {
			return fmt.Errorf("the journal belongs to %s %s, not %s", field, g, w)
		}
	}
	return nil
}

// ---- canonical class-key identity -------------------------------------------------------------------------

// parseClassKey is THE canonical reader of a class key, and the inverse of classKey(bridge, sessionID).
//
// The key is `bridge|sessionID` with exactly one separator. Accepting anything looser — "contains a pipe" —
// lets `br-guest|a|b` be read two ways, and a record whose identity can be read two ways is a record that can
// be made to describe a different session than the one it was written for.
func parseClassKey(key string) (bridge, session string, ok bool) {
	i := strings.Index(key, "|")
	if i <= 0 {
		return "", "", false // no separator, or an empty bridge
	}
	if strings.Contains(key[i+1:], "|") {
		return "", "", false // ambiguous: the canonical form has exactly one separator
	}
	bridge, session = key[:i], key[i+1:]
	if strings.TrimSpace(bridge) == "" || strings.TrimSpace(session) == "" {
		return "", "", false
	}
	return bridge, session, true
}

// maxPlausibleBootMs bounds a boot-relative reading. /proc/uptime on a machine that has been up for ten years
// is about 3.2e11 ms; anything past this is not an uptime, it is a value chosen to push a deadline out of
// reach. Bounding it also removes any arithmetic that could overflow into a negative comparison.
const maxPlausibleBootMs int64 = 100 * 365 * 24 * 60 * 60 * 1000 // one century

// validateAttempts checks the whole set for coherent, bounded security state.
func validateAttempts(attempts []activationAttempt) error {
	seen := map[string]bool{}
	for _, a := range attempts {
		if strings.TrimSpace(a.Key) == "" {
			return fmt.Errorf("a record has no activation key")
		}
		bridge, keySession, ok := parseClassKey(a.Key)
		if !ok {
			return fmt.Errorf("record %q: the activation key is not a canonical bridge|session identity", a.Key)
		}
		_ = bridge
		if strings.TrimSpace(a.SessionID) == "" {
			return fmt.Errorf("record %q: no session identity", a.Key)
		}
		if keySession != a.SessionID {
			// The record carries two identities and they disagree. Whichever one a reader happens to use, the
			// other is wrong — so a bound written for one session could be enforced against another, or a
			// session could be looked up and found to have no bound at all.
			return fmt.Errorf("record %q: the key names session %q but the record names %q",
				a.Key, keySession, a.SessionID)
		}
		if seen[a.Key] {
			// Two records for one activation would let a reader pick whichever bound it preferred.
			return fmt.Errorf("record %q: duplicate activation key", a.Key)
		}
		seen[a.Key] = true

		hasBootRelative := a.BeganBootMs != 0 || a.DeadlineBootMs != 0 || a.BackoffUntilBootMs != 0
		if hasBootRelative && !plausibleBootID(strings.TrimSpace(a.BootID)) {
			// Boot-relative numbers without a trustworthy boot they belong to cannot be compared with
			// anything. This is the shape that let a prior boot's deadline be measured against a new uptime.
			return fmt.Errorf("record %q: boot-relative readings with no trustworthy boot identity", a.Key)
		}
		for name, v := range map[string]int64{
			"began": a.BeganBootMs, "deadline": a.DeadlineBootMs, "backoff": a.BackoffUntilBootMs} {
			if v < 0 {
				return fmt.Errorf("record %q: negative %s reading (%d)", a.Key, name, v)
			}
			if v > maxPlausibleBootMs {
				return fmt.Errorf("record %q: implausible %s reading (%d)", a.Key, name, v)
			}
		}
		if a.BeganBootMs != 0 || a.DeadlineBootMs != 0 {
			if a.DeadlineBootMs <= a.BeganBootMs {
				return fmt.Errorf("record %q: deadline %d does not follow start %d", a.Key, a.DeadlineBootMs, a.BeganBootMs)
			}
			if a.DeadlineBootMs-a.BeganBootMs > phase3ActivationGrace.Milliseconds() {
				// THE WIDENED GRACE, caught directly: a record may not claim more grace than the policy grants.
				return fmt.Errorf("record %q: claims a %dms grace, longer than the %dms policy",
					a.Key, a.DeadlineBootMs-a.BeganBootMs, phase3ActivationGrace.Milliseconds())
			}
		}
		if a.Strikes < 0 {
			return fmt.Errorf("record %q: negative strike count (%d)", a.Key, a.Strikes)
		}
		if a.BackoffUntilBootMs != 0 && a.Strikes < 1 {
			return fmt.Errorf("record %q: a backoff is in force with no strike to justify it", a.Key)
		}
		if a.Strikes > 0 && a.BackoffUntilBootMs == 0 && a.BeganBootMs == 0 {
			return fmt.Errorf("record %q: %d strike(s) recorded with neither a backoff nor an attempt in progress",
				a.Key, a.Strikes)
		}
	}
	return nil
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

// crossBoot reports that this record's monotonic readings cannot be compared with the current clock.
//
// It is deliberately TRUE for an empty identity on either side. The earlier form required both to be non-empty
// before it would report a boot change, so an unreadable boot id made it compare "" against "" and answer
// "same boot" — and a prior boot's deadline was then measured against the new boot's uptime. Reboot detection
// that fails open is not reboot detection.
//
// The caller never passes an untrusted identity anyway (currentBootID is now an error-returning read), but the
// predicate is written so that it could not be wrong even if one day it were.
func (q *quarantineState) crossBoot(currentBootID string) bool {
	return q.BootID == "" || currentBootID == "" || q.BootID != currentBootID
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
	if _, session, ok := parseClassKey(key); ok {
		return session
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
	bootID, bootErr := p.currentBootID()
	if bootErr != nil {
		// Without a trustworthy boot identity a reboot cannot be detected, so a deadline recorded now could
		// later be compared against a different boot's uptime. Nothing is recorded and nobody is authorized.
		return "the boot identity is not trustworthy (" + bootErr.Error() + "); not authorizing"
	}
	q := p.quarantineFor(key)
	if q.BeganBootMs != 0 && !q.crossBoot(bootID) {
		return "" // already begun on this boot's timeline; the existing deadline stands
	}
	q.BootID = bootID
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
