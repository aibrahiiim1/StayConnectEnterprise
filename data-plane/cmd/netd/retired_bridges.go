package main

import "context"

// endReasonNetworkRemoved is recorded for a session whose client network was replaced or switched off: its bridge is gone for good.
const endReasonNetworkRemoved = "NETWORK_REMOVED"

// retiredBridgeCheck answers, for one pass, whether a guest bridge belongs to a client network that no longer exists: the bridge is
// not on the system and no network of the confirmed configuration is switched on with it. It answers never (nil) while an apply is
// applying or awaiting confirmation, or when either fact cannot be read, so a bridge an apply is rebuilding is never taken for a removed one.
func (a *applier) retiredBridgeCheck(ctx context.Context) func(bridge string) bool {
	if id, _, err := a.inFlight(ctx); err != nil || id != "" {
		return nil
	}
	live, err := a.liveGuestBridges()
	if err != nil {
		return nil
	}
	id, _, intent, err := a.currentActiveIntent(ctx)
	if err != nil || id == "" {
		return nil
	}
	named := map[string]bool{}
	for _, n := range intent {
		if n.Enabled {
			named[n.BridgeName] = true
		}
	}
	return func(bridge string) bool {
		if bridge == "" || bridge == a.legacyBridge || len(bridge) < 4 || bridge[:4] != "br-g" {
			return false
		}
		return !live[bridge] && !named[bridge]
	}
}
