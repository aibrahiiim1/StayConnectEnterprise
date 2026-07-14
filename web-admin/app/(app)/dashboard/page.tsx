"use client";

import { useEffect, useState } from "react";
import { api, Subscription, TopResp, UsageSummary, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { KpiCard } from "@/components/kpi-card";
import { Badge } from "@/components/ui/badge";
import { formatBytes } from "@/lib/utils";
import { EmptyState } from "@/components/ui/empty-state";
import { isPlatform } from "@/lib/console";
import PlatformDashboard from "@/components/platform-dashboard";

export default function DashboardPage() {
  const [me, setMe] = useState<Whoami | null>(null);
  const [summary, setSummary] = useState<UsageSummary | null>(null);
  const [sub, setSub] = useState<Subscription | null>(null);
  const [top, setTop] = useState<TopResp | null>(null);
  const [tenantID, setTenantID] = useState<string | null>(null);
  const [sites, setSites] = useState<{ id: string; name: string }[]>([]);
  const [siteFilter, setSiteFilter] = useState<string>(""); // "" = All Sites
  const [err, setErr] = useState<string | null>(null);

  // Resolve the tenant context. Platform admins default to dev; tenant users
  // use their DefaultTenantID.
  useEffect(() => {
    (async () => {
      try {
        const who = await api.get<Whoami>("/v1/auth/whoami");
        setMe(who);
        // Platform administrators get the cross-tenant Platform Dashboard — they
        // are NOT scoped into a single tenant and have no personal subscription.
        if (isPlatform(who)) return;
        const t = who.default_tenant_id;
        if (!t) {
          setErr("No tenant scope available.");
          return;
        }
        setTenantID(t);
      } catch (e: any) {
        setErr(e?.message ?? "Failed to load identity");
      }
    })();
  }, []);

  // Load the tenant's sites for the scope selector.
  useEffect(() => {
    if (!tenantID) return;
    api.get<{ data: { id: string; name: string }[] }>(`/v1/tenants/${tenantID}/sites`)
      .then((r) => setSites(r.data ?? []))
      .catch(() => {});
  }, [tenantID]);

  useEffect(() => {
    if (!tenantID) return;
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    const scopeQ = siteFilter ? `&site_id=${siteFilter}` : "";
    const q = `tenant_id=${tenantID}&tz=${encodeURIComponent(tz)}${scopeQ}`;
    (async () => {
      try {
        const [s, sb, t] = await Promise.all([
          api.get<UsageSummary>(`/v1/tenants/${tenantID}/usage/summary?tz=${encodeURIComponent(tz)}${scopeQ}`),
          api.get<Subscription>(`/v1/tenants/${tenantID}/subscription`).catch(() => null),
          api.get<TopResp>(`/v1/tenants/${tenantID}/usage/top-sites?top_n=5&${q}`),
        ]);
        setSummary(s);
        setSub(sb);
        setTop(t);
      } catch (e: any) {
        setErr(e?.message ?? "Failed to load dashboard");
      }
    })();
  }, [tenantID, siteFilter]);

  // Platform admins see the cross-tenant Platform Dashboard (no subscription badge).
  if (isPlatform(me)) return <PlatformDashboard />;

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">
            Group overview · {siteFilter ? (sites.find((s) => s.id === siteFilter)?.name ?? "Site") : "All Sites"}
          </div>
          <h1 className="text-2xl font-semibold">Group Dashboard</h1>
          <div className="mt-1">
            <label className="text-xs text-muted mr-2">Scope:</label>
            <select
              value={siteFilter}
              onChange={(e) => setSiteFilter(e.target.value)}
              className="text-sm bg-panel border border-border rounded px-2 py-1"
            >
              <option value="">All Sites (aggregated)</option>
              {sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
            </select>
          </div>
        </div>
        {sub && (
          <div className="text-sm text-muted">
            <Badge tone={sub.status === "active" ? "ok" : sub.status === "trialing" ? "info" : "warn"}>{sub.status}</Badge>{" "}
            <span className="ml-2 text-text">{sub.plan_name}</span>
            <span className="ml-2">· billed {sub.billing_cycle}</span>
          </div>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
        <KpiCard
          label="Active sessions"
          value={summary?.active_sessions ?? "—"}
          hint="Devices online right now"
        />
        <KpiCard
          label="Data this month"
          value={summary ? formatBytes(summary.total_bytes) : "—"}
          hint={
            summary?.cap_bytes && summary.cap_used_percent !== undefined
              ? `${summary.cap_used_percent.toFixed(1)}% of ${formatBytes(summary.cap_bytes)} cap`
              : "No monthly cap"
          }
        />
        <KpiCard
          label="Sessions today"
          value={summary?.sessions_today ?? "—"}
          hint={summary ? `since ${new Date(summary.period_start).toLocaleDateString()}` : undefined}
        />
        <KpiCard
          label="Plan"
          value={sub?.plan_code ?? "—"}
          hint={sub ? `renews ${new Date(sub.current_period_end).toLocaleDateString()}` : undefined}
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Top sites (this month)</CardTitle>
        </CardHeader>
        <CardBody>
          {!top ? (
            <EmptyState title="Loading…" />
          ) : top.rows.length === 0 ? (
            <EmptyState title="No usage yet" hint="Activity will appear here as guests connect." />
          ) : (
            <div className="space-y-2">
              {top.rows.map((r) => {
                const max = top.rows[0].total_bytes || 1;
                const pct = (r.total_bytes / max) * 100;
                return (
                  <div key={r.id}>
                    <div className="flex justify-between text-sm mb-1">
                      <span className="truncate">{r.name}</span>
                      <span className="text-muted">{formatBytes(r.total_bytes)}</span>
                    </div>
                    <div className="h-1.5 bg-panel2 rounded overflow-hidden">
                      <div className="h-full bg-brand" style={{ width: `${pct}%` }} />
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
