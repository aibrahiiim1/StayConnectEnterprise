// Central owns module dependencies; the console must use its catalog.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";

export type ModuleInfo = { id: string; label: string; requires: string[] };
export type SiteTypePreset = { id: string; preset: string[] };
export type ModuleCatalog = { modules: ModuleInfo[]; site_types: SiteTypePreset[] };

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

function dependantsOf(catalog: ModuleCatalog, id: string, selected: string[]): string[] {
  return selected.filter((m) => m !== id && requiredBy(catalog, m).includes(id));
}

/** Preserve unknown IDs so a newer Central's grants are not silently removed. */
function ordered(catalog: ModuleCatalog, ids: Iterable<string>): string[] {
  const set = new Set(ids);
  const known = catalog.modules.map((m) => m.id).filter((id) => set.has(id));
  const unknown = [...set].filter((id) => !catalog.modules.some((m) => m.id === id)).sort();
  return [...known, ...unknown];
}

/** Keep dependency selections valid when a module is added or removed. */
export function toggleModule(catalog: ModuleCatalog, selected: string[], id: string, checked: boolean): string[] {
  if (checked) return ordered(catalog, [...selected, id, ...requiredBy(catalog, id)]);
  const drop = new Set([id, ...dependantsOf(catalog, id, selected)]);
  return ordered(catalog, selected.filter((m) => !drop.has(m)));
}

export function presetFor(catalog: ModuleCatalog, siteType: string | null | undefined): string[] {
  const preset = catalog.site_types.find((t) => t.id === siteType)?.preset ?? [];
  return ordered(catalog, preset.flatMap((id) => [id, ...requiredBy(catalog, id)]));
}

export function requiresLabel(catalog: ModuleCatalog, id: string): string {
  const spec = catalog.modules.find((m) => m.id === id);
  return (spec?.requires ?? []).map((d) => catalog.modules.find((m) => m.id === d)?.label ?? d).join(" and ");
}

let cached: Promise<ModuleCatalog> | null = null;

export function useModuleCatalog(): { catalog: ModuleCatalog | null; error: unknown } {
  const [catalog, setCatalog] = useState<ModuleCatalog | null>(null);
  const [error, setError] = useState<unknown>(null);
  useEffect(() => {
    let live = true;
    if (!cached) {
      cached = api.get<ModuleCatalog>("/cloud/v1/modules").catch((e) => {
        cached = null;
        throw e;
      });
    }
    cached.then((c) => { if (live) setCatalog(c); }).catch((e) => { if (live) setError(e); });
    return () => { live = false; };
  }, []);
  return { catalog, error };
}

export function resetModuleCatalogCache() {
  cached = null;
}
