"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { ArrowRight, BadgeCheck, CalendarClock, LayoutDashboard, MapPin, Server } from "lucide-react";
import { api, type Appliance, type License, type ListResp, type Site } from "@/lib/api";
import { licenseState } from "@/lib/license-state";
import { Badge } from "@/components/ui/badge";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { CustomerScope } from "@/components/customer-scope";
import { HelpList, HelpSection } from "@/components/help";

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
  // CENTRAL IS USED FOR LICENSING ONLY (CLAUDE.md 0E). The earlier dashboard asked ctrlapi for per-customer
  // usage (/v1/tenants/{id}/usage/*): those routes do not exist, because appliances do not report guest
  // activity to Central, so every figure on it was a permanent "—" behind a 404. This dashboard shows only what
  // Central actually holds: the licenses it issued, the sites and appliances they are bound to, and what needs
  // attention. Guest activity lives on each hotel's appliance, in Hotel Admin.
  const { isPlatform, selectedTenantId, ready } = useCustomer();
  const [fleetLicenses, setFleetLicenses] = useState<FleetLicenseSummary | null>(null);
  const [fleetLoaded, setFleetLoaded] = useState(false);
  const [licenses, setLicenses] = useState<License[] | null>(null);
  const [sites, setSites] = useState<Site[] | null>(null);
  const [appliances, setAppliances] = useState<Appliance[] | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const tenantID = selectedTenantId; // "" = All customers
  // A platform role other than the super-admin (platform_support, platform_billing) has no customer of its own
  // and no customer selector. ctrlapi refuses every customer-scoped list for it with 400 "tenant scope
  // required", so asking would only paint an error over blank figures. Ask for nothing and say why instead.
  const noCustomer = ready && !isPlatform && !tenantID;

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
    if (!ready) return;
    let live = true;
    setErr(null);
    if (noCustomer) {
      setLicenses(null);
      setSites(null);
      setAppliances(null);
      return;
    }
    (async () => {
      const q = `tenant_id=${tenantID}`;
      const [l, s, a] = await Promise.allSettled([
        api.get<ListResp<License>>(`/cloud/v1/licenses?${q}`),
        api.get<ListResp<Site>>(`/v1/sites?${q}`),
        api.get<ListResp<Appliance>>(`/v1/appliances?${q}`),
      ]);
      if (!live) return;
      setLicenses(l.status === "fulfilled" ? l.value.data ?? [] : null);
      setSites(s.status === "fulfilled" ? s.value.data ?? [] : null);
      setAppliances(a.status === "fulfilled" ? a.value.data ?? [] : null);
      const failed = [l, s, a].find((r) => r.status === "rejected") as PromiseRejectedResult | undefined;
      if (failed) setErr(failed.reason);
    })();
    return () => { live = false; };
  }, [ready, tenantID, noCustomer]);

  const now = Date.now();
  const current = (licenses ?? []).filter((l) => licenseState(l, now).key !== "superseded");
  const attention = current
    .map((l) => ({ l, st: licenseState(l, now) }))
    .filter(({ l, st }) => {
      if (st.key === "active") {
        const days = (new Date(l.valid_until).getTime() - now) / 86400000;
        return days <= 30;
      }
      return st.key !== "revoked";
    })
    .sort((x, y) => new Date(x.l.valid_until).getTime() - new Date(y.l.valid_until).getTime())
    .slice(0, 8);
  const siteName = (id: string) => sites?.find((s) => s.id === id)?.name ?? "Unknown site";
  const onlineRecently = (a: Appliance) =>
    a.last_seen_at ? now - new Date(a.last_seen_at).getTime() < 5 * 60 * 1000 : false;
  const dash = <span className="text-muted-foreground">—</span>;
  const loading = !noCustomer && licenses === null && sites === null && appliances === null && !err;

  return (
    <PageShell>
      <PageHeader
        eyebrow="Overview"
        title="Dashboard"
        icon={<LayoutDashboard />}
        description="Licenses by state and what needs attention."
        help={
          <>
            <HelpSection title="What this page shows">
              <p>
                The licenses Central has issued, by state, the sites and appliances they cover, and the licenses
                that need attention soonest.
              </p>
            </HelpSection>
            <HelpSection title="Central is used for licensing only">
              <p>
                Guests, sessions, usage and network health are managed on each hotel&apos;s appliance, in Hotel
                Admin, and keep working when Central is unreachable. That is why no guest figures appear here.
              </p>
            </HelpSection>
            <HelpSection title="Reading the figures">
              <HelpList
                items={[
                  <><strong>Fleet license summary</strong> counts licenses Central issued to customers and sites. Central is the issuer and holds no license of its own.</>,
                  <><strong>Orphaned</strong> licenses are bound to an appliance or site that was deleted. They never count as Active and should be reconciled.</>,
                  <><strong>Need attention</strong> lists licenses expiring within 30 days, in grace, expired, suspended or unbound, soonest first.</>,
                  <>A license state change never drops existing guest sessions.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={
          <Link
            href="/licenses"
            className="inline-flex h-9 items-center gap-2 rounded-md bg-primary px-3.5 text-[0.8125rem] font-semibold text-primary-foreground shadow-control transition-colors hover:bg-primary-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2"
          >
            View licenses <ArrowRight className="size-4 rtl:-scale-x-100" aria-hidden />
          </Link>
        }
      >
        <CustomerScope />
      </PageHeader>

      <ErrorBanner err={err} />

      {noCustomer && (
        <Callout tone="info" title="Your role is not tied to a customer">
          The figures on this page belong to one customer at a time, so there is nothing to show here for your
          role. The screens your role can open are in the menu.
        </Callout>
      )}

      {isPlatform && <FleetLicenseSummaryCard summary={fleetLicenses} loaded={fleetLoaded} />}

      <section aria-label="Key figures" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard
          label="Sites"
          icon={<MapPin />}
          href="/sites"
          value={loading ? <Skeleton className="h-7 w-12" /> : sites ? sites.filter((s) => s.status !== "archived").length : dash}
          hint="Properties, one hotel each"
        />
        <StatCard
          label="Appliances"
          icon={<Server />}
          href="/appliances"
          value={loading ? <Skeleton className="h-7 w-12" /> : appliances ? appliances.length : dash}
          hint={
            appliances
              ? `${appliances.filter(onlineRecently).length} reached Central in the last 5 minutes`
              : "Registered to these sites"
          }
        />
        <StatCard
          label="Licensed appliances"
          icon={<BadgeCheck />}
          tone="ok"
          href="/licenses"
          value={
            loading ? (
              <Skeleton className="h-7 w-12" />
            ) : licenses ? (
              current.filter((l) => ["active", "grace"].includes(licenseState(l, now).key)).length
            ) : (
              dash
            )
          }
          hint="Licenses in force, grace included"
        />
        <StatCard
          label="Need attention"
          icon={<CalendarClock />}
          tone={attention.length > 0 ? "warn" : "default"}
          href="/licenses"
          value={loading ? <Skeleton className="h-7 w-12" /> : licenses ? attention.length : dash}
          hint="Expiring within 30 days, in grace, expired, suspended or unbound"
        />
      </section>

      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>Licenses that need attention</CardTitle>
            <CardDescription>Soonest first.</CardDescription>
          </div>
        </CardHeader>
        <CardBody>
          {noCustomer ? (
            <EmptyState icon={<BadgeCheck />} title="No customer in scope" hint="License figures are shown per customer." />
          ) : licenses === null && !err ? (
            <div className="space-y-3" aria-busy="true">
              <span className="sr-only">Loading</span>
              {Array.from({ length: 3 }).map((_, i) => <Skeleton key={i} className="h-9" />)}
            </div>
          ) : licenses === null ? (
            <EmptyState icon={<BadgeCheck />} title="Licenses could not be loaded" hint="Try again shortly." />
          ) : attention.length === 0 ? (
            <EmptyState icon={<BadgeCheck />} title="Nothing needs attention" hint="Every license is in force for more than 30 days." />
          ) : (
            <ul className="divide-y divide-border">
              {attention.map(({ l, st }) => (
                <li key={l.id} className="flex flex-wrap items-center justify-between gap-3 py-2.5">
                  <div className="min-w-0">
                    <div className="truncate text-sm font-medium">{siteName(l.site_id)}</div>
                    <div className="text-caption text-muted-foreground">
                      Valid until {new Date(l.valid_until).toLocaleDateString()}
                      {l.max_concurrent_online_guests !== undefined
                        ? ` · ${l.max_concurrent_online_guests === 0 ? "unlimited" : l.max_concurrent_online_guests} guests online at once`
                        : ""}
                    </div>
                  </div>
                  <Badge tone={st.tone === "default" ? "neutral" : st.tone} dot>
                    {st.key === "active" ? "Expiring soon" : st.label}
                  </Badge>
                </li>
              ))}
            </ul>
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
          <CardDescription>Licenses issued to customers and sites.</CardDescription>
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
