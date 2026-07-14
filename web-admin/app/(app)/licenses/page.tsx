"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

type License = {
  id: string; tenant_id?: string; commercial_plan_code?: string; plan_code?: string;
  state?: string; status?: string; issued_at?: string; valid_until?: string;
};

export default function LicensesPage() {
  const [rows, setRows] = useState<License[]>([]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const arr = (r: any) => (Array.isArray(r) ? r : r?.data ?? []);
        setRows(arr(await api.get<any>("/cloud/v1/licenses")));
      } catch (e: any) { setErr(e?.message ?? "Failed to load licenses"); }
    })();
  }, []);

  const soon = Date.now() + 30 * 24 * 3600 * 1000;

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4"><div className="text-xs text-muted uppercase tracking-wider">Platform</div>
        <h1 className="text-2xl font-semibold">Licenses &amp; Entitlements</h1></div>
      {err && <div className="text-err text-sm mb-4">{err}</div>}
      <Card>
        <CardHeader><CardTitle>Issued licenses ({rows.length})</CardTitle></CardHeader>
        <CardBody>
          <table className="w-full text-sm">
            <thead><tr className="text-muted text-left border-b border-border">
              <th className="py-2">License ID</th><th>Tenant</th><th>Plan</th><th>State</th><th>Valid until</th></tr></thead>
            <tbody>
              {rows.map((l) => {
                const st = l.state || l.status || "—";
                const exp = l.valid_until && new Date(l.valid_until).getTime() < soon;
                return (
                  <tr key={l.id} className="border-b border-border">
                    <td className="py-2"><code>{l.id.slice(0, 8)}…</code></td>
                    <td className="text-muted"><code>{(l.tenant_id ?? "—").slice(0, 8)}…</code></td>
                    <td>{l.commercial_plan_code || l.plan_code || "—"}</td>
                    <td><Badge tone={st === "Active" ? "ok" : "warn"}>{st}</Badge></td>
                    <td className={exp ? "text-warn" : ""}>{l.valid_until ? new Date(l.valid_until).toLocaleDateString() : "—"}{exp ? " ⚠" : ""}</td>
                  </tr>
                );
              })}
              {rows.length === 0 && <tr><td colSpan={5} className="py-3 text-muted">No licenses.</td></tr>}
            </tbody>
          </table>
        </CardBody>
      </Card>
    </div>
  );
}
