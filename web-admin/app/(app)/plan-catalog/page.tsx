"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

type Plan = {
  id: string; name?: string; code?: string; billing_cycle?: string; billing_interval?: string;
  price_cents?: number; currency?: string; trial_days?: number;
  max_sites?: number; site_limit?: number; max_appliances?: number; appliance_limit?: number;
  active?: boolean; status?: string;
};

export default function PlanCatalogPage() {
  const [rows, setRows] = useState<Plan[]>([]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const arr = (r: any) => (Array.isArray(r) ? r : r?.data ?? []);
        setRows(arr(await api.get<any>("/cloud/v1/commercial-plans")));
      } catch (e: any) { setErr(e?.message ?? "Failed to load plan catalog"); }
    })();
  }, []);

  const price = (p: Plan) => (p.price_cents != null ? `${(p.price_cents / 100).toFixed(2)} ${p.currency ?? ""}` : "—");
  const active = (p: Plan) => p.active ?? (p.status ? p.status === "active" : true);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4"><div className="text-xs text-muted uppercase tracking-wider">Platform</div>
        <h1 className="text-2xl font-semibold">Plan Catalog</h1>
        <p className="text-sm text-muted">Commercial plans, pricing and entitlements — platform-managed. Tenants see their own plan read-only under “My Subscription”.</p></div>
      {err && <div className="text-err text-sm mb-4">{err}</div>}
      <Card>
        <CardHeader><CardTitle>Plans ({rows.length})</CardTitle></CardHeader>
        <CardBody>
          <table className="w-full text-sm">
            <thead><tr className="text-muted text-left border-b border-border">
              <th className="py-2">Name</th><th>Code</th><th>Interval</th><th>Price</th><th>Sites</th><th>Appliances</th><th>Status</th></tr></thead>
            <tbody>
              {rows.map((p) => (
                <tr key={p.id} className="border-b border-border">
                  <td className="py-2">{p.name || p.code}</td><td><code>{p.code}</code></td>
                  <td>{p.billing_cycle || p.billing_interval || "—"}</td><td>{price(p)}</td>
                  <td>{p.max_sites ?? p.site_limit ?? "—"}</td><td>{p.max_appliances ?? p.appliance_limit ?? "—"}</td>
                  <td><Badge tone={active(p) ? "ok" : "warn"}>{active(p) ? "active" : "retired"}</Badge></td>
                </tr>
              ))}
              {rows.length === 0 && <tr><td colSpan={7} className="py-3 text-muted">No plans in the catalog.</td></tr>}
            </tbody>
          </table>
        </CardBody>
      </Card>
    </div>
  );
}
