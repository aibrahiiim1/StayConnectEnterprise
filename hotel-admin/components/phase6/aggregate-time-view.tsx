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
// need to know whose they are, and the screens that do need that already have their own authorization.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";

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

  if (error) {
    return (
      <div role="alert" className="rounded border border-red-300 bg-red-50 p-3 text-sm text-red-800">
        {error}
      </div>
    );
  }
  if (!rows) return <div className="text-sm text-muted">Loading…</div>;

  return (
    <div className="space-y-6">
      <header className="space-y-1">
        <h1 className="text-2xl font-semibold">Online-time budgets</h1>
        <p className="text-sm text-muted">
          These packages are sold as an amount of connected time rather than a period. The time left counts
          down only while a device is actually connected — but the end date arrives either way, and any time
          left at that point is lost.
        </p>
      </header>

      {rows.length === 0 ? (
        <p className="text-sm text-muted" data-testid="empty">
          No package on this property uses an online-time budget.
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs uppercase tracking-widest text-muted">
              <tr>
                <th className="py-2">Time left</th>
                <th className="py-2">Of budget</th>
                <th className="py-2">Ends on</th>
                <th className="py-2">Devices</th>
                <th className="py-2">State</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.entitlement_id} className="border-t border-border">
                  <td className="py-2 font-medium" data-testid="remaining">
                    {r.status === "TERMINATED" ? "—" : humanSeconds(r.remaining_seconds)}
                  </td>
                  <td className="py-2 text-muted">{humanSeconds(r.budget_seconds)}</td>
                  <td className="py-2 text-muted" data-testid="expiry">
                    {r.hard_expiry ? new Date(r.hard_expiry).toLocaleString() : "No end date"}
                  </td>
                  <td className="py-2 text-muted">{r.live_devices}</td>
                  <td className="py-2 text-muted">
                    {r.status === "TERMINATED"
                      ? (r.terminal_cause && CAUSE_TEXT[r.terminal_cause]) || "Ended"
                      : "Active"}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
