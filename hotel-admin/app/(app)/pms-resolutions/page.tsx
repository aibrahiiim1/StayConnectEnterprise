"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { InfoHint } from "@/components/ui/quiet";
import type { PmsResolution } from "@/lib/api";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { Pagination } from "@/components/ui/data";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { Card, CardBody, CardFooter, CardHeader, CardTitle, SectionCard } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Explain } from "@/components/ui/tooltip";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection } from "@/components/help";
import { SplitBar } from "@/components/ui/chart";
import { MonoId, SkeletonRows, Meter } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { AlertTriangle, RefreshCw, ShieldCheck } from "lucide-react";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

type Outcome = { label: string; meaning: string; fix?: React.ReactNode; tone: "ok" | "warn" | "err"; known: boolean };

const LINK = "font-medium underline underline-offset-2";

const OUTCOMES: Record<string, Outcome> = {
  VERIFIED: {
    label: "Matched a stay",
    meaning: "The room number and the detail the guest typed matched exactly one stay. Whether the guest then got online is recorded in Guest sign-in attempts.",
    tone: "ok",
    known: true,
  },
  NO_MATCH: {
    label: "No matching stay",
    meaning: "Nothing in the guest list matched the room number and the detail the guest typed.",
    fix: <>Usually a typo, or a guest whose check-in has not reached the appliance yet. Check <Link href="/stay-events" className={LINK}>PMS activity</Link>.</>,
    tone: "warn",
    known: true,
  },
  AMBIGUOUS: {
    label: "More than one match",
    meaning: "The details matched more than one stay, so the appliance refused rather than picking one.",
    fix: <>Usually two stays claiming one room, or a network whose PMS connections both know the guest. Check <Link href="/pms-routing" className={LINK}>PMS routing</Link> and <Link href="/pms-source-conflicts" className={LINK}>Duplicate sources</Link>.</>,
    tone: "warn",
    known: true,
  },
  INDETERMINATE: {
    label: "Could not be decided",
    meaning: "The appliance could not reach a safe answer, so it refused rather than guessing.",
    fix: (
      <>
        The PMS could not be reached, its guest list was too old to trust, it cannot check the detail the guest
        typed, or the network is not pointed at any PMS. Check the{" "}
        <Link href="/pms-interfaces" className={LINK}>PMS connection</Link> and{" "}
        <Link href="/pms-routing" className={LINK}>PMS routing</Link>.
      </>
    ),
    tone: "err",
    known: true,
  },
};

const outcome = (code: string): Outcome =>
  OUTCOMES[code] ?? {
    label: "Result not recognised",
    meaning: "This console has no wording for this result.",
    tone: "warn",
    known: false,
  };

function percent(part: number, whole: number) {
  if (whole <= 0) return 0;
  const p = Math.round((part / whole) * 100);
  if (p === 100 && part < whole) return 99;
  if (p === 0 && part > 0) return 1;
  return p;
}

type ResolutionsResp = PagedFields & {
  data?: PmsResolution[];
  summary?: {
    window?: number;
    total?: number;
    verified?: number;
    newest_resolved_at?: string;
    outcomes?: { outcome_code: string; count: number }[];
    networks?: { guest_network_id: string; guest_network_name?: string; total: number; verified: number }[];
  };
};

export default function PMSResolutionsPage() {
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const list = useServerPage<ResolutionsResp>({ path: "/pms-resolutions" });
  const { resp, current, err, loading: refreshing } = list;
  const loadFailed = err !== null && !resp;
  const rows = current && resp ? resp.data ?? [] : null;
  useEffect(() => { if (current && resp) setUpdatedAt(Date.now()); }, [current, resp]);

  const summary = useMemo(() => {
    const s = resp?.summary;
    if (!s) return null;
    return {
      window: s.window ?? 0,
      total: s.total ?? 0,
      verified: s.verified ?? 0,
      outcomes: (s.outcomes ?? []).map((o) => [o.outcome_code, o.count] as [string, number]),
      networks: (s.networks ?? []).map((n) => [n.guest_network_id, {
        name: n.guest_network_name || "Network no longer configured",
        total: n.total,
        verified: n.verified,
      }] as [string, { name: string; total: number; verified: number }]),
      newest: s.newest_resolved_at,
    };
  }, [resp]);

  const suspectNetwork = useMemo(() => {
    if (!summary || summary.networks.length < 2) return null;
    const bad = summary.networks.filter(([, n]) => n.total >= 3 && n.verified === 0);
    const good = summary.networks.filter(([, n]) => n.verified > 0);
    return bad.length > 0 && good.length > 0 ? bad : null;
  }, [summary]);

  const refused = summary ? summary.total - summary.verified : 0;
  const allTime = list.total ?? summary?.total ?? 0;
  const windowed = summary !== null && allTime > summary.total;
  const refusals = summary ? summary.outcomes.filter(([code]) => code !== "VERIFIED") : [];

  return (
    <PageShell width="wide">
      <PageHeader
        title="Guest sign-in checks"
        description="Recent room sign-in checks against the PMS and why they were refused."
        help={
          <>
            <HelpSection title="When to use this page">
              <p>
                Recent room sign-in checks against the PMS and why they were refused. This is where to look when
                guests say they cannot get online with their room number.
              </p>
              <p>
                Each refusal reason below says what it means and, where there is one, what to do about it. If
                sign-in fails on one network and works on others, the network is usually pointed at the wrong
                property management system, or at none — check <strong>PMS routing</strong>.
              </p>
            </HelpSection>
            <HelpSection title="No guest is named on this page">
              <p>
                Only the outcome and the network are recorded here. A list of who tried to sign in and failed would
                let anyone with a read-only account work out who is staying at the site, so it is deliberately
                not collected — the guest&rsquo;s own stay is on{" "}
                <Link href="/stays" className="underline underline-offset-2">Stays</Link>, where looking at one
                person is the point and the access is gated accordingly.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={!!err && !!resp} onRefresh={() => void list.reload()} />
        }
      />

      {err !== null && resp && <ErrorBanner err={err} />}

      {summary && summary.total > 0 && (
        <div className={cn("space-y-5", FADE)}>
          <StatGrid cols={4}>
            <StatCard
              label={windowed ? `Latest ${summary.total.toLocaleString()} checks` : "Checks recorded"}
              value={summary.total.toLocaleString()}
              hint={
                windowed
                  ? `${allTime.toLocaleString()} recorded in all`
                  : summary.newest ? `Most recent ${formatRelative(summary.newest)}` : undefined
              }
              explain={windowed ? (
                <Explain>The figures on this page describe the latest {summary.total.toLocaleString()} checks. The list below goes back further.</Explain>
              ) : undefined}
            />
            <StatCard
              label="Matched a stay"
              value={summary.verified.toLocaleString()}
              tone="ok"
              hint={`${percent(summary.verified, summary.total)}% of checks`}
            />
            <StatCard
              label="Refused"
              value={refused.toLocaleString()}
              tone={refused > 0 ? "warn" : "default"}
              hint={refused > 0 ? "See why below" : "Every check matched a stay"}
            />
            <StatCard
              label="Networks involved"
              value={summary.networks.length.toLocaleString()}
              hint="Where the checks came from"
            />
          </StatGrid>

          <p className="text-xs text-muted-foreground">
            A match means the details matched a stay. Whether the guest then got online, for example when the stay has
            no internet package, is recorded in{" "}
            <Link href="/guest-signin-attempts" className={LINK}>Guest sign-in attempts</Link>.
          </p>

          {suspectNetwork && (
            <Callout tone="warning" title="Sign-in is failing on one network and working on others" className={FADE}>
              {suspectNetwork.map(([id, n]) => (
                <div key={id}>
                  None of the {n.total.toLocaleString()} checks from <strong>{n.name}</strong> matched a stay, while
                  other networks are matching.
                </div>
              ))}
              <div className="mt-1.5 text-xs">
                That pattern usually means the network is pointed at the wrong property management system, or at none.{" "}
                <Link href="/pms-routing" className={LINK}>
                  Check which PMS it uses
                </Link>
                .
              </div>
            </Callout>
          )}

          <div className="grid gap-5 lg:grid-cols-2">
            <SectionCard title="Why guests were refused" bodyClassName="space-y-4">
              <SplitBar
                total={summary.total}
                parts={[
                  { name: "Matched a stay", value: summary.verified, tone: "ok" },
                  { name: "Refused", value: refused, tone: refused > 0 ? "err" : "neutral" },
                ]}
              />
              {refusals.length === 0 ? (
                <p className="text-sm text-muted-foreground">No check was refused.</p>
              ) : (
                <ul className="divide-y divide-border">
                  {refusals.map(([code, n]) => {
                    const o = outcome(code);
                    return (
                      <li key={code} className="flex gap-3 py-2 first:pt-0 last:pb-0">
                        <Badge tone={o.tone} className="mt-0.5 shrink-0 tabular">{n.toLocaleString()}</Badge>
                        <div className="min-w-0">
                          <div className="flex items-center gap-1 text-sm font-medium">{o.label}<InfoHint>{o.meaning}</InfoHint></div>
                          {o.fix && <p className="text-xs text-muted-foreground">{o.fix}</p>}
                          {!o.known && (
                            <p className="text-xs text-muted-foreground">
                              For support: result <span className="font-mono">{code}</span>
                            </p>
                          )}
                        </div>
                      </li>
                    );
                  })}
                </ul>
              )}
            </SectionCard>

            <SectionCard
              title="By Wi-Fi network"
              description="Where the checks came from, and how many matched a stay."
              actions={<Link href="/pms-routing" className="text-xs font-medium text-primary hover:underline">PMS routing</Link>}
              bodyClassName="space-y-4"
            >
              {summary.networks.map(([id, n]) => (
                <div key={id}>
                  <div className="flex items-baseline justify-between gap-3">
                    <span className="truncate text-sm font-medium">{n.name}</span>
                    <span className="shrink-0 text-xs tabular text-muted-foreground">
                      {n.verified.toLocaleString()} of {n.total.toLocaleString()} matched
                    </span>
                  </div>
                  <Meter
                    className="mt-1.5"
                    value={n.verified}
                    max={n.total}
                    tone={n.verified === 0 ? "err" : n.verified === n.total ? "ok" : "warn"}
                  />
                </div>
              ))}
            </SectionCard>
          </div>
        </div>
      )}

      <Card className={cn("overflow-hidden", refreshing && rows !== null && refreshingClass)}>
        <CardHeader>
          <CardTitle>Recent checks</CardTitle>
          <span className="text-xs text-muted-foreground">Newest first</span>
        </CardHeader>
        <CardBody className="p-0">
          {rows === null ? (
            loadFailed ? (
              <div role="alert" className={FADE}>
                <EmptyState
                  size="sm"
                  icon={<AlertTriangle />}
                  title="Guest sign-in checks could not be loaded"
                  hint="The appliance did not return the checks, so what happened to them is not known. Nothing has been changed."
                  action={
                    <Button variant="secondary" disabled={refreshing} onClick={() => void list.reload()}>
                      <RefreshCw className={cn(refreshing && "animate-spin motion-reduce:animate-none")} /> Try again
                    </Button>
                  }
                />
              </div>
            ) : (
              <SkeletonRows rows={6} cols={4} />
            )
          ) : rows.length === 0 ? (
            <div className={FADE}>
              <EmptyState
                size="sm"
                icon={<ShieldCheck />}
                title="No guest has tried to sign in with a room number"
                hint={
                  <>
                    Either nobody has tried yet, or no client network offers room sign-in. Check{" "}
                    <Link href="/sign-in-methods" className={LINK}>Sign-in methods</Link> and{" "}
                    <Link href="/room-sign-in" className={LINK}>Room sign-in</Link>.
                  </>
                }
              />
            </div>
          ) : (
            <Table label="Recent checks">
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Wi-Fi network</TH>
                  <TH>Result</TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r) => {
                  const o = outcome(r.outcome_code);
                  return (
                    <TR key={r.id}>
                      <TD nowrap muted title={formatDate(r.resolved_at)}>
                        {formatRelative(r.resolved_at)}
                      </TD>
                      <TD>
                        {r.guest_network_name ? (
                          <span className="text-sm">{r.guest_network_name}</span>
                        ) : (
                          <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                            Network no longer configured
                            <MonoId value={r.guest_network_id} title="Client network" />
                          </span>
                        )}
                      </TD>
                      <TD nowrap>
                        <StatusBadge tone={r.resolved ? "ok" : o.tone}>{o.label}</StatusBadge>
                        {!o.known && (
                          <div className="mt-0.5 font-mono text-2xs text-muted-foreground">{r.outcome_code}</div>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </CardBody>
        {rows && rows.length > 0 && (
          <CardFooter>
            <Pagination className="w-full"
              offset={list.offset}
              limit={list.pageSize}
              shown={rows.length}
              total={list.total}
              hasMore={list.hasMore}
              onChange={list.setOffset}
            />
          </CardFooter>
        )}
      </Card>
    </PageShell>
  );
}
