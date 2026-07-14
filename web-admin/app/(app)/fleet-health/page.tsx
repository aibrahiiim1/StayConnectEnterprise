"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";

type Appliance = {
  id?: string; appliance_id?: string; tenant_id?: string; site_id?: string; name?: string; serial?: string;
  status?: string; online?: boolean; last_heartbeat?: string; last_seen?: string;
  software_version?: string; version?: string;
};

function online(a: Appliance): boolean {
  if (typeof a.online === "boolean") return a.online;
  if (a.status) return /online|active|healthy|connected/i.test(a.status);
  const ts = a.last_heartbeat || a.last_seen;
  if (ts) return Date.now() - new Date(ts).getTime() < 5 * 60 * 1000;
  return false;
}

export default function FleetHealthPage() {
  const [rows, setRows] = useState<Appliance[]>([]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const arr = (r: any) => (Array.isArray(r) ? r : r?.data ?? []);
        setRows(arr(await api.get<any>("/cloud/v1/fleet")));
      } catch (e: any) { setErr(e?.message ?? "Failed to load fleet"); }
    })();
  }, []);

  const on = rows.filter(online).length;

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4"><div className="text-xs text-muted uppercase tracking-wider">Platform · across all customers</div>
        <h1 className="text-2xl font-semibold">Fleet Health</h1>
        <p className="text-sm text-muted">{on} online / {rows.length - on} offline · {rows.length} appliances</p></div>
      {err && <div className="text-err text-sm mb-4">{err}</div>}
      <Card>
        <CardHeader><CardTitle>Appliances</CardTitle></CardHeader>
        <CardBody>
          <table className="w-full text-sm">
            <thead><tr className="text-muted text-left border-b border-border">
              <th className="py-2">Appliance</th><th>Tenant</th><th>Site</th><th>Version</th><th>Last heartbeat</th><th>Status</th></tr></thead>
            <tbody>
              {rows.map((a, i) => {
                const ts = a.last_heartbeat || a.last_seen;
                return (
                  <tr key={a.id || a.appliance_id || i} className="border-b border-border">
                    <td className="py-2">{a.name || a.serial || <code>{(a.appliance_id || a.id || "").slice(0, 8)}…</code>}</td>
                    <td className="text-muted"><code>{(a.tenant_id ?? "—").slice(0, 8)}…</code></td>
                    <td className="text-muted"><code>{(a.site_id ?? "—").slice(0, 8)}…</code></td>
                    <td>{a.software_version || a.version || "—"}</td>
                    <td className="text-muted">{ts ? new Date(ts).toLocaleString() : "—"}</td>
                    <td><Badge tone={online(a) ? "ok" : "warn"}>{online(a) ? "online" : "offline"}</Badge></td>
                  </tr>
                );
              })}
              {rows.length === 0 && <tr><td colSpan={6} className="py-3 text-muted">No appliances reporting.</td></tr>}
            </tbody>
          </table>
        </CardBody>
      </Card>
    </div>
  );
}
