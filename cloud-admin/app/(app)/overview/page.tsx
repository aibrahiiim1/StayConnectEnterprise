"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { ArrowRight, BadgeCheck, Building2, CheckCircle2, MapPin, Server } from "lucide-react";
import { api, ApiError, type AttentionItem, type Overview, type RequestOptions } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { LinkButton } from "@/components/ui/button";
import { Badge, StatusDot } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { Skeleton } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { HelpList, HelpSection } from "@/components/help";
import { usePermissions } from "@/lib/permissions";
import { usePoll } from "@/lib/use-poll";
import {
  ACTIVATION, ACTIVATION_ORDER, CONNECTION, CONNECTION_ORDER, LICENSE, LICENSE_ORDER, ago, attentionInfo,
  type StateInfo,
} from "@/lib/status";
import { cn } from "@/lib/utils";

const POLL_MS = 30_000;

function CountRow({ info, count, href }: { info: StateInfo; count: number; href: string }) {
  return (
    <li>
      <Link
        href={href}
        className={cn(
          "group flex items-center gap-3 px-5 py-2 transition-colors hover:bg-accent/50",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
          count === 0 && "text-muted-foreground",
        )}
      >
        <StatusDot tone={count === 0 ? "default" : info.tone} />
        <span className="min-w-0 flex-1 truncate text-sm">{info.label}</span>
        <span className={cn("tabular text-sm", count > 0 && "font-semibold text-foreground")}>{count.toLocaleString()}</span>
        <ArrowRight className="size-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 group-focus-visible:opacity-100 rtl:-scale-x-100" aria-hidden />
      </Link>
    </li>
  );
}

function Breakdown({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <SectionCard title={title} flush>
      <ul className="py-1.5">{children}</ul>
    </SectionCard>
  );
}

function attentionHref(a: AttentionItem, canSeeAlerts: boolean): string {
  if (a.kind === "security_alert" && canSeeAlerts) return "/system/security-alerts";
  return a.appliance_id ? `/appliances/${a.appliance_id}` : "/appliances";
}

export default function OverviewPage() {
  const { can } = usePermissions();
  const [data, setData] = useState<Overview | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async (opts?: RequestOptions) => {
    setRefreshing(true);
    try {
      setData(await api.get<Overview>("/cloud/v1/overview", opts));
      setUpdatedAt(Date.now());
      setErr(null);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return;
      setErr(e);
    } finally {
      setRefreshing(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);
  usePoll(load, POLL_MS);

  const canAct = (a: AttentionItem) =>
    a.kind === "waiting_activation" ? can["appliances.activate"] : a.kind.startsWith("license_") ? can["licenses.manage"] : can["appliances.manage"];

  const activeLicenses = data ? (data.licenses.active ?? 0) + (data.licenses.expiring ?? 0) : 0;

  return (
    <PageShell>
      <PageHeader
        title="Overview"
        description="Every customer, appliance and license at a glance."
        help={
          <HelpSection title="Reading this page">
            <HelpList
              items={[
                <><strong>Needs attention</strong> lists appliances waiting for activation, out of contact, or short of a valid license, with the action that resolves each.</>,
                <>Every count opens the list behind it, already filtered.</>,
                <>Central shows licensing only; client activity stays on each appliance.</>,
              ]}
            />
          </HelpSection>
        }
        actions={
          <LiveStatus
            updatedAt={updatedAt}
            refreshing={refreshing && !!data}
            onRefresh={load}
            intervalSeconds={POLL_MS / 1000}
            error={!!err && !!data}
          />
        }
      />

      {!data && <ErrorBanner err={err} />}

      {!data ? (
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading</span>
          <StatGrid cols={4}>
            {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-[5.25rem]" />)}
          </StatGrid>
          <Skeleton className="h-40" />
          <div className="grid gap-5 lg:grid-cols-3">
            <Skeleton className="h-56" /><Skeleton className="h-56" /><Skeleton className="h-56" />
          </div>
        </div>
      ) : (
        <div className={cn("space-y-5", refreshing && refreshingClass)}>
          <section aria-label="Totals">
            <StatGrid cols={4}>
              <StatCard label="Customers" icon={<Building2 />} value={data.customers.toLocaleString()} href="/customers" />
              <StatCard label="Sites" icon={<MapPin />} value={data.sites.toLocaleString()} />
              <StatCard
                label="Appliances"
                icon={<Server />}
                value={data.appliances.total.toLocaleString()}
                hint={`${(data.appliances.activated ?? 0).toLocaleString()} activated`}
                href="/appliances"
              />
              <StatCard
                label="Valid licenses"
                icon={<BadgeCheck />}
                value={activeLicenses.toLocaleString()}
                hint={data.licenses.expiring ? `${data.licenses.expiring.toLocaleString()} expiring soon` : undefined}
                tone={data.licenses.expiring ? "warn" : "default"}
                href="/licenses"
              />
            </StatGrid>
          </section>

          <SectionCard
            title={<span id="attention-title">Needs attention</span>}
            actions={data.attention.length > 0 ? <Badge tone="warn">{data.attention.length}</Badge> : undefined}
            flush
          >
            {data.attention.length === 0 ? (
              <EmptyState
                size="sm"
                icon={<CheckCircle2 className="text-success" />}
                title="Nothing needs attention"
                hint="No appliance is waiting, offline or short of a valid license."
              />
            ) : (
              <ul className="divide-y divide-border" aria-label="Items that need attention">
                {data.attention.map((a, i) => {
                  const info = attentionInfo(a.kind);
                  const where = [a.customer_name, a.site_name].filter(Boolean).join(" · ");
                  return (
                    <li key={`${a.kind}-${a.appliance_id ?? i}`} className="flex items-center gap-3 px-5 py-2.5">
                      <StatusDot tone={info.tone} />
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-baseline gap-x-2">
                          <span className="text-sm font-medium">{info.title}</span>
                          {a.serial && <span className="font-mono text-xs text-muted-foreground">{a.serial}</span>}
                        </div>
                        <div className="truncate text-xs text-muted-foreground">
                          {[a.detail, where, a.since ? `since ${ago(a.since)}` : null].filter(Boolean).join(" · ")}
                        </div>
                      </div>
                      <LinkButton href={attentionHref(a, can["system.read"])} size="sm" variant={canAct(a) ? "secondary" : "ghost"}>
                        {canAct(a) ? info.action : info.viewAction}
                        <span className="sr-only"> {a.serial ?? ""}</span>
                      </LinkButton>
                    </li>
                  );
                })}
              </ul>
            )}
          </SectionCard>

          <div className="grid gap-5 lg:grid-cols-3">
            <Breakdown title="Activation">
              {ACTIVATION_ORDER.map((k) => (
                <CountRow key={k} info={ACTIVATION[k]} count={data.appliances[k] ?? 0} href={`/appliances?activation=${k}`} />
              ))}
            </Breakdown>
            <Breakdown title="Connection">
              {CONNECTION_ORDER.map((k) => (
                <CountRow key={k} info={CONNECTION[k]} count={data.appliances[k] ?? 0} href={`/appliances?connection=${k}`} />
              ))}
            </Breakdown>
            <Breakdown title="Licenses">
              {LICENSE_ORDER.map((k) => (
                <CountRow
                  key={k}
                  info={LICENSE[k]}
                  count={data.licenses[k] ?? 0}
                  href={k === "none" ? "/appliances?license=none" : `/licenses?state=${k}`}
                />
              ))}
            </Breakdown>
          </div>
        </div>
      )}
    </PageShell>
  );
}
