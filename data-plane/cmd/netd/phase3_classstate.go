package main

// DURABLE MANAGED-CLASS STATE AND GENERATION AUTHORITY.
//
// The generation ("epoch") is the only trustworthy answer to "did this counter series restart?". Getting it
// wrong fails in two opposite, equally silent ways:
//
//   RESTART. netd restarts, the kernel classes survive untouched, but in-memory state is empty. If the class
//   is treated as new, acctd — holding a checkpoint at the older generation — refuses every observation as
//   stale. Accounting stops for those sessions with nothing but a warning.
//
//   REBOOT / RESTORE. Every tc class is genuinely gone. If a recreated class reuses a generation a checkpoint
//   still pins, a counter that restarted at zero is measured against the old series: a regression, or a
//   phantom delta billed to whoever now occupies the class.
//
// Two separate guarantees are needed, and neither substitutes for the other:
//
//   AUTHORITY — where a new generation comes from. A durable, appliance-scoped, database-backed allocator
//   that reconciles against the highest generation any surviving accounting checkpoint pins. Explicitly NOT
//   the wall clock: system time moves backwards on RTC reset, NTP correction, offline operation and image
//   restore, and a generation that went backwards is exactly the collision this value exists to prevent. If
//   the allocator cannot be reached, no generation is manufactured — the class is not made accountable.
//
//   CONTINUITY — when an existing generation may be carried forward. A matching Linux boot id is not enough:
//   it says the machine has not rebooted, not that a particular class still exists. A qdisc can be flushed, a
//   class recreated by hand, a minor reused by a different session — all within one boot. Continuity is
//   therefore proven against the ACTUAL kernel inventory, and anything not found there is dropped so its
//   successor allocates a fresh generation.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/shape"
)

// managedClass is what must survive a restart for accounting to continue without a gap or a false reset.
type managedClass struct {
	SessionID string `json:"session_id"`
	DeviceID  string `json:"device_id"`
	Bridge    string `json:"bridge"`
	Minor     int    `json:"minor"`
	Epoch     int64  `json:"epoch"`
	// BootID records which boot issued the generation. It is evidence for an operator reading the file, and a
	// cheap first filter — but it is never the proof that the class still exists.
	BootID string `json:"boot_id"`
}

// classState is the durable managed-class inventory, scoped to one appliance under one assignment.
type classState struct {
	TenantID    string                  `json:"tenant_id"`
	SiteID      string                  `json:"site_id"`
	ApplianceID string                  `json:"appliance_id"`
	BootID      string                  `json:"boot_id"`
	Classes     map[string]managedClass `json:"classes"`
}

// classStore persists the inventory. Writes are atomic and fsynced: a torn file after a power cut would be
// indistinguishable from corruption, and this file's whole purpose is to be trustworthy after exactly that.
type classStore struct {
	mu   sync.Mutex
	path string
}

func (c *classStore) load() (classState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	empty := classState{Classes: map[string]managedClass{}}
	if c.path == "" {
		return empty, false
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return empty, false
	}
	var st classState
	if json.Unmarshal(raw, &st) != nil {
		// A partial or corrupted file is treated as ABSENT, never as "no classes at their old generations".
		// Absent means every surviving class is re-proved against the kernel and re-generated from the
		// allocator; "no classes" would silently authorise reuse.
		return empty, false
	}
	if st.Classes == nil {
		st.Classes = map[string]managedClass{}
	}
	return st, true
}

func (c *classStore) save(st classState) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		return nil
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	// fsync the file, then the directory: without the second, the rename itself can be lost to a power cut
	// and the appliance comes back with the OLD inventory while the kernel has the new classes.
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// readBootID returns a value that changes on every boot and only on a boot.
func readBootID(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ---- generation authority ---------------------------------------------------

// generationAllocator hands out strictly increasing, never-reissued class generations for this appliance.
type generationAllocator interface {
	AllocateClassGeneration(ctx context.Context, tenant, site, appliance string) (int64, error)
}

type pgGenerations struct {
	pool interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	}
}

func (g *pgGenerations) AllocateClassGeneration(ctx context.Context, tenant, site, appliance string) (int64, error) {
	var gen int64
	err := g.pool.QueryRow(ctx,
		`SELECT iam_v2.allocate_class_generation($1::uuid,$2::uuid,$3::uuid)`, tenant, site, appliance).Scan(&gen)
	return gen, err
}

// errNoGenerationAuthority is returned when a class cannot be made accountable because no generation could be
// allocated. It is deliberately not recoverable by guessing.
var errNoGenerationAuthority = errors.New("no class-generation authority available")

// allocEpoch obtains the next generation. It never invents one: if the allocator is unreachable the class is
// left unshaped and the reason is reported, because a class installed without an accountable generation is a
// class whose traffic cannot be attributed to anyone.
func (p *phase3Shaping) allocEpoch(ctx context.Context) (int64, error) {
	if p.generations == nil {
		return 0, errNoGenerationAuthority
	}
	gen, err := p.generations.AllocateClassGeneration(ctx, p.mode.TenantID, p.mode.SiteID, p.mode.ApplianceID)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errNoGenerationAuthority, err)
	}
	if gen <= 0 {
		return 0, errNoGenerationAuthority
	}
	return gen, nil
}

// ---- continuity, proven against the kernel ----------------------------------

// restore rebuilds in-memory state from durable state, keeping ONLY classes the kernel still has.
//
// installed reports which (bridge, minor) slots are actually present; verified says whether that reading
// could be taken at all. A persisted entry is carried forward only when its slot is present and the boot is
// unchanged; anything else — removed, replaced by hand, or a minor now used by a different session — is
// dropped, so its successor allocates a new generation rather than inheriting a checkpoint that describes a
// different counter series.
func (p *phase3Shaping) restore(st classState, bootID string, installed map[string]map[int]bool, verified bool) {
	p.epochs = map[string]int64{}
	p.minorOwner = map[string]string{}
	p.classes = map[string]managedClass{}
	p.bootID = bootID
	p.restoreNote = ""

	// Scope: an inventory written under a different tenant/site/appliance describes someone else's classes.
	if st.TenantID != "" && (st.TenantID != p.mode.TenantID || st.SiteID != p.mode.SiteID ||
		st.ApplianceID != p.mode.ApplianceID) {
		p.restoreNote = "durable class state belongs to a different appliance scope; every class re-generated"
		return
	}
	if !verified {
		// The kernel could not be read, so no claim of continuity can be proved. Dropping the inventory is
		// the safe direction: successors allocate new generations, which is never wrong — carrying one
		// forward without proof can be.
		p.restoreNote = "kernel inventory unreadable at startup; no class generation carried forward"
		return
	}

	sameBoot := bootID != "" && st.BootID == bootID
	dropped := 0
	for k, c := range st.Classes {
		if !sameBoot || !installed[c.Bridge][c.Minor] {
			dropped++
			continue
		}
		p.classes[k] = c
		p.epochs[classKey(c.Bridge, c.SessionID)] = c.Epoch
		p.minorOwner[minorKey(c.Bridge, c.Minor)] = c.SessionID
	}
	if dropped > 0 {
		p.restoreNote = fmt.Sprintf(
			"%d managed class(es) could not be proved present in the kernel; each will be re-generated", dropped)
	}
}

// kernelInventory reads which managed minors are actually installed on the given bridges.
func kernelInventory(ctx context.Context, shp shaper, bridges []string) (map[string]map[int]bool, bool) {
	out := map[string]map[int]bool{}
	for _, b := range bridges {
		classes, err := shp.ReadClasses(ctx, b)
		if err != nil {
			return nil, false
		}
		m := map[int]bool{}
		for minor := range classes {
			if minor >= shape.GuestMinorBase && minor <= shape.GuestMinorMax {
				m[minor] = true
			}
		}
		out[b] = m
	}
	return out, true
}

// bridgesIn lists the bridges a persisted inventory refers to.
func bridgesIn(st classState) []string {
	seen := map[string]bool{}
	for _, c := range st.Classes {
		if c.Bridge != "" {
			seen[c.Bridge] = true
		}
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

// snapshot renders the current inventory for persistence.
func (p *phase3Shaping) snapshot() classState {
	st := classState{
		TenantID: p.mode.TenantID, SiteID: p.mode.SiteID, ApplianceID: p.mode.ApplianceID,
		BootID: p.bootID, Classes: map[string]managedClass{},
	}
	for k, c := range p.classes {
		st.Classes[k] = c
	}
	return st
}
