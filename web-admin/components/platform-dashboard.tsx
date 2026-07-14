"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { KpiCard } from "@/components/kpi-card";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";

type Tenant = { id: string; slug: string; name?: string };
type Appliance = { id?: string; appliance_id?: string; tenant_id?: string; site_id?: string; name?: string;
  status?: string; online?: boolean; last_heartbeat?: string; last_seen?: string; software_version?: string; version?: string };
type License = { id: string; state?: string; status?: string; valid_until?: string; tenant_id?: string };

function isOnline(a: Appliance): boolean {
  if (typeof a.online === "boolean") return a.online;
  if (a.status) return /online|active|healthy|connected/i.test(a.status);
  const ts = a.last_heartbeat || a.last_seen;
  if (ts) return Date.now() - new Date(ts).getTime() < 5 * 60 * 1000; // 5 min
  return false;
}

export default function PlatformDashboard() {
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [appliances, setAppliances] = useState<Appliance[]>([]);
  const [licenses, setLicenses] = useState<License[]>([]);
  const [plans, setPlans] = useState<any[]>([]);
  const [sites, setSites] = useState<number | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        const arr = (r: any) => (Array.isArray(r) ? r : r?.data ?? []);
        const [ts, fl, lic, pl] = await Promise.all([
          api.get<any>("/cloud/v1/tenants").catch(() => ({ data: [] })),
          api.get<any>("/cloud/v1/fleet").catch(() => ({ data: [] })),
          api.get<any>("/cloud/v1/licenses").catch(() => ({ data: [] })),
          api.get<any>("/cloud/v1/commercial-plans").catch(() => ({ data: [] })),
        ]);
        const tenantList: Tenant[] = arr(ts);
        setTenants(tenantList);
        setAppliances(arr(fl));
        setLicenses(arr(lic));
        setPlans(arr(pl));
        // sites: aggregate per tenant (cross-tenant total)
        const siteCounts = await Promise.all(
          tenantList.map((t) =>
            api.get<any>(`/cloud/v1/sites?tenant_id=${t.id}`).then((r) => arr(r).length).catch(() => 0)
          )
        );
        setSites(siteCounts.reduce((a, b) => a + b, 0));
      } catch (e: any) {
        setErr(e?.message ?? "Failed to load platform dashboard");
      }
    })();
  }, []);

  const online = appliances.filter(isOnline).length;
  const offline = appliances.length - online;
  const soon = Date.now() + 30 * 24 * 3600 * 1000;
  const expiring = licenses.filter((l) => l.valid_until && new Date(l.valid_until).getTime() < soon).length;
  const versions = Array.from(
    appliances.reduce((m, a) => {
      const v = a.software_version || a.version || "unknown";
      m.set(v, (m.get(v) ?? 0) + 1);
      return m;
    }, new Map<string, number>())
  );

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">StayConnect Platform · across all customers</div>
        <h1 className="text-2xl font-semibold">Platform Dashboard</h1>
      </div>
      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
        <KpiCard label="Customers" value={tenants.length} />
        <KpiCard label="Sites" value={sites ?? "—"} />
        <KpiCard label="Appliances" value={appliances.length} />
        <KpiCard label="Online / Offline" value={`${online} / ${offline}`} />
        <KpiCard label="Licenses" value={licenses.length} />
        <KpiCard label="Expiring (30d)" value={expiring} />
        <KpiCard label="Plan catalog" value={plans.length} />
        <KpiCard label="Fleet heartbeat" value={appliances.length ? (offline === 0 ? "healthy" : `${offline} stale`) : "—"} />
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <Card>
          <CardHeader><CardTitle>Appliances by software version</CardTitle></CardHeader>
          <CardBody className="text-sm">
            {versions.length === 0 ? <span className="text-muted">No appliances reporting.</span> :
              versions.map(([v, n]) => (
                <div key={v} className="flex justify-between border-b border-border py-1"><span>{v}</span><span className="text-muted">{n}</span></div>
              ))}
          </CardBody>
        </Card>
        <Card>
          <CardHeader><CardTitle>Customers</CardTitle></CardHeader>
          <CardBody className="text-sm">
            {tenants.length === 0 ? <span className="text-muted">No customers.</span> :
              tenants.map((t) => (
                <div key={t.id} className="flex justify-between border-b border-border py-1"><span>{t.name || t.slug}</span><span className="text-muted">{t.slug}</span></div>
              ))}
          </CardBody>
        </Card>
      </div>
      <p className="mt-4 text-xs text-muted">Platform scope · aggregates across all customers. No personal subscription applies to a platform administrator.</p>
    </div>
  );
}
