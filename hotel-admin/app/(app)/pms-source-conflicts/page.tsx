"use client";

// Phase 3 (DARK) — PMS SOURCE CONFLICTS.
//
// A source conflict is two interfaces claiming authority over the same thing: both say room 412 is occupied,
// by different people. The guest sees only that they cannot get online, and the front desk has no way to
// explain it, because from every other surface both interfaces look perfectly healthy.
//
// The page exists so the question is visible before a guest asks it. Both interfaces are named by the labels
// an operator recognises — a conflict rendered as two UUIDs is a conflict nobody resolves.

import { useEffect, useState } from "react";
import { api, PmsSourceConflict } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";

const severityTone = (s?: string) =>
  s === "HIGH" ? "err" : s === "MEDIUM" ? "warn" : s === "LOW" ? "info" : "default";

export default function PMSSourceConflictsPage() {
  const [rows, setRows] = useState<PmsSourceConflict[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const r = await api.get<{ conflicts: PmsSourceConflict[] }>("/pms-source-conflicts");
        if (alive) setRows(r.conflicts ?? []);
      } catch (e: any) {
        if (!alive) return;
        setErr(e?.message ?? "Failed to load source conflicts");
        setRows([]);
      }
    })();
    return () => {
      alive = false;
    };
  }, []);

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Source conflicts</h1>
      <p className="text-sm text-muted">
        Where two PMS interfaces claim the same source. Until one is given authority, guests matching the
        contested rooms cannot be verified.
      </p>

      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}

      <Card>
        <CardBody>
          {rows === null ? (
            <p className="text-sm">Loading…</p>
          ) : rows.length === 0 ? (
            <EmptyState
              title="No source conflicts"
              hint="No two interfaces are claiming the same source."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Interface</TH>
                  <TH>Conflicts with</TH>
                  <TH>Severity</TH>
                  <TH>Resolution</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((c) => (
                  <TR key={c.id}>
                    <TD>{c.interface_a_label || c.interface_a}</TD>
                    <TD>{c.interface_b_label || c.interface_b}</TD>
                    <TD>
                      {c.severity ? (
                        <Badge tone={severityTone(c.severity) as any}>{c.severity.toLowerCase()}</Badge>
                      ) : (
                        "—"
                      )}
                    </TD>
                    <TD>{c.resolution ? c.resolution.replace(/_/g, " ").toLowerCase() : "unresolved"}</TD>
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
