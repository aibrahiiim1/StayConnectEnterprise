"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, License, Subscription, TopResp, UsageSummary, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { KpiCard } from "@/components/kpi-card";
import { Badge } from "@/components/ui/badge";
import { formatBytes } from "@/lib/utils";
import { EmptyState } from "@/components/ui/empty-state";

// FleetLicenseSummary counts the licenses the Platform has ISSUED to managed
// tenants/sites, by state. The Central Platform is the vendor license issuer and
// holds no license of its own, so its dashboard never shows a bare "License:
// Active" — only this ownership-scoped fleet roll-up.
type FleetLicenseSummary = {
  active: number;
  expiring: number;
  expired: number;
  suspended: number;
  revoked: number;
  total: number;
};

const EXPIRING_WINDOW_MS = 30 * 24 * 60 * 60 * 1000;

function summarizeLicenses(rows: License[]): FleetLicenseSummary {
  const now = Date.now();
  const s: FleetLicenseSummary = { active: 0, expiring: 0, expired: 0, suspended: 0, revoked: 0, total: 0 };
  for (const l of rows) {
    if (l.status === "superseded") continue; // replaced by a newer license; not a live entitlement
    s.total++;
    if (l.status === "revoked") { s.revoked++; continue; }
    if (l.status === "suspended") { s.suspended++; continue; }
    const validUntil = l.valid_until ? new Date(l.valid_until).getTime() : 0;
    if (validUntil && validUntil < now) s.expired++;
    else if (validUntil && validUntil - now <= EXPIRING_WINDOW_MS) s.expiring++;
    else s.active++;
  }
  return s;
}

export default function DashboardPage() {
  const [me, setMe] = useState<Whoami | null>(null);
  const [summary, setSummary] = useState<UsageSummary | null>(null);
  const [sub, setSub] = useState<Subscription | null>(null);
  const [top, setTop] = useState<TopResp | null>(null);
  const [tenantID, setTenantID] = useState<string | null>(null);
  const [fleetLicenses, setFleetLicenses] = useState<FleetLicenseSummary | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const isPlatform = !!me?.is_super_admin;

  // Resolve the tenant context. Platform admins default to dev; tenant users
  // use their DefaultTenantID.
  useEffect(() => {
    (async () => {
      try {
        const who = await api.get<Whoami>("/v1/auth/whoami");
        setMe(who);
        let t = who.default_tenant_id;
        if (!t && who.is_super_admin) {
          const ts = await api.get<{ data: { id: string; slug: string }[] }>("/v1/tenants");
          t = ts.data.find((x) => x.slug === "dev")?.id ?? ts.data[0]?.id;
        }
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

  // Platform (super-admin) view: roll up licenses the Platform issued across the
  // whole fleet. Omitting tenant_id returns every tenant's licenses for a
  // super-admin (control-plane licenses.list).
  useEffect(() => {
    if (!isPlatform) return;
    (async () => {
      try {
        const lic = await api.get<{ data: License[] }>("/cloud/v1/licenses");
        setFleetLicenses(summarizeLicenses(lic.data ?? []));
      } catch {
        setFleetLicenses(null);
      }
    })();
  }, [isPlatform]);

  useEffect(() => {
    if (!tenantID) return;
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    const q = `tenant_id=${tenantID}&tz=${encodeURIComponent(tz)}`;
    (async () => {
      try {
        const [s, sb, t] = await Promise.all([
          api.get<UsageSummary>(`/v1/tenants/${tenantID}/usage/summary?tz=${encodeURIComponent(tz)}`),
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
  }, [tenantID]);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Overview</div>
          <h1 className="text-2xl font-semibold">Dashboard</h1>
        </div>
        {/* A subscription/plan belongs to a TENANT's own org. Never surface it on the
            Platform (vendor) header — the Platform issues licenses, it isn't licensed. */}
        {!isPlatform && sub && (
          <div className="text-sm text-muted">
            <Badge tone={sub.status === "active" ? "ok" : sub.status === "trialing" ? "info" : "warn"}>{sub.status}</Badge>{" "}
            <span className="ml-2 text-text">{sub.plan_name}</span>
            <span className="ml-2">· billed {sub.billing_cycle}</span>
          </div>
        )}
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
        {isPlatform ? (
          <KpiCard
            label="Licensed appliances"
            value={fleetLicenses ? fleetLicenses.active + fleetLicenses.expiring : "—"}
            hint="Live entitlements issued to the fleet"
          />
        ) : (
          <KpiCard
            label="Plan"
            value={sub?.plan_code ?? "—"}
            hint={sub ? `renews ${new Date(sub.current_period_end).toLocaleDateString()}` : undefined}
          />
        )}
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
        ) : summary.total === 0 ? (
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
