"use client";

import { useEffect, useState } from "react";
import { api, Whoami } from "./api";

// useTenant resolves the active tenant id. For tenant-scoped operators this
// is their session's default_tenant_id. For platform admins it falls back to
// the "dev" tenant, else the first tenant listed. Returns null while loading.
export function useTenant(): string | null {
  const [id, setId] = useState<string | null>(null);
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const w = await api.get<Whoami>("/v1/auth/whoami");
        if (cancelled) return;
        if (w.default_tenant_id) {
          setId(w.default_tenant_id);
          return;
        }
        const ts = await api.get<{ data: { id: string; slug: string }[] }>("/v1/tenants");
        if (cancelled) return;
        const t = ts.data.find((x) => x.slug === "dev") ?? ts.data[0];
        setId(t?.id ?? null);
      } catch {
        if (!cancelled) setId(null);
      }
    })();
    return () => { cancelled = true; };
  }, []);
  return id;
}
