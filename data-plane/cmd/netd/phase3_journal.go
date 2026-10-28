package main

// Write-ahead activation journal: a guest gets a provisional lease before the Session is proven active, so
// the attempt and its deadline are fsynced (file and directory entry) BEFORE the first provisional
// authorization. Otherwise a crash in that window awards a fresh grace on restart, repeatably.
//
//	durable attempt record  →  provisional nft authorization  →  ... → proven ACTIVE → record cleared
//
// A failed write denies access (nothing was granted); a failed clear leaves the record, which is the safe
// direction. It is a separate file from the class inventory, which is written once per pass.

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
	// Key is classKey(bridge, sessionID).
	Key       string `json:"key"`
	SessionID string `json:"session_id"`
	// BootID is the boot whose monotonic timeline the readings below belong to; across a boot change they are
	// meaningless.
	BootID string `json:"boot_id"`
	// Boot-relative monotonic milliseconds (see phase3_securitytime.go); the authority for whether the grace is spent.
	BeganBootMs    int64 `json:"began_boot_ms"`
	DeadlineBootMs int64 `json:"deadline_boot_ms"`
	// Strikes counts attempts that exhausted the grace; drives the backoff and only grows.
	Strikes            int   `json:"strikes"`
	BackoffUntilBootMs int64 `json:"backoff_until_boot_ms,omitempty"`
	// BeganWall is audit only; nothing decides from the wall clock.
	BeganWall string `json:"began_wall_audit_only,omitempty"`
}

type journalState struct {
	TenantID    string              `json:"tenant_id"`
	SiteID      string              `json:"site_id"`
	ApplianceID string              `json:"appliance_id"`
	Attempts    []activationAttempt `json:"attempts"`
}

type activationJournal struct {
	mu   sync.Mutex
	path string
	// writeErr and syncErr are test-only failure injection; production leaves them nil.
	writeErr error
	syncErr  error
}

// journalLoad separates "never recorded an attempt" from "the record exists and cannot be trusted"; conflating
// them turns a corrupt file into a fresh grace period.
type journalLoad struct {
	Attempts   []activationAttempt
	Unreadable bool
}

// load takes the scope as a parameter so the file cannot be read without stating whose it must be.
func (j *activationJournal) load(tenant, site, appliance string) journalLoad {
	if j == nil || j.path == "" {
		return journalLoad{}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := os.ReadFile(j.path)
	if err != nil {
		if os.IsNotExist(err) {
			// The only legitimate clean first run; every other outcome is unknown.
			return journalLoad{}
		}
		return journalLoad{Unreadable: true}
	}
	var st journalState
	if json.Unmarshal(raw, &st) != nil {
		return journalLoad{Unreadable: true}
	}
	// A journal from another Tenant/Site/Appliance (restored image, cloned VM) says nothing about these
	// sessions; reading it as "no attempts" would award a fresh grace. It is unknown, never re-scoped.
	if err := validateScope(st, tenant, site, appliance); err != nil {
		slog.Warn("phase3: the activation journal does not belong to this appliance; treating it as unknown",
			"err", err)
		return journalLoad{Unreadable: true}
	}
	// Valid JSON is not trust: an edited deadline, negative start or duplicate key silently widens the grace,
	// so an incoherent file is unknown (fail-closed). Never normalise it into a plausible record.
	if err := validateAttempts(st.Attempts); err != nil {
		slog.Warn("phase3: the activation journal parsed but is not coherent; treating it as unknown",
			"err", err)
		return journalLoad{Unreadable: true}
	}
	return journalLoad{Attempts: st.Attempts}
}

// validateScope requires exact equality with every value present on both sides: an empty Tenant is not
// harmless, its ownership cannot be established.
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

// parseClassKey inverts classKey; exactly one separator, because a key readable two ways (`br|a|b`) can be
// made to describe a different session.
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

// maxPlausibleBootMs (one century) rejects values chosen to push a deadline out of reach and avoids overflow.
const maxPlausibleBootMs int64 = 100 * 365 * 24 * 60 * 60 * 1000

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
			// Disagreeing identities could enforce one session's bound against another.
			return fmt.Errorf("record %q: the key names session %q but the record names %q",
				a.Key, keySession, a.SessionID)
		}
		if seen[a.Key] {
			return fmt.Errorf("record %q: duplicate activation key", a.Key)
		}
		seen[a.Key] = true

		hasBootRelative := a.BeganBootMs != 0 || a.DeadlineBootMs != 0 || a.BackoffUntilBootMs != 0
		if hasBootRelative && !plausibleBootID(strings.TrimSpace(a.BootID)) {
			// Boot-relative readings without a trustworthy boot could be measured against a new uptime.
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
				// A record may not claim more grace than the policy grants.
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

// save returns only after the bytes and the directory entry are fsynced. A non-nil error means the bound is
// not durable and no guest may be authorized, so every failure is returned, not logged.
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
	// Never write a file load would refuse: a refused journal is read as unknown history after the restart (audit Phase 8 L4).
	if err := validateAttempts(attempts); err != nil {
		return fmt.Errorf("activation journal not written, it would not load back: %w", err)
	}
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
	// Without the directory fsync the rename can be lost to a power cut. Fatal on Linux (production); other
	// platforms (Windows dev) cannot sync a directory handle at all.
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

// quarantineState is one class's activation-uncertainty record. All times are boot-relative monotonic
// milliseconds; the wall clock is never consulted.
type quarantineState struct {
	// BootID is the boot these readings belong to; if it differs from the current boot they are not comparable.
	BootID string
	// BeganBootMs is when this attempt first failed to prove ACTIVE; zero means not yet.
	BeganBootMs        int64
	DeadlineBootMs     int64
	BackoffUntilBootMs int64
	Strikes            int
	// BeganWall is audit only, carried so a rewrite keeps it.
	BeganWall string
	// freshAttempt marks an elapsed backoff that has been spent on one fresh attempt. It is in memory only: the
	// elapsed backoff stays in the record, so a record with strikes always carries a backoff or an attempt and the
	// journal's own validator accepts it after a restart (audit Phase 8 L4).
	freshAttempt bool
}

// strike records an attempt that exhausted its grace and starts the next backoff.
func (q *quarantineState) strike(nowBoot int64) {
	q.Strikes++
	q.BackoffUntilBootMs = nowBoot + quarantineBackoff(q.Strikes).Milliseconds()
	q.freshAttempt = false
}

// crossBoot is TRUE for an empty identity on either side: comparing "" with "" must not read as "same boot",
// or a prior boot's deadline gets measured against the new uptime.
func (q *quarantineState) crossBoot(currentBootID string) bool {
	return q.BootID == "" || currentBootID == "" || q.BootID != currentBootID
}

// Caller holds p.mu.
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

func (p *phase3Shaping) sessionForKey(key string) string {
	if c, ok := p.classes[key]; ok {
		return c.SessionID
	}
	if _, session, ok := parseClassKey(key); ok {
		return session
	}
	return ""
}

// Caller holds p.mu.
func (p *phase3Shaping) persistAttempts() error {
	if p.journal == nil {
		return nil
	}
	return p.journal.save(p.mode.TenantID, p.mode.SiteID, p.mode.ApplianceID, p.journalAttempts())
}

// beginAttempt is the write-ahead boundary: it returns only once the attempt and its deadline are fsynced.
// A non-empty return means the bound is not durable and the guest must not be authorized. It is idempotent
// for an attempt in progress, so a retry continues the countdown instead of restarting it. Caller holds p.mu.
func (p *phase3Shaping) beginAttempt(ctx context.Context, key, sessionID string, nowBoot int64) string {
	if p.journal == nil {
		return "" // no journal configured (unit tests that drive the kernel halves directly)
	}
	if p.unprovenUnknown {
		// The history is unknown; a fresh record would award a new grace. Only durable proof that the Session
		// is already active admits; everyone else waits for an operator to resolve the file.
		if p.enforcement != nil {
			if active, err := p.enforcement.Confirm(ctx, sessionID); err == nil && active {
				return ""
			}
		}
		return "the durable activation journal is unreadable, so no grace period can be granted; not authorizing"
	}
	bootID, bootErr := p.currentBootID()
	if bootErr != nil {
		// Without a trustworthy boot identity a reboot is undetectable; record nothing, authorize nobody.
		return "the boot identity is not trustworthy (" + bootErr.Error() + "); not authorizing"
	}
	prevQ, hadPrev := p.quarantined[key]
	var prev quarantineState
	if hadPrev && prevQ != nil {
		prev = *prevQ
	}
	hadPrev = hadPrev && prevQ != nil
	q := p.quarantineFor(key)
	if q.BeganBootMs != 0 && !q.crossBoot(bootID) {
		return "" // already begun on this boot's timeline; the existing deadline stands
	}
	q.BootID = bootID
	q.BeganBootMs = nowBoot
	q.DeadlineBootMs = nowBoot + phase3ActivationGrace.Milliseconds()
	q.BeganWall = p.wallStamp()
	if err := p.journal.save(p.mode.TenantID, p.mode.SiteID, p.mode.ApplianceID, p.journalAttempts()); err != nil {
		// Roll back so a later pass does not believe a bound exists that was never written. The previous record is
		// put back, not deleted: deleting it would forget the strikes and reset the backoff (audit Phase 8 L4).
		if hadPrev {
			*q = prev
		} else {
			delete(p.quarantined, key)
		}
		return "the activation bound could not be made durable (" + err.Error() + "); not authorizing"
	}
	return ""
}

// restoreAttempts is deliberately unconditional (no scope, boot or kernel check): doubt about a class does not
// undo the fact that a guest was already given provisional access, and dropping it would award a fresh grace.
// An unreadable journal is not an empty one: unproven activations are then treated as having spent their grace.
func (p *phase3Shaping) restoreAttempts(l journalLoad) {
	p.quarantined = map[string]*quarantineState{}
	p.unprovenUnknown = l.Unreadable
	p.journalUnreadable, p.classSaveFailed = l.Unreadable, false
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
