// Package netstatus derives what an operator is told about a client network from facts, never from the fact that a save or an API call succeeded:
// whether it has ever been confirmed, what the active revision is running now, what is still only saved, what is being applied, how the last change ended,
// and what the appliance observes. It is pure, so every rule can be tested exhaustively.
package netstatus

import (
	"fmt"
	"sort"
	"strings"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

type Status string

const (
	NotActivated   Status = "not_activated"
	Activating     Status = "activating"
	Active         Status = "active"
	ActiveDegraded Status = "active_degraded"
	Disabled       Status = "disabled"
	ChangePending  Status = "change_pending"
	Failed         Status = "failed"
)

// Change is one saved setting that differs from what is running.
type Change struct {
	Field   string `json:"field"`
	Running string `json:"running"`
	Saved   string `json:"saved"`
}

// Revision is the part of a network_config_revisions row the rules need.
type Revision struct {
	Intent []netcfg.GuestNetwork
	Reason string // the recorded failure or rollback reason, for a revision that did not hold
}

// Health is what the appliance observed about the running network; Known is false when it could not be asked.
type Health struct {
	Known bool
	// Failing names the appliance-side checks that failed (port link, VLAN interface, bridge, DHCP, DNS).
	Failing []string
}

// Staged is a switch-on or switch-off the operator asked for that has not been applied yet.
type Staged struct{ Kind string } // "ENABLE" or "DISABLE"

type Input struct {
	Staged        *Staged
	Saved         netcfg.GuestNetwork
	Running       *netcfg.GuestNetwork // this network's entry in the active revision's intent; nil when absent
	EverActivated bool
	InFlight      *Revision // the revision that is applying or awaiting confirmation
	LastFailed    *Revision // the newest revision that failed or was rolled back after the active one
	Health        Health
}

type Result struct {
	Status  Status   `json:"status"`
	Reason  string   `json:"reason"`
	Changes []Change `json:"changes"`
}

// Derive applies the rules, in order of what an operator most needs to know.
func Derive(in Input) Result {
	running := in.Running != nil && in.Running.Enabled
	var changes []Change
	if running {
		changes = Diff(*in.Running, in.Saved)
	} else if in.Saved.Enabled {
		changes = []Change{{Field: "enabled", Running: "off", Saved: "on"}}
	}

	// A revision that is being applied or awaits confirmation speaks first.
	if in.InFlight != nil {
		if fl := find(in.InFlight.Intent, in.Saved.ID); fl != nil {
			switch {
			case fl.Enabled && !running:
				return Result{Activating, "It is being applied and waits for confirmation. It starts serving clients once confirmed, or the change is rolled back.", changes}
			case running != fl.Enabled || len(Diff(runningOrEmpty(in.Running), *fl)) > 0:
				return Result{ChangePending, "A change to it is being applied and waits for confirmation.", changes}
			}
		}
	}

	if in.Staged != nil {
		switch {
		case in.Staged.Kind == "ENABLE" && !running:
			what := "Activation is staged. Review and apply to start it; it serves clients only once confirmed."
			if in.EverActivated {
				what = "Switching it back on is staged. Review and apply to start it; it serves clients only once confirmed."
			}
			return Result{ChangePending, what, []Change{{Field: "enabled", Running: "off", Saved: "on"}}}
		case in.Staged.Kind == "DISABLE" && running:
			return Result{ChangePending, "Switching it off is staged. Clients stay connected until it is applied.", []Change{{Field: "enabled", Running: "on", Saved: "off"}}}
		}
	}

	if running {
		if len(changes) > 0 {
			if in.LastFailed != nil && sameSettings(find(in.LastFailed.Intent, in.Saved.ID), in.Saved) {
				return failed(in.LastFailed, changes)
			}
			return Result{ChangePending, "Saved settings differ from what is running (" + fields(changes) + "). Review and apply to make them take effect.", changes}
		}
		if in.Health.Known && len(in.Health.Failing) > 0 {
			return Result{ActiveDegraded, "It is confirmed and running, but the appliance observes a fault: " + strings.Join(in.Health.Failing, ", ") + ".", nil}
		}
		return Result{Active, "Confirmed and running.", nil}
	}

	if in.Saved.Enabled {
		if in.LastFailed != nil && sameSettings(find(in.LastFailed.Intent, in.Saved.ID), in.Saved) {
			return failed(in.LastFailed, changes)
		}
		return Result{ChangePending, "Saved as enabled but not running. Apply to start it.", changes}
	}
	if in.EverActivated {
		return Result{Disabled, "Switched off. Its settings are kept; enabling it needs no new activation.", nil}
	}
	return Result{NotActivated, "Prepared but never activated. Nothing serves clients on it until it is activated and confirmed.", nil}
}

func failed(r *Revision, changes []Change) Result {
	why := strings.TrimSpace(r.Reason)
	if why == "" {
		why = "no reason was recorded"
	}
	return Result{Failed, "The last change did not hold: " + why, changes}
}

func find(nets []netcfg.GuestNetwork, id string) *netcfg.GuestNetwork {
	for i := range nets {
		if nets[i].ID == id {
			return &nets[i]
		}
	}
	return nil
}

func runningOrEmpty(n *netcfg.GuestNetwork) netcfg.GuestNetwork {
	if n == nil {
		return netcfg.GuestNetwork{}
	}
	return *n
}

// sameSettings reports whether the failed revision carried these very settings, so the failure explains the change still pending.
func sameSettings(a *netcfg.GuestNetwork, b netcfg.GuestNetwork) bool {
	if a == nil {
		return false
	}
	return a.Enabled == b.Enabled && len(Diff(*a, b)) == 0
}

func fields(cs []Change) string {
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.Field)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Diff lists the operator-visible settings that differ between what is running and what is saved.
func Diff(running, saved netcfg.GuestNetwork) []Change {
	var out []Change
	add := func(field, r, s string) {
		if r != s {
			out = append(out, Change{Field: field, Running: r, Saved: s})
		}
	}
	onoff := func(b bool) string {
		if b {
			return "on"
		}
		return "off"
	}
	add("enabled", onoff(running.Enabled), onoff(saved.Enabled))
	add("name", running.Name, saved.Name)
	add("gateway_ip", running.GatewayIP, saved.GatewayIP)
	add("subnet_cidr", running.SubnetCIDR, saved.SubnetCIDR)
	add("dhcp_mode", string(running.DHCPMode), string(saved.DHCPMode))
	add("dns_mode", running.DNSMode, saved.DNSMode)
	add("dns_servers", strings.Join(running.DNSServers, ","), strings.Join(saved.DNSServers, ","))
	add("domain_name", running.DomainName, saved.DomainName)
	add("lease_default_seconds", fmt.Sprint(running.LeaseDefault), fmt.Sprint(saved.LeaseDefault))
	add("lease_min_seconds", fmt.Sprint(running.LeaseMin), fmt.Sprint(saved.LeaseMin))
	add("lease_max_seconds", fmt.Sprint(running.LeaseMax), fmt.Sprint(saved.LeaseMax))
	add("captive_portal_enabled", onoff(running.CaptiveEnabled), onoff(saved.CaptiveEnabled))
	add("internet_access_enabled", onoff(running.InternetEnabled), onoff(saved.InternetEnabled))
	add("nat_enabled", onoff(running.NATEnabled), onoff(saved.NATEnabled))
	add("client_isolation_enabled", onoff(running.ClientIsolation), onoff(saved.ClientIsolation))
	add("pools", poolsText(running.Pools), poolsText(saved.Pools))
	return out
}

func poolsText(p []netcfg.Pool) string {
	parts := make([]string, 0, len(p))
	for _, x := range p {
		parts = append(parts, x.StartIP+"-"+x.EndIP)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}
