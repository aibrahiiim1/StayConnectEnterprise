"use client";

import { createContext, useContext, useEffect, useState, useCallback } from "react";
import { api, Whoami } from "./api";

export type CustomerTenant = { id: string; slug: string; name: string };

type CustomerContextValue = {
  me: Whoami;
  /** Platform admin (super-admin) — may switch between customers and "All Customers". */
  isPlatform: boolean;
  /** Customers the operator may select. For a tenant operator this is just their own. */
  tenants: CustomerTenant[];
  /** "" = All Customers (platform only). Otherwise the selected customer's tenant id. */
  selectedTenantId: string;
  setSelectedTenantId: (id: string) => void;
  /** Human label for the current context: "All Customers" or the customer name. */
  selectedTenantName: string;
  /** True once whoami + the tenant list have loaded and a context is established. */
  ready: boolean;
  reloadTenants: () => Promise<void>;
};

const Ctx = createContext<CustomerContextValue | null>(null);
const STORAGE_KEY = "sc.customerContext";

/**
 * CustomerProvider is the single source of truth for the Control Panel's Global
 * Customer Context. A platform admin picks "All Customers" ("") or one customer;
 * the choice persists in localStorage and survives navigation/refresh. A regular
 * tenant operator is hard-pinned to their own customer (the selector is hidden),
 * mirroring the server, which ignores their ?tenant_id= entirely.
 */
export function CustomerProvider({ me, children }: { me: Whoami; children: React.ReactNode }) {
  const isPlatform = !!me.is_super_admin;
  const [tenants, setTenants] = useState<CustomerTenant[]>([]);
  const [selectedTenantId, setSel] = useState<string>("");
  const [ready, setReady] = useState(false);

  const reloadTenants = useCallback(async () => {
    if (!isPlatform) return;
    try {
      const r = await api.get<{ data: CustomerTenant[] }>("/v1/tenants");
      setTenants(r.data ?? []);
    } catch { /* keep prior list */ }
  }, [isPlatform]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      if (isPlatform) {
        let list: CustomerTenant[] = [];
        try {
          const r = await api.get<{ data: CustomerTenant[] }>("/v1/tenants");
          list = r.data ?? [];
        } catch { /* ignore */ }
        if (cancelled) return;
        setTenants(list);
        const saved = typeof window !== "undefined" ? window.localStorage.getItem(STORAGE_KEY) : null;
        if (saved !== null && (saved === "" || list.some((t) => t.id === saved))) {
          setSel(saved);
        } else {
          setSel(""); // default to All Customers
        }
      } else {
        // Tenant operator: pinned to their own customer; no cross-customer view.
        setSel(me.default_tenant_id ?? "");
        setTenants(me.default_tenant_id ? [{ id: me.default_tenant_id, slug: "", name: "Your customer" }] : []);
      }
      if (!cancelled) setReady(true);
    })();
    return () => { cancelled = true; };
  }, [isPlatform, me.default_tenant_id]);

  const setSelectedTenantId = useCallback((id: string) => {
    setSel(id);
    if (isPlatform && typeof window !== "undefined") window.localStorage.setItem(STORAGE_KEY, id);
  }, [isPlatform]);

  const selectedTenantName =
    selectedTenantId === ""
      ? "All Customers"
      : tenants.find((t) => t.id === selectedTenantId)?.name ?? "Unknown customer";

  return (
    <Ctx.Provider
      value={{ me, isPlatform, tenants, selectedTenantId, setSelectedTenantId, selectedTenantName, ready, reloadTenants }}
    >
      {children}
    </Ctx.Provider>
  );
}

export function useCustomer(): CustomerContextValue {
  const c = useContext(Ctx);
  if (!c) throw new Error("useCustomer must be used within CustomerProvider");
  return c;
}
