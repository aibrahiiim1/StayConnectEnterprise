"use client";

// Phase 3 (DARK) — Stay events, including the MANUAL REVIEW queue. An event that could not be applied safely
// is shown with its bounded review code, so an operator can see exactly why the system refused to guess.

import { useEffect, useState } from "react";
import { api, ListResp, StayEvent } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

const STATUSES = ["", "MANUAL_REVIEW", "PENDING", "APPLIED", "SKIPPED_DUPLICATE", "REJECTED"];

const toneFor = (s: string) =>
  s === "APPLIED" ? "info" : s === "MANUAL_REVIEW" ? "warn" : s === "REJECTED" ? "err" : "default";

export default function StayEventsPage() {
  const [status, setStatus] = useState("MANUAL_REVIEW");
  const [rows, setRows] = useState<StayEvent[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      setRows(null);
      setErr(null);
      try {
        const q = status ? "?processing_status=" + encodeURIComponent(status) : "";
        const r = await api.get<ListResp<StayEvent>>("/pms-events" + q);
        if (alive) setRows(r.data);
      } catch (e: any) {
        if (alive) {
          setErr(e?.message ?? "Failed to load events");
          setRows([]);
        }
      }
    })();
    return () => {
      alive = false;
    };
  }, [status]);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Stay events</h1>
        <label className="text-sm">
          <span className="sr-only">Filter by processing status</span>
          <select
            aria-label="Filter by processing status"
            className="rounded border px-2 py-1"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
          >
            {STATUSES.map((s) => (
              <option key={s || "all"} value={s}>
                {s === "" ? "All events" : s.replace(/_/g, " ").toLowerCase()}
              </option>
            ))}
          </select>
        </label>
      </div>

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
            <EmptyState title="Nothing to review" hint="No PMS events match this filter." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Identity</TH>
                  <TH>Type</TH>
                  <TH>Status</TH>
                  <TH>Reason</TH>
                  <TH>PMS time</TH>
                  <TH>Received</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((e) => (
                  <TR key={e.id}>
                    <TD>{e.external_event_identity}</TD>
                    <TD>{e.event_type}</TD>
                    <TD>
                      <Badge tone={toneFor(e.processing_status) as any}>
                        {e.processing_status.replace(/_/g, " ").toLowerCase()}
                      </Badge>
                    </TD>
                    <TD>{e.review_code ?? "—"}</TD>
                    <TD>{e.pms_timestamp_utc ? formatRelative(e.pms_timestamp_utc) : "—"}</TD>
                    <TD>{formatRelative(e.received_at)}</TD>
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
