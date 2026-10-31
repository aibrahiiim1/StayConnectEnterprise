"use client";

// Navigation asks the appliance what edged actually mounted at runtime; NEXT_PUBLIC_PHASE* build flags only decide what
// is compiled in.

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

/**
 * While unknown, nothing is hidden: a menu that empties briefly on load is worse, and pages enforce their own state.
 */
export const CAPABILITIES_UNKNOWN: Capabilities | null = null;

let cached: Capabilities | null = null;
let cachedAt = 0;
let inflight: Promise<Capabilities> | null = null;

/**
 * Cached for a minute: module state changes when a licence is re-issued, and a page must not keep offering a module for
 * the whole session.
 */
const CAPABILITIES_TTL_MS = 60_000;

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
        // An appliance that cannot answer is not one with no features: fail open and let each page report its own
        // trouble.
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

/**
 * Module-owned surfaces follow the licence and the site's choice. Mirrors `surfaceModules` in
 * data-plane/cmd/edged/modules.go (a test checks).
 */
export const MODULE_SURFACES: readonly string[] = [
  "pms-stays", "pms-events", "pms-event-decisions", "pms-resolutions", "guest-signin-attempts", "guest-signin-credentials",
  "guest-signin-protection", "guest-signin-restrictions", "checkout-grace", "after-checkout-access", "operational-alerts",
  "pms-interfaces", "pms-routing", "pms-source-conflicts", "pms-reconciliation", "pms-roster-reconciliation",
  "post-stay-profiles", "stay-transfers", "pms-financial-onboarding", "payment-providers",
  "financial-review", "financial-ops",
  "social-providers", "notification-providers",
];

/** Whether a nav resource is served here. Unknown answers YES for a core surface and NO for a module-owned one. */
export function surfaceAvailable(caps: Capabilities | null, resource: string): boolean {
  if (!caps || caps.surfaces.length === 0) return !MODULE_SURFACES.includes(resource);
  return caps.surfaces.includes(resource);
}

/** Optional modules fail closed: an unconfirmed module is treated as absent. */
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
