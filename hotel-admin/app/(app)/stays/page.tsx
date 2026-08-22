"use client";

// STAYS — who the property management system says is in the building.
//
// The list used to identify a stay by its reservation string and room number only, which meant an operator
// looking for "the party in 412" or "Mr Andersen" had to open rows until they found the right one. Arrival
// and departure were already in the API and shown nowhere; the primary guest's name was already on the
// detail view but not in the list; and posting_allowed rendered as "closed" with no way to learn why.
//
// It is now a list an operator can scan and search, plus a detail view for one stay. Guest names appear
// because operating a hotel front desk requires them — but they are treated as what they are: they are not
// written to logs or diagnostics, and the search runs entirely in the browser over rows the operator is
// already authorised to see.

import { useEffect, useMemo, useState } from "react";
import { api, ListResp, Stay, StayDetail } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { formatRelative } from "@/lib/utils";
import { Search, X } from "lucide-react";

// Operator wording for the lifecycle. The wire values are unchanged; nobody outside the domain should have
// to read SCREAMING_SNAKE to find out whether a guest is in the building.
const STATUS_LABELS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "Post-stay access",
  CANCELLED: "Cancelled",
  NO_SHOW: "No show",
};
const STATUSES = ["", "IN_HOUSE", "RESERVED", "CHECKED_OUT", "POST_STAY_ACTIVE", "CANCELLED", "NO_SHOW"];

const toneFor = (status: string) =>
  status === "IN_HOUSE" ? "info" : status === "CHECKED_OUT" ? "default" : "warn";

const label = (s: string) => STATUS_LABELS[s] ?? s.replace(/_/g, " ").toLowerCase();

/** A date the operator reads, not an ISO timestamp. Arrival/departure are dates in the PMS, not instants. */
function shortDate(v?: string | null): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString(undefined, { day: "2-digit", month: "short" });
}

export default function StaysPage() {
  const [status, setStatus] = useState("IN_HOUSE"); // the question an operator asks by default
  const [q, setQ] = useState("");
  const [rows, setRows] = useState<Stay[] | null>(null);
  const [detail, setDetail] = useState<StayDetail | null>(null);
  const [detailBusy, setDetailBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      setRows(null);
      setErr(null);
      try {
        const query = status ? "?status=" + encodeURIComponent(status) : "";
        const r = await api.get<ListResp<Stay>>("/pms-stays" + query);
        if (alive) setRows(r.data);
      } catch (e: any) {
        if (alive) { setErr(e?.message ?? "Could not load stays"); setRows([]); }
      }
    })();
    return () => { alive = false; };
  }, [status]);

  // Client-side, over the page already loaded. The list is capped server-side at 200 rows, so this is a
  // filter over what is on screen rather than a search of the whole property — worth being honest about in
  // the empty state below.
  const filtered = useMemo(() => {
    if (!rows) return null;
    const needle = q.trim().toLowerCase();
    if (!needle) return rows;
    return rows.filter((s) =>
      (s.room ?? "").toLowerCase().includes(needle) ||
      (s.primary_guest ?? "").toLowerCase().includes(needle) ||
      s.external_reservation_id.toLowerCase().includes(needle));
  }, [rows, q]);

  async function open(id: string) {
    setErr(null); setDetailBusy(true);
    try { setDetail(await api.get<StayDetail>("/pms-stays/" + id)); }
    catch (e: any) { setErr(e?.message ?? "Could not load this stay"); }
    finally { setDetailBusy(false); }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-3 flex-wrap">
        <div>
          <h1 className="text-lg font-semibold">Stays</h1>
          <p className="text-sm text-muted mt-1">
            What the property management system reports. This is a read-only view — stays are changed in the PMS.
          </p>
        </div>
        <div className="flex gap-2 items-center">
          <div className="relative">
            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-muted" />
            <Input aria-label="Search stays" placeholder="Room, name or reservation"
              className="pl-8 w-60" value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <select aria-label="Filter by status" value={status} onChange={(e) => setStatus(e.target.value)}
            className="bg-panel2 border border-border rounded-md px-2 py-2 text-sm">
            {STATUSES.map((s) => (
              <option key={s || "all"} value={s}>{s === "" ? "All stays" : label(s)}</option>
            ))}
          </select>
        </div>
      </div>

      {err && <ErrorBanner err={err} />}

      <Card>
        <CardBody>
          {filtered === null ? (
            <p className="text-sm text-muted py-6 text-center">Loading stays…</p>
          ) : filtered.length === 0 ? (
            <EmptyState
              title={q ? "No stays match that search" : "No stays to show"}
              hint={q
                ? "Search covers the most recent 200 stays for the selected status."
                : "Nothing has arrived from the property management system for this filter yet. If you expect guests here, check the PMS connection."} />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Room</TH><TH>Guest</TH><TH>Reservation</TH><TH>Stay</TH>
                  <TH>Status</TH><TH>Charges</TH><TH>&nbsp;</TH>
                </TR>
              </THead>
              <tbody>
                {filtered.map((s) => (
                  <TR key={s.id}>
                    <TD className="font-medium">
                      {s.room ?? "—"}
                      {s.vip ? <Badge tone="warn">VIP</Badge> : null}
                    </TD>
                    <TD>
                      <div>{s.primary_guest ?? <span className="text-muted">Not provided</span>}</div>
                      {s.occupants > 1 && (
                        <div className="text-xs text-muted">+{s.occupants - 1} sharing</div>
                      )}
                    </TD>
                    <TD className="text-xs text-muted">{s.external_reservation_id}</TD>
                    <TD className="whitespace-nowrap">
                      {shortDate(s.arrival)} → {shortDate(s.departure)}
                    </TD>
                    <TD>
                      <Badge tone={toneFor(s.status) as any}>{label(s.status)}</Badge>
                      {s.effective_checkout_at && (
                        <div className="text-xs text-muted">left {formatRelative(s.effective_checkout_at)}</div>
                      )}
                    </TD>
                    <TD>
                      {s.posting_allowed
                        ? <span className="text-xs">Can be charged</span>
                        : <span className="text-xs text-muted" title={s.posting_block_reason ?? undefined}>
                            Closed{s.posting_block_reason ? ` · ${s.posting_block_reason}` : ""}
                          </span>}
                    </TD>
                    <TD><Button variant="ghost" onClick={() => open(s.id)}>View</Button></TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {detailBusy && !detail && <p className="text-sm text-muted">Loading stay…</p>}

      {detail && (
        <Card>
          <CardBody className="space-y-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <h2 className="text-base font-semibold">
                  Room {detail.room ?? "—"}
                  {detail.primary_guest ? ` · ${detail.primary_guest}` : ""}
                </h2>
                <p className="text-xs text-muted mt-0.5">
                  Reservation {detail.external_reservation_id}
                  {detail.pms_interface_label ? ` · from ${detail.pms_interface_label}` : ""}
                </p>
              </div>
              <Button variant="ghost" onClick={() => setDetail(null)}><X size={16} /> Close</Button>
            </div>

            <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4 text-sm">
              <Field label="Status" value={label(detail.status)} />
              <Field label="Arrival" value={shortDate(detail.arrival)} />
              <Field label="Departure" value={shortDate(detail.departure)} />
              <Field label="Left the property"
                value={detail.effective_checkout_at ? formatRelative(detail.effective_checkout_at) : "Still in house"} />
              <Field label="Charges to room"
                value={detail.posting_allowed ? "Allowed" : `Closed${detail.posting_block_reason ? ` — ${detail.posting_block_reason}` : ""}`} />
              {detail.posting_permission_source && (
                <Field label="Decided by" value={detail.posting_permission_source} />
              )}
              <Field label="PMS last confirmed"
                value={detail.occupancy_evidence_at ? formatRelative(detail.occupancy_evidence_at) : "No confirmation recorded"} />
              <Field label="Occupants" value={String(detail.occupants)} />
              {/* Rendered ONLY when the connector supplied them. A permanent row of dashes would suggest the
                  PMS is failing to send something, when in fact this feed was never asked for it. */}
              {detail.room_type && <Field label="Room type" value={detail.room_type} />}
              {detail.rate_plan && <Field label="Rate plan" value={detail.rate_plan} />}
              {detail.travel_agent && <Field label="Travel agent" value={detail.travel_agent} />}
              {detail.vip != null && <Field label="VIP" value={detail.vip ? "Yes" : "No"} />}
            </div>

            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <h3 className="text-sm font-medium mb-1">Guests on this stay</h3>
                {detail.occupant_list.length === 0 ? (
                  <p className="text-sm text-muted">The PMS did not send guest names for this stay.</p>
                ) : (
                  <ul className="text-sm space-y-1">
                    {detail.occupant_list.map((o, i) => (
                      <li key={i}>
                        {o.display_name ?? <span className="text-muted">Name not provided</span>}{" "}
                        {o.is_primary && <Badge tone="info">main guest</Badge>}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
              <div>
                <h3 className="text-sm font-medium mb-1">Folios</h3>
                {detail.folios.length === 0 ? (
                  <p className="text-sm text-muted">No folio is linked to this stay.</p>
                ) : (
                  <ul className="text-sm space-y-1">
                    {detail.folios.map((f) => (
                      <li key={f.external_folio_id}>
                        {f.external_folio_id} · {f.folio_kind.toLowerCase()} · {f.status.toLowerCase()}
                        {f.is_default_posting_target && <> · <Badge tone="info">charges go here</Badge></>}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          </CardBody>
        </Card>
      )}
    </div>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-xs uppercase tracking-wide text-muted">{label}</div>
      <div>{value}</div>
    </div>
  );
}
