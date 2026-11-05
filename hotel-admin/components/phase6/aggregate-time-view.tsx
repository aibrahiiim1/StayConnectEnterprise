"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, Ban, Hourglass, RefreshCw, Timer, Wifi } from "lucide-react";
import { api } from "@/lib/api";
import { usePoll } from "@/lib/use-poll";
import { cn, formatDate } from "@/lib/utils";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { Meter, SkeletonRows } from "@/components/ui/misc";

type Row = {
  entitlement_id: string;
  status: string;
  package?: string;
  budget_seconds: number;
  consumed_seconds: number;
  remaining_seconds: number;
  hard_expiry?: string;
  terminal_cause?: string;
  live_devices: number;
};

const LIST_LIMIT = 200;
const POLL_SECONDS = 30;
const SOON_MS = 24 * 3600 * 1000;

function duration(s: number): string {
  if (s < 60) return "under a minute";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return m ? `${h} h ${m} min` : `${h} h`;
  return `${m} min`;
}

function timeLeft(s: number): string {
  return s <= 0 ? "None left" : duration(s);
}

function endsIn(ms: number): string {
  if (ms < 3600 * 1000) return "under an hour";
  const h = Math.ceil(ms / (3600 * 1000));
  return `${h} h`;
}

const CAUSE_TEXT: Record<string, string> = {
  AGGREGATE_ONLINE_TIME_EXHAUSTED: "Used all of its online time",
  AGGREGATE_OUTER_WINDOW_EXPIRED: "Reached its end date with time still unused",
  VALIDITY_WINDOW_ELAPSED: "Its validity period ended",
};

const STATE: Record<string, { label: string; tone: "ok" | "warn" | "info" }> = {
  ACTIVE: { label: "Active", tone: "ok" },
  PENDING: { label: "Not started yet", tone: "info" },
  SUSPENDED: { label: "Paused", tone: "warn" },
};

export function AggregateTimeView() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const load = useCallback(() => {
    setLoading(true);
    return api
      .get<{ data?: Row[] }>("/sessions/aggregate-time")
      .then((m) => {
        setRows(m.data ?? []);
        setError(null);
        setUpdatedAt(Date.now());
      })
      .catch((e) => setError(String((e as { message?: string })?.message ?? e)))
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => {
    void load();
  }, [load]);
  usePoll(load, POLL_SECONDS * 1000, { enabled: rows !== null });

  const counts = useMemo(() => {
    const list = rows ?? [];
    const active = list.filter((r) => r.status === "ACTIVE");
    return {
      active: active.length,
      devices: active.reduce((n, r) => n + (r.live_devices ?? 0), 0),
      ended: list.filter((r) => r.status === "TERMINATED").length,
    };
  }, [rows]);

  const capped = (rows?.length ?? 0) >= LIST_LIMIT;
  const now = updatedAt ?? Date.now();
  const failedFirst = rows === null && error !== null;

  return (
    <PageShell>
      <PageHeader
        title="Online-time budgets"
        description="Packages sold as an amount of connected time. Check both the time left and the end date."
        meta={
          failedFirst ? undefined : (
            <LiveStatus
              updatedAt={updatedAt}
              refreshing={loading}
              error={error !== null}
              onRefresh={() => void load()}
              intervalSeconds={POLL_SECONDS}
            />
          )
        }
        help={
          <>
            <HelpSection title="Two clocks, never one">
              <p>
                These packages are sold as an amount of connected time rather than a period. The time left counts
                down only while a device is actually connected &mdash; but the end date arrives either way, and any
                time left at that point is lost.
              </p>
            </HelpSection>
            <HelpSection title="How a budget ends">
              <HelpList items={[
                "Its online time is used up.",
                "Its end date is reached, with or without time still unused.",
                "Its validity period ends.",
              ]} />
            </HelpSection>
            <HelpSection title="The same numbers the client sees">
              <p>
                The time left shown here comes from the same record as the client&rsquo;s own page, so the site team and
                the client&rsquo;s phone agree.
              </p>
            </HelpSection>
          </>
        }
      />

      {rows !== null && rows.length > 0 && (
        <StatGrid cols={3}>
          <StatCard label="Budgets in use" value={counts.active.toLocaleString()} icon={<Timer />} />
          <StatCard label="Devices connected on them" value={counts.devices.toLocaleString()} icon={<Wifi />} />
          <StatCard
            icon={<Ban />}
            label="Ended"
            value={counts.ended.toLocaleString()}
            hint={capped ? `Among the ${LIST_LIMIT} budgets listed` : "Used up, or past its end date"}
          />
        </StatGrid>
      )}

      <Card className="overflow-hidden">
        {rows === null ? (
          failedFirst ? (
            <EmptyState
              icon={<AlertTriangle />}
              title="Online-time budgets could not be loaded"
              hint={error ?? undefined}
              action={
                <Button variant="secondary" disabled={loading} onClick={() => void load()}>
                  <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
                </Button>
              }
            />
          ) : (
            <SkeletonRows rows={4} cols={6} />
          )
        ) : rows.length === 0 ? (
          <div data-testid="empty">
            <EmptyState
              size="sm"
              icon={<Hourglass />}
              title="No package on this site uses an online-time budget"
              hint="Packages measured in connected time appear here while they are in use."
            />
          </div>
        ) : (
          <div
            className={cn(
              "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none",
              loading && refreshingClass,
            )}
          >
            <Table label="Online-time budgets">
              <THead>
                <TR>
                  <TH>Package</TH>
                  <TH>Time left</TH>
                  <TH className="hidden sm:table-cell">Of budget</TH>
                  <TH>Ends on</TH>
                  <TH numeric className="hidden sm:table-cell">Connected devices</TH>
                  <TH>State</TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((r) => {
                  const ended = r.status === "TERMINATED";
                  const state = STATE[r.status] ?? { label: r.status, tone: "info" as const };
                  const name = r.package?.trim() || "Unnamed package";
                  const left = r.hard_expiry ? new Date(r.hard_expiry).getTime() - now : null;
                  const soon = !ended && r.remaining_seconds > 0 && left !== null && left > 0 && left <= SOON_MS;
                  return (
                    <TR key={r.entitlement_id}>
                      <TD truncate title={name} className="max-w-48 font-medium">{name}</TD>
                      <TD className="min-w-36">
                        <div className="font-medium tabular" data-testid="remaining">
                          {ended ? "—" : timeLeft(r.remaining_seconds)}
                        </div>
                        {!ended && r.budget_seconds > 0 && (
                          <Meter
                            className="mt-1.5 max-w-40"
                            value={r.consumed_seconds}
                            max={r.budget_seconds}
                            name="Online time used"
                            caption={`${duration(r.consumed_seconds)} of ${duration(r.budget_seconds)} used`}
                          />
                        )}
                      </TD>
                      <TD nowrap muted className="hidden sm:table-cell tabular">
                        {r.budget_seconds > 0 ? duration(r.budget_seconds) : "—"}
                      </TD>
                      <TD nowrap muted>
                        <div data-testid="expiry">{r.hard_expiry ? formatDate(r.hard_expiry) : "No end date"}</div>
                        {soon && left !== null && (
                          <div className="mt-1 text-caption text-warning-subtle-foreground">
                            Ends in {endsIn(left)}, unused time is lost
                          </div>
                        )}
                      </TD>
                      <TD numeric muted className="hidden sm:table-cell">{r.live_devices}</TD>
                      <TD nowrap>
                        {ended ? (
                          <Badge tone="neutral">{(r.terminal_cause && CAUSE_TEXT[r.terminal_cause]) || "Ended"}</Badge>
                        ) : (
                          <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
            {capped && (
              <p className="border-t border-border px-4 py-3 text-caption text-muted-foreground">
                Showing the first {LIST_LIMIT} budgets. Budgets still in use are listed first.
              </p>
            )}
          </div>
        )}
      </Card>
    </PageShell>
  );
}
