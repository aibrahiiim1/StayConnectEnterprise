package main

import (
	"context"
	"log/slog"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nftconverge"
)

// The reconciliation engine lives in internal/nftconverge so that the REAL-KERNEL suite can drive the same code
// against real nft in a disposable namespace. A copy of it here would be a copy the kernel gate does not test —
// and this mechanism's whole claim is about what the kernel actually ends up holding.
//
// See internal/nftconverge for why the stored bundle is not the source of truth, and why a steady-state restart
// must issue no nft command at all.

type nftConvergeOutcome = nftconverge.Outcome

// applierRunner adapts the applier's command seams to the engine.
type applierRunner struct{ a *applier }

func (r applierRunner) Run(ctx context.Context, name string, args ...string) error {
	return r.a.run(ctx, name, args...)
}

func (r applierRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.a.output(ctx, name, args...)
}

func (a *applier) nftEngine() *nftconverge.Engine {
	return &nftconverge.Engine{Topo: a.topo, Dir: a.generatedDir, NftPath: a.nftPath, R: applierRunner{a}}
}

// ensureNftStructure makes the live ruleset match what this binary renders for the given intent, and returns
// whether anything had to change.
func (a *applier) ensureNftStructure(ctx context.Context, intent []netcfg.GuestNetwork, trigger string) (nftConvergeOutcome, error) {
	if a.dryRun {
		return nftConvergeOutcome{Trigger: trigger, DesiredFP: netcfg.RenderFingerprint(intent, a.topo)}, nil
	}
	res, err := a.nftEngine().Ensure(ctx, intent, trigger)
	if err != nil {
		return res, err
	}
	if res.Changed {
		slog.Info("netd nft structure converged",
			"trigger", trigger, "from_fp", res.LiveFP, "to_fp", res.DesiredFP,
			"table_existed", res.TableWas, "carried_elements", res.Carried)
	}
	return res, nil
}
