package main

// netd may delete only a bridge it can PROVE it created. A bridge name alone proves nothing: the same br-g<vlan> may have belonged to a network
// netd removed long ago and been recreated by someone else since. Proof needs a record and live evidence that agree:
//   - the record: a revision netd applied (active, superseded, awaiting confirmation, applying or rolled back) names the bridge, with the networks that
//     used it and the interface it was built on;
//   - the live bridge: it carries netd's ownership mark (alias "onegate:<network id>") naming one of THOSE networks, or, for a bridge created before
//     the mark existed, its member interface is exactly the one the record says.
// Anything else named br-g* is reported and left alone, as is everything when the record cannot be read.

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

// recordedBridge is what the applied revisions say about a bridge name.
type recordedBridge struct {
	Member string          // the interface attached to it: the VLAN sub-interface, or the parent for an untagged network; "" when the record is ambiguous
	IDs    map[string]bool // every network that used this bridge name
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

// proveOwned reports whether the record and the live bridge agree that netd created it.
func (a *applier) proveOwned(name string, recorded map[string]recordedBridge) bool {
	rec, ok := recorded[name]
	if !ok {
		return false
	}
	if mark := a.ownerMark(name); mark != "" {
		return rec.IDs[mark]
	}
	member := a.bridgeMemberOf(name)
	return rec.Member != "" && member == rec.Member
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
		failed = append(failed, "the applied-revision record could not be read ("+rerr.Error()+"), so nothing could be proven netd's and bridges "+
			strings.Join(unmanaged, ",")+" were left in place")
	}
	return removed, failed, unmanaged, nil
}

// destroyOwnedBridge deletes a bridge netd owns and the VLAN sub-interface it attached, but only a member netd can prove it created: it carries the
// mark of a network the record ties to this bridge, or it is exactly the interface the record says netd attached.
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
	rec := recorded[bridge]
	if mark := a.ownerMark(member); mark != "" {
		if !rec.IDs[mark] {
			return nil
		}
	} else if rec.Member != member {
		return nil
	}
	if err := a.run(ctx, "ip", "link", "del", member); err != nil {
		return fmt.Errorf("delete VLAN interface %s of %s: %w", member, bridge, err)
	}
	return nil
}

// adoptWantedBridges marks the wanted bridges that already exist without a mark, but only when the bridge's single member is exactly the interface
// the configuration says; an unreadable member is not evidence.
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
		if a.bridgeMemberOf(br) != wantMember {
			continue // the member is unreadable or is not what the configuration says: not adopted
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
