"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import {
  Activity, ArrowRight, BadgeCheck, BarChart3, CalendarClock, Database, LayoutDashboard, Server,
} from "lucide-react";
import { api, TopResp, UsageSummary } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Meter, Skeleton } from "@/components/ui/misc";
import { CustomerScope } from "@/components/customer-scope";
import { formatBytes } from "@/lib/utils";

// FleetLicenseSummary counts the licenses the Platform has ISSUED to managed
// customers/sites, by state. Counting is authoritative and OWNERSHIP-AWARE: it is
// computed server-side (GET /cloud/v1/licenses/fleet-summary) so a license bound
// to a deleted appliance or site is NEVER counted as Active — it is reported as
// "orphaned" instead. Central is the vendor issuer and holds no license of its own.
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
  // Dashboard follows the Customer context: a concrete customer shows that customer's usage; "All customers"
  // (platform) shows the fleet-wide license roll-up and no single-customer usage.
  const { isPlatform, selectedTenantId, ready } = useCustomer();
  const [summary, setSummary] = useState<UsageSummary | null>(null);
  const [top, setTop] = useState<TopResp | null>(null);
  const [fleetLicenses, setFleetLicenses] = useState<FleetLicenseSummary | null>(null);
  const [fleetLoaded, setFleetLoaded] = useState(false);
  const [usageLoading, setUsageLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const tenantID = selectedTenantId; // "" = All customers
  const allCustomers = tenantID === "";

  useEffect(() => {
    if (!isPlatform) return;
    (async () => {
      try {
        setFleetLicenses(await api.get<FleetLicenseSummary>("/cloud/v1/licenses/fleet-summary"));
      } catch {
        setFleetLicenses(null);
      } finally {
        setFleetLoaded(true);
      }
    })();
  }, [isPlatform]);

  useEffect(() => {
    if (!ready || allCustomers) { setSummary(null); setTop(null); return; }
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    const q = `tenant_id=${tenantID}&tz=${encodeURIComponent(tz)}`;
    setUsageLoading(true);
    setErr(null);
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
      } finally {
        setUsageLoading(false);
      }
    })();
  }, [ready, tenantID, allCustomers]);

  const kpiLoading = !allCustomers && usageLoading && !summary;
  const dash = <span className="text-muted-foreground">—</span>;

  return (
    <PageShell>
      <PageHeader
        eyebrow="Overview"
        title="Dashboard"
        icon={<LayoutDashboard />}
        description="Fleet health at a glance: licenses issued by state and, for one customer, how busy their sites are."
        actions={
          isPlatform ? (
            <Link
              href="/licenses"
              className="inline-flex h-9 items-center gap-2 rounded-md bg-primary px-3.5 text-[0.8125rem] font-semibold text-primary-foreground shadow-control transition-colors hover:bg-primary-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
            >
              View licenses <ArrowRight className="size-4 rtl:-scale-x-100" aria-hidden />
            </Link>
          ) : undefined
        }
      >
        <CustomerScope />
      </PageHeader>

      <ErrorBanner err={err} />

      {isPlatform && <FleetLicenseSummaryCard summary={fleetLicenses} loaded={fleetLoaded} />}

      <section aria-label="Key figures" className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          label="Active sessions"
          icon={<Activity />}
          tone="primary"
          value={kpiLoading ? <Skeleton className="h-7 w-16" /> : summary?.active_sessions ?? dash}
          hint={allCustomers ? "Select a customer to see this" : "Devices online right now"}
        />
        <StatCard
          label="Data this month"
          icon={<Database />}
          tone="info"
          value={kpiLoading ? <Skeleton className="h-7 w-20" /> : summary ? formatBytes(summary.total_bytes) : dash}
          hint={
            allCustomers
              ? "Select a customer to see this"
              : summary?.cap_bytes && summary.cap_used_percent !== undefined
                ? `${summary.cap_used_percent.toFixed(1)}% of ${formatBytes(summary.cap_bytes)} cap`
                : "No monthly cap"
          }
        />
        <StatCard
          label="Sessions today"
          icon={<CalendarClock />}
          value={kpiLoading ? <Skeleton className="h-7 w-16" /> : summary?.sessions_today ?? dash}
          hint={
            allCustomers
              ? "Select a customer to see this"
              : summary ? `Since ${new Date(summary.period_start).toLocaleDateString()}` : undefined
          }
        />
        <StatCard
          label="Licensed appliances"
          icon={<Server />}
          tone="ok"
          value={fleetLicenses ? fleetLicenses.active + fleetLicenses.expiring : dash}
          hint="Live licenses issued to the fleet"
        />
      </section>

      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>Top sites (this month)</CardTitle>
            <CardDescription>The five sites that used the most data.</CardDescription>
          </div>
        </CardHeader>
        <CardBody>
          {allCustomers ? (
            <EmptyState
              icon={<BarChart3 />}
              title="Select a customer"
              hint="Choose a customer in the sidebar to see its per-site usage. The license summary above spans all customers."
            />
          ) : !top ? (
            <div className="space-y-4" aria-busy="true">
              <span className="sr-only">Loading</span>
              {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-7" />)}
            </div>
          ) : top.rows.length === 0 ? (
            <EmptyState icon={<BarChart3 />} title="No usage yet" hint="Activity will appear here as guests connect." />
          ) : (
            <ol className="space-y-3.5">
              {top.rows.map((r) => (
                <li key={r.id}>
                  <Meter
                    value={r.total_bytes}
                    max={top.rows[0].total_bytes || 1}
                    tone="info"
                    label={<span className="text-sm text-foreground">{r.name}</span>}
                    caption={formatBytes(r.total_bytes)}
                  />
                </li>
              ))}
            </ol>
          )}
        </CardBody>
      </Card>
    </PageShell>
  );
}

// The counts-by-state roll-up of licenses Central has issued to its managed fleet.
function FleetLicenseSummaryCard({ summary, loaded }: { summary: FleetLicenseSummary | null; loaded: boolean }) {
  const tiles: { label: string; value: number; tone: "ok" | "warn" | "err"; hint: string }[] = summary
    ? [
        { label: "Active", value: summary.active, tone: "ok", hint: "Valid and in force" },
        { label: "Expiring in 30 days or less", value: summary.expiring, tone: "warn", hint: "Renew soon" },
        { label: "Expired", value: summary.expired, tone: "err", hint: "New guest logins refused" },
        { label: "Suspended", value: summary.suspended, tone: "warn", hint: "Paused; can be resumed" },
        { label: "Revoked", value: summary.revoked, tone: "err", hint: "Permanently ended" },
        // Only surfaced when > 0: licenses whose bound appliance/site no longer exists. These never count as
        // Active; they should be reconciled.
        ...(summary.orphaned > 0
          ? [{ label: "Orphaned", value: summary.orphaned, tone: "err" as const, hint: "Appliance or site was deleted" }]
          : []),
      ]
    : [];
  return (
    <Card>
      <CardHeader>
        <div className="space-y-0.5">
          <CardTitle>Fleet license summary</CardTitle>
          <CardDescription>
            Licenses Central has issued to customers and sites. Central is the license issuer and holds no license
            of its own.
          </CardDescription>
        </div>
        <Link href="/licenses" className="inline-flex items-center gap-1 text-sm font-medium text-primary hover:underline">
          View licenses <ArrowRight className="size-3.5 rtl:-scale-x-100" aria-hidden />
        </Link>
      </CardHeader>
      <CardBody>
        {!loaded ? (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5" aria-busy="true">
            <span className="sr-only">Loading</span>
            {Array.from({ length: 5 }).map((_, i) => <Skeleton key={i} className="h-20" />)}
          </div>
        ) : summary === null ? (
          <EmptyState icon={<BadgeCheck />} title="License summary unavailable" hint="The fleet summary could not be loaded. Try again shortly." />
        ) : summary.total === 0 && summary.orphaned === 0 ? (
          <EmptyState icon={<BadgeCheck />} title="No licenses issued yet" hint="Activate an appliance under Onboarding to issue its first license." />
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
            {tiles.map((t) => (
              <div key={t.label} className="rounded-md border border-border bg-surface/60 p-3.5">
                <div className="flex items-center gap-1.5 text-xs font-medium text-muted-foreground">
                  <span
                    className={
                      t.tone === "ok" ? "size-2 rounded-full bg-success" : t.tone === "warn" ? "size-2 rounded-full bg-warning" : "size-2 rounded-full bg-destructive"
                    }
                    aria-hidden
                  />
                  {t.label}
                </div>
                <div className="mt-1.5 text-metric tabular">{t.value}</div>
                <div className="mt-0.5 text-caption text-muted-foreground">{t.hint}</div>
              </div>
            ))}
          </div>
        )}
      </CardBody>
    </Card>
  );
}
