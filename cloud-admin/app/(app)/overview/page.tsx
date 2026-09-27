"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { ArrowRight, CheckCircle2 } from "lucide-react";
import { api, ApiError, type AttentionItem, type Overview } from "@/lib/api";
import { Card, CardHeader, CardTitle } from "@/components/ui/card";
import { LinkButton } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Skeleton } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { usePermissions } from "@/lib/permissions";
import { usePoll } from "@/lib/use-poll";
import {
  ACTIVATION, ACTIVATION_ORDER, CONNECTION, CONNECTION_ORDER, LICENSE, LICENSE_ORDER, ago, attentionInfo,
  type StateInfo,
} from "@/lib/status";
import { cn } from "@/lib/utils";

const POLL_MS = 30_000;

const DOT: Record<string, string> = {
  ok: "bg-success",
  warn: "bg-warning",
  err: "bg-destructive",
  info: "bg-info",
  default: "bg-muted-foreground/50",
};

/** One state and its count, as a link to the list filtered to it. */
function CountRow({ info, count, href }: { info: StateInfo; count: number; href: string }) {
  return (
    <li>
      <Link
        href={href}
        className={cn(
          "group flex items-center gap-3 rounded-md px-2 py-2 transition-colors hover:bg-accent/70",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          count === 0 && "text-muted-foreground",
        )}
      >
        <span className={cn("size-2 shrink-0 rounded-full", count === 0 ? "bg-border-strong" : DOT[info.tone])} aria-hidden />
        <span className="min-w-0 flex-1 truncate text-sm">{info.label}</span>
        <span className={cn("text-emphasis tabular", count === 0 && "font-normal")}>{count.toLocaleString()}</span>
        <ArrowRight className="size-3.5 shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 rtl:-scale-x-100" aria-hidden />
      </Link>
    </li>
  );
}

function Breakdown({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <Card>
      <CardHeader className="py-3">
        <CardTitle className="text-sm">{title}</CardTitle>
      </CardHeader>
      <ul className="p-2">{children}</ul>
    </Card>
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

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      setData(await api.get<Overview>("/cloud/v1/overview"));
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

  return (
    <PageShell>
      <PageHeader
        title="Overview"
        description="Every customer, appliance and license at a glance. Select a number to see the list behind it."
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
        <div className="space-y-4" aria-busy="true">
          <span className="sr-only">Loading</span>
          <Skeleton className="h-40" />
          <div className="grid gap-4 lg:grid-cols-3">
            <Skeleton className="h-64" /><Skeleton className="h-64" /><Skeleton className="h-64" />
          </div>
        </div>
      ) : (
        <div className={cn("space-y-5", refreshing && refreshingClass)}>
          <section aria-labelledby="attention-title">
            <Card>
              <CardHeader>
                <CardTitle id="attention-title">Needs attention</CardTitle>
                {data.attention.length > 0 && (
                  <Badge tone="warn">{data.attention.length}</Badge>
                )}
              </CardHeader>
              {data.attention.length === 0 ? (
                <EmptyState
                  icon={<CheckCircle2 className="text-success" />}
                  title="Nothing needs attention"
                  hint="No appliance is waiting, offline or short of a valid license."
                  className="py-10"
                />
              ) : (
                <ul className="divide-y divide-border" aria-label="Items that need attention">
                  {data.attention.map((a, i) => {
                    const info = attentionInfo(a.kind);
                    const where = [a.customer_name, a.site_name].filter(Boolean).join(" · ");
                    return (
                      <li key={`${a.kind}-${a.appliance_id ?? i}`} className="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3">
                        <span className={cn("size-2 shrink-0 rounded-full", DOT[info.tone])} aria-hidden />
                        <div className="min-w-0 flex-1">
                          <div className="flex flex-wrap items-baseline gap-x-2">
                            <span className="text-sm font-semibold">{info.title}</span>
                            {a.serial && <span className="font-mono text-xs text-muted-foreground">{a.serial}</span>}
                          </div>
                          <div className="truncate text-caption text-muted-foreground">
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
            </Card>
          </section>

          <section aria-label="Totals" className="flex flex-wrap gap-x-8 gap-y-2 px-1 text-sm text-muted-foreground">
            <Link href="/customers" className="rounded hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <span className="text-headline tabular text-foreground">{data.customers.toLocaleString()}</span>{" "}
              {data.customers === 1 ? "customer" : "customers"}
            </Link>
            <span>
              <span className="text-headline tabular text-foreground">{data.sites.toLocaleString()}</span>{" "}
              {data.sites === 1 ? "site" : "sites"}
            </span>
            <Link href="/appliances" className="rounded hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
              <span className="text-headline tabular text-foreground">{data.appliances.total.toLocaleString()}</span>{" "}
              {data.appliances.total === 1 ? "appliance" : "appliances"}
            </Link>
          </section>

          <div className="grid gap-4 lg:grid-cols-3">
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
