"use client";

// WHAT THIS APPLIANCE ACTUALLY SERVES, ASKED RATHER THAN ASSUMED.
//
// Navigation used to be decided by NEXT_PUBLIC_PHASE*_ADMIN, substituted when the bundle is BUILT. edged
// decides what to mount at RUNTIME, from the appliance's own configuration. On PRE-LIVE those two facts
// disagreed about eight destinations — Guest devices, Online-time budgets, Post-stay access, Cross-PMS
// transfer, Charge health, Manual review, Settlements and Recovery were all in the menu and all answered
// 404. An operator clicking one got a blank screen and a console full of errors, with nothing to say the
// feature is not enabled here.
//
// The build flags are still what decide whether a screen is COMPILED IN. This decides whether the appliance
// in front of the operator can actually serve it, which is a different question and the one the menu should
// be answering.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";

export type Capabilities = {
  /** Resource names edged has mounted on this appliance, e.g. "pms-stays". */
  surfaces: string[];
};

/** While unknown, nothing is hidden: a menu that empties itself for a second on every load would be worse
 *  than one that is briefly optimistic, and every page enforces its own state anyway. */
export const CAPABILITIES_UNKNOWN: Capabilities | null = null;

let cached: Capabilities | null = null;
let inflight: Promise<Capabilities> | null = null;

/** One request per browser session. The answer changes when edged is redeployed, not while an operator
 *  works, and every screen in the admin would otherwise ask for it again. */
export function loadCapabilities(): Promise<Capabilities> {
  if (cached) return Promise.resolve(cached);
  if (!inflight) {
    inflight = api.get<Capabilities>("/capabilities")
      .then((c) => {
        cached = { surfaces: Array.isArray(c?.surfaces) ? c.surfaces : [] };
        return cached;
      })
      .catch(() => {
        // An appliance that cannot answer is not an appliance with no features. Failing open keeps the menu
        // as it was and leaves each page to report its own trouble.
        inflight = null;
        return { surfaces: [] };
      });
  }
  return inflight;
}

export function useCapabilities(): Capabilities | null {
  const [caps, setCaps] = useState<Capabilities | null>(cached);
  useEffect(() => {
    let live = true;
    loadCapabilities().then((c) => { if (live) setCaps(c); });
    return () => { live = false; };
  }, []);
  return caps;
}

/** Whether a nav resource is served here. Unknown answers YES — see CAPABILITIES_UNKNOWN. */
export function surfaceAvailable(caps: Capabilities | null, resource: string): boolean {
  if (!caps || caps.surfaces.length === 0) return true;
  return caps.surfaces.includes(resource);
}
