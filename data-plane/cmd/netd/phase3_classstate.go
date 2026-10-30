package main

// Durable managed-class state. The class generation (epoch) tells acctd whether a counter series restarted:
// it comes from a DB allocator (never the wall clock) and is carried forward only when proven in the kernel.

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
	// BootID is a cheap first filter, never proof that the class still exists.
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

// classStore persists the inventory with atomic, fsynced writes: a torn file after a power cut is indistinguishable from corruption.
type classStore struct {
	mu   sync.Mutex
	path string
}

// load returns (state, usable, unreadableOrCorrupt). Unreadable or corrupt must stay distinct from absent:
// treating it as "nothing ever failed" would grant every guest a fresh grace period.
func (c *classStore) load() (classState, bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	empty := classState{Classes: map[string]managedClass{}}
	if c.path == "" {
		return empty, false, false
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, false, false
		}
		return empty, false, true
	}
	var st classState
	if json.Unmarshal(raw, &st) != nil {
		// Corrupt means absent for classes (re-proved and re-generated), never "no classes", which would
		// silently authorise reuse; for the activation clock it means unknown, which fails closed.
		return empty, false, true
	}
	if st.Classes == nil {
		st.Classes = map[string]managedClass{}
	}
	return st, true, false
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
	// fsync the file, then the directory: otherwise the rename can be lost to a power cut.
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

// errNoGenerationAuthority means no generation could be allocated; never recover by guessing.
var errNoGenerationAuthority = errors.New("no class-generation authority available")

// allocEpoch never invents a generation: without one the class is left unshaped, since its traffic could not be attributed.
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

// restore keeps only persisted classes whose slot is present in the kernel and whose boot is unchanged;
// the rest are dropped so successors allocate a new generation instead of inheriting a stale checkpoint.
func (p *phase3Shaping) restore(st classState, bootID string, installed map[string]map[int]bool, verified bool) {
	p.epochs = map[string]int64{}
	p.minorOwner = map[string]string{}
	p.classes = map[string]managedClass{}
	p.bootID = bootID
	p.restoreNote = ""

	// An inventory from another tenant/site/appliance describes someone else's classes.
	if st.TenantID != "" && (st.TenantID != p.mode.TenantID || st.SiteID != p.mode.SiteID ||
		st.ApplianceID != p.mode.ApplianceID) {
		p.restoreNote = "durable class state belongs to a different appliance scope; every class re-generated"
		return
	}
	if !verified {
		// Without a kernel reading, continuity is unprovable; new generations are never wrong, carried-forward ones can be.
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
			if shape.IsGuestMinor(minor) {
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
