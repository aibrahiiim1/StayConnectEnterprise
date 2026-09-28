// LICENSE MODULES. The registry (ids, labels, dependencies) and the site-type presets come from Central
// (GET /cloud/v1/modules), so the console never hard-codes a dependency. The functions below are pure so the
// dependency rules are testable without a screen.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";

export type ModuleInfo = { id: string; label: string; requires: string[] };
export type SiteTypePreset = { id: string; preset: string[] };
export type ModuleCatalog = { modules: ModuleInfo[]; site_types: SiteTypePreset[] };

/** Every module `id` needs, directly or through another dependency. */
export function requiredBy(catalog: ModuleCatalog, id: string, seen: Set<string> = new Set()): string[] {
  const spec = catalog.modules.find((m) => m.id === id);
  for (const dep of spec?.requires ?? []) {
    if (!seen.has(dep)) {
      seen.add(dep);
      requiredBy(catalog, dep, seen);
    }
  }
  return [...seen];
}

/** Every selected module that needs `id`, directly or through another dependency. */
function dependantsOf(catalog: ModuleCatalog, id: string, selected: string[]): string[] {
  return selected.filter((m) => m !== id && requiredBy(catalog, m).includes(id));
}

/** In registry order, then any id the registry does not list (kept, never silently dropped). */
function ordered(catalog: ModuleCatalog, ids: Iterable<string>): string[] {
  const set = new Set(ids);
  const known = catalog.modules.map((m) => m.id).filter((id) => set.has(id));
  const unknown = [...set].filter((id) => !catalog.modules.some((m) => m.id === id)).sort();
  return [...known, ...unknown];
}

/**
 * Ticking a module ticks everything it requires; unticking one unticks everything that requires it. So the
 * selection is always one Central will accept.
 */
export function toggleModule(catalog: ModuleCatalog, selected: string[], id: string, checked: boolean): string[] {
  if (checked) return ordered(catalog, [...selected, id, ...requiredBy(catalog, id)]);
  const drop = new Set([id, ...dependantsOf(catalog, id, selected)]);
  return ordered(catalog, selected.filter((m) => !drop.has(m)));
}

/** The suggested modules for a site type (with their dependencies), or [] when there is none. */
export function presetFor(catalog: ModuleCatalog, siteType: string | null | undefined): string[] {
  const preset = catalog.site_types.find((t) => t.id === siteType)?.preset ?? [];
  return ordered(catalog, preset.flatMap((id) => [id, ...requiredBy(catalog, id)]));
}

/** The modules a checkbox for `id` needs, as labels, for its hint. */
export function requiresLabel(catalog: ModuleCatalog, id: string): string {
  const spec = catalog.modules.find((m) => m.id === id);
  return (spec?.requires ?? []).map((d) => catalog.modules.find((m) => m.id === d)?.label ?? d).join(" and ");
}

let cached: Promise<ModuleCatalog> | null = null;

/** Loads the catalog once per page load. `null` while loading; `error` when Central could not answer. */
export function useModuleCatalog(): { catalog: ModuleCatalog | null; error: unknown } {
  const [catalog, setCatalog] = useState<ModuleCatalog | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let live = true;
    if (!cached) {
      cached = api.get<ModuleCatalog>("/cloud/v1/modules").catch((e) => {
        cached = null; // let a later dialog try again
        throw e;
      });
    }
    cached.then((c) => { if (live) setCatalog(c); }).catch((e) => { if (live) setError(e); });
    return () => { live = false; };
  }, []);
  return { catalog, error };
}

/** Test hook: forget the cached catalog. */
export function resetModuleCatalogCache() {
  cached = null;
}
