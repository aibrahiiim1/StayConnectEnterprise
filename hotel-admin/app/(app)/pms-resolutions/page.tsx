"use client";

// Privacy invariant: no guest, room number or reservation appears on this page. A list of failed sign-ins would let
// any read-only admin session enumerate who is staying at the property; the per-guest view is a separate, role-gated screen.

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import type { PmsResolution } from "@/lib/api";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { Pagination } from "@/components/ui/data";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { Card, CardBody, CardFooter, CardHeader, CardTitle, SectionCard } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Explain } from "@/components/ui/tooltip";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection } from "@/components/help";
import { SplitBar } from "@/components/ui/chart";
import { MonoId, SkeletonRows, Meter } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { ShieldCheck } from "lucide-react";

// An unknown code falls through to a de-underscored label rather than being hidden, so it is still countable.
const OUTCOMES: Record<string, { label: string; meaning: string; fix?: string; tone: "ok" | "warn" | "err" }> = {
  VERIFIED: {
    label: "Verified",
    meaning: "The room number and name matched a guest who is checked in. The guest was let online.",
    tone: "ok",
  },
  NO_MATCH: {
    label: "No matching room",
    meaning: "Nothing in the guest list matched what the guest typed.",
    fix: "Usually a typo, or a guest whose check-in has not reached the appliance yet. Check PMS activity.",
    tone: "warn",
  },
  NAME_MISMATCH: {
    label: "Name did not match",
    meaning: "The room exists and is occupied, but the surname the guest typed does not match the one on the stay.",
    fix: "Guests often give a first name, a spouse's name or a company name. The desk can confirm the name on the reservation.",
    tone: "warn",
  },
  AMBIGUOUS: {
    label: "More than one match",
    meaning: "The details matched more than one stay, so the appliance refused rather than picking one.",
    fix: "Usually two stays claiming one room in the same guest list, or a shared network whose PMS connections both know the guest. Check PMS routing and Duplicate sources.",
    tone: "warn",
  },
  AMBIGUOUS_ROOM: {
    label: "Room matched twice",
    meaning: "Two stays currently claim the same room, so the appliance could not tell which guest this is.",
    fix: "A departing and an arriving guest overlapping on one room is the usual cause. It clears itself once the PMS sends the check-out.",
    tone: "warn",
  },
  NOT_IN_HOUSE: {
    label: "Not checked in",
    meaning: "The room and name matched a stay that is not currently in house.",
    fix: "A guest trying to sign in before check-in or after check-out. The Grace Period and post-stay access cover the intended cases.",
    tone: "warn",
  },
  STALE_OCCUPANCY: {
    label: "Guest list too old",
    meaning: "The appliance's copy of the guest list is older than the connection allows it to trust.",
    fix: "The PMS connection is not delivering updates. Open PMS connection.",
    tone: "err",
  },
  INDETERMINATE: {
    label: "Could not be decided",
    meaning: "The appliance could not reach a safe conclusion, so it refused rather than guessing.",
    fix: "If this is the common outcome, the PMS feed is the place to look.",
    tone: "err",
  },
  FEED_UNAVAILABLE: {
    label: "PMS unavailable",
    meaning: "The property management system could not be consulted at all.",
    fix: "Open PMS connection. Vouchers and client accounts still work while it is down.",
    tone: "err",
  },
  NO_ROUTE: {
    label: "Network not pointed at a PMS",
    meaning: "The guest's Wi-Fi network is not mapped to any property management system, so there was nothing to check against.",
    fix: "Set the mapping on PMS routing.",
    tone: "err",
  },
};

const outcome = (code: string) =>
  OUTCOMES[code] ?? {
    label: code.replace(/_/g, " ").toLowerCase(),
    meaning: "This outcome is not one this screen has wording for. The code is the connector's own.",
    tone: "warn" as const,
  };

type ResolutionsResp = PagedFields & {
  data?: PmsResolution[];
  /** Summary of the most recent `window` checks. */
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

  // The table pages through every check; the breakdown covers only the most recent, since it answers what is failing now.
  const list = useServerPage<ResolutionsResp>({ path: "/pms-resolutions" });
  const { resp, current, err, loading: refreshing } = list;
  const rows = current && resp ? resp.data ?? [] : err && !resp ? [] : null;
  useEffect(() => { if (current && resp) setUpdatedAt(Date.now()); }, [current, resp]);
  const load = (_manual?: boolean) => void list.reload();

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

  // One network failing while others succeed is a routing problem, so call it out.
  const suspectNetwork = useMemo(() => {
    if (!summary || summary.networks.length < 2) return null;
    const bad = summary.networks.filter(([, n]) => n.total >= 3 && n.verified === 0);
    const good = summary.networks.filter(([, n]) => n.verified > 0);
    return bad.length > 0 && good.length > 0 ? bad : null;
  }, [summary]);

  const failed = summary ? summary.total - summary.verified : 0;

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
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={!!err && !!rows?.length} onRefresh={() => void load(true)} />
        }
      />

      <ErrorBanner err={err} />

      {summary && summary.total > 0 && (
        <StatGrid cols={4}>
          <StatCard
            label="Checks recorded"
            value={(list.total ?? summary.total).toLocaleString()}
            hint={summary.newest ? `Most recent ${formatRelative(summary.newest)}` : undefined}
            explain={list.total !== null && list.total > summary.total ? (
              <Explain>The figures below cover the latest {summary.total.toLocaleString()} checks.</Explain>
            ) : undefined}
          />
          <StatCard
            label="Let online"
            value={summary.verified.toLocaleString()}
            tone="ok"
            hint={`${Math.round((summary.verified / summary.total) * 100)}% of attempts`}
          />
          <StatCard
            label="Refused"
            value={failed.toLocaleString()}
            tone={failed > 0 ? "warn" : "default"}
            hint={failed > 0 ? "See why below" : "Everyone who tried got in"}
          />
          <StatCard
            label="Networks involved"
            value={summary.networks.length.toLocaleString()}
            hint="Where the attempts came from"
          />
        </StatGrid>
      )}

      {suspectNetwork && (
        <Callout tone="warning" title="Sign-in is failing on one network and working on others">
          {suspectNetwork.map(([id, n]) => (
            <div key={id}>
              Every one of the {n.total} attempts from <strong>{n.name}</strong> was refused, while other networks
              are succeeding.
            </div>
          ))}
          <div className="mt-1.5 text-xs">
            That pattern usually means the network is pointed at the wrong property management system, or at none.{" "}
            <Link href="/pms-routing" className="font-medium underline underline-offset-2">
              Check which PMS it uses
            </Link>
            .
          </div>
        </Callout>
      )}

      {summary && summary.total > 0 && (
        <div className="grid gap-5 lg:grid-cols-2">
          <SectionCard title="Why guests were refused" bodyClassName="space-y-4">
            <SplitBar
              total={summary.total}
              parts={[
                { name: "Let online", value: summary.verified, tone: "ok" },
                { name: "Refused", value: failed, tone: failed > 0 ? "err" : "neutral" },
              ]}
            />
            <ul className="divide-y divide-border">
              {summary.outcomes.map(([code, n]) => {
                const o = outcome(code);
                return (
                  <li key={code} className="flex gap-3 py-2.5 first:pt-0 last:pb-0">
                    <Badge tone={o.tone} className="mt-0.5 shrink-0 tabular">{n}</Badge>
                    <div className="min-w-0">
                      <div className="text-sm font-medium">{o.label}</div>
                      <p className="text-xs text-muted-foreground">{o.meaning}{o.fix ? ` ${o.fix}` : ""}</p>
                    </div>
                  </li>
                );
              })}
            </ul>
          </SectionCard>

          <SectionCard
            title="By Wi-Fi network"
            description="Where the attempts came from, and how many got in."
            actions={<Link href="/pms-routing" className="text-xs font-medium text-primary hover:underline">PMS routing</Link>}
            bodyClassName="space-y-4"
          >
            {summary.networks.map(([id, n]) => (
              <div key={id}>
                <div className="flex items-baseline justify-between gap-3">
                  <span className="truncate text-sm font-medium">{n.name}</span>
                  <span className="shrink-0 text-xs tabular text-muted-foreground">
                    {n.verified} of {n.total} let online
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
      )}

      <Card className={cn("overflow-hidden", refreshing && refreshingClass)}>
        <CardHeader>
          <CardTitle>Recent attempts</CardTitle>
          <span className="text-xs text-muted-foreground">Newest first</span>
        </CardHeader>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={6} cols={4} />
          ) : rows.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<ShieldCheck />}
              title="No guest has tried to sign in with a room number"
              hint="Either nobody has tried yet, or no client network offers room sign-in."
            />
          ) : (
            <Table label="Recent attempts">
              {/* No prose here: a test asserts the table contains no "room", "reservation", "stay" or "folio" text. */}
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
