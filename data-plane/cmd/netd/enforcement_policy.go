package main

// The enforcement policy (public.enforcement_policy, migration 0190) is read here and folded into the topology the ruleset
// is rendered from. A change in the table changes the render fingerprint, so the periodic re-assertion (nft_watch.go)
// installs it within one tick without a revision.
//
// FAIL STATIC. A policy that has never been read (database down at boot, schema older than 0190) renders as the previous
// release did: nothing is newly denied. A policy that was read stays in force when a later read fails. Only a successful
// read of the table moves the ruleset.

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

type enforcementPolicy struct {
	PrivateDeny   bool
	PrivateAllow  []string
	AllowUpstream bool
	MACBinding    bool
	CutoffSeconds int
	Version       int64
}

// defaultEnforcementPolicy is the migration's default row.
func defaultEnforcementPolicy() enforcementPolicy {
	return enforcementPolicy{PrivateDeny: true, AllowUpstream: true, MACBinding: true, CutoffSeconds: 5, Version: 0}
}

type policyStore interface {
	LoadEnforcementPolicy(ctx context.Context) (enforcementPolicy, bool, error)
}

func (s *store) LoadEnforcementPolicy(ctx context.Context) (enforcementPolicy, bool, error) {
	p := defaultEnforcementPolicy()
	err := s.db.QueryRow(ctx, `
        SELECT private_deny_enabled, private_allow_cidrs, allow_upstream_infrastructure, mac_binding_enabled, cutoff_seconds, config_version
          FROM public.enforcement_policy LIMIT 1`).
		Scan(&p.PrivateDeny, &p.PrivateAllow, &p.AllowUpstream, &p.MACBinding, &p.CutoffSeconds, &p.Version)
	if errors.Is(err, pgx.ErrNoRows) || undefinedTable(err) {
		return enforcementPolicy{}, false, nil
	}
	if err != nil {
		return enforcementPolicy{}, false, err
	}
	return p, true, nil
}

// upstreamInfrastructure is the appliance's own WAN gateway and resolvers: the hotel's configured upstream. They are /32
// exceptions so the default private deny can never cut the path a guest's own traffic leaves through.
func (a *applier) upstreamInfrastructure() []string {
	if a.upstreamFn != nil {
		return a.upstreamFn()
	}
	var out []string
	if gw := defaultGatewayFor(a.topo.WANInterface); net.ParseIP(gw) != nil {
		out = append(out, gw)
	}
	for _, d := range ifaceDNS(a.topo.WANInterface) {
		out = append(out, d)
	}
	return out
}

// refreshEnforcementPolicy re-reads the policy and the upstream list. It never returns an error: a failed read keeps what is in force.
func (a *applier) refreshEnforcementPolicy(ctx context.Context) {
	ps, ok := a.st.(policyStore)
	if !ok {
		return
	}
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, found, err := ps.LoadEnforcementPolicy(rctx)
	if err != nil {
		a.policyMu.Lock()
		first := a.policyErr == ""
		a.policyErr = err.Error()
		a.policyMu.Unlock()
		if first {
			slog.Warn("netd: the enforcement policy could not be read; the policy in force is kept", "err", err)
		}
		return
	}
	var up []string
	if found && p.AllowUpstream {
		up = a.upstreamInfrastructure()
	}
	a.policyMu.Lock()
	defer a.policyMu.Unlock()
	a.policyErr = ""
	if !found {
		return
	}
	if len(up) == 0 && len(a.policyUpstream) > 0 && p.AllowUpstream {
		up = a.policyUpstream // a transient `ip`/resolvectl failure must not shrink the exceptions
	}
	a.policy, a.policyLoaded, a.policyUpstream = p, true, up
}

// applyPolicy folds the policy into a topology. Called by renderTopo.
func (a *applier) applyPolicy(t *netcfg.Topology) {
	a.policyMu.Lock()
	defer a.policyMu.Unlock()
	if !a.policyLoaded {
		return
	}
	allow := append([]string{}, a.policy.PrivateAllow...)
	if a.policy.AllowUpstream {
		allow = append(allow, a.policyUpstream...)
	}
	t.PrivateReach = netcfg.PrivateReach{Deny: a.policy.PrivateDeny, Allow: allow}
}

// currentPolicy is what the data plane acts on now; the second result is false until a read has succeeded.
func (a *applier) currentPolicy() (enforcementPolicy, bool) {
	a.policyMu.Lock()
	defer a.policyMu.Unlock()
	return a.policy, a.policyLoaded
}

func summarisePolicy(p enforcementPolicy) string {
	return "deny=" + boolWord(p.PrivateDeny) + " allow=" + strings.Join(p.PrivateAllow, ",")
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
