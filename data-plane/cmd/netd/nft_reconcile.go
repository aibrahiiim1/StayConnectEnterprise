package main

import (
	"context"
	"log/slog"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
	"github.com/stayconnect/enterprise/data-plane/internal/nftconverge"
)

// The reconciliation engine lives in internal/nftconverge so the real-kernel suite tests the same code; see it
// for why the stored bundle is not the source of truth and why a steady-state restart issues no nft command.

type nftConvergeOutcome = nftconverge.Outcome

type applierRunner struct{ a *applier }

func (r applierRunner) Run(ctx context.Context, name string, args ...string) error {
	return r.a.run(ctx, name, args...)
}

func (r applierRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.a.output(ctx, name, args...)
}

func (a *applier) nftEngine() *nftconverge.Engine {
	return &nftconverge.Engine{Topo: a.renderTopo(), Dir: a.generatedDir, NftPath: a.nftPath, R: applierRunner{a}}
}

// ensureNftStructure makes the live ruleset match the rendered intent and reports whether anything changed.
func (a *applier) ensureNftStructure(ctx context.Context, intent []netcfg.GuestNetwork, trigger string) (nftConvergeOutcome, error) {
	a.nftMu.Lock()
	defer a.nftMu.Unlock()
	return a.ensureNftStructureLocked(ctx, intent, trigger)
}

// ensureNftStructureLocked requires a.nftMu to be held.
func (a *applier) ensureNftStructureLocked(ctx context.Context, intent []netcfg.GuestNetwork, trigger string) (nftConvergeOutcome, error) {
	if a.dryRun {
		return nftConvergeOutcome{Trigger: trigger, DesiredFP: netcfg.RenderFingerprint(intent, a.renderTopo())}, nil
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
