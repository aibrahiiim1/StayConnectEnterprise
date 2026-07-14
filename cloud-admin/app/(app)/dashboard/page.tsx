"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, TopResp, UsageSummary } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { KpiCard } from "@/components/kpi-card";
import { Badge } from "@/components/ui/badge";
import { formatBytes } from "@/lib/utils";
import { EmptyState } from "@/components/ui/empty-state";

// FleetLicenseSummary counts the licenses the Platform has ISSUED to managed
// tenants/sites, by state. Counting is authoritative and OWNERSHIP-AWARE: it is
// computed server-side (GET /cloud/v1/licenses/fleet-summary) so a license bound
// to a deleted appliance or site is NEVER counted as Active — it is reported as
// "orphaned" instead. The Central Platform is the vendor issuer and holds no
// license of its own, so its dashboard shows only this fleet roll-up.
type FleetLicenseSummary = {
  active: number;
  expiring: number;
  expired: number;
  suspended: number;
  revoked: number;
  orphaned: number;
  total: number;
};

export default function DashboardPage() {
  // Dashboard follows the Global Customer Context: a concrete customer shows that
  // customer's usage; "All Customers" (platform) shows the fleet-wide license
  // roll-up and no single-customer usage.
  const { me, isPlatform, selectedTenantId, selectedTenantName, ready } = useCustomer();
  const [summary, setSummary] = useState<UsageSummary | null>(null);
  const [top, setTop] = useState<TopResp | null>(null);
  const [fleetLicenses, setFleetLicenses] = useState<FleetLicenseSummary | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const tenantID = selectedTenantId; // "" = All Customers
  const allCustomers = tenantID === "";

  // Platform (super-admin) view: authoritative ownership-aware roll-up computed
  // server-side. A license whose bound appliance/site was deleted is counted as
  // "orphaned", never as Active.
  useEffect(() => {
    if (!isPlatform) return;
    (async () => {
      try {
        setFleetLicenses(await api.get<FleetLicenseSummary>("/cloud/v1/licenses/fleet-summary"));
      } catch {
        setFleetLicenses(null);
      }
    })();
  }, [isPlatform]);

  useEffect(() => {
    if (!ready || allCustomers) { setSummary(null); setTop(null); return; }
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    const q = `tenant_id=${tenantID}&tz=${encodeURIComponent(tz)}`;
    (async () => {
      try {
        const [s, t] = await Promise.all([
          api.get<UsageSummary>(`/v1/tenants/${tenantID}/usage/summary?tz=${encodeURIComponent(tz)}`),
          api.get<TopResp>(`/v1/tenants/${tenantID}/usage/top-sites?top_n=5&${q}`),
        ]);
        setSummary(s);
        setTop(t);
      } catch (e: any) {
        setErr(e?.message ?? "Failed to load dashboard");
      }
    })();
  }, [ready, tenantID, allCustomers]);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Overview</div>
          <h1 className="text-2xl font-semibold">Dashboard</h1>
          <div className="mt-1 text-sm text-muted">{allCustomers ? "All Customers — fleet-wide view" : <>Customer: <span className="text-text font-medium">{selectedTenantName}</span></>}</div>
        </div>
        {/* Simple license model: no plan/subscription is surfaced anywhere in
            the normal workflow — the license itself is the entitlement. */}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {isPlatform && <FleetLicenseSummaryCard summary={fleetLicenses} />}

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
          label="Licensed appliances"
          value={fleetLicenses ? fleetLicenses.active + fleetLicenses.expiring : "—"}
          hint="Live entitlements issued to the fleet"
        />
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Top sites (this month)</CardTitle>
        </CardHeader>
        <CardBody>
          {allCustomers ? (
            <EmptyState title="Select a customer" hint="Pick a customer (top-left) to see its per-site usage. The fleet license roll-up above spans all customers." />
          ) : !top ? (
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

// FleetLicenseSummaryCard renders the counts-by-state roll-up of licenses the
// Platform has issued to its managed fleet, with explicit issuer framing so the
// Platform is never mistaken for a licensed entity.
function FleetLicenseSummaryCard({ summary }: { summary: FleetLicenseSummary | null }) {
  const tiles: { label: string; value: number; tone: string }[] = summary
    ? [
        { label: "Active", value: summary.active, tone: "text-ok" },
        { label: "Expiring ≤30d", value: summary.expiring, tone: "text-warn" },
        { label: "Expired", value: summary.expired, tone: "text-warn" },
        { label: "Suspended", value: summary.suspended, tone: "text-warn" },
        { label: "Revoked", value: summary.revoked, tone: "text-err" },
        // Only surfaced when > 0: licenses whose bound appliance/site no longer
        // exists. These never count as Active; they should be reconciled.
        ...(summary.orphaned > 0 ? [{ label: "Orphaned", value: summary.orphaned, tone: "text-err" }] : []),
      ]
    : [];
  return (
    <Card className="mb-6">
      <CardHeader>
        <div>
          <CardTitle>Fleet License Summary</CardTitle>
          <div className="text-xs text-muted mt-0.5">
            Licenses this Platform has issued to managed tenants and sites. The Central Platform is the license
            issuer and holds no license of its own.
          </div>
        </div>
        <Link href="/licenses" className="text-sm text-brand hover:underline whitespace-nowrap">
          View licenses →
        </Link>
      </CardHeader>
      <CardBody>
        {summary === null ? (
          <EmptyState title="Loading…" />
        ) : summary.total === 0 && summary.orphaned === 0 ? (
          <EmptyState title="No licenses issued yet" hint="Issue a license to a site to entitle its appliances." />
        ) : (
          <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-5 gap-4">
            {tiles.map((t) => (
              <div key={t.label} className="rounded-md border border-border bg-panel2/40 p-3">
                <div className={`text-2xl font-semibold ${t.tone}`}>{t.value}</div>
                <div className="text-xs text-muted uppercase tracking-wider mt-1">{t.label}</div>
              </div>
            ))}
          </div>
        )}
      </CardBody>
    </Card>
  );
}
