"use client";

import { useCallback, useMemo } from "react";
import { usePathname, useRouter, useSearchParams } from "next/navigation";

/**
 * Filters that live in the address, so every filtered list can be linked to (the Overview's numbers do exactly
 * that) and survives a reload. Setting a value to "" removes it from the address.
 *
 * Uses useSearchParams, so the calling page must render inside a <Suspense> boundary.
 */
export function useQueryState<K extends string>(keys: readonly K[]): [Record<K, string>, (patch: Partial<Record<K, string>>) => void] {
  const params = useSearchParams();
  const router = useRouter();
  const pathname = usePathname() ?? "";
  const key = params?.toString() ?? "";

  const values = useMemo(() => {
    const out = {} as Record<K, string>;
    for (const k of keys) out[k] = params?.get(k) ?? "";
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const set = useCallback(
    (patch: Partial<Record<K, string>>) => {
      const next = new URLSearchParams(key);
      for (const [k, v] of Object.entries(patch) as [string, string | undefined][]) {
        if (v) next.set(k, v);
        else next.delete(k);
      }
      const q = next.toString();
      router.replace(q ? `${pathname}?${q}` : pathname, { scroll: false });
    },
    [key, pathname, router],
  );

  return [values, set];
}
