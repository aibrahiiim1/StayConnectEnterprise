"use client";

// Phase 3 (DARK) — Stays. Read-only operational evidence: which reservations the appliance believes are in
// house, when a checkout boundary was established, and who is on the stay. Sharing a stay is ordinary, so the
// occupant count is shown plainly rather than as an exception.

import { useEffect, useState } from "react";
import { api, ListResp, Stay, StayDetail } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

const STATUSES = ["", "IN_HOUSE", "RESERVED", "CHECKED_OUT", "POST_STAY_ACTIVE", "CANCELLED", "NO_SHOW"];

const toneFor = (status: string) =>
  status === "IN_HOUSE" ? "info" : status === "CHECKED_OUT" ? "default" : "warn";

export default function StaysPage() {
  const [status, setStatus] = useState("");
  const [rows, setRows] = useState<Stay[] | null>(null);
  const [detail, setDetail] = useState<StayDetail | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      setRows(null);
      setErr(null);
      try {
        const q = status ? "?status=" + encodeURIComponent(status) : "";
        const r = await api.get<ListResp<Stay>>("/pms-stays" + q);
        if (alive) setRows(r.data);
      } catch (e: any) {
        if (alive) {
          setErr(e?.message ?? "Failed to load stays");
          setRows([]);
        }
      }
    })();
    return () => {
      alive = false;
    };
  }, [status]);

  async function open(id: string) {
    setErr(null);
    try {
      setDetail(await api.get<StayDetail>("/pms-stays/" + id));
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load the stay");
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <h1 className="text-xl font-semibold">Stays</h1>
        <label className="text-sm">
          <span className="sr-only">Filter by status</span>
          <select
            aria-label="Filter by status"
            className="rounded border px-2 py-1"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
          >
            {STATUSES.map((s) => (
              <option key={s || "all"} value={s}>
                {s === "" ? "All statuses" : s.replace(/_/g, " ").toLowerCase()}
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
            <EmptyState title="No stays" hint="Nothing has been ingested from the PMS for this filter yet." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Reservation</TH>
                  <TH>Room</TH>
                  <TH>Status</TH>
                  <TH>Occupants</TH>
                  <TH>Checkout boundary</TH>
                  <TH>Postings</TH>
                  <TH>&nbsp;</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((s) => (
                  <TR key={s.id}>
                    <TD>{s.external_reservation_id}</TD>
                    <TD>{s.room ?? "—"}</TD>
                    <TD>
                      <Badge tone={toneFor(s.status) as any}>{s.status.replace(/_/g, " ").toLowerCase()}</Badge>
                    </TD>
                    <TD>{s.occupants}</TD>
                    <TD>{s.effective_checkout_at ? formatRelative(s.effective_checkout_at) : "—"}</TD>
                    <TD>{s.posting_allowed ? "allowed" : "closed"}</TD>
                    <TD>
                      <Button onClick={() => open(s.id)}>Details</Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {detail && (
        <Card>
          <CardBody>
            <div className="flex items-start justify-between">
              <h2 className="text-lg font-medium">Stay {detail.external_reservation_id}</h2>
              <Button onClick={() => setDetail(null)}>Close</Button>
            </div>
            <h3 className="mt-3 text-sm font-medium">Occupants</h3>
            <ul className="text-sm">
              {detail.occupant_list.length === 0 && <li>No occupants recorded.</li>}
              {detail.occupant_list.map((o, i) => (
                <li key={i}>
                  {o.display_name ?? "(unnamed)"} {o.is_primary && <Badge tone="info">primary</Badge>}
                </li>
              ))}
            </ul>
            <h3 className="mt-3 text-sm font-medium">Folios</h3>
            <ul className="text-sm">
              {detail.folios.length === 0 && <li>No folios linked.</li>}
              {detail.folios.map((f) => (
                <li key={f.external_folio_id}>
                  {f.external_folio_id} · {f.folio_kind.toLowerCase()} · {f.status.toLowerCase()}
                  {f.is_default_posting_target && (
                    <>
                      {" · "}
                      <Badge tone="info">posting target</Badge>
                    </>
                  )}
                </li>
              ))}
            </ul>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
