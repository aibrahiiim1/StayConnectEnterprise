"use client";

// Phase 3 (DARK) — RESOLUTION EVIDENCE.
//
// Every attempt to verify a guest against the PMS is recorded with its outcome. This is where an operator
// answers "is anyone getting in?" and, when the answer is no, whether the failures share a shape: all on one
// guest network, all ambiguous, all indeterminate.
//
// NO GUEST IDENTITY APPEARS HERE, and that is a deliberate constraint on the page rather than an omission
// from the data. A resolution list that named rooms or guests would be a way for anyone with a read-only
// admin session to enumerate who is staying at the property — precisely what the guest-facing uniform
// failure exists to prevent. The outcome code and the network are enough to act on; the individual guest's
// stay is a different, role-gated surface where an operator is legitimately looking at one person.

import { useEffect, useMemo, useState } from "react";
import { api, ListResp, PmsResolution } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

const outcomeTone = (outcome: string, resolved: boolean) =>
  resolved ? "ok"
    : outcome.startsWith("AMBIGUOUS") ? "warn"
      : outcome.startsWith("INDETERMINATE") ? "err" : "default";

export default function PMSResolutionsPage() {
  const [rows, setRows] = useState<PmsResolution[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const r = await api.get<ListResp<PmsResolution>>("/pms-resolutions");
        if (alive) setRows(r.data ?? []);
      } catch (e: any) {
        if (!alive) return;
        setErr(e?.message ?? "Failed to load resolutions");
        setRows([]);
      }
    })();
    return () => {
      alive = false;
    };
  }, []);

  // The summary is what makes the list actionable. A hundred rows of "NO_MATCH" is a different problem from
  // a hundred rows of "INDETERMINATE", and the difference is invisible when you are scrolling.
  const summary = useMemo(() => {
    if (!rows) return null;
    const byOutcome = new Map<string, number>();
    let verified = 0;
    for (const r of rows) {
      byOutcome.set(r.outcome_code, (byOutcome.get(r.outcome_code) ?? 0) + 1);
      if (r.resolved) verified += 1;
    }
    return {
      total: rows.length,
      verified,
      outcomes: [...byOutcome.entries()].sort((a, b) => b[1] - a[1]),
    };
  }, [rows]);

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Resolution evidence</h1>
      <p className="text-sm text-muted">
        The most recent attempts to verify a guest against the PMS. Outcomes and networks only — no guest or
        room is named here.
      </p>

      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}

      {summary && summary.total > 0 && (
        <Card>
          <CardBody>
            <h2 className="text-lg font-medium">Recent outcomes</h2>
            <p className="mt-1 text-sm">
              {summary.verified} of {summary.total} verified
            </p>
            <ul className="mt-2 flex flex-wrap gap-2 text-sm">
              {summary.outcomes.map(([code, n]) => (
                <li key={code}>
                  <Badge tone={outcomeTone(code, code === "VERIFIED") as any}>
                    {code.replace(/_/g, " ").toLowerCase()} · {n}
                  </Badge>
                </li>
              ))}
            </ul>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody>
          {rows === null ? (
            <p className="text-sm">Loading…</p>
          ) : rows.length === 0 ? (
            <EmptyState
              title="No resolutions recorded"
              hint="No guest has attempted PMS verification on this site yet."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Guest network</TH>
                  <TH>Outcome</TH>
                  <TH>Verified</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((r) => (
                  <TR key={r.id}>
                    <TD>{formatRelative(r.resolved_at)}</TD>
                    <TD>{r.guest_network_id}</TD>
                    <TD>
                      <Badge tone={outcomeTone(r.outcome_code, r.resolved) as any}>
                        {r.outcome_code.replace(/_/g, " ").toLowerCase()}
                      </Badge>
                    </TD>
                    <TD>{r.resolved ? "yes" : "no"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
