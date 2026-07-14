"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";

type Tenant = { id: string; slug: string; name?: string };

export default function CustomersPage() {
  const [rows, setRows] = useState<Tenant[]>([]);
  const [counts, setCounts] = useState<Record<string, { sites: number; appliances: number }>>({});
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const arr = (r: any) => (Array.isArray(r) ? r : r?.data ?? []);
        const ts: Tenant[] = arr(await api.get<any>("/cloud/v1/tenants"));
        setRows(ts);
        const c: Record<string, { sites: number; appliances: number }> = {};
        await Promise.all(ts.map(async (t) => {
          const [s, a] = await Promise.all([
            api.get<any>(`/cloud/v1/sites?tenant_id=${t.id}`).then((r) => arr(r).length).catch(() => 0),
            api.get<any>(`/cloud/v1/appliances?tenant_id=${t.id}`).then((r) => arr(r).length).catch(() => 0),
          ]);
          c[t.id] = { sites: s, appliances: a };
        }));
        setCounts(c);
      } catch (e: any) { setErr(e?.message ?? "Failed to load customers"); }
    })();
  }, []);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4"><div className="text-xs text-muted uppercase tracking-wider">Platform</div>
        <h1 className="text-2xl font-semibold">Customers / Hotel Groups</h1></div>
      {err && <div className="text-err text-sm mb-4">{err}</div>}
      <Card>
        <CardHeader><CardTitle>All customers ({rows.length})</CardTitle></CardHeader>
        <CardBody>
          <table className="w-full text-sm">
            <thead><tr className="text-muted text-left border-b border-border">
              <th className="py-2">Customer</th><th>Slug</th><th>Sites</th><th>Appliances</th><th>Tenant ID</th></tr></thead>
            <tbody>
              {rows.map((t) => (
                <tr key={t.id} className="border-b border-border">
                  <td className="py-2">{t.name || t.slug}</td><td>{t.slug}</td>
                  <td>{counts[t.id]?.sites ?? "—"}</td><td>{counts[t.id]?.appliances ?? "—"}</td>
                  <td className="text-muted"><code>{t.id}</code></td>
                </tr>
              ))}
              {rows.length === 0 && <tr><td colSpan={5} className="py-3 text-muted">No customers.</td></tr>}
            </tbody>
          </table>
        </CardBody>
      </Card>
    </div>
  );
}
