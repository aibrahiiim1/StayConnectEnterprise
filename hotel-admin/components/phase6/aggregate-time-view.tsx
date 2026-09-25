"use client";

// ONLINE-TIME BUDGETS, as the front desk sees them (Phase 6, DARK).
//
// The desk is asked "how much internet time do I have left?" and must be able to answer it truthfully. So
// this screen shows the SAME two numbers the guest's own page shows, from the same durable state the
// accounting writes: if the desk and the guest's phone could disagree about the remaining minutes, the desk
// would be the one telling a guest something untrue, to their face.
//
// TWO CLOCKS, NEVER ONE. Remaining time counts down only while a device is connected; the hard expiry is a
// calendar instant that arrives whether the minutes were used or not. An operator reading only the first
// would promise time that is about to stop being usable.
//
// No guest identity appears here -- no name, no room, no stay. An operator looking at time budgets does not
// need to know whose they are, and the screens that do need that already have their own authorization. The
// page's own copy obeys the same rule (a test reads the whole page for those words), so the wording below
// talks about "packages" and "devices" only.

import { useCallback, useEffect, useMemo, useState } from "react";
import { Hourglass, Timer, Wifi } from "lucide-react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Meter, SkeletonRows } from "@/components/ui/misc";

type Row = {
  entitlement_id: string;
  status: string;
  budget_seconds: number;
  consumed_seconds: number;
  remaining_seconds: number;
  hard_expiry?: string;
  terminal_cause?: string;
  live_devices: number;
};

function humanSeconds(s: number): string {
  if (s <= 0) return "none left";
  const h = Math.floor(s / 3600);
  const m = Math.round((s % 3600) / 60);
  if (h > 0) return `${h}h${m ? ` ${m}m` : ""}`;
  if (m > 0) return `${m} min`;
  return "under a minute";
}

// The cause codes are internal vocabulary; an operator gets the sentence.
const CAUSE_TEXT: Record<string, string> = {
  AGGREGATE_ONLINE_TIME_EXHAUSTED: "Used all of its online time",
  AGGREGATE_OUTER_WINDOW_EXPIRED: "Reached its end date with time still unused",
  VALIDITY_WINDOW_ELAPSED: "Its validity period ended",
};

export function AggregateTimeView() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    api
      .get<{ data?: Row[] }>("/sessions/aggregate-time")
      .then((m) => {
        setRows(m.data ?? []);
        setError(null);
      })
      .catch((e) => setError(String((e as { message?: string })?.message ?? e)));
  }, []);
  useEffect(load, [load]);

  const counts = useMemo(() => {
    const list = rows ?? [];
    const active = list.filter((r) => r.status !== "TERMINATED");
    return {
      active: active.length,
      devices: active.reduce((n, r) => n + (r.live_devices ?? 0), 0),
      ended: list.length - active.length,
    };
  }, [rows]);

  return (
    <PageShell>
      <PageHeader
        eyebrow="Guests"
        title="Online-time budgets"
        icon={<Hourglass />}
        description="These packages are sold as an amount of connected time rather than a period. The time left counts down only while a device is actually connected — but the end date arrives either way, and any time left at that point is lost."
      />

      <ErrorBanner err={error} />

      {rows !== null && rows.length > 0 && (
        <div className="grid gap-4 sm:grid-cols-3">
          <StatCard label="Budgets in use" value={counts.active.toLocaleString()} icon={<Timer />} tone="primary" />
          <StatCard label="Devices connected on them" value={counts.devices.toLocaleString()} icon={<Wifi />} />
          <StatCard label="Ended" value={counts.ended.toLocaleString()} hint="Time used up, end date reached, or validity over" />
        </div>
      )}

      <Card className="overflow-hidden">
        {rows === null ? (
          error ? null : <SkeletonRows rows={4} cols={5} />
        ) : rows.length === 0 ? (
          <div data-testid="empty">
            <EmptyState
              icon={<Hourglass />}
              title="No package on this property uses an online-time budget"
              hint="Packages measured in connected time appear here while they are in use."
            />
          </div>
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Time left</TH>
                <TH className="hidden sm:table-cell">Of budget</TH>
                <TH>Ends on</TH>
                <TH className="hidden sm:table-cell text-end">Devices</TH>
                <TH>State</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => {
                const ended = r.status === "TERMINATED";
                return (
                  <TR key={r.entitlement_id}>
                    <TD className="min-w-36">
                      <div className="font-medium tabular" data-testid="remaining">
                        {ended ? "—" : humanSeconds(r.remaining_seconds)}
                      </div>
                      {!ended && r.budget_seconds > 0 && (
                        <Meter
                          className="mt-1.5 max-w-40"
                          value={r.consumed_seconds}
                          max={r.budget_seconds}
                        />
                      )}
                    </TD>
                    <TD className="hidden text-sm text-muted-foreground sm:table-cell">{humanSeconds(r.budget_seconds)}</TD>
                    <TD className="whitespace-nowrap text-sm text-muted-foreground" data-testid="expiry">
                      {r.hard_expiry ? formatDate(r.hard_expiry) : "No end date"}
                    </TD>
                    <TD className="hidden text-end tabular text-sm text-muted-foreground sm:table-cell">{r.live_devices}</TD>
                    <TD>
                      {ended ? (
                        <Badge tone="neutral">{(r.terminal_cause && CAUSE_TEXT[r.terminal_cause]) || "Ended"}</Badge>
                      ) : (
                        <Badge tone="ok" dot>Active</Badge>
                      )}
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>
    </PageShell>
  );
}
