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

/** One optional module as the appliance reports it (edged relays scd's four-gate resolver). */
export type ModuleFlags = {
  deployed: boolean;
  licensed: boolean;
  enabled: boolean;
  ready: boolean;
  effective: boolean;
  manageable: boolean;
};

export type Capabilities = {
  /** Resource names edged has mounted on this appliance, e.g. "pms-stays". */
  surfaces: string[];
  /** Optional-module state, or null when the appliance could not report it (treat every module as absent). */
  modules?: Record<string, ModuleFlags> | null;
};

/** While unknown, nothing is hidden: a menu that empties itself for a second on every load would be worse
 *  than one that is briefly optimistic, and every page enforces its own state anyway. */
export const CAPABILITIES_UNKNOWN: Capabilities | null = null;

let cached: Capabilities | null = null;
let cachedAt = 0;
let inflight: Promise<Capabilities> | null = null;

/** Kept for a minute: surfaces change when edged is redeployed, but MODULE state changes when a licence is
 *  re-issued or a module is switched, and a page must not keep offering a module for the rest of the session. */
const CAPABILITIES_TTL_MS = 60_000;

/** Forget the cached answer (after switching a module, so the menu follows at once). */
export function invalidateCapabilities() {
  cached = null;
  inflight = null;
}

export function loadCapabilities(): Promise<Capabilities> {
  if (cached && Date.now() - cachedAt < CAPABILITIES_TTL_MS) return Promise.resolve(cached);
  if (!inflight) {
    inflight = api.get<Capabilities>("/capabilities")
      .then((c) => {
        cached = {
          surfaces: Array.isArray(c?.surfaces) ? c.surfaces : [],
          modules: c && typeof c.modules === "object" && c.modules ? c.modules : null,
        };
        cachedAt = Date.now();
        inflight = null;
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

/** MODULE-OWNED SURFACES follow the licence and the site's choice (docs/architecture/
 *  ONEGATE_MODULES_AND_ACQUISITION.md). edged stops reporting one whose module is not manageable here. This
 *  list mirrors `surfaceModules` in data-plane/cmd/edged/modules.go, which a test checks. */
export const MODULE_SURFACES: readonly string[] = [
  "pms-stays", "pms-events", "pms-resolutions", "guest-signin-attempts", "guest-signin-credentials",
  "guest-signin-protection", "guest-signin-restrictions", "checkout-grace", "operational-alerts",
  "pms-interfaces", "pms-routing", "pms-source-conflicts", "pms-reconciliation", "pms-roster-reconciliation",
  "post-stay-profiles", "stay-transfers", "pms-financial-onboarding", "payment-providers",
  "financial-review", "financial-ops",
];

/** Whether a nav resource is served here.
 *
 *  Unknown answers YES for a core surface — see CAPABILITIES_UNKNOWN — and NO for a module-owned one. A
 *  licence-controlled destination is offered only on a positive answer: the menu must never present a module
 *  the site is not licensed for, even for the moment before the answer arrives. */
export function surfaceAvailable(caps: Capabilities | null, resource: string): boolean {
  if (!caps || caps.surfaces.length === 0) return !MODULE_SURFACES.includes(resource);
  return caps.surfaces.includes(resource);
}

/** OPTIONAL MODULES FAIL CLOSED. A module the appliance has not confirmed is treated as absent: a page never
 *  shows a hotel, card or optional sign-in concept on the strength of "we could not tell". */
export function moduleLicensed(caps: Capabilities | null, id: string): boolean {
  const m = caps?.modules?.[id];
  return !!m && m.deployed && m.licensed;
}

/** Licensed, switched on and ready: what a client can actually be offered. */
export function moduleEffective(caps: Capabilities | null, id: string): boolean {
  return !!caps?.modules?.[id]?.effective;
}

/** Hospitality history (stays, PMS activity, reconciliation) is kept after the licence stops covering it. */
export function moduleHasHistory(caps: Capabilities | null, id: string): boolean {
  return !!caps?.modules?.[id]?.manageable;
}
