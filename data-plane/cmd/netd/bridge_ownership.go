package main

// Which bridges netd may delete. A bridge is a resource shared with the rest of the machine: netd creates only the ones its revisions
// describe, and may remove only the ones it can PROVE it created. The previous rule, "any bridge whose name starts with br-g that the
// current configuration does not want", would delete one an operator or another tool made.
//
// Proof is either of:
//   - the ownership mark netd puts on every bridge and VLAN interface it creates (the interface alias "onegate:<network id>"), or
//   - the bridge's name appearing in a revision netd applied (active, superseded, awaiting confirmation, applying or rolled back),
//     which is how a bridge created before the mark existed is recognised. The record also names the member interface netd attached.
//
// A healthy network's bridge is never a candidate: it is wanted by the configuration in force. An existing wanted bridge without a mark
// is ADOPTED (marked) once its member matches the configuration, so later cleanup does not depend on the record alone.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// ownerMarkPrefix is the interface alias prefix that marks a resource netd created.
const ownerMarkPrefix = "onegate:"

// recordedBridge is what the applied revisions say about a bridge netd created.
type recordedBridge struct {
	Member string // the interface attached to it: the VLAN sub-interface, or the parent for an untagged network
}

// ownerMark reads a resource's ownership mark ("" when it has none).
func (a *applier) ownerMark(name string) string {
	if a.ownerMarkFn != nil {
		return a.ownerMarkFn(name)
	}
	raw, err := os.ReadFile("/sys/class/net/" + name + "/ifalias")
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(raw))
	if strings.HasPrefix(v, ownerMarkPrefix) {
		return strings.TrimPrefix(v, ownerMarkPrefix)
	}
	return ""
}

// setOwnerMark marks a resource netd created or adopted. A failure is returned and recorded by the caller: an unmarked resource stays
// recognisable through the revision record, so it is not fatal.
func (a *applier) setOwnerMark(ctx context.Context, name, networkID string) error {
	if networkID == "" {
		return nil
	}
	return a.run(ctx, "ip", "link", "set", "dev", name, "alias", ownerMarkPrefix+networkID)
}

// recordedBridges returns the bridges netd's applied revisions describe.
func (a *applier) recordedBridges(ctx context.Context) (map[string]recordedBridge, error) {
	if a.recordedFn != nil {
		return a.recordedFn(ctx)
	}
	if a.st == nil {
		return nil, nil
	}
	return a.st.RecordedBridges(ctx)
}

// proveOwned reports whether netd can prove it created the live bridge: marked, or recorded by an applied revision.
func (a *applier) proveOwned(name string, recorded map[string]recordedBridge) bool {
	if a.ownerMark(name) != "" {
		return true
	}
	_, ok := recorded[name]
	return ok
}

// removeStrayBridges destroys the live guest bridges that keep() does not want AND that netd can prove it owns. A nil keep removes every
// owned bridge (the factory-clean tear-down). unmanaged lists the br-g* bridges left alone because their origin cannot be proven; they are
// reported, never deleted. err is returned only when the bridges could not be listed, which is not permission to guess.
func (a *applier) removeStrayBridges(ctx context.Context, keep func(string) bool) (removed, failed, unmanaged []string, err error) {
	live, err := a.liveGuestBridges()
	if err != nil {
		return nil, nil, nil, err
	}
	recorded, rerr := a.recordedBridges(ctx)
	if rerr != nil {
		// Without the record only marked bridges are provable; an unmarked, unrecorded-looking bridge is left alone.
		recorded = nil
	}
	var names []string
	for br := range live {
		names = append(names, br)
	}
	sort.Strings(names)
	for _, br := range names {
		if keep != nil && keep(br) {
			continue
		}
		if !a.proveOwned(br, recorded) {
			unmanaged = append(unmanaged, br)
			continue
		}
		if derr := a.destroyOwnedBridge(ctx, br, recorded); derr != nil {
			failed = append(failed, br+": "+derr.Error())
			continue
		}
		removed = append(removed, br)
	}
	if rerr != nil && len(unmanaged) > 0 {
		failed = append(failed, "the applied-revision record could not be read ("+rerr.Error()+"), so unmarked bridges "+
			strings.Join(unmanaged, ",")+" were left alone")
	}
	return removed, failed, unmanaged, nil
}

// destroyOwnedBridge deletes a bridge netd owns and the VLAN sub-interface it attached, but only a member netd can prove it created: the
// member carries the mark, or it is exactly the interface the record says netd attached.
func (a *applier) destroyOwnedBridge(ctx context.Context, bridge string, recorded map[string]recordedBridge) error {
	if bridge == "" || bridge == a.topo.MgmtInterface || bridge == a.topo.WANInterface || bridge == a.legacyBridge {
		return fmt.Errorf("refusing to delete %q: it is not a managed guest bridge", bridge)
	}
	member := a.bridgeMemberOf(bridge)
	if err := a.run(ctx, "ip", "link", "del", bridge); err != nil {
		return fmt.Errorf("delete bridge %s: %w", bridge, err)
	}
	if member == "" || !strings.Contains(member, ".") {
		return nil
	}
	if a.ownerMark(member) == "" {
		if rec, ok := recorded[bridge]; !ok || rec.Member != member {
			return nil // not provably netd's VLAN interface: left in place
		}
	}
	if err := a.run(ctx, "ip", "link", "del", member); err != nil {
		return fmt.Errorf("delete VLAN interface %s of %s: %w", member, bridge, err)
	}
	return nil
}

// adoptWantedBridges marks the wanted bridges that already exist without a mark. Called only after the configuration's member check has
// passed for them, so the bridge is verified to be the network the configuration says it is.
func (a *applier) adoptWantedBridges(ctx context.Context, desired map[string]netcfg.GuestNetwork, live map[string]bool) []string {
	var problems []string
	for br, n := range desired {
		if !live[br] || n.ID == "" || a.ownerMark(br) != "" {
			continue
		}
		wantMember := n.ParentInterface
		if n.NetworkType == "vlan" {
			wantMember = netcfg.VLANIfaceName(n.ParentInterface, n.VLANID)
		}
		if got := a.bridgeMemberOf(br); got != "" && got != wantMember {
			continue // not what the configuration says: not adopted
		}
		if err := a.setOwnerMark(ctx, br, n.ID); err != nil {
			problems = append(problems, "mark "+br+": "+err.Error())
		}
		if n.NetworkType == "vlan" && a.hasIface(wantMember) && a.ownerMark(wantMember) == "" {
			if err := a.setOwnerMark(ctx, wantMember, n.ID); err != nil {
				problems = append(problems, "mark "+wantMember+": "+err.Error())
			}
		}
	}
	return problems
}
